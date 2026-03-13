package memwatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"text/template"
)

// SlackNotifier реализует интерфейс Notifier через Slack Incoming Webhook.
// Использует только stdlib (net/http, encoding/json) — никаких внешних HTTP клиентов.
//
// Создавать только через NewSlackNotifier — конструктор валидирует URL и
// парсит шаблон сообщения, возвращая ошибку вместо паники.
//
// SlackWebhookURL передаётся через k8s Secret (не plaintext в deployment).
type SlackNotifier struct {
	// webhookURL и tmpl приватные — доступ только через NewSlackNotifier.
	// Это гарантирует что SlackNotifier всегда в валидном состоянии.
	webhookURL string
	tmpl       *template.Template
}

// NewSlackNotifier создаёт SlackNotifier.
//
// Выполняет две проверки при создании (не при отправке):
//  1. webhookURL не пустой — пустой URL гарантированно даст ошибку при Notify.
//  2. Шаблон из templates/slack.tmpl парсится без ошибок — невалидный синтаксис
//     возвращает error здесь, а не панику в package init.
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
	tmpl, err := parseTemplate("slack", slackTmplContent)
	if err != nil {
		return nil, fmt.Errorf("parse slack template: %w", err)
	}
	return &SlackNotifier{
		webhookURL: webhookURL,
		tmpl:       tmpl,
	}, nil
}

// Notify отправляет сообщение в Slack с информацией о дампе.
//
// Вызывается из watcher.go::writeDump() в горутине с context.WithTimeout(15s).
// При ошибке (недоступен Slack, rate limit, невалидный webhook) —
// возвращает ошибку, которая логируется в watcher.go и не влияет на цикл.
//
// Формат сообщения определён в templates/slack.tmpl.
func (s *SlackNotifier) Notify(ctx context.Context, n DumpNotification) error {
	// Рендерим текст через шаблон, распарсенный в NewSlackNotifier.
	// Шаблон использует Slack mrkdwn: *bold*, `code`, <URL|text>.
	text, err := renderMessage(s.tmpl, newMessageData(n))
	if err != nil {
		return fmt.Errorf("render slack message: %w", err)
	}

	// Slack Incoming Webhook принимает JSON {"text": "..."}.
	payload, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return fmt.Errorf("marshal slack payload: %w", err)
	}

	// Создаём запрос с переданным ctx (у него timeout 15s из watcher.go).
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

	// Slack возвращает 200 OK с телом "ok" при успехе.
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("slack webhook returned status %d", resp.StatusCode)
	}
	return nil
}
