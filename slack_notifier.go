package memwatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// SlackNotifier реализует интерфейс Notifier через Slack Incoming Webhook.
// Использует только stdlib (net/http, encoding/json) — никаких внешних HTTP клиентов.
//
// Создавать только через NewSlackNotifier — конструктор валидирует URL.
//
// SlackWebhookURL передаётся через k8s Secret (не plaintext в deployment).
// Рендеринг сообщения выполняется Templator'ом (SlackTemplator) до вызова Notify.
type SlackNotifier struct {
	webhookURL string
}

// NewSlackNotifier создаёт SlackNotifier с указанным webhook URL.
//
// Пример:
//
//	slack, err := memwatcher.NewSlackNotifier(cfg.SlackWebhookURL)
//	if err != nil {
//	    return fmt.Errorf("init slack notifier: %w", err)
//	}
func NewSlackNotifier(webhookURL string) (*SlackNotifier, error) {
	if webhookURL == "" {
		return nil, errors.New("slack webhook URL is required")
	}
	return &SlackNotifier{webhookURL: webhookURL}, nil
}

// Notify отправляет готовую строку msg в Slack Incoming Webhook.
//
// Вызывается из watcher.go::notify() в горутине с context.WithTimeout.
// При ошибке возвращает её — логируется в watcher.go, не влияет на цикл мониторинга.
func (s *SlackNotifier) Notify(ctx context.Context, msg string) error {
	payload, err := json.Marshal(map[string]string{"text": msg})
	if err != nil {
		return fmt.Errorf("marshal slack payload: %w", err)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.webhookURL, bytes.NewReader(payload))
	if err != nil {
		return fmt.Errorf("create slack request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send slack notification: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack webhook returned status %d", resp.StatusCode)
	}
	return nil
}
