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
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// fastPollInterval — интервал проверки памяти при HeapInuse ≥ 70%.
// 500ms: быстрое обнаружение пересечения тира.
// Не создаёт нагрузки — в hot path используется runtime/metrics.Read()
// которая читает атомарные счётчики без STW паузы.
const fastPollInterval = 500 * time.Millisecond

// Watcher следит за HeapInuse и при приближении к GOMEMLIMIT пишет диагностические дампы.
//
// Жизненный цикл:
//
//	watcher, err := memwatcher.New(cfg)
//	if err != nil { ... }
//	go watcher.Run(ctx)
type Watcher struct {
	cfg Config

	// profiler — CPU профайлер. Запускается при Tier1, снапшот берётся при Tier2/3.
	profiler *profiler

	// counter — Prometheus counter для отслеживания частоты дампов.
	// Инициализируется в New() через registerCounter() — без паники.
	// Метка: heap_dump_triggered_total{service, tier}.
	counter *prometheus.CounterVec

	// lastDumpAt2, lastDumpAt3 — время последнего дампа каждого тира.
	// Используются для cooldown между дампами.
	lastDumpAt2 time.Time
	lastDumpAt3 time.Time

	stopCh chan struct{}
	once   sync.Once
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
	// setDefaults устанавливает PollInterval = 5s если он был == 0.
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
		cfg:      cfg,
		profiler: &profiler{},
		counter:  counter,
		stopCh:   make(chan struct{}),
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

// Stop завершает работу Watcher. Безопасно вызывать несколько раз (sync.Once).
// Альтернатива отмене ctx — для graceful shutdown при обработке SIGTERM.
func (w *Watcher) Stop() {
	w.once.Do(func() { close(w.stopCh) })
}

// Run запускает основной цикл мониторинга памяти. Блокирует горутину до отмены ctx или Stop().
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

	heap := newHeapMonitor(goMemLimit)

	w.cfg.Log.Info("memwatcher: started",
		zap.String("service", w.cfg.ServiceName),
		zap.Int64("gomemlimit_bytes", goMemLimit),
		zap.String("dump_dir", w.cfg.DumpDir),
		zap.String("poll_interval", w.cfg.PollInterval.String()),
		zap.String("fast_poll_interval", fastPollInterval.String()),
	)

	// PollInterval проверен в New() — time.NewTicker не паникует.
	currentInterval := w.cfg.PollInterval
	ticker := time.NewTicker(currentInterval)
	defer ticker.Stop()

	// Go 1.23: ticker.Reset() сам выполняет Stop+drain — ручной drain не нужен.
	setInterval := func(d time.Duration) {
		if d == currentInterval {
			return
		}
		ticker.Reset(d)
		currentInterval = d
	}

	for {
		select {
		case <-ctx.Done():
			w.profiler.stop()
			return

		case <-w.stopCh:
			w.profiler.stop()
			return

		case <-ticker.C:
			// runtime/metrics.Read() — читает атомарные счётчики runtime без STW.
			// STW происходит только в writeDump() когда нужен полный MemStats snapshot.
			inuse, tier := heap.read()
			switch tier {
			case heapTier3:
				w.profiler.ensureRunning()
				setInterval(fastPollInterval)
				if time.Since(w.lastDumpAt3) >= w.cfg.CooldownTier3 {
					reason := fmt.Sprintf("heap_inuse >= 90%% GOMEMLIMIT (%.1f%%)", heap.pct(inuse))
					if err := w.writeDump(ctx, "3", reason, heap); err != nil {
						w.cfg.Log.Error("memwatcher: writeDump failed", zap.Error(err))
					} else {
						w.lastDumpAt3 = time.Now()
					}
				}

			case heapTier2:
				w.profiler.ensureRunning()
				setInterval(fastPollInterval)
				if time.Since(w.lastDumpAt2) >= w.cfg.CooldownTier2 {
					reason := fmt.Sprintf("heap_inuse >= 80%% GOMEMLIMIT (%.1f%%)", heap.pct(inuse))
					if err := w.writeDump(ctx, "2", reason, heap); err != nil {
						w.cfg.Log.Error("memwatcher: writeDump failed", zap.Error(err))
					} else {
						w.lastDumpAt2 = time.Now()
					}
				}

			case heapTier1:
				w.profiler.ensureRunning()
				setInterval(fastPollInterval)

			default:
				w.profiler.stop()
				setInterval(w.cfg.PollInterval)
			}
		}
	}
}

// writeDump создаёт директорию дампа и записывает все профили.
// Вызывается синхронно из Run() — блокирует тик на время записи (~100ms-2s).
//
// Возвращает ошибку только если не удалось создать директорию — критическая ошибка.
// В этом случае cooldown не обновляется и watcher попробует снова через CooldownTier{N}.
// Ошибки записи отдельных файлов логируются внутри dumper и не поднимаются наружу
// (частичный дамп лучше нуля).
//
// Здесь и только здесь вызывается runtime.ReadMemStats() со STW паузой ~100μs.
// В основном цикле Run() STW не происходит — используется runtime/metrics.Read().
func (w *Watcher) writeDump(ctx context.Context, tier, reason string, heap *heapMonitor) error {
	// Cleanup ПЕРЕД записью — освобождаем место на PVC до попытки записи нового дампа.
	w.cleanup()

	timestamp := time.Now().UTC().Format("20060102-150405")
	dirName := fmt.Sprintf("memdump-%s-%s", w.cfg.ServiceName, timestamp)
	dirPath := filepath.Join(w.cfg.DumpDir, dirName)

	if err := os.MkdirAll(dirPath, 0o755); err != nil {
		return fmt.Errorf("create dump dir %s: %w", dirPath, err)
	}

	// Единственное место STW в пакете: полный snapshot MemStats нужен только для дампа.
	// Захватываем сразу после создания директории — максимально свежие данные.
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)

	pct := float64(ms.HeapInuse) / float64(heap.limit) * 100
	stats := buildRuntimeStats(w.cfg.ServiceName, reason, pct,
		heap.limit, heap.thresholds[1], heap.thresholds[2], ms)
	cpuData := w.profiler.snapshot()

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

	// Каждый нотификатор вызывается параллельно в отдельной горутине.
	// Общая горутина-обёртка гарантирует что Run() не блокируется на notify.
	if len(w.cfg.Notifiers) > 0 {
		go func() {
			notifyCtx, cancel := context.WithTimeout(context.Background(), w.cfg.NotifyTimeout)
			defer cancel()
			for _, n := range w.cfg.Notifiers {
				go func(n Notifier) {
					if err := n.Notify(notifyCtx, notification); err != nil {
						w.cfg.Log.Error("memwatcher: notifier error", zap.Error(err))
					}
				}(n)
			}
		}()
	}

	return nil
}
