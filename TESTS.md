# Unit Tests

Запуск: `go test ./...`  
Запуск с race detector: `go test -race ./...`

## Покрытие (46 тестов)

| Файл | Тестов | Что проверяет |
|---|---|---|
| `watcher_test.go` | 10 | `New()` валидация (table-driven), дефолты, `registerCounter` без паники при дублировании |
| `message_test.go` | 9 | `parseTemplate` (валидный/невалидный/пустой), `newMessageData` (конвертация байт→MB), рендеринг Slack/Telegram шаблонов, отсутствие Pyroscope-строки при пустом URL |
| `notifier_test.go` | 10 | Конструкторы Slack/Telegram (пустые поля), `Notify()` с `httptest.Server` (успех, 500, отменённый ctx), `NoopNotifier` |
| `multi_notifier_test.go` | 7 | Фильтрация nil, агрегация ошибок через `errors.Join`, параллельность (два slow-notifier за < 180ms) |
| `server_test.go` | 7 | `ListHandler` (несуществующий dir → `[]`, фильтрация non-memdump), `DownloadHandler` (path traversal → 400, 404, успех, директория вместо файла) |
| `dump_test.go` | 5 | `writeAll` без/с CPU-данными, `writeFile` (успех, bad dir, перезапись) |
| `cpu_test.go` | 7 | State machine (start→stop, start→snapshot, идемпотентность), 3 цикла последовательно, race test с 20 горутинами |
| `stats_test.go` | 6 | Маппинг полей MemStats, формула `LiveObjects = Mallocs - Frees`, Timestamp в RFC3339Nano, `GCPauseRecentNs` ≤ 10 элементов |
