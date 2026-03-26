package memwatcher

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"go.uber.org/zap"
)

// Config — настройки Watcher.
//
// Передаётся в New(cfg Config). Все поля опциональны кроме ServiceName.
// setDefaults() заполняет нулевые поля разумными значениями перед валидацией в New().
//
// Пример минимальной конфигурации:
//
//	watcher, err := memwatcher.New(memwatcher.Config{
//	    ServiceName: "billz_auth_service",
//	    DumpDir:     "/dumps/billz_auth_service",
//	    Log:         log,
//	})
//	if err != nil {
//	    return fmt.Errorf("init memwatcher: %w", err)
//	}
type Config struct {
	// ServiceName — имя сервиса (обязательное).
	// Используется в:
	//   - имени директории дампа: "memdump-{ServiceName}-{timestamp}"
	//   - метке Prometheus: heap_dump_triggered_total{service=ServiceName}
	//   - ссылке на Pyroscope UI
	//   - поле OOMNotification.Service
	ServiceName string

	// DumpDir — директория для записи дампов (PVC mount).
	// Каждый дамп создаёт поддиректорию вида "memdump-{service}-{timestamp}".
	// Default: "/tmp" (не переживает рестарт пода — только для dev/testing).
	// Production: путь к PVC mount, например "/dumps/billz_auth_service".
	DumpDir string

	// Tier1Pct — порог Tier1 (ускоренный polling + CPU profiler) в % от GOMEMLIMIT.
	// Default: 70. Должен быть < Tier2Pct.
	Tier1Pct int

	// Tier2Pct — порог Tier2 (первый дамп) в % от GOMEMLIMIT.
	// Default: 80. Должен быть < Tier3Pct.
	Tier2Pct int

	// Tier3Pct — порог Tier3 (повторный дамп с коротким cooldown) в % от GOMEMLIMIT.
	// Default: 90. Должен быть < 100.
	Tier3Pct int

	// PollInterval — базовый интервал проверки памяти когда HeapInuse < 70%.
	// При приближении к OOM (≥70%) автоматически сокращается до 500ms.
	// Должен быть > 0. Default: 5s.
	//
	// Polling использует runtime/metrics.Read() — без STW паузы.
	// Снизить до 1-2s безопасно даже в продакшн.
	PollInterval time.Duration

	// CooldownTier2 — минимальный интервал между дампами при Tier2 (≥80%).
	// Предотвращает заполнение диска при затяжном OOM.
	// Default: 5m.
	CooldownTier2 time.Duration

	// CooldownTier3 — минимальный интервал между дампами при Tier3 (≥90%).
	// Короче чем Tier2 — на 90% ситуация критичнее, нужно больше снапшотов.
	// Default: 1m.
	CooldownTier3 time.Duration

	// Channels — список получателей уведомлений после каждого записанного дампа.
	// Каждый вызывается параллельно в отдельной горутине с timeout NotifyTimeout.
	// Default: nil (уведомления отключены).
	// Пример: []memwatcher.NotificationChannel{
	//    {Templator: slackTmpl, Notifier: slack},
	//    {Templator: tgTmpl,   Notifier: tg},
	//},
	Channels []NotificationChannel

	// NotifyTimeout — timeout для вызова каждого нотификатора.
	// Default: 15s.
	NotifyTimeout time.Duration

	// MaxDumps — максимальное количество директорий дампов в DumpDir.
	// При превышении удаляются самые старые (по timestamp в имени директории).
	// 0 — без ограничения (по умолчанию).
	MaxDumps int

	// DumpTTL — максимальный возраст директории дампа.
	// После каждого успешного дампа директории старше DumpTTL удаляются.
	// 0 — без ограничения (по умолчанию).
	DumpTTL time.Duration

	// ShutdownDumpTimeout — максимальное время на запись финального дампа при Stop().
	// Stop() пишет дамп если HeapInuse ≥ 80% (Tier2) на момент остановки.
	// Должен быть меньше GracefulShutdownTimeout сервиса.
	// Default: 30s.
	ShutdownDumpTimeout time.Duration

	// PyroscopeBaseURL — базовый URL Pyroscope UI для генерации ссылок в уведомлениях.
	// Пример: "https://pyroscope.observability.internal".
	// Если пустой — поле PyroscopeURL в OOMNotification остаётся пустым.
	// Default: "" (отключено).
	PyroscopeBaseURL string

	// DumpBaseURL — базовый URL gateway для генерации ссылок на дампы в Telegram/Slack уведомлениях.
	// Пример: "https://dev-api-admin.makebillz.top".
	// Итоговая ссылка: "{DumpBaseURL}/v3/debug/dumps/{service}/{dump}/heap.pprof".
	// Если пустой — DumpURL и DumpCardURL в уведомлениях не заполняются.
	DumpBaseURL string

	// Uploader — загрузчик дампов в удалённое хранилище (MinIO).
	// Default: NoopUploader{} (загрузка отключена, MinIO не нужен).
	Uploader DumpUploader

	// UploadTimeout — timeout для одной попытки загрузки директории дампа в MinIO.
	// Если за это время upload не завершился — lock снимается, другой сервис на ноде попробует позже.
	// Default: 2m.
	UploadTimeout time.Duration

	// ScanInterval — интервал периодического сканирования родительской директории дампов.
	// При каждом тике сервис проверяет /var/dumps/ на наличие незагруженных дампов
	// от других сервисов на той же ноде (peer-upload через O_EXCL lock).
	// Игнорируется если Uploader == NoopUploader.
	// Default: 5m.
	ScanInterval time.Duration

	// Log — логгер. Любой *zap.Logger автоматически удовлетворяет Logger интерфейсу.
	// Default: zap.NewProduction() (stderr, JSON формат).
	Log Logger

	// Registerer — Prometheus registry для регистрации метрики heap_dump_triggered_total.
	// Default: prometheus.DefaultRegisterer (глобальный registry сервиса).
	// В тестах передавайте prometheus.NewRegistry() для изоляции — это предотвращает
	// ошибку дублирования регистрации между тест-кейсами.
	Registerer prometheus.Registerer
}

