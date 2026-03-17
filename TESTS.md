# Tests

Запуск: `go test ./...`  
Запуск с race detector: `go test -race ./...`  
Запуск только unit: `go test .`  
Запуск только integration: `go test ./tests/`

## Текущая структура (v9)

Все unit-тесты в `package memwatcher` (белый ящик), интеграционные в `tests/` (`package tests`).

### Unit-тесты — `package memwatcher` (60 тестов)

| Файл | Тестов | Что проверяет |
|---|---|---|
| `watcher_test.go` | 4 | `New()` валидация (table-driven, 8 подтестов), дефолты Config, `registerCounter` без паники при дублировании |
| `heap_monitor_test.go` | 6 | Пороги Tier1/2/3 при GOMEMLIMIT=1000, порядок порогов, `Pct()` (0/25/50/100%), `Read()` без паники, inuse ≥ 0 |
| `cleanup_test.go` | 5 | MaxDumps=0/DumpTTL=0 (нет удалений), несуществующий dir, MaxDumps=2 (удаляет старые), DumpTTL (удаляет по времени), игнорирует не-memdump директории |
| `dump_test.go` | 5 | `writeAll` без/с CPU данными (файлы + валидный JSON), `writeFile` (успех, bad dir, перезапись) |
| `notifier_test.go` | 11 | Конструкторы Slack/Telegram (пустые поля), `Notify()` с `httptest.Server` (успех, 500, отменённый ctx), `NoopNotifier` |
| `server_test.go` | 8 | `ListHandler` (несуществующий dir → `[]`, фильтрация non-memdump), `DownloadHandler` (path traversal → 400, 404, успех, директория вместо файла), `NewDumpServer` |
| `profiler_test.go` | 7 | State machine (start→stop, start→snapshot, идемпотентность `ensureRunning`), 3 цикла последовательно, race test с 20 горутинами |
| `message_test.go` | 8 | `parseTemplate` (валидный/невалидный/пустой), `newMessageData` (конвертация байт→MB), рендеринг Slack/Telegram шаблонов, отсутствие Pyroscope-строки при пустом URL |
| `stats_test.go` | 6 | Маппинг полей MemStats, формула `LiveObjects = Mallocs - Frees`, Timestamp в RFC3339Nano, `GCPauseRecentNs` ≤ 10 элементов |

### Интеграционные тесты — `tests/` `package tests` (11 тестов)

| Тест | Группа | Что проверяет |
|---|---|---|
| `TestWatcher_Stop_TerminatesRun` | Lifecycle | `Stop()` завершает `Run()` за ≤ 1 сек |
| `TestWatcher_Stop_WritesShutdownDump` | Lifecycle | `Stop()` при heap ≥ Tier2 пишет финальный дамп |
| `TestWatcher_CtxCancel_TerminatesRun` | Lifecycle | `cancel()` завершает `Run()` за ≤ 1 сек |
| `TestWatcher_NoGomemlimit_ExitsImmediately` | Lifecycle | `Run()` выходит за ≤ 200ms без GOMEMLIMIT |
| `TestWatcher_WriteDump_CreatesExpectedFiles` | WriteDump | Создаётся `memdump-{svc}-{ts}/` с нужными файлами |
| `TestWatcher_WriteDump_CleanupBeforeWrite` | WriteDump | Старые дампы удаляются ДО записи нового (MaxDumps) |
| `TestWatcher_WriteDump_NotifiesAllNotifiers` | WriteDump | Все нотификаторы из `[]Notifier` получают `DumpNotification` |
| `TestWatcher_WriteDump_NotifierTimeout` | WriteDump | Медленный нотификатор не блокирует `WriteDump` (async) |
| `TestDumpServer_ServeHTTP_Routing` | DumpServer | Пустой path → ListHandler; непустой → DownloadHandler |
| `TestDumpServer_RegisterHandlers` | DumpServer | `GET /debug/dumps/` через `http.ServeMux` → 200 |
| `TestIntegration_WriteDump_ThenServe` | End-to-end | Файлы от `WriteDump` видны через `DumpServer.ListHandler` |

**Итого: 71 тест** (60 unit + 11 integration)

---

## Запланированная структура (v10)

Переход с `package memwatcher` (белый ящик) на `package memwatcher_test` (публичный API) + `export_test.go` bridge.

Подробнее: `jira_utils/epics/PD-2471/DEV-11039/solution_tests_export.md`

```
package memwatcher_test:   watcher, heap_monitor, cleanup, server, notifier, integration
package memwatcher:        dump, profiler, message, stats  (только детали реализации)
export_test.go:            HeapMonitor.Thresholds(), Limit(), Watcher.Cleanup()
```

Количество тестов не меняется. `tests/` директория удаляется — интеграционные тесты переедут в `integration_test.go` (`package memwatcher_test`).

---

## Точки входа публичного API

```
New(cfg Config) (*Watcher, error)
  (w *Watcher) Run(ctx context.Context)
  (w *Watcher) Stop()
  (w *Watcher) WriteDump(tier, reason string, heap *HeapMonitor) error

NewHeapMonitor(goMemLimit int64) *HeapMonitor
  (h *HeapMonitor) Read() (inuse uint64, tier HeapTier)
  (h *HeapMonitor) Pct(inuse uint64) float64

NewDumpServer(dumpDir string) *DumpServer
  (s *DumpServer) ServeHTTP(w http.ResponseWriter, r *http.Request)
  (s *DumpServer) ListHandler(w http.ResponseWriter, r *http.Request)
  (s *DumpServer) DownloadHandler(w http.ResponseWriter, r *http.Request)
  (s *DumpServer) RegisterHandlers(mux *http.ServeMux)

NewSlackNotifier(...) (*SlackNotifier, error)
NewTelegramNotifier(...) (*TelegramNotifier, error)
```
