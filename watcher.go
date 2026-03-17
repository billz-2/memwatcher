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

// Пакет memwatcher реализует pre-OOM диагностику для Go сервисов.
//
// Механика работы (общая схема):
//
//  1. Watcher.Run() запускает цикл мониторинга памяти.
//  2. Каждые PollInterval секунд читает runtime.MemStats.HeapInuse.
//  3. Сравнивает HeapInuse с порогами (70/80/90% от GOMEMLIMIT):
//     - ≥70% → ускоряет polling до 5s, запускает CPU profiler (cpuProfiler).
//     - ≥80% → дополнительно пишет дамп всех профилей (dumper.writeAll).
//     - ≥90% → повторный дамп с более коротким cooldown.
//  4. writeDump создаёт директорию, заполняет её через dumper, инкрементирует
//     Prometheus метрику и запускает Notifier.Notify() в отдельной горoutine.
//  5. DumpServer отдаёт накопленные дампы по HTTP (/debug/dumps/).
//
// Связи между файлами:
//
//	config.go      — Config (настройки + дефолты)
//	logger.go      — Logger (интерфейс для логгера)
//	notifier.go    — Notifier / NoopNotifier / DumpNotification
//	slack_notifier.go — SlackNotifier (реализация Notifier через Slack webhook)
//	watcher.go     — Watcher (основной цикл), использует cpu.go, dump.go, stats.go
//	cpu.go         — cpuProfiler (управление runtime/pprof CPU профилем)
//	dump.go        — dumper (запись pprof файлов с fsync)
//	stats.go       — RuntimeStats + buildRuntimeStats (snapshot MemStats → JSON)
//	server.go      — DumpServer (HTTP хендлеры для просмотра/скачивания дампов)

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

	w := &Watcher{
		cfg:      cfg,
		profiler: &profiler{},
		counter:  counter,
		stopCh:   make(chan struct{}),
	}
	if err := w.validateAndHeal(); err != nil {
		return nil, err
	}
	return w, nil
}

// validateAndHeal проверяет порядок tier-порогов и автоматически исправляет
// некорректные значения, отправляя config_warning нотификацию при heal.
func (w *Watcher) validateAndHeal() error {
	// 1. Индивидуальные диапазоны
	for _, pct := range []int{w.cfg.Tier1Pct, w.cfg.Tier2Pct, w.cfg.Tier3Pct} {
		if pct >= 100 {
			return fmt.Errorf("memwatcher: TierNPct must be < 100, got %d", pct)
		}
	}

	// 2. Heal попарно: сначала Tier2/Tier3, потом Tier1/Tier2
	var healed []string
	resetValues := map[string]int{}

	if w.cfg.Tier2Pct >= w.cfg.Tier3Pct {
		healed = append(healed,
			fmt.Sprintf("Tier2Pct=%d >= Tier3Pct=%d", w.cfg.Tier2Pct, w.cfg.Tier3Pct))
		w.cfg.Tier2Pct = 80
		w.cfg.Tier3Pct = 90
		resetValues["Tier2Pct"] = 80
		resetValues["Tier3Pct"] = 90
	}
	if w.cfg.Tier1Pct >= w.cfg.Tier2Pct {
		healed = append(healed,
			fmt.Sprintf("Tier1Pct=%d >= Tier2Pct=%d", w.cfg.Tier1Pct, w.cfg.Tier2Pct))
		w.cfg.Tier1Pct = 70
		w.cfg.Tier2Pct = 80
		resetValues["Tier1Pct"] = 70
		if _, ok := resetValues["Tier2Pct"]; !ok {
			resetValues["Tier2Pct"] = 80
		}
	}

	// 3. Нотификация если был heal
	if len(healed) > 0 {
		w.notify(TemplateKeyConfigWarning, ConfigWarningNotification{
			Service:       w.cfg.ServiceName,
			InvalidFields: healed,
			ResetValues:   resetValues,
			Timestamp:     time.Now().UTC(),
		})
	}

	// 4. Повторная проверка после heal
	if w.cfg.Tier1Pct >= w.cfg.Tier2Pct || w.cfg.Tier2Pct >= w.cfg.Tier3Pct {
		return fmt.Errorf("memwatcher: tier thresholds invalid after heal: %d/%d/%d",
			w.cfg.Tier1Pct, w.cfg.Tier2Pct, w.cfg.Tier3Pct)
	}

	return nil
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

	heap := NewHeapMonitor(goMemLimit, w.cfg.Tier1Pct, w.cfg.Tier2Pct, w.cfg.Tier3Pct)

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
			// Финальный дамп если heap ≥ Tier2 на момент graceful shutdown.
			// Нотификации надёжны — используют context.Background() + NotifyTimeout.
			// Клиент вызывает Stop() до cancelFunc(), давая время на запись дампа.
			inuse, tier := heap.Read()
			if tier >= HeapTier2 {
				reason := fmt.Sprintf("shutdown dump: heap_inuse %.1f%%", heap.Pct(inuse))
				if err := w.writeDump("shutdown", reason, heap); err != nil {
					w.cfg.Log.Error("memwatcher: shutdown dump failed", zap.Error(err))
				}
			}
			return

		case <-ticker.C:
			// runtime/metrics.Read() — читает атомарные счётчики runtime без STW.
			// STW происходит только в writeDump() когда нужен полный MemStats snapshot.
			inuse, tier := heap.Read()
			switch tier {
			case HeapTier3:
				w.profiler.ensureRunning()
				setInterval(fastPollInterval)
				if time.Since(w.lastDumpAt3) >= w.cfg.CooldownTier3 {
					reason := fmt.Sprintf("heap_inuse >= 90%% GOMEMLIMIT (%.1f%%)", heap.Pct(inuse))
					if err := w.writeDump("3", reason, heap); err != nil {
						w.cfg.Log.Error("memwatcher: writeDump failed", zap.Error(err))
					} else {
						w.lastDumpAt3 = time.Now()
					}
				}

			case HeapTier2:
				w.profiler.ensureRunning()
				setInterval(fastPollInterval)
				if time.Since(w.lastDumpAt2) >= w.cfg.CooldownTier2 {
					reason := fmt.Sprintf("heap_inuse >= 80%% GOMEMLIMIT (%.1f%%)", heap.Pct(inuse))
					if err := w.writeDump("2", reason, heap); err != nil {
						w.cfg.Log.Error("memwatcher: writeDump failed", zap.Error(err))
					} else {
						w.lastDumpAt2 = time.Now()
					}
				}

			case HeapTier1:
				w.profiler.ensureRunning()
				setInterval(fastPollInterval)

			default:
				w.profiler.stop()
				setInterval(w.cfg.PollInterval)
			}
		}
	}
}

