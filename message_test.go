package memwatcher

import (
	"strings"
	"testing"
	"time"
)

// TestNewSlackTemplator_ParsesAllTemplates проверяет что NewSlackTemplator успешно
// загружает все *.slack.tmpl из embed.FS, включая oom и config_warning.
func TestNewSlackTemplator_ParsesAllTemplates(t *testing.T) {
	tmpl, err := NewSlackTemplator()
	if err != nil {
		t.Fatalf("NewSlackTemplator: %v", err)
	}
	if tmpl == nil {
		t.Fatal("NewSlackTemplator returned nil")
	}
}

// TestNewTelegramTemplator_ParsesAllTemplates проверяет что NewTelegramTemplator успешно
// загружает все *.telegram.tmpl из embed.FS.
func TestNewTelegramTemplator_ParsesAllTemplates(t *testing.T) {
	tmpl, err := NewTelegramTemplator()
	if err != nil {
		t.Fatalf("NewTelegramTemplator: %v", err)
	}
	if tmpl == nil {
		t.Fatal("NewTelegramTemplator returned nil")
	}
}

// TestTemplator_Get_OOM_Slack проверяет рендеринг OOM шаблона для Slack.
func TestTemplator_Get_OOM_Slack(t *testing.T) {
	tmpl, err := NewSlackTemplator()
	if err != nil {
		t.Fatalf("NewSlackTemplator: %v", err)
	}

	data := OOMNotification{
		Service:         "billz_auth_service",
		TriggerReason:   "heap_inuse >= 80% GOMEMLIMIT",
		HeapInuseMB:     820,
		PctOfGoMemLimit: 82.3,
		DumpDirName:     "memdump-billz_auth_service-20260312-100523",
	}

	msg, err := tmpl.Get(TemplateKeyOOM, data)
	if err != nil {
		t.Fatalf("Get(oom): %v", err)
	}

	for _, want := range []string{data.Service, data.DumpDirName, "820"} {
		if !strings.Contains(msg, want) {
			t.Errorf("slack oom message missing %q\nmessage: %s", want, msg)
		}
	}
}

// TestTemplator_Get_OOM_Telegram проверяет рендеринг OOM шаблона для Telegram.
func TestTemplator_Get_OOM_Telegram(t *testing.T) {
	tmpl, err := NewTelegramTemplator()
	if err != nil {
		t.Fatalf("NewTelegramTemplator: %v", err)
	}

	data := OOMNotification{
		Service:         "billz_auth_service",
		TriggerReason:   "heap_inuse >= 90% GOMEMLIMIT",
		HeapInuseMB:     915,
		PctOfGoMemLimit: 91.5,
		DumpDirName:     "memdump-billz_auth_service-20260312-100523",
		PyroscopeURL:    "https://pyroscope.example.com/foo",
	}

	msg, err := tmpl.Get(TemplateKeyOOM, data)
	if err != nil {
		t.Fatalf("Get(oom): %v", err)
	}

	for _, want := range []string{data.Service, data.DumpDirName, data.PyroscopeURL, "915"} {
		if !strings.Contains(msg, want) {
			t.Errorf("telegram oom message missing %q\nmessage: %s", want, msg)
		}
	}
}

// TestTemplator_Get_ConfigWarning_Slack проверяет рендеринг config_warning шаблона для Slack.
func TestTemplator_Get_ConfigWarning_Slack(t *testing.T) {
	tmpl, err := NewSlackTemplator()
	if err != nil {
		t.Fatalf("NewSlackTemplator: %v", err)
	}

	data := ConfigWarningNotification{
		Service:       "test_svc",
		InvalidFields: []string{"Tier2Pct=90 >= Tier3Pct=85"},
		ResetValues:   map[string]int{"Tier1Pct": 70, "Tier2Pct": 80, "Tier3Pct": 90},
		Timestamp:     time.Now().UTC(),
	}

	msg, err := tmpl.Get(TemplateKeyConfigWarning, data)
	if err != nil {
		t.Fatalf("Get(config_warning): %v", err)
	}

	for _, want := range []string{"test_svc", "Tier2Pct=90 >= Tier3Pct=85"} {
		if !strings.Contains(msg, want) {
			t.Errorf("slack config_warning message missing %q\nmessage: %s", want, msg)
		}
	}
}

// TestTemplator_Get_UnknownKey проверяет что неизвестный ключ возвращает ошибку.
func TestTemplator_Get_UnknownKey(t *testing.T) {
	tmpl, err := NewSlackTemplator()
	if err != nil {
		t.Fatalf("NewSlackTemplator: %v", err)
	}

	_, err = tmpl.Get("unknown_key", struct{}{})
	if err == nil {
		t.Error("expected error for unknown template key, got nil")
	}
}

// TestTemplator_Get_NoPyroscopeURL проверяет что при пустом PyroscopeURL
// Pyroscope строка не попадает в сообщение ({{if .PyroscopeURL}} в шаблоне).
func TestTemplator_Get_NoPyroscopeURL(t *testing.T) {
	for _, tc := range []struct {
		name string
		fn   func() (Templator, error)
	}{
		{"slack", NewSlackTemplator},
		{"telegram", NewTelegramTemplator},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, err := tc.fn()
			if err != nil {
				t.Fatalf("%s: %v", tc.name, err)
			}
			data := OOMNotification{
				Service:      "svc",
				DumpDirName:  "memdump-svc-123",
				PyroscopeURL: "",
			}
			msg, err := tmpl.Get(TemplateKeyOOM, data)
			if err != nil {
				t.Fatalf("Get: %v", err)
			}
			if strings.Contains(msg, "pyroscope") || strings.Contains(msg, "Pyroscope") {
				t.Errorf("message should not mention Pyroscope when URL is empty\nmessage: %s", msg)
			}
		})
	}
}
