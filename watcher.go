package memwatcher

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"runtime/debug"
	"strings"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"

	"github.com/billz-2/memwatcher/internal/cpuprofiler"
	"github.com/billz-2/memwatcher/internal/dump"
	"github.com/billz-2/memwatcher/internal/stats"
)

// Пакет memwatcher реализует pre-OOM диагностику для Go сервисов.
//
// Механика работы (общая схема):
//
//  1. Watcher.Run() запускает цикл мониторинга памяти.
//  2. Каждые PollInterval секунд читает HeapInuse через runtime/metrics (без STW).
//  3. Сравнивает HeapInuse с порогами (Tier1/Tier2/Tier3 от GOMEMLIMIT):
//     - ≥Tier1 → ускоряет polling до 500ms, запускает CPU profiler.
//     - ≥Tier2 → дополнительно пишет дамп всех профилей (dump.Dumper.WriteAll).
//     - ≥Tier3 → повторный дамп с более коротким cooldown.
//  4. writeDump создаёт директорию, заполняет её через dump.Dumper, инкрементирует
//     Prometheus метрику и запускает Notifier.Notify() в отдельной горoutine.
//  5. DumpServer отдаёт накопленные дампы по HTTP (/debug/dumps/).
//
// Структура пакета:
//
//	config.go            — Config (настройки + дефолты + validateAndHeal)
//	logger.go            — Logger (интерфейс для логгера)
//	notifier.go          — Notifier / NoopNotifier
//	notification.go      — OOMNotification / ConfigWarningNotification
//	slack_notifier.go    — SlackNotifier (реализация Notifier через Slack webhook)
//	telegram_notifier.go — TelegramNotifier (реализация Notifier через Telegram Bot API)
//	templator.go         — Templator / NewSlackTemplator / NewTelegramTemplator
//	watcher.go           — Watcher (основной цикл мониторинга)
//	heap_monitor.go      — HeapMonitor (чтение HeapInuse без STW через runtime/metrics)
//	cleanup.go           — TTL и count-based очистка старых дампов
//	server.go            — DumpServer (HTTP хендлеры для просмотра/скачивания дампов)
//
// internal/:
//
//	internal/cpuprofiler — Profiler (управление runtime/pprof CPU профилем)
//	internal/dump        — Dumper (запись pprof файлов с fsync)
//	internal/stats       — RuntimeStats + Build (snapshot MemStats → JSON)

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
	// Деталь реализации: живёт в internal/cpuprofiler, пользователь не взаимодействует напрямую.
	profiler *cpuprofiler.Profiler

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
		profiler: &cpuprofiler.Profiler{},
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

// StartupUpload сканирует родительскую директорию дампов и загружает в MinIO все дампы
// без маркера .uploaded. Это включает дампы от других сервисов на той же ноде (peer-upload).
//
// Вызывать один раз при старте сервиса в отдельной горутине:
//
//	go func() { _ = watcher.StartupUpload(ctx) }()
//
// Если Uploader == NoopUploader — возвращает nil без каких-либо действий.
// Если родительская директория не существует — возвращает nil (нет дампов ещё).
func (w *Watcher) StartupUpload(ctx context.Context) error {
	if _, ok := w.cfg.Uploader.(NoopUploader); ok {
		return nil
	}

	// Сканируем /var/dumps/ — всю ноду, не только свой DumpDir.
	// DumpDir = "/var/dumps/billz_user_service" → scanRoot = "/var/dumps"
	scanRoot := filepath.Dir(w.cfg.DumpDir)

	services, err := os.ReadDir(scanRoot)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("memwatcher: StartupUpload ReadDir %s: %w", scanRoot, err)
	}

	for _, svcDir := range services {
		if !svcDir.IsDir() {
			continue
		}
		dumps, err := os.ReadDir(filepath.Join(scanRoot, svcDir.Name()))
		if err != nil {
			continue
		}
		for _, d := range dumps {
			if !d.IsDir() || !strings.HasPrefix(d.Name(), "memdump-") {
				continue
			}
			dumpPath := filepath.Join(scanRoot, svcDir.Name(), d.Name())
			w.tryUpload(ctx, dumpPath)
		}
	}
	return nil
}