// NotificationChannel — пара рендерер + транспорт для одного канала доставки.
// Templator и Notifier конфигурируются отдельно → независимо тестируются.
type NotificationChannel struct {
	Templator Templator
	Notifier  Notifier
}

// setDefaults заполняет незаданные поля Config разумными значениями.
// Вызывается внутри New() до валидации — пользователю вызывать не нужно.
func (c *Config) setDefaults() {
	if c.DumpDir == "" {
		c.DumpDir = "/tmp"
	}
	if c.PollInterval == 0 {
		c.PollInterval = 5 * time.Second
	}
	if c.CooldownTier2 == 0 {
		c.CooldownTier2 = 5 * time.Minute
	}
	if c.CooldownTier3 == 0 {
		c.CooldownTier3 = time.Minute
	}
	if c.NotifyTimeout == 0 {
		c.NotifyTimeout = 15 * time.Second
	}
	if c.ShutdownDumpTimeout == 0 {
		c.ShutdownDumpTimeout = 30 * time.Second
	}
	if c.Uploader == nil {
		c.Uploader = NoopUploader{}
	}
	if c.UploadTimeout == 0 {
		c.UploadTimeout = 2 * time.Minute
	}
	if c.ScanInterval == 0 {
		c.ScanInterval = 5 * time.Minute
	}
	// Channels: nil — корректный default, означает "без уведомлений"
	// MaxDumps, DumpTTL: 0 — корректный default, означает "без ограничения"
	if c.Log == nil {
		c.Log = newStderrLogger()
	}
	if c.Registerer == nil {
		c.Registerer = prometheus.DefaultRegisterer
	}
	if c.Tier1Pct == 0 {
		c.Tier1Pct = 70
	}
	if c.Tier2Pct == 0 {
		c.Tier2Pct = 80
	}
	if c.Tier3Pct == 0 {
		c.Tier3Pct = 90
	}
}

// newStderrLogger создаёт дефолтный zap логгер в production режиме (stderr, JSON).
// При ошибке создания возвращает zap.Nop — молчащий логгер, чтобы не паниковать.
func newStderrLogger() Logger {
	log, err := zap.NewProduction()
	if err != nil {
		return zap.NewNop()
	}
	return log
}

