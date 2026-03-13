package memwatcher

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

// minimalConfig возвращает минимально валидную конфигурацию для New().
// Используется как основа в table-driven тестах: каждый кейс мутирует одно поле.
func minimalConfig() Config {
	return Config{
		ServiceName:   "test_svc",
		PollInterval:  time.Second,
		CooldownTier2: time.Minute,
		CooldownTier3: 30 * time.Second,
		Registerer:    prometheus.NewRegistry(), // изолированный registry, не паникует
	}
}

// TestNew_Validation проверяет что New() возвращает ошибку для невалидной конфигурации,
// и успешно создаёт Watcher для минимально валидной конфигурации.
func TestNew_Validation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Config)
		wantErr bool
	}{
		{
			name:    "valid config",
			mutate:  func(c *Config) {},
			wantErr: false,
		},
		{
			name:    "empty ServiceName",
			mutate:  func(c *Config) { c.ServiceName = "" },
			wantErr: true,
		},
		{
			name:    "negative PollInterval",
			mutate:  func(c *Config) { c.PollInterval = -time.Second },
			wantErr: true,
		},
		{
			name:    "zero PollInterval keeps default via setDefaults",
			mutate:  func(c *Config) { c.PollInterval = 0 },
			wantErr: false, // setDefaults ставит 5s
		},
		{
			name:    "negative CooldownTier2",
			mutate:  func(c *Config) { c.CooldownTier2 = -time.Minute },
			wantErr: true,
		},
		{
			name:    "zero CooldownTier2 keeps default via setDefaults",
			mutate:  func(c *Config) { c.CooldownTier2 = 0 },
			wantErr: false,
		},
		{
			name:    "negative CooldownTier3",
			mutate:  func(c *Config) { c.CooldownTier3 = -time.Second },
			wantErr: true,
		},
		{
			name:    "zero CooldownTier3 keeps default via setDefaults",
			mutate:  func(c *Config) { c.CooldownTier3 = 0 },
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := minimalConfig()
			tc.mutate(&cfg)

			w, err := New(cfg)
			if tc.wantErr {
				if err == nil {
					t.Errorf("New() error = nil, want error")
				}
				if w != nil {
					t.Errorf("New() returned non-nil Watcher on error")
				}
			} else {
				if err != nil {
					t.Errorf("New() unexpected error: %v", err)
				}
				if w == nil {
					t.Errorf("New() returned nil Watcher without error")
				}
			}
		})
	}
}

// TestNew_DefaultsApplied проверяет что setDefaults применяется внутри New()
// и заполняет нулевые поля (Notifier, Log).
func TestNew_DefaultsApplied(t *testing.T) {
	cfg := Config{
		ServiceName: "test_svc",
		Registerer:  prometheus.NewRegistry(),
		// PollInterval, CooldownTier2, CooldownTier3, Notifier, Log — нулевые
	}

	w, err := New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	// После New() конфиг Watcher должен иметь ненулевые дефолты.
	if w.cfg.PollInterval <= 0 {
		t.Errorf("PollInterval not set by defaults, got %v", w.cfg.PollInterval)
	}
	if w.cfg.Notifier == nil {
		t.Errorf("Notifier should not be nil after defaults applied")
	}
	if w.cfg.Log == nil {
		t.Errorf("Log should not be nil after defaults applied")
	}
}

// TestRegisterCounter_NoPanic проверяет что повторная регистрация метрики
// в одном registry не приводит к панике — возвращается уже существующий counter.
func TestRegisterCounter_NoPanic(t *testing.T) {
	reg := prometheus.NewRegistry()

	// Первая регистрация — создаёт метрику.
	c1, err := registerCounter(reg)
	if err != nil {
		t.Fatalf("first registerCounter failed: %v", err)
	}
	if c1 == nil {
		t.Fatal("first registerCounter returned nil")
	}

	// Вторая регистрация в том же registry — должна вернуть тот же counter без ошибки.
	c2, err := registerCounter(reg)
	if err != nil {
		t.Fatalf("second registerCounter (same registry) failed: %v", err)
	}
	if c2 == nil {
		t.Fatal("second registerCounter returned nil")
	}
}

// TestRegisterCounter_DefaultRegistry проверяет что регистрация в DefaultRegisterer
// обрабатывает AlreadyRegisteredError (если тест запускается повторно или
// другой тест уже зарегистрировал метрику).
func TestRegisterCounter_DefaultRegistry(t *testing.T) {
	// Два последовательных вызова с DefaultRegisterer — оба должны быть без паники и ошибок.
	c1, err := registerCounter(prometheus.DefaultRegisterer)
	if err != nil {
		t.Fatalf("registerCounter with DefaultRegisterer: %v", err)
	}
	c2, err := registerCounter(prometheus.DefaultRegisterer)
	if err != nil {
		t.Fatalf("second registerCounter with DefaultRegisterer: %v", err)
	}

	// Оба должны вернуть рабочий counter (не nil).
	if c1 == nil || c2 == nil {
		t.Error("registerCounter returned nil")
	}
}
