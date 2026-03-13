package memwatcher

import (
	"context"
	"errors"
	"sync"
)

// MultiNotifier рассылает уведомление всем notifier'ам параллельно.
//
// Используется когда нужно отправить уведомление одновременно
// в несколько каналов (например Slack + Telegram).
//
// Механика:
//   - Все notifier'ы вызываются в отдельных горутинах одновременно.
//   - Ошибки собираются через sync.Mutex и объединяются через errors.Join.
//   - Ошибка одного notifier'а не блокирует и не отменяет остальных.
//   - Возвращает nil только если все notifier'ы завершились без ошибок.
//
// Параллельный запуск важен потому что каждый Notify() делает HTTP запрос
// и ограничен timeout'ом ctx (15s из watcher.go). При последовательном вызове
// зависший Slack мог бы не оставить времени для Telegram.
//
// Пример использования:
//
//	notifier := memwatcher.NewMultiNotifier(
//	    &memwatcher.SlackNotifier{WebhookURL: cfg.SlackWebhookURL},
//	    &memwatcher.TelegramNotifier{BotToken: cfg.TelegramBotToken, ChatID: cfg.TelegramChatID},
//	)
//	watcher := memwatcher.New(memwatcher.Config{
//	    ...
//	    Notifier: notifier,
//	})
type MultiNotifier struct {
	// notifiers — список получателей. Заполняется в NewMultiNotifier,
	// после создания не изменяется — не требует дополнительной синхронизации.
	notifiers []Notifier
}

// NewMultiNotifier создаёт MultiNotifier из произвольного числа notifier'ов.
// nil-значения автоматически фильтруются — можно безопасно передавать
// условно инициализированные notifier'ы без предварительных nil-проверок.
//
// Если в результате фильтрации не осталось ни одного notifier'а —
// возвращает NoopNotifier, чтобы вызывающий код не получил пустой MultiNotifier
// который молча ничего не делает.
func NewMultiNotifier(notifiers ...Notifier) Notifier {
	filtered := make([]Notifier, 0, len(notifiers))
	for _, n := range notifiers {
		if n != nil {
			filtered = append(filtered, n)
		}
	}
	// Если после фильтрации ничего не осталось — возвращаем заглушку.
	// Это позволяет писать код без проверки len(notifiers) > 0:
	//   notifier := memwatcher.NewMultiNotifier(slack, telegram)
	//   // всегда можно передать в Config.Notifier
	if len(filtered) == 0 {
		return NoopNotifier{}
	}
	return &MultiNotifier{notifiers: filtered}
}

// Notify рассылает уведомление всем notifier'ам параллельно.
//
// Возвращает объединённую ошибку через errors.Join если один или несколько
// notifier'ов завершились с ошибкой. Вызывающий (watcher.go) логирует
// её целиком — в логе будут видны ошибки всех упавших notifier'ов.
func (m *MultiNotifier) Notify(ctx context.Context, n DumpNotification) error {
	var (
		mu   sync.Mutex
		errs []error
		wg   sync.WaitGroup
	)

	for _, notifier := range m.notifiers {
		wg.Add(1)
		// Копируем переменную в локальную область видимости горутины.
		// Без этого все горутины захватили бы одно и то же значение из цикла
		// (классическая ошибка замыкания в Go до 1.22).
		go func(nr Notifier) {
			defer wg.Done()
			if err := nr.Notify(ctx, n); err != nil {
				mu.Lock()
				errs = append(errs, err)
				mu.Unlock()
			}
		}(notifier)
	}

	// Ждём завершения всех горутин.
	// ctx с timeout 15s из watcher.go применяется к каждому Notify() — все
	// HTTP запросы прервутся автоматически при истечении timeout.
	wg.Wait()

	// errors.Join(nil, nil) == nil, поэтому при успехе всех notifier'ов
	// возвращаем nil без дополнительных проверок.
	return errors.Join(errs...)
}
