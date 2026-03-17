package memwatcher_test

import (
	"context"
	"testing"
	"time"

	"github.com/billz-2/memwatcher"
	"github.com/prometheus/client_golang/prometheus"
)

// minimalConfig возвращает минимально валидную конфигурацию для New().
// Используется как основа в table-driven тестах: каждый кейс мутирует одно поле.
func minimalConfig() memwatcher.Config {
	return memwatcher.Config{
		ServiceName:   "test_svc",
		PollInterval:  time.Second,
		CooldownTier2: time.Minute,
		CooldownTier3: 30 * time.Second,
		Registerer:    prometheus.NewRegistry(),
	}
}

// TestNew_Validation проверяет что New() возвращает ошибку для невалидной конфигурации,
// и успешно создаёт Watcher для минимально валидной конфигурации.
func TestNew_Validation(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*memwatcher.Config)
		wantErr bool
	}{
		{
			name:    "valid config",
			mutate:  func(c *memwatcher.Config) {},
			wantErr: false,
		},
		{
			name:    "empty ServiceName",
			mutate:  func(c *memwatcher.Config) { c.ServiceName = "" },
			wantErr: true,
		},
		{
			name:    "negative PollInterval",
			mutate:  func(c *memwatcher.Config) { c.PollInterval = -time.Second },
			wantErr: true,
		},
		{
			name:    "zero PollInterval keeps default via setDefaults",
			mutate:  func(c *memwatcher.Config) { c.PollInterval = 0 },
			wantErr: false,
		},
		{
			name:    "negative CooldownTier2",
			mutate:  func(c *memwatcher.Config) { c.CooldownTier2 = -time.Minute },
			wantErr: true,
		},
		{
			name:    "zero CooldownTier2 keeps default via setDefaults",
			mutate:  func(c *memwatcher.Config) { c.CooldownTier2 = 0 },
			wantErr: false,
		},
		{
			name:    "negative CooldownTier3",
			mutate:  func(c *memwatcher.Config) { c.CooldownTier3 = -time.Second },
			wantErr: true,
		},
		{
			name:    "zero CooldownTier3 keeps default via setDefaults",
			mutate:  func(c *memwatcher.Config) { c.CooldownTier3 = 0 },
			wantErr: false,
		},
		// --- Tier validation ---
		{
			name:    "Tier1Pct >= 100 returns error",
			mutate:  func(c *memwatcher.Config) { c.Tier1Pct = 100 },
			wantErr: true,
		},
		{
			name:    "Tier2Pct >= 100 returns error",
			mutate:  func(c *memwatcher.Config) { c.Tier2Pct = 100 },
			wantErr: true,
		},
		{
			name:    "Tier3Pct >= 100 returns error",
			mutate:  func(c *memwatcher.Config) { c.Tier3Pct = 100 },
			wantErr: true,
		},
		{
			name: "Tier2Pct >= Tier3Pct — heal succeeds (no error)",
			mutate: func(c *memwatcher.Config) {
				c.Tier2Pct = 90
				c.Tier3Pct = 85
			},
			wantErr: false,
		},
		{
			name: "Tier1Pct >= Tier2Pct — heal succeeds (no error)",
			mutate: func(c *memwatcher.Config) {
				c.Tier1Pct = 80
				c.Tier2Pct = 75
			},
			wantErr: false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := minimalConfig()
			tc.mutate(&cfg)

			w, err := memwatcher.New(cfg)
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
// и заполняет нулевые поля (Log, NotifyTimeout).
func TestNew_DefaultsApplied(t *testing.T) {
	cfg := memwatcher.Config{
		ServiceName: "test_svc",
		Registerer:  prometheus.NewRegistry(),
	}

	w, err := memwatcher.New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error: %v", err)
	}
	if w == nil {
		t.Fatal("New() returned nil")
	}
}

// TestNew_Heal_WithNotifier проверяет что heal при невалидных порогах
// не мешает созданию Watcher и нотификатор получает config_warning.
func TestNew_Heal_WithNotifier(t *testing.T) {
	received := make(chan string, 2)
	fakeTemplator := &fakeTemplatorStub{}
	fakeNot := &fakeNotifierStub{received: received}

	cfg := minimalConfig()
	cfg.Tier2Pct = 90
	cfg.Tier3Pct = 85 // Tier2 >= Tier3 → heal
	cfg.Channels = []memwatcher.NotificationChannel{
		{Templator: fakeTemplator, Notifier: fakeNot},
	}
	cfg.NotifyTimeout = 500 * time.Millisecond

	w, err := memwatcher.New(cfg)
	if err != nil {
		t.Fatalf("New() unexpected error after heal: %v", err)
	}
	if w == nil {
		t.Fatal("New() returned nil after heal")
	}

	select {
	case msg := <-received:
		if msg != "config_warning" {
			t.Errorf("expected config_warning notification, got %q", msg)
		}
	case <-time.After(time.Second):
		t.Error("config_warning notification was not sent within 1s")
	}
}

// fakeTemplatorStub — возвращает ключ шаблона как текст сообщения.
type fakeTemplatorStub struct{}

func (f *fakeTemplatorStub) Get(key string, _ any) (string, error) { return key, nil }

// fakeNotifierStub — записывает сообщения в канал.
type fakeNotifierStub struct {
	received chan string
}

func (f *fakeNotifierStub) Notify(_ context.Context, msg string) error {
	select {
	case f.received <- msg:
	default:
	}
	return nil
}
