package memwatcher

import (
	"strings"
	"testing"
)

// TestParseTemplate_Valid проверяет что валидный шаблон парсится без ошибок.
func TestParseTemplate_Valid(t *testing.T) {
	tmpl, err := parseTemplate("test", "Hello {{.Service}}")
	if err != nil {
		t.Fatalf("parseTemplate valid template: %v", err)
	}
	if tmpl == nil {
		t.Fatal("parseTemplate returned nil template")
	}
}

// TestParseTemplate_Invalid проверяет что невалидный шаблон возвращает ошибку,
// а не вызывает панику (как template.Must).
func TestParseTemplate_Invalid(t *testing.T) {
	_, err := parseTemplate("bad", "{{.Unclosed")
	if err == nil {
		t.Fatal("parseTemplate with invalid syntax should return error, got nil")
	}
}

// TestParseTemplate_Empty проверяет что пустой шаблон корректно парсится.
func TestParseTemplate_Empty(t *testing.T) {
	tmpl, err := parseTemplate("empty", "")
	if err != nil {
		t.Fatalf("parseTemplate empty: %v", err)
	}
	if tmpl == nil {
		t.Fatal("parseTemplate returned nil for empty template")
	}
}

// TestNewMessageData_BytesToMB проверяет конвертацию байт → мегабайты.
func TestNewMessageData_BytesToMB(t *testing.T) {
	notif := DumpNotification{
		Service:         "svc",
		TriggerReason:   "heap >= 80%",
		HeapInuseBytes:  200 * 1024 * 1024, // 200 MB
		PctOfGoMemLimit: 83.5,
		DumpDirName:     "memdump-svc-20260312-100523",
		PyroscopeURL:    "https://pyroscope.example.com/foo",
	}
	data := newMessageData(notif)

	if data.HeapInuseMB != 200 {
		t.Errorf("HeapInuseMB = %d, want 200", data.HeapInuseMB)
	}
	if data.Service != notif.Service {
		t.Errorf("Service = %q, want %q", data.Service, notif.Service)
	}
	if data.TriggerReason != notif.TriggerReason {
		t.Errorf("TriggerReason mismatch")
	}
	if data.PctOfGoMemLimit != notif.PctOfGoMemLimit {
		t.Errorf("PctOfGoMemLimit = %v, want %v", data.PctOfGoMemLimit, notif.PctOfGoMemLimit)
	}
	if data.DumpDirName != notif.DumpDirName {
		t.Errorf("DumpDirName mismatch")
	}
	if data.PyroscopeURL != notif.PyroscopeURL {
		t.Errorf("PyroscopeURL mismatch")
	}
}

// TestNewMessageData_ZeroBytes проверяет что 0 байт даёт 0 MB без паники.
func TestNewMessageData_ZeroBytes(t *testing.T) {
	data := newMessageData(DumpNotification{HeapInuseBytes: 0})
	if data.HeapInuseMB != 0 {
		t.Errorf("HeapInuseMB = %d, want 0", data.HeapInuseMB)
	}
}

// TestRenderMessage_SlackTemplate проверяет что встроенный Slack шаблон рендерится
// и содержит ключевые поля.
func TestRenderMessage_SlackTemplate(t *testing.T) {
	tmpl, err := parseTemplate("slack", slackTmplContent)
	if err != nil {
		t.Fatalf("parse slack template: %v", err)
	}

	data := MessageData{
		Service:         "billz_auth_service",
		TriggerReason:   "heap_inuse >= 80% GOMEMLIMIT (82.3%)",
		HeapInuseMB:     820,
		PctOfGoMemLimit: 82.3,
		DumpDirName:     "memdump-billz_auth_service-20260312-100523",
		PyroscopeURL:    "",
	}

	msg, err := renderMessage(tmpl, data)
	if err != nil {
		t.Fatalf("renderMessage: %v", err)
	}

	for _, want := range []string{
		data.Service,
		data.DumpDirName,
		"820",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("rendered slack message does not contain %q\nmessage: %s", want, msg)
		}
	}
}

// TestRenderMessage_TelegramTemplate проверяет встроенный Telegram шаблон.
func TestRenderMessage_TelegramTemplate(t *testing.T) {
	tmpl, err := parseTemplate("telegram", telegramTmplContent)
	if err != nil {
		t.Fatalf("parse telegram template: %v", err)
	}

	data := MessageData{
		Service:         "billz_auth_service",
		TriggerReason:   "heap_inuse >= 90% GOMEMLIMIT (91.5%)",
		HeapInuseMB:     915,
		PctOfGoMemLimit: 91.5,
		DumpDirName:     "memdump-billz_auth_service-20260312-100523",
		PyroscopeURL:    "https://pyroscope.example.com/foo",
	}

	msg, err := renderMessage(tmpl, data)
	if err != nil {
		t.Fatalf("renderMessage: %v", err)
	}

	for _, want := range []string{
		data.Service,
		data.DumpDirName,
		data.PyroscopeURL,
		"915",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("rendered telegram message does not contain %q\nmessage: %s", want, msg)
		}
	}
}

// TestRenderMessage_NoPyroscopeURL проверяет что Pyroscope строка не появляется
// в сообщении если URL пустой (шаблоны используют {{if .PyroscopeURL}}).
func TestRenderMessage_NoPyroscopeURL(t *testing.T) {
	for _, tc := range []struct {
		name    string
		content string
	}{
		{"slack", slackTmplContent},
		{"telegram", telegramTmplContent},
	} {
		t.Run(tc.name, func(t *testing.T) {
			tmpl, _ := parseTemplate(tc.name, tc.content)
			data := MessageData{
				Service:      "svc",
				DumpDirName:  "memdump-svc-123",
				PyroscopeURL: "", // пустой
			}
			msg, err := renderMessage(tmpl, data)
			if err != nil {
				t.Fatalf("renderMessage: %v", err)
			}
			if strings.Contains(msg, "pyroscope") || strings.Contains(msg, "Pyroscope") {
				t.Errorf("message should not contain Pyroscope reference when URL is empty\nmessage: %s", msg)
			}
		})
	}
}
