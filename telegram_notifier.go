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

// TelegramNotifier реализует Notifier через Telegram Bot API.
// Использует только stdlib — никаких внешних зависимостей.
//
// Создавать только через NewTelegramNotifier — конструктор валидирует поля и
// парсит шаблон сообщения, возвращая ошибку вместо паники.
//
// Настройка:
//  1. Создать бота через @BotFather → получить botToken
//  2. Добавить бота в нужный канал/группу как администратора
//  3. Получить chatID: через @userinfobot или GET /getUpdates
//
// При использовании вместе со SlackNotifier — оберни в NewMultiNotifier().
type TelegramNotifier struct {
	// botToken, chatID и tmpl приватные — доступ только через NewTelegramNotifier.
	botToken string
	chatID   string
	tmpl     *template.Template
	// baseURL — базовый URL Telegram Bot API. Default: "https://api.telegram.org".
	// Переопределяется в тестах для подстановки httptest.Server.
	baseURL string
}

// NewTelegramNotifier создаёт TelegramNotifier.
//
// Выполняет проверки при создании (не при отправке):
//  1. botToken не пустой — иначе Telegram API вернёт 401.
//  2. chatID не пустой — иначе Telegram API вернёт 400.
//  3. Шаблон из templates/telegram.tmpl парсится без ошибок.
//
// Пример:
//
//	tg, err := memwatcher.NewTelegramNotifier(cfg.TelegramBotToken, cfg.TelegramChatID)
//	if err != nil {
//	    return fmt.Errorf("init telegram notifier: %w", err)
//	}
func NewTelegramNotifier(botToken, chatID string) (*TelegramNotifier, error) {
	if botToken == "" {
		return nil, errors.New("telegram bot token is required")
	}
	if chatID == "" {
		return nil, errors.New("telegram chat ID is required")
	}
	tmpl, err := parseTemplate("telegram", telegramTmplContent)
	if err != nil {
		return nil, fmt.Errorf("parse telegram template: %w", err)
	}
	return &TelegramNotifier{
		botToken: botToken,
		chatID:   chatID,
		tmpl:     tmpl,
		baseURL:  "https://api.telegram.org",
	}, nil
}

// telegramSendMessageRequest — тело запроса к Telegram Bot API /sendMessage.
type telegramSendMessageRequest struct {
	ChatID    string `json:"chat_id"`
	Text      string `json:"text"`
	// ParseMode — "HTML" выбран вместо "MarkdownV2" потому что MarkdownV2 требует
	// экранирования символов: . - ( ) % _ и т.д., что ломает форматирование путей и процентов.
	ParseMode string `json:"parse_mode"`
}

// Notify отправляет сообщение в Telegram с информацией о дампе.
//
// Вызывается из watcher.go::writeDump() в горутине с context.WithTimeout(15s).
// При ошибке (недоступен API, rate limit 429) — возвращает ошибку,
// которая логируется в watcher.go и не влияет на цикл.
//
// Формат сообщения определён в templates/telegram.tmpl.
func (t *TelegramNotifier) Notify(ctx context.Context, n DumpNotification) error {
	// Рендерим текст через шаблон, распарсенный в NewTelegramNotifier.
	// Шаблон использует HTML разметку: <b>, <code>, <a href>.
	text, err := renderMessage(t.tmpl, newMessageData(n))
	if err != nil {
		return fmt.Errorf("render telegram message: %w", err)
	}

	body, err := json.Marshal(telegramSendMessageRequest{
		ChatID:    t.chatID,
		Text:      text,
		ParseMode: "HTML",
	})
	if err != nil {
		return fmt.Errorf("marshal telegram payload: %w", err)
	}

	// Telegram Bot API endpoint: POST /bot{TOKEN}/sendMessage
	url := fmt.Sprintf("%s/bot%s/sendMessage", t.baseURL, t.botToken)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return fmt.Errorf("create telegram request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("send telegram notification: %w", err)
	}
	defer resp.Body.Close()

	// Telegram возвращает 200 OK с {"ok": true, ...} при успехе.
	// Ошибки: 400 (bad chat_id), 401 (bad token), 403 (бот кикнут), 429 (rate limit).
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("telegram api returned status %d", resp.StatusCode)
	}
	return nil
}
