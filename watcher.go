package memwatcher

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"runtime/metrics"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Константы порогов и интервала быстрого polling.
const (
	// fastPollInterval — интервал проверки памяти при HeapInuse ≥ 70%.
	// 500ms: быстрое обнаружение пересечения тира.
	// Не создаёт нагрузки — в hot path используется runtime/metrics.Read()
	// которая читает атомарные счётчики без STW паузы.
	fastPollInterval = 500 * time.Millisecond

	// tier1Pct — порог Tier1: ускоряем polling и запускаем CPU профиль.
	tier1Pct = 0.70

	// tier2Pct — порог Tier2: первый дамп всех профилей.
	tier2Pct = 0.80

	// tier3Pct — порог Tier3: повторный дамп с более коротким cooldown.
	tier3Pct = 0.90

	// heapObjectsMetric + heapUnusedMetric в сумме дают HeapInuse из runtime.MemStats.
	//
	// runtime/metrics не имеет прямого аналога ms.HeapInuse.
	// HeapInuse = spans в активном использовании = live objects + fragmentation внутри spans.
	// В runtime/metrics это разбито на две отдельные метрики:
	//   objects:bytes — память занятая живыми объектами      (≈ ms.HeapAlloc)
	//   unused:bytes  — выделенные spans без объектов (фрагментация внутри heap)
	// objects + unused = HeapInuse (spans не возвращённые ОС, но не idle)
	//
	// Обе метрики читаются без STW — атомарные счётчики runtime.
	heapObjectsMetric = "/memory/classes/heap/objects:bytes"
	heapUnusedMetric  = "/memory/classes/heap/unused:bytes"
)

// Watcher следит за HeapInuse и при приближении к GOMEMLIMIT пишет диагностические дампы.
//
// Жизненный цикл:
//
//	watcher, err := memwatcher.New(cfg)
//	if err != nil { ... }
//	go watcher.Run(ctx)
type Watcher struct {
	cfg Config

	// cpu — профайлер CPU. Запускается при Tier1, снапшот берётся при Tier2/3.
	cpu *cpuProfiler

	// counter — Prometheus counter для отслеживания частоты дампов.
	// Инициализируется в New() через registerCounter() — без паники.
	// Метка: heap_dump_triggered_total{service, tier}.
	counter *prometheus.CounterVec

	// lastDumpAt2, lastDumpAt3 — время последнего дампа каждого тира.
	// Используются для cooldown между дампами.
	lastDumpAt2 time.Time
	lastDumpAt3 time.Time
}

// New создаёт Watcher и возвращает ошибку если конфигурация невалидна.
//
// Выполняет при создании (не в runtime):
//  1. Применяет дефолты для нулевых полей Config.
//  2. Валидирует обязательные поля и граничные значения.
//  3. Регистрирует Prometheus метрику — ошибка дублирования обрабатывается,
//     не паникует (важно для тестов с изолированными registry).
//
// После успешного New() вызов Run() гарантированно не вызовет паники
// из-за конфигурации.
func New(cfg Config) (*Watcher, error) {
	cfg.setDefaults()

	if cfg.ServiceName == "" {
		return nil, errors.New("memwatcher: ServiceName is required")
	}
	// setDefaults устанавливает PollInterval = 30s если он был == 0.
	// Если после setDefaults он всё ещё <= 0 — значит был явно передан отрицательным.
	// time.NewTicker паникует при d <= 0, поэтому ловим здесь.
	if cfg.PollInterval <= 0 {
		return nil, fmt.Errorf("memwatcher: PollInterval must be > 0, got %v", cfg.PollInterval)
	}
	if cfg.CooldownTier2 <= 0 {
		return nil, fmt.Errorf("memwatcher: CooldownTier2 must be > 0, got %v", cfg.CooldownTier2)
	}
	if cfg.CooldownTier3 <= 0 {
		return nil, fmt.Errorf("memwatcher: CooldownTier3 must be > 0, got %v", cfg.CooldownTier3)
	}

	counter, err := registerCounter(cfg.Registerer)
	if err != nil {
		return nil, fmt.Errorf("memwatcher: register prometheus metric: %w", err)
	}

	return &Watcher{
		cfg:     cfg,
		cpu:     &cpuProfiler{},
		counter: counter,
	}, nil
}

