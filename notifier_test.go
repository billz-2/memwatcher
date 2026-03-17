package memwatcher_test

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/billz-2/memwatcher"
)

// ---- SlackNotifier ----

// TestNewSlackNotifier_EmptyURL проверяет что пустой webhookURL возвращает ошибку.
func TestNewSlackNotifier_EmptyURL(t *testing.T) {
	n, err := memwatcher.NewSlackNotifier("")
	if err == nil {
		t.Fatal("NewSlackNotifier(\"\") should return error")
	}
	if n != nil {
		t.Fatal("NewSlackNotifier should return nil notifier on error")
	}
}

// TestNewSlackNotifier_ValidURL проверяет успешное создание.
func TestNewSlackNotifier_ValidURL(t *testing.T) {
	n, err := memwatcher.NewSlackNotifier("https://hooks.slack.com/services/T00/B00/xxx")
	if err != nil {
		t.Fatalf("NewSlackNotifier valid URL: %v", err)
	}
	if n == nil {
		t.Fatal("NewSlackNotifier returned nil")
	}
}

// TestSlackNotifier_Notify_Success проверяет что Notify() шлёт POST на webhookURL
// и тело содержит JSON с полем "text".
func TestSlackNotifier_Notify_Success(t *testing.T) {
	var receivedBody []byte

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("expected POST, got %s", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		receivedBody = body
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n, err := memwatcher.NewSlackNotifier(srv.URL)
	if err != nil {
		t.Fatalf("NewSlackNotifier: %v", err)
	}

	if err := n.Notify(context.Background(), "test dump notification"); err != nil {
		t.Fatalf("Notify() unexpected error: %v", err)
	}

	var payload map[string]any
	if err := json.Unmarshal(receivedBody, &payload); err != nil {
		t.Fatalf("Slack payload is not valid JSON: %v", err)
	}
	if _, ok := payload["text"]; !ok {
		t.Error("Slack payload missing 'text' field")
	}
}

// TestSlackNotifier_Notify_ServerError проверяет что HTTP 500 от сервера
// возвращается как ошибка.
func TestSlackNotifier_Notify_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer srv.Close()

	n, _ := memwatcher.NewSlackNotifier(srv.URL)
	err := n.Notify(context.Background(), "msg")
	if err == nil {
		t.Fatal("Notify() should return error on HTTP 500")
	}
}

// TestSlackNotifier_Notify_ContextCancelled проверяет что отменённый контекст
// возвращает ошибку до отправки запроса.
func TestSlackNotifier_Notify_ContextCancelled(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	n, _ := memwatcher.NewSlackNotifier(srv.URL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := n.Notify(ctx, "msg")
	if err == nil {
		t.Fatal("Notify() with cancelled context should return error")
	}
}

// ---- TelegramNotifier ----

// TestNewTelegramNotifier_EmptyToken проверяет что пустой botToken возвращает ошибку.
func TestNewTelegramNotifier_EmptyToken(t *testing.T) {
	n, err := memwatcher.NewTelegramNotifier("", "123456")
	if err == nil {
		t.Fatal("NewTelegramNotifier empty token should return error")
	}
	if n != nil {
		t.Fatal("should return nil on error")
	}
}

// TestNewTelegramNotifier_EmptyChatID проверяет что пустой chatID возвращает ошибку.
func TestNewTelegramNotifier_EmptyChatID(t *testing.T) {
	n, err := memwatcher.NewTelegramNotifier("bot-token", "")
	if err == nil {
		t.Fatal("NewTelegramNotifier empty chatID should return error")
	}
	if n != nil {
		t.Fatal("should return nil on error")
	}
}

// TestNewTelegramNotifier_Valid проверяет успешное создание.
func TestNewTelegramNotifier_Valid(t *testing.T) {
	n, err := memwatcher.NewTelegramNotifier("bot-token", "-100123456789")
	if err != nil {
		t.Fatalf("NewTelegramNotifier valid args: %v", err)
	}
	if n == nil {
		t.Fatal("NewTelegramNotifier returned nil")
	}
}

// TestTelegramNotifier_Notify_Success проверяет что Notify() шлёт POST на /bot{token}/sendMessage
// и тело содержит JSON с полями chat_id и text.
func TestTelegramNotifier_Notify_Success(t *testing.T) {
	var receivedBody []byte
	var receivedPath string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedPath = r.URL.Path
		body, _ := io.ReadAll(r.Body)
		receivedBody = body
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	n, err := memwatcher.NewTelegramNotifier("test-token", "-100123456789")
	if err != nil {
		t.Fatalf("NewTelegramNotifier: %v", err)
	}
	// SetBaseURL — тест-хелпер из export_test.go
	n.SetBaseURL(srv.URL)

	if err := n.Notify(context.Background(), "test message"); err != nil {
		t.Fatalf("Notify() unexpected error: %v", err)
	}

	expectedPath := "/bottest-token/sendMessage"
	if receivedPath != expectedPath {
		t.Errorf("request path = %q, want %q", receivedPath, expectedPath)
	}

	var payload map[string]any
	if err := json.Unmarshal(receivedBody, &payload); err != nil {
		t.Fatalf("Telegram payload not valid JSON: %v", err)
	}
	if _, ok := payload["chat_id"]; !ok {
		t.Error("Telegram payload missing 'chat_id'")
	}
	if _, ok := payload["text"]; !ok {
		t.Error("Telegram payload missing 'text'")
	}
}

// TestTelegramNotifier_Notify_ServerError проверяет что не-2xx ответ возвращается как ошибка.
func TestTelegramNotifier_Notify_ServerError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"ok":false,"description":"Bad Request"}`))
	}))
	defer srv.Close()

	n, _ := memwatcher.NewTelegramNotifier("token", "-123")
	n.SetBaseURL(srv.URL)

	err := n.Notify(context.Background(), "msg")
	if err == nil {
		t.Fatal("Notify() should return error on non-2xx response")
	}
}

// TestNoopNotifier проверяет что NoopNotifier.Notify() всегда возвращает nil.
func TestNoopNotifier(t *testing.T) {
	n := memwatcher.NoopNotifier{}
	err := n.Notify(context.Background(), "anything")
	if err != nil {
		t.Errorf("NoopNotifier.Notify() = %v, want nil", err)
	}
}