// tryUpload пытается загрузить директорию дампа в MinIO с атомарным O_EXCL lock.
// Если дамп уже загружен (.uploaded) — пропускает.
// Если другой сервис держит lock (.uploading, свежий) — пропускает.
// Если lock зависший (stale) — очищает и пробует захватить.
func (w *Watcher) tryUpload(ctx context.Context, dumpDirPath string) {
	uploadedPath := filepath.Join(dumpDirPath, ".uploaded")
	lockPath := filepath.Join(dumpDirPath, ".uploading")

	if _, err := os.Stat(uploadedPath); err == nil {
		return // уже загружено
	}

	w.clearStaleLock(lockPath)

	// Атомарный захват lock: только один процесс создаст файл (O_EXCL гарантирует ОС).
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		return // чужой lock активен
	}
	fmt.Fprintf(f, "%d\n%d\n", os.Getpid(), time.Now().Unix())
	f.Close()

	uploadCtx, cancel := context.WithTimeout(ctx, w.cfg.UploadTimeout)
	defer cancel()

	if err = w.cfg.Uploader.Upload(uploadCtx, dumpDirPath); err != nil {
		w.cfg.Log.Error("memwatcher: upload failed, lock released",
			zap.String("dir", dumpDirPath),
			zap.Error(err))
		if err = os.Remove(lockPath); err != nil {
			w.cfg.Log.Error("memwatcher: remove lock failed",
				zap.String("dir", dumpDirPath),
				zap.Error(err))
		}
		return
	}

	// Атомарно .uploading → .uploaded: нет окна где директория без маркера.
	if err := os.Rename(lockPath, uploadedPath); err != nil {
		w.cfg.Log.Error("memwatcher: rename .uploading→.uploaded failed",
			zap.String("dir", dumpDirPath),
			zap.Error(err))
		if err = os.Remove(lockPath); err != nil {
			w.cfg.Log.Error("memwatcher: remove lock failed",
				zap.String("dir", dumpDirPath),
				zap.Error(err))
		}
	}
}

