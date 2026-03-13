# memwatcher

Pre-OOM диагностика для Go сервисов. Следит за `HeapInuse` относительно `GOMEMLIMIT`, при приближении к OOM — пишет диагностические дампы на диск (PVC) с гарантией через `fsync`.

## Содержание

- [Требования](#требования)
- [Установка](#установка)
- [Быстрый старт](#быстрый-старт)
- [Пороги](#пороги)
- [Файлы дампа](#файлы-дампа)
- [Анализ дампов](#анализ-дампов)
- [HTTP endpoints](#http-endpoints)
- [Уведомления](#уведомления)
- [k8s конфигурация](#k8s-конфигурация)
- [Метрики Prometheus](#метрики-prometheus)
- [Config reference](#config-reference)
- [Examples](#examples)
- [Tests](#tests)

## Требования

- Go 1.21+
- `GOMEMLIMIT` должен быть выставлен (иначе ватчер не стартует)

## Установка

```bash
go get github.com/billz-2/memwatcher@v0.1.0
```

## Быстрый старт

```go
watcher, err := memwatcher.New(memwatcher.Config{
    ServiceName: "billz_auth_service",
    DumpDir:     "/dumps/billz_auth_service", // PVC mount
    Log:         log,                         // любой *zap.Logger
})
if err != nil {
    return fmt.Errorf("init memwatcher: %w", err)
}
go watcher.Run(ctx)
```

## Пороги

| Tier | % от GOMEMLIMIT | Polling | Действие |
|------|:-:|:-:|---|
| — | < 70% | 5s (базовый) | мониторинг без STW |
| 1 | ≥ 70% | 500ms | CPU profiler запущен |
| 2 | ≥ 80% | 500ms | дамп всех профилей (cooldown 5m) |
| 3 | ≥ 90% | 500ms | повторный дамп (cooldown 1m) |

При возврате ниже 70% — polling замедляется обратно до 5s, CPU профиль останавливается.

> Polling использует `runtime/metrics.Read()` — **без STW паузы** в каждом тике.
> `runtime.ReadMemStats()` (STW ~100μs) вызывается только при записи дампа.

## Файлы дампа

Каждый дамп сохраняется в `{DumpDir}/memdump-{service}-{timestamp}/`:

| Приоритет | Файл | Размер | Содержимое |
|:-:|------|:-:|-----------|
| 1 | `runtime_stats.json` | < 1 KB | Полный `runtime.MemStats` snapshot |
| 2 | `goroutines.pprof` | KB–MB | Stack traces всех горутин |
| 3 | `heap.pprof` | 1–20 MB | Heap profile |
| 4 | `allocs.pprof` | 1–20 MB | История аллокаций |
| 5 | `block.pprof` | KB–MB | Blocking profile |
| 6 | `mutex.pprof` | KB–MB | Mutex contention |
| 7 | `cpu.pprof` | 5–50 MB | CPU profile (только если был запущен) |

> Для полноценных block/mutex профилей сервис должен вызвать
> `runtime.SetBlockProfileRate(5)` и `runtime.SetMutexProfileFraction(5)`.

## Анализ дампов

```bash
# Heap: где живёт память
go tool pprof heap.pprof

# Горутины: goroutine leak
go tool pprof goroutines.pprof

# CPU: что делал сервис пока росла память
go tool pprof cpu.pprof

# Быстрый просмотр runtime_stats.json
cat runtime_stats.json | jq '{heap_inuse_bytes, live_objects_count, num_goroutines, gc_cpu_fraction}'
```

## HTTP endpoints

Добавить в роутер сервиса:

```go
dumpServer := memwatcher.NewDumpServer(cfg.MemWatcherDumpDir)

// gin пример:
dumpsGroup := router.Group("/debug/dumps")
dumpsGroup.Use(security.PprofAuthMiddleware(cfg)) // тот же токен что для /debug/pprof
dumpsGroup.GET("/", gin.WrapF(dumpServer.ListHandler))
dumpsGroup.GET("/*path", func(c *gin.Context) {
    dumpServer.DownloadHandler(c.Writer, c.Request)
})
```

### GET /debug/dumps/

Список всех дампов:

```json
[
  {
    "name": "memdump-billz_auth_service-20260311-100523",
    "created_at": "2026-03-11T10:05:23Z",
    "size_bytes": 24576000,
    "files": ["runtime_stats.json", "heap.pprof", "goroutines.pprof", "cpu.pprof"]
  }
]
```

### GET /debug/dumps/{dir}/{file}

Скачать файл дампа:

```bash
curl -H "Authorization: Bearer $TOKEN" \
  https://service/debug/dumps/memdump-billz_auth_service-20260311-100523/heap.pprof \
  -O
```

## Уведомления

### Slack

```go
slack, err := memwatcher.NewSlackNotifier(cfg.SlackWebhookURL) // из k8s Secret: SLACK_WEBHOOK_URL
if err != nil {
    return fmt.Errorf("init slack notifier: %w", err)
}

watcher, err := memwatcher.New(memwatcher.Config{
    ...
    Notifier: slack,
})
```

### Telegram

```go
tg, err := memwatcher.NewTelegramNotifier(cfg.TelegramBotToken, cfg.TelegramChatID)
if err != nil {
    return fmt.Errorf("init telegram notifier: %w", err)
}

watcher, err := memwatcher.New(memwatcher.Config{
    ...
    Notifier: tg,
})
```

> **Как получить ChatID:** добавить бота в чат/канал, отправить любое сообщение,
> открыть `https://api.telegram.org/bot{TOKEN}/getUpdates` — поле `chat.id`.

### Несколько каналов одновременно (MultiNotifier)

```go
// NewSlackNotifier / NewTelegramNotifier возвращают error при пустых полях —
// безопасно передавать nil если канал не настроен.
var slack, tg memwatcher.Notifier
if cfg.SlackWebhookURL != "" {
    slack, err = memwatcher.NewSlackNotifier(cfg.SlackWebhookURL)
    if err != nil { return err }
}
if cfg.TelegramBotToken != "" {
    tg, err = memwatcher.NewTelegramNotifier(cfg.TelegramBotToken, cfg.TelegramChatID)
    if err != nil { return err }
}

// nil-значения фильтруются автоматически
notifier := memwatcher.NewMultiNotifier(slack, tg)

watcher, err := memwatcher.New(memwatcher.Config{
    ...
    Notifier: notifier,
})
```

`MultiNotifier` вызывает все notifier'ы **параллельно** в рамках общего timeout 15s.
Ошибка одного не блокирует другие.

### Кастомный Notifier

```go
type MyNotifier struct{}

func (n *MyNotifier) Notify(ctx context.Context, d memwatcher.DumpNotification) error {
    // d.Service, d.TriggerReason, d.HeapInuseBytes, d.PctOfGoMemLimit,
    // d.DumpDirName, d.PyroscopeURL
    return nil
}
```

### Изоляция в тестах (Registerer)

```go
// В тестах передавайте изолированный registry чтобы избежать
// ошибки дублирования регистрации метрики между тест-кейсами.
watcher, err := memwatcher.New(memwatcher.Config{
    ServiceName: "test_service",
    Registerer:  prometheus.NewRegistry(), // изолированный registry
    Log:         zap.NewNop(),
})
```

## k8s конфигурация

### Переменные окружения

```yaml
env:
  - name: GOMEMLIMIT
    value: "480MiB"                          # ~94% от limits.memory
  - name: MEM_WATCHER_DUMP_DIR
    value: "/dumps/billz_auth_service"
  - name: PYROSCOPE_URL
    value: "http://pyroscope.observability.svc.cluster.local:4040"
  - name: PYROSCOPE_BASE_URL
    value: "https://pyroscope.observability.internal"
  # SLACK_WEBHOOK_URL и TELEGRAM_* — из k8s Secret (см. ниже)
```

### k8s Secret для уведомлений

```yaml
apiVersion: v1
kind: Secret
metadata:
  name: memwatcher-notifications
type: Opaque
stringData:
  SLACK_WEBHOOK_URL: "https://hooks.slack.com/services/..."
  TELEGRAM_BOT_TOKEN: "7123456789:AABBcc..."
  TELEGRAM_CHAT_ID: "-1001234567890"
```

### PVC для дампов

```yaml
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: billz-auth-service-dumps
  namespace: production
spec:
  accessModes:
    - ReadWriteOnce
  resources:
    requests:
      storage: 5Gi
```

```yaml
# В deployment:
volumeMounts:
  - name: dumps-volume
    mountPath: /dumps

volumes:
  - name: dumps-volume
    persistentVolumeClaim:
      claimName: billz-auth-service-dumps
```

> **Правило GOMEMLIMIT:** `limits.memory × 0.94` — оставляем 6% для non-Go overhead.
> Если выставить равным `limits.memory` — при OOM kill процесс не успеет сделать дамп.

## Метрики Prometheus

```
microservices_heap_dump_triggered_total{service, tier}
```

Пример запроса:
```promql
# Частота дампов за последний час
increase(microservices_heap_dump_triggered_total[1h])

# Алерт: более 3 дампов за 30 минут
increase(microservices_heap_dump_triggered_total[30m]) > 3
```

## Config reference

| Поле | Тип | Default | Описание |
|------|-----|---------|----------|
| `ServiceName` | string | — | Имя сервиса (обязательное) |
| `DumpDir` | string | `/tmp` | Директория для дампов (PVC mount) |
| `PollInterval` | Duration | `5s` | Базовый интервал проверки (< 70%), без STW |
| `CooldownTier2` | Duration | `5m` | Min интервал между дампами при ≥ 80% |
| `CooldownTier3` | Duration | `1m` | Min интервал между дампами при ≥ 90% |
| `Notifier` | Notifier | `NoopNotifier` | Уведомления после дампа |
| `PyroscopeBaseURL` | string | `""` | Базовый URL Pyroscope для ссылок |
| `Log` | Logger | stderr (zap) | Логгер совместимый с `*zap.Logger` |
| `Registerer` | prometheus.Registerer | `prometheus.DefaultRegisterer` | Registry для метрик (изолируй в тестах) |

## Examples

### [examples/oom-demo](examples/oom-demo/)

Полноценный пример демонстрирует работу memwatcher в контейнере с симуляцией утечки памяти.

**Что включено:**

```
examples/oom-demo/
  main.go          # HTTP сервер: /status, /leak?mb=N, /reset, /debug/dumps/
  Dockerfile       # multi-stage build (контекст = корень модуля)
  Makefile         # make run, make oom, make dumps, make k8s-deploy, ...
  k8s/
    namespace.yaml
    deployment.yaml  # GOMEMLIMIT=200MiB, limits: 350Mi
    service.yaml     # NodePort :30080
    secret.yaml      # шаблон для Slack/Telegram credentials
  .env.example     # переменные для notifiers
```

**Быстрый старт (локально):**

```bash
cd examples/oom-demo
cp .env.example .env
# Опционально: заполни SLACK_WEBHOOK_URL / TELEGRAM_BOT_TOKEN + TELEGRAM_CHAT_ID в .env

make run          # docker run -m 300m -e GOMEMLIMIT=200MiB
make oom          # 10 × 20 MB → trigger Tier2 (80%) и Tier3 (90%)
make dumps        # список директорий с дампами
make status       # текущий HeapInuse %
make reset        # освободить память
```

**С уведомлениями:**

```bash
make run \
  SLACK_WEBHOOK_URL=https://hooks.slack.com/services/... \
  TELEGRAM_BOT_TOKEN=123456:ABC \
  TELEGRAM_CHAT_ID=-100123456789
```

**Kubernetes (minikube / kind):**

```bash
# Задеплоить + создать Secret с notifier-настройками
make k8s-deploy \
  SLACK_WEBHOOK_URL=https://hooks.slack.com/... \
  TELEGRAM_BOT_TOKEN=123456:ABC \
  TELEGRAM_CHAT_ID=-100123456789

make k8s-oom      # симуляция OOM внутри pod
make k8s-dumps    # список дампов внутри pod
make k8s-logs     # логи pod
```

> **Почему `limits: 350Mi` > `GOMEMLIMIT: 200MiB`:**  
> Зазор ~150MB даёт Go runtime время сработать (Tier2 → Tier3 → запись дампов → уведомления)
> до того как k8s убьёт pod по `limits.memory`.

## Tests

46 unit-тестов, подробности в [TESTS.md](TESTS.md).

```bash
# Запуск
go test ./...

# С race detector
go test -race ./...
```
