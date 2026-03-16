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
	//   - поле DumpNotification.Service
	ServiceName string

	// DumpDir — директория для записи дампов (PVC mount).
	// Каждый дамп создаёт поддиректорию вида "memdump-{service}-{timestamp}".
	// Default: "/tmp" (не переживает рестарт пода — только для dev/testing).
	// Production: путь к PVC mount, например "/dumps/billz_auth_service".
	DumpDir string

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

	// Notifiers — список получателей уведомлений после каждого записанного дампа.
	// Каждый вызывается параллельно в отдельной горутине с timeout NotifyTimeout.
	// Default: nil (уведомления отключены).
	// Пример: []memwatcher.Notifier{slackNotifier, telegramNotifier}
	Notifiers []Notifier

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

	// PyroscopeBaseURL — базовый URL Pyroscope UI для генерации ссылок в уведомлениях.
	// Пример: "https://pyroscope.observability.internal".
	// Если пустой — поле PyroscopeURL в DumpNotification остаётся пустым.
	// Default: "" (отключено).
	PyroscopeBaseURL string

	// Log — логгер. Любой *zap.Logger автоматически удовлетворяет Logger интерфейсу.
	// Default: zap.NewProduction() (stderr, JSON формат).
	Log Logger

	// Registerer — Prometheus registry для регистрации метрики heap_dump_triggered_total.
	// Default: prometheus.DefaultRegisterer (глобальный registry сервиса).
	// В тестах передавайте prometheus.NewRegistry() для изоляции — это предотвращает
	// ошибку дублирования регистрации между тест-кейсами.
	Registerer prometheus.Registerer
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
	// Notifiers: nil — корректный default, означает "без уведомлений"
	// MaxDumps, DumpTTL: 0 — корректный default, означает "без ограничения"
	if c.Log == nil {
		c.Log = newStderrLogger()
	}
	if c.Registerer == nil {
		c.Registerer = prometheus.DefaultRegisterer
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
