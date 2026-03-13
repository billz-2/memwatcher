package memwatcher

import "context"

// DumpNotification содержит информацию о записанном дампе.
// Передаётся в Notifier.Notify() после каждого успешного writeDump.
//
// Собирается в watcher.go::writeDump() из:
//   - полей Config (ServiceName, PyroscopeBaseURL)
//   - runtime.MemStats (HeapInuseBytes)
//   - вычисленных значений (PctOfGoMemLimit, TriggerReason)
type DumpNotification struct {
	// Service — имя сервиса из Config.ServiceName.
	// Используется в тексте уведомления и в ссылке на Pyroscope.
	Service string

	// DumpDirName — имя директории дампа без полного пути.
	// Формат: "memdump-{ServiceName}-{timestamp}".
	// Позволяет получателю уведомления знать, где лежат файлы.
	DumpDirName string

	// TriggerReason — человекочитаемая причина дампа.
	// Пример: "heap_inuse >= 80% GOMEMLIMIT (83.2%)".
	TriggerReason string

	// HeapInuseBytes — значение HeapInuse в момент срабатывания порога.
	// Берётся из runtime.MemStats.HeapInuse, захваченных до writeDump.
	HeapInuseBytes uint64

	// PctOfGoMemLimit — процент использования GOMEMLIMIT.
	// Вычисляется как HeapInuse / GOMEMLIMIT * 100.
	// Удобен для отображения в уведомлении без лишних вычислений на стороне получателя.
	PctOfGoMemLimit float64

	// PyroscopeURL — прямая ссылка на профили в Pyroscope UI.
	// Формируется только если Config.PyroscopeBaseURL != "".
	// Пример: "https://pyroscope.internal/ui?query=billz_auth_service{}&from=now-5m&until=now".
	// Пустая строка если Pyroscope не настроен.
	PyroscopeURL string
}

// Notifier — интерфейс для отправки уведомлений после записи дампа.
//
// Вызывается из watcher.go::writeDump() асинхронно (в отдельной горутине)
// с context.WithTimeout(15s), чтобы медленный или упавший notifier
// не блокировал основной цикл мониторинга.
//
// Ошибка Notify() логируется и игнорируется — уведомление некритично,
// дамп уже записан на диск.
type Notifier interface {
	Notify(ctx context.Context, n DumpNotification) error
}

// NoopNotifier — заглушка Notifier, ничего не делает.
//
// Используется по умолчанию в Config.setDefaults() если Notifier не задан.
// Позволяет не делать nil-проверку при вызове Notifier.Notify() в watcher.go.
type NoopNotifier struct{}

func (NoopNotifier) Notify(_ context.Context, _ DumpNotification) error { return nil }