// WriteDump создаёт диагностический дамп немедленно.
//
// Вызывается автоматически из Run() при превышении порогов HeapTier2/HeapTier3.
// Может быть вызван вручную — например, по HTTP запросу /debug/force-dump
// или при обработке SIGUSR1:
//
//	heap := memwatcher.NewHeapMonitor(debug.SetMemoryLimit(-1))
//	if err := w.WriteDump("manual", "forced by operator", heap); err != nil {
//	    log.Error("dump failed", zap.Error(err))
//	}
//
// Возвращает ошибку только если не удалось создать директорию — критическая ошибка.
// В этом случае cooldown не обновляется и watcher попробует снова через CooldownTier{N}.
// Ошибки записи отдельных файлов логируются внутри dumper и не поднимаются наружу
// (частичный дамп лучше нуля).
//
// Здесь и только здесь вызывается runtime.ReadMemStats() со STW паузой ~100μs.
// В основном цикле Run() STW не происходит — используется runtime/metrics.Read().
func (w *Watcher) WriteDump(tier, reason string, heap *HeapMonitor) error {
	return w.writeDump(tier, reason, heap)
}

func (w *Watcher) writeDump(tier, reason string, heap *HeapMonitor) error {
	method := "Watcher.writeDump"

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
		zap.String("method", method),
		zap.String("dir", dirPath),
		zap.String("tier", tier),
		zap.String("reason", reason),
	)

	pyroscopeURL := w.cfg.PyroscopeBaseURL
	if pyroscopeURL != "" {
		pyroscopeURL = fmt.Sprintf(
			"%s/ui?query=%s{}&from=now-5m&until=now",
			w.cfg.PyroscopeBaseURL,
			w.cfg.ServiceName,
		)
	}
	w.notify(TemplateKeyOOM, OOMNotification{
		Service:         w.cfg.ServiceName,
		TriggerReason:   reason,
		HeapInuseMB:     ms.HeapInuse / 1024 / 1024,
		PctOfGoMemLimit: pct,
		DumpDirName:     dirName,
		PyroscopeURL:    pyroscopeURL,
		Timestamp:       time.Now().UTC(),
	})
	return nil
}

// notify рендерит и отправляет уведомление всем каналам параллельно.
// key: TemplateKeyOOM, TemplateKeyConfigWarning
// data: OOMNotification, ConfigWarningNotification
func (w *Watcher) notify(key string, data any) {
	method := "Watcher.notify"
	for _, ch := range w.cfg.Channels {
		go func(ch NotificationChannel) {
			// Получаем шаблон по ключу
			text, err := ch.Templator.Get(key, data)
			if err != nil {
				w.cfg.Log.Error("memwatcher: render notification",
					zap.String("method", method),
					zap.String("key", key),
					zap.Error(err))
				return
			}

			ctx, cancel := context.WithTimeout(context.Background(), w.cfg.NotifyTimeout)
			defer cancel()

			err = ch.Notifier.Notify(ctx, text)
			if err != nil {
				w.cfg.Log.Error("memwatcher: send notification",
					zap.String("method", method),
					zap.String("key", key),
					zap.Error(err))
			}
		}(ch)
	}
}