// registerCounter регистрирует Prometheus counter в указанном registry.
//
// Обрабатывает prometheus.AlreadyRegisteredError — это нормальная ситуация
// в тестах когда несколько Watcher'ов создаются в одном процессе.
// В этом случае переиспользуем уже зарегистрированный экземпляр.
func registerCounter(reg prometheus.Registerer) (*prometheus.CounterVec, error) {
	counter := prometheus.NewCounterVec(prometheus.CounterOpts{
		Namespace: "microservices",
		Name:      "heap_dump_triggered_total",
		Help:      "Number of times a heap dump was triggered.",
	}, []string{"service", "tier"})

	if err := reg.Register(counter); err != nil {
		// AlreadyRegisteredError: метрика уже зарегистрирована в этом registry.
		// Безопасно переиспользовать существующий экземпляр.
		var are prometheus.AlreadyRegisteredError
		if errors.As(err, &are) {
			existing, ok := are.ExistingCollector.(*prometheus.CounterVec)
			if !ok {
				return nil, errors.New("existing collector is not *prometheus.CounterVec")
			}
			return existing, nil
		}
		return nil, err
	}
	return counter, nil
}

// Run запускает основной цикл мониторинга памяти. Блокирует горутину до отмены ctx.
//
// Если GOMEMLIMIT не задан (math.MaxInt64) — логирует предупреждение и выходит.
// После успешного New() паники в Run() невозможны из-за конфигурации.
//
// Архитектура polling:
//
//	Медленный режим (< 70%): PollInterval (default 5s) — runtime/metrics.Read(), без STW.
//	Быстрый режим  (≥ 70%): fastPollInterval (500ms)   — runtime/metrics.Read(), без STW.
//	При дампе (≥ 80%/90%):  однократно runtime.ReadMemStats() для полного snapshot — STW ~100μs.
//
// Итог: STW только в момент записи дампа, не в каждом тике.
func (w *Watcher) Run(ctx context.Context) {
	// debug.SetMemoryLimit(-1) возвращает текущий GOMEMLIMIT без его изменения.
	// math.MaxInt64 = Go runtime default = "нет лимита".
	goMemLimit := debug.SetMemoryLimit(-1)
	if goMemLimit == math.MaxInt64 {
		w.cfg.Log.Error("memwatcher: GOMEMLIMIT is not set, watcher will not start — set GOMEMLIMIT env var")
		return
	}

	threshold70 := uint64(float64(goMemLimit) * tier1Pct)
	threshold80 := uint64(float64(goMemLimit) * tier2Pct)
	threshold90 := uint64(float64(goMemLimit) * tier3Pct)

	w.cfg.Log.Info("memwatcher: started",
		zap.String("service", w.cfg.ServiceName),
		zap.Int64("gomemlimit_bytes", goMemLimit),
		zap.String("dump_dir", w.cfg.DumpDir),
		zap.String("poll_interval", w.cfg.PollInterval.String()),
		zap.String("fast_poll_interval", fastPollInterval.String()),
	)

	// sample переиспользуется на каждом тике — одна аллокация на весь жизненный цикл.
	// runtime/metrics.Read() пишет результат прямо в переданный slice без новых аллокаций.
	sample := []metrics.Sample{
		{Name: heapObjectsMetric},
		{Name: heapUnusedMetric},
	}

	// PollInterval проверен в New() — time.NewTicker не паникует.
	currentInterval := w.cfg.PollInterval
	ticker := time.NewTicker(currentInterval)
	defer ticker.Stop()

	// setInterval безопасно меняет интервал ticker'а.
	// Stop → drain → Reset — стандартный паттерн для Go 1.21.
	setInterval := func(d time.Duration) {
		if d == currentInterval {
			return
		}
		ticker.Stop()
		select {
		case <-ticker.C:
		default:
		}
		ticker.Reset(d)
		currentInterval = d
	}

	for {
		select {
		case <-ctx.Done():
			w.cpu.stop()
			return

		case <-ticker.C:
			// runtime/metrics.Read() — читает атомарные счётчики runtime без STW.
			// HeapInuse = objects + unused (spans выделенные, но не возвращённые ОС).
			// STW происходит только в writeDump() когда нужен полный MemStats snapshot.
			metrics.Read(sample)
			heapInuse := readHeapInuse(sample)

			switch {
			case heapInuse >= threshold90:
				w.cpu.ensureRunning()
				setInterval(fastPollInterval)
				if time.Since(w.lastDumpAt3) >= w.cfg.CooldownTier3 {
					pct := float64(heapInuse) / float64(goMemLimit) * 100
					reason := fmt.Sprintf("heap_inuse >= 90%% GOMEMLIMIT (%.1f%%)", pct)
					w.writeDump(ctx, "3", reason, uint64(goMemLimit), threshold80, threshold90)
					w.lastDumpAt3 = time.Now()
				}

			case heapInuse >= threshold80:
				w.cpu.ensureRunning()
				setInterval(fastPollInterval)
				if time.Since(w.lastDumpAt2) >= w.cfg.CooldownTier2 {
					pct := float64(heapInuse) / float64(goMemLimit) * 100
					reason := fmt.Sprintf("heap_inuse >= 80%% GOMEMLIMIT (%.1f%%)", pct)
					w.writeDump(ctx, "2", reason, uint64(goMemLimit), threshold80, threshold90)
					w.lastDumpAt2 = time.Now()
				}

			case heapInuse >= threshold70:
				w.cpu.ensureRunning()
				setInterval(fastPollInterval)

			default:
				w.cpu.stop()
				setInterval(w.cfg.PollInterval)
			}
		}
	}
}