// clearStaleLock удаляет .uploading файл если его TTL истёк (UploadTimeout + 30s).
// Это означает что предыдущий владелец был убит (OOM kill, SIGKILL и т.д.).
func (w *Watcher) clearStaleLock(lockPath string) {
	info, err := os.Stat(lockPath)
	if err != nil {
		return
	}
	ttl := w.cfg.UploadTimeout + 30*time.Second
	if time.Since(info.ModTime()) < ttl {
		return
	}
	w.cfg.Log.Warn("memwatcher: clearing stale upload lock",
		zap.String("lock", lockPath),
		zap.Duration("age", time.Since(info.ModTime())))
	os.Remove(lockPath)
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
	method := "Watcher.Run"

	// debug.SetMemoryLimit(-1) возвращает текущий GOMEMLIMIT без его изменения.
	// math.MaxInt64 = Go runtime default = "нет лимита".
	goMemLimit := debug.SetMemoryLimit(-1)
	if goMemLimit == math.MaxInt64 {
		w.cfg.Log.Error("memwatcher: GOMEMLIMIT is not set, watcher will not start — set GOMEMLIMIT env var",
			zap.String("method", method))
		return
	}

	heap := NewHeapMonitor(goMemLimit, w.cfg.Tier1Pct, w.cfg.Tier2Pct, w.cfg.Tier3Pct)

	w.cfg.Log.Info("memwatcher: started",
		zap.String("method", method),
		zap.String("service", w.cfg.ServiceName),
		zap.Int64("gomemlimit_bytes", goMemLimit),
		zap.String("dump_dir", w.cfg.DumpDir),
		zap.String("poll_interval", w.cfg.PollInterval.String()),
		zap.String("fast_poll_interval", fastPollInterval.String()))

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

	// scanTickerC: nil-канал блокирует навсегда — peer-scan отключён при NoopUploader.
	var scanTickerC <-chan time.Time
	if _, ok := w.cfg.Uploader.(NoopUploader); !ok {
		st := time.NewTicker(w.cfg.ScanInterval)
		defer st.Stop()
		scanTickerC = st.C
	}

	for {
		select {
		case <-ctx.Done():
			w.profiler.Stop()
			return

		case <-scanTickerC:
			go func() {
				if err := w.StartupUpload(ctx); err != nil {
					w.cfg.Log.Error("memwatcher: periodic scan failed", zap.Error(err))
				}
			}()

		case <-w.stopCh:
			w.profiler.Stop()
			// Финальный дамп если heap ≥ Tier2 на момент graceful shutdown.
			// Нотификации надёжны — используют context.Background() + NotifyTimeout.
			// Клиент вызывает Stop() до cancelFunc(), давая время на запись дампа.
			inuse, tier := heap.Read()
			if tier >= HeapTier2 {
				reason := fmt.Sprintf("shutdown dump: heap_inuse %.1f%%", heap.Pct(inuse))
				if err := w.writeDump("shutdown", reason, heap); err != nil {
					w.cfg.Log.Error("memwatcher: shutdown dump failed",
						zap.String("method", method),
						zap.Error(err))
				}
			}
			return

		case <-ticker.C:
			// runtime/metrics.Read() — читает атомарные счётчики runtime без STW.
			// STW происходит только в writeDump() когда нужен полный MemStats snapshot.
			inuse, tier := heap.Read()
			switch tier {
			case HeapTier3:
				w.profiler.EnsureRunning()
				setInterval(fastPollInterval)
				if time.Since(w.lastDumpAt3) >= w.cfg.CooldownTier3 {
					reason := fmt.Sprintf("heap_inuse >= 90%% GOMEMLIMIT (%.1f%%)", heap.Pct(inuse))
					if err := w.writeDump("3", reason, heap); err != nil {
						w.cfg.Log.Error("memwatcher: writeDump failed",
							zap.String("method", method),
							zap.Error(err))
						break
					}
					w.lastDumpAt3 = time.Now()
				}

			case HeapTier2:
				w.profiler.EnsureRunning()
				setInterval(fastPollInterval)
				if time.Since(w.lastDumpAt2) >= w.cfg.CooldownTier2 {
					reason := fmt.Sprintf("heap_inuse >= 80%% GOMEMLIMIT (%.1f%%)", heap.Pct(inuse))
					if err := w.writeDump("2", reason, heap); err != nil {
						w.cfg.Log.Error("memwatcher: writeDump failed",
							zap.String("method", method),
							zap.Error(err))
						break
					}
					w.lastDumpAt2 = time.Now()
				}

			case HeapTier1:
				w.profiler.EnsureRunning()
				setInterval(fastPollInterval)

			default:
				w.profiler.Stop()
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
	rtStats := stats.Build(w.cfg.ServiceName, reason, pct,
		heap.limit, heap.thresholds[1], heap.thresholds[2], ms)
	statsJSON, err := json.MarshalIndent(rtStats, "", "  ")
	if err != nil {
		w.cfg.Log.Error("memwatcher: marshal runtime stats",
			zap.String("method", method),
			zap.Error(err))
		statsJSON = nil
	}
	cpuData := w.profiler.Snapshot()

	d := &dump.Dumper{Dir: dirPath, Log: w.cfg.Log}
	d.WriteAll(statsJSON, cpuData)

	w.counter.WithLabelValues(w.cfg.ServiceName, tier).Inc()

	w.cfg.Log.Info("memwatcher: dump complete",
		zap.String("method", method),
		zap.String("dir", dirPath),
		zap.String("tier", tier),
		zap.String("reason", reason))

	pyroscopeURL := w.cfg.PyroscopeBaseURL
	if pyroscopeURL != "" {
		pyroscopeURL = fmt.Sprintf(
			"%s/ui?query=%s{}&from=now-5m&until=now",
			w.cfg.PyroscopeBaseURL,
			w.cfg.ServiceName)
	}

	dumpURL := ""
	dumpCardURL := ""
	if w.cfg.DumpBaseURL != "" {
		base := fmt.Sprintf("%s/v3/debug/dumps/%s/%s",
			w.cfg.DumpBaseURL, w.cfg.ServiceName, dirName)
		dumpURL = base + "/heap.pprof"
		dumpCardURL = base + "/"
		if w.cfg.DumpAuthToken != "" {
			dumpURL += "?token=" + w.cfg.DumpAuthToken
			dumpCardURL += "?token=" + w.cfg.DumpAuthToken
		}
	}

	w.notify(TemplateKeyOOM, OOMNotification{
		Service:         w.cfg.ServiceName,
		TriggerReason:   reason,
		HeapInuseMB:     ms.HeapInuse / 1024 / 1024,
		PctOfGoMemLimit: pct,
		DumpDirName:     dirName,
		DumpURL:         dumpURL,
		DumpCardURL:     dumpCardURL,
		PyroscopeURL:    pyroscopeURL,
		Timestamp:       time.Now().UTC(),
	})

	// Загрузка в MinIO асинхронно — только если Uploader настроен (не NoopUploader).
	// Пропуск NoopUploader предотвращает гонку с t.TempDir() cleanup в тестах:
	// горутина могла бы создать .uploading файл пока RemoveAll удаляет директорию.
	if _, ok := w.cfg.Uploader.(NoopUploader); !ok {
		go w.tryUpload(context.Background(), dirPath)
	}

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

			if err = ch.Notifier.Notify(ctx, text); err != nil {
				w.cfg.Log.Error("memwatcher: send notification",
					zap.String("method", method),
					zap.String("key", key),
					zap.Error(err))
			}
		}(ch)
	}
}
