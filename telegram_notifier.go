package memwatcher

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
)

// TelegramNotifier реализует Notifier через Telegram Bot API.
// Использует только stdlib — никаких внешних зависимостей.
//
// Создавать только через NewTelegramNotifier — конструктор валидирует поля.
// Рендеринг сообщения выполняется Templator'ом (TelegramTemplator) до вызова Notify.
//
// Настройка:
//  1. Создать бота через @BotFather → получить botToken
//  2. Добавить бота в нужный канал/группу как администратора
//  3. Получить chatID: через @userinfobot или GET /getUpdates
type TelegramNotifier struct {
	botToken string
	chatID   string
	// baseURL — базовый URL Telegram Bot API. Default: "https://api.telegram.org".
	// Переопределяется в тестах для подстановки httptest.Server.
	baseURL string
}

// NewTelegramNotifier создаёт TelegramNotifier.
//
// Выполняет проверки при создании (не при отправке):
//  1. botToken не пустой — иначе Telegram API вернёт 401.
//  2. chatID не пустой — иначе Telegram API вернёт 400.
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
	return &TelegramNotifier{
		botToken: botToken,
		chatID:   chatID,
		baseURL:  "https://api.telegram.org",
	}, nil
}

// telegramSendMessageRequest — тело запроса к Telegram Bot API /sendMessage.
type telegramSendMessageRequest struct {
	ChatID string `json:"chat_id"`
	Text   string `json:"text"`
	// ParseMode — "HTML" выбран вместо "MarkdownV2" потому что MarkdownV2 требует
	// экранирования символов: . - ( ) % _ и т.д., что ломает форматирование путей и процентов.
	ParseMode string `json:"parse_mode"`
}

// Notify отправляет готовую строку msg в Telegram.
//
// Вызывается из watcher.go::notify() в горутине с context.WithTimeout.
// При ошибке возвращает её — логируется в watcher.go, не влияет на цикл мониторинга.
func (t *TelegramNotifier) Notify(ctx context.Context, msg string) error {
	body, err := json.Marshal(telegramSendMessageRequest{
		ChatID:    t.chatID,
		Text:      msg,
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
