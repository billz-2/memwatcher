package memwatcher

import "go.uber.org/zap/zapcore"

// Пакет memwatcher реализует pre-OOM диагностику для Go сервисов.
//
// Механика работы (общая схема):
//
//  1. Watcher.Run() запускает цикл мониторинга памяти.
//  2. Каждые PollInterval секунд читает runtime.MemStats.HeapInuse.
//  3. Сравнивает HeapInuse с порогами (70/80/90% от GOMEMLIMIT):
//     - ≥70% → ускоряет polling до 5s, запускает CPU profiler (cpuProfiler).
//     - ≥80% → дополнительно пишет дамп всех профилей (dumper.writeAll).
//     - ≥90% → повторный дамп с более коротким cooldown.
//  4. writeDump создаёт директорию, заполняет её через dumper, инкрементирует
//     Prometheus метрику и запускает Notifier.Notify() в отдельной горoutine.
//  5. DumpServer отдаёт накопленные дампы по HTTP (/debug/dumps/).
//
// Связи между файлами:
//
//	config.go      — Config (настройки + дефолты)
//	logger.go      — Logger (интерфейс для логгера)
//	notifier.go    — Notifier / NoopNotifier / DumpNotification
//	slack_notifier.go — SlackNotifier (реализация Notifier через Slack webhook)
//	watcher.go     — Watcher (основной цикл), использует cpu.go, dump.go, stats.go
//	cpu.go         — cpuProfiler (управление runtime/pprof CPU профилем)
//	dump.go        — dumper (запись pprof файлов с fsync)
//	stats.go       — RuntimeStats + buildRuntimeStats (snapshot MemStats → JSON)
//	server.go      — DumpServer (HTTP хендлеры для просмотра/скачивания дампов)

// Logger — минимальный интерфейс логгера, совместимый с *zap.Logger.
//
// Использует zapcore.Field (а не interface{}) чтобы работать напрямую
// с полями zap без лишних аллокаций: zap.String(...), zap.Error(...),
// zap.Int64(...) возвращают zapcore.Field.
//
// Все сервисы (auth, gateway, user, payme) используют *zap.Logger,
// который автоматически удовлетворяет этому интерфейсу — никакой обёртки
// не требуется при вызове memwatcher.New(cfg).
type Logger interface {
	Info(msg string, fields ...zapcore.Field)
	Error(msg string, fields ...zapcore.Field)
}
