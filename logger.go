package memwatcher

import "go.uber.org/zap/zapcore"

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
