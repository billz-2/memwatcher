package memwatcher

import "context"

// Notifier — интерфейс для отправки уведомлений после записи дампа.
//
// Вызывается из watcher.go::writeDump() асинхронно (в отдельной горутине)
// с context.WithTimeout(15s), чтобы медленный или упавший notifier
// не блокировал основной цикл мониторинга.
//
// Ошибка Notify() логируется и игнорируется — уведомление некритично,
// дамп уже записан на диск.
type Notifier interface {
	Notify(ctx context.Context, msg string) error
}

// NoopNotifier — заглушка Notifier, ничего не делает.
//
// Используется по умолчанию в Config.setDefaults() если Notifier не задан.
// Позволяет не делать nil-проверку при вызове Notifier.Notify() в watcher.go.
type NoopNotifier struct{}

func (NoopNotifier) Notify(_ context.Context, _ string) error { return nil }