// readHeapInuse вычисляет HeapInuse из двух метрик runtime/metrics без STW.
//
// Защита от KindBad: если метрика не распознана рантаймом (например при обновлении Go
// с переименованием метрик) — возвращаем 0 вместо паники.
// В этом случае тик просто пропускается, следующий тик повторит попытку.
func readHeapInuse(sample []metrics.Sample) uint64 {
	if sample[0].Value.Kind() != metrics.KindUint64 ||
		sample[1].Value.Kind() != metrics.KindUint64 {
		return 0
	}
	return sample[0].Value.Uint64() + sample[1].Value.Uint64()
}

// writeDump создаёт директорию дампа и записывает все профили.
// Вызывается синхронно из Run() — блокирует тик на время записи (~100ms-2s).
//
// Здесь и только здесь вызывается runtime.ReadMemStats() со STW паузой ~100μs.
// В основном цикле Run() STW не происходит — используется runtime/metrics.Read().
func (w *Watcher) writeDump(
	ctx context.Context,
	tier, reason string,
	goMemLimit, threshold80, threshold90 uint64,
) {
	timestamp := time.Now().UTC().Format("20060102-150405")
	dirName := fmt.Sprintf("memdump-%s-%s", w.cfg.ServiceName, timestamp)
	dirPath := filepath.Join(w.cfg.DumpDir, dirName)

	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		w.cfg.Log.Error("memwatcher: failed to create dump dir",
			zap.String("path", dirPath),
			zap.Error(err),
		)
		return
	}

	// Единственное место STW в пакете: полный snapshot MemStats нужен только для дампа.
	// Захватываем сразу после создания директории — максимально свежие данные.
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	pct := float64(ms.HeapInuse) / float64(goMemLimit) * 100
	stats := buildRuntimeStats(w.cfg.ServiceName, reason, pct, goMemLimit, threshold80, threshold90, ms)
	cpuData := w.cpu.snapshot()

	d := &dumper{dir: dirPath, log: w.cfg.Log}
	d.writeAll(stats, cpuData)

	w.counter.WithLabelValues(w.cfg.ServiceName, tier).Inc()

	w.cfg.Log.Info("memwatcher: dump complete",
		zap.String("dir", dirPath),
		zap.String("tier", tier),
		zap.String("reason", reason),
	)

	notification := DumpNotification{
		Service:         w.cfg.ServiceName,
		DumpDirName:     dirName,
		TriggerReason:   reason,
		HeapInuseBytes:  ms.HeapInuse,
		PctOfGoMemLimit: pct,
	}
	if w.cfg.PyroscopeBaseURL != "" {
		notification.PyroscopeURL = fmt.Sprintf(
			"%s/ui?query=%s{}&from=now-5m&until=now",
			w.cfg.PyroscopeBaseURL,
			w.cfg.ServiceName,
		)
	}

	// Notifier вызывается async в goroutine с timeout 15s независимым от основного ctx.
	go func() {
		notifyCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := w.cfg.Notifier.Notify(notifyCtx, notification); err != nil {
			w.cfg.Log.Error("memwatcher: notifier error", zap.Error(err))
		}
	}()
}
