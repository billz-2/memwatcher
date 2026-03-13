# oom-demo

Пример приложения для демонстрации работы [memwatcher](../../README.md).

Запускается локально через Docker или деплоится в Kubernetes.
Симулирует утечку памяти через HTTP ендпоинт `/leak` — memwatcher срабатывает и пишет диагностические дампы.

## Быстрый старт

```bash
cd examples/oom-demo

# 1. Скопируй .env и при желании заполни notifiers
cp .env.example .env

# 2. Собери и запусти (GOMEMLIMIT=200MiB, docker limit 300MB)
make run

# 3. Смотри статус
make status

# 4. Сценарий OOM (10 x 20MB = 200MB → memwatcher сработает)
make oom

# 5. Смотри дампы
make dumps

# 6. Скачать heap профиль
make download DUMP=memdump-oom-demo-... FILE=heap.pprof
go tool pprof heap.pprof
```

## Полный сценарий запуска

> Локальный Docker, `GOMEMLIMIT=200MiB`. Занимает ~15 секунд.

### Шаг 1 — Подготовка и запуск

```bash
cp .env.example .env
make run
```

Ожидаемые логи контейнера (`make logs`):

```
starting oom-demo on :8080 (dump dir: /dumps)
GOMEMLIMIT: 200 MB
notifier: none configured (set SLACK_WEBHOOK_URL or TELEGRAM_BOT_TOKEN+TELEGRAM_CHAT_ID)
memwatcher started
{"level":"info","msg":"memwatcher: started","poll_interval":"500ms","fast_poll_interval":"500ms"}
```

### Шаг 2 — Проверяем исходное состояние

```bash
make status
```

```json
{
  "heap_inuse_bytes": 1540096,
  "heap_inuse_mb": 1,
  "gomemlimit_bytes": 209715200,
  "pct_of_gomemlimit": 0.73,
  "num_goroutines": 4,
  "total_leaked_mb": 0,
  "hint": "normal"
}
```

### Шаг 3 — Запускаем сценарий OOM

```bash
make oom
# 10 × 20 MB с паузой 1s между шагами (итого ~200 MB за 10 секунд)
```

При `POLL_INTERVAL=500ms` memwatcher успевает поймать каждый тир:

```
~7s  → 140 MB (70%) → Tier 1: CPU profiler запущен, poll = 500ms
~8s  → 160 MB (80%) → Tier 2: первый дамп
~9s  → 180 MB (90%) → Tier 3: второй дамп
```

### Шаг 4 — Смотрим дампы

```bash
make dumps
```

```json
[
  {
    "name": "memdump-oom-demo-20260312-100523",
    "created_at": "2026-03-12T10:05:23Z",
    "size_bytes": 7036,
    "files": ["allocs.pprof", "block.pprof", "cpu.pprof", "goroutines.pprof", "heap.pprof", "mutex.pprof", "runtime_stats.json"]
  },
  {
    "name": "memdump-oom-demo-20260312-100524",
    "created_at": "2026-03-12T10:05:24Z",
    "size_bytes": 11806,
    "files": ["allocs.pprof", "block.pprof", "cpu.pprof", "goroutines.pprof", "heap.pprof", "mutex.pprof", "runtime_stats.json"]
  }
]
```

Дампы также доступны напрямую на хосте в папке `./dumps/`.

### Шаг 5 — Анализ heap профиля

```bash
make download DUMP=memdump-oom-demo-20260312-100523 FILE=heap.pprof
go tool pprof heap.pprof
```

```
(pprof) top
Showing nodes accounting for 160MB, 99% of 161MB total
      flat  flat%   sum%        cum   cum%
    160MB  99.4%  99.4%     160MB  99.4%  main.leakHandler
```

Видно что `leakHandler` держит всю память.

```bash
# Быстрый просмотр ключевых метрик из runtime_stats.json
make download DUMP=memdump-oom-demo-20260312-100523 FILE=runtime_stats.json
cat runtime_stats.json | jq '{heap_inuse_mb: (.heap_inuse_bytes/1024/1024|floor), live_objects_count, num_goroutines, gc_cpu_fraction}'
```

```json
{
  "heap_inuse_mb": 161,
  "live_objects_count": 9,
  "num_goroutines": 5,
  "gc_cpu_fraction": 0.12
}
```

> `gc_cpu_fraction: 0.12` — GC занимает 12% CPU: heap pressure высокий.

### Шаг 6 — Сброс перед следующим прогоном

```bash
make reset
# → reset: freed N chunks, GC forced

make status
# hint: "normal", total_leaked_mb: 0
```

> **Важно:** всегда делай `make reset` после `make oom`.
> Пока память выше 90%, Tier 3 срабатывает повторно каждую минуту (`CooldownTier3=1m`) —
> это правильное поведение для продакшн, но в demo приводит к накоплению дампов.

### Одна команда вместо всего выше

```bash
make oom    # симуляция
make reset  # сброс памяти
```

---

## Структура

```
oom-demo/
  main.go          # HTTP сервер с memwatcher
  Dockerfile       # multi-stage build
  Makefile         # все команды
  .env.example     # шаблон переменных окружения
  k8s/
    namespace.yaml
    deployment.yaml  # GOMEMLIMIT=200MiB, limits: 350Mi
    service.yaml     # NodePort :30080
    secret.yaml      # шаблон Secret для Slack/Telegram
```

## HTTP ендпоинты

| Метод | Path | Описание |
|---|---|---|
| `GET` | `/status` | Текущий HeapInuse, % от GOMEMLIMIT, кол-во горутин |
| `POST` | `/leak?mb=N` | Аллоцировать N MB и удержать в памяти (default: 10) |
| `POST` | `/reset` | Освободить все удержанные аллокации + GC |
| `GET` | `/debug/dumps/` | Список директорий с дампами (JSON) |
| `GET` | `/debug/dumps/{dir}/{file}` | Скачать файл дампа |

## Локально (Docker)

### 1. Подготовка

```bash
cp .env.example .env
```

Опционально заполни `.env` для получения уведомлений:

```bash
SLACK_WEBHOOK_URL=https://hooks.slack.com/services/T00/B00/xxx
TELEGRAM_BOT_TOKEN=123456:ABC-xxx
TELEGRAM_CHAT_ID=-100123456789
```

### 2. Запуск

```bash
make run
```

Запускает контейнер с параметрами:
- `-m 300m` — hard limit Docker
- `GOMEMLIMIT=200MiB` — мягкий лимит Go runtime
- порт `8080` на локалхосте

### 3. Сценарий OOM

```bash
# Посмотреть текущее состояние памяти
make status

# Автоматический сценарий: 10 × 20 MB = 200 MB
# Tier1 (70%) срабатывает ~на 7-м шаге, Tier2 (80%) ~на 8-м
make oom

# Или вручную порциями
make leak MB=30
make leak MB=30
make leak MB=30
# ...

# Освободить память и начать заново
make reset
```

### 4. Просмотр результатов

```bash
# Список дампов
make dumps

# Скачать heap профиль конкретного дампа
make download DUMP=memdump-oom-demo-20260312-100523 FILE=heap.pprof

# Анализ
go tool pprof heap.pprof
```

### Вспомогательные команды

```bash
make logs    # логи контейнера
make stop    # остановить и удалить контейнер
```

## Kubernetes (minikube / kind)

### Требования

- `kubectl` настроен на нужный кластер
- Образ `oom-demo:latest` доступен в кластере:
  - **minikube:** `eval $(minikube docker-env) && make build`
  - **kind:** `make build && kind load docker-image oom-demo:latest`

### Деплой

```bash
# Собрать образ и задеплоить
make build
make k8s-deploy

# С notifiers (создаёт/обновляет Secret)
make k8s-deploy \
  SLACK_WEBHOOK_URL=https://hooks.slack.com/services/... \
  TELEGRAM_BOT_TOKEN=123456:ABC \
  TELEGRAM_CHAT_ID=-100123456789
```

### Сценарий OOM в кластере

```bash
make k8s-oom      # 10 × 20 MB внутри pod
make k8s-status   # текущий HeapInuse
make k8s-dumps    # список дампов внутри pod
make k8s-logs     # логи pod
```

### Доступ к сервису

```bash
# minikube
minikube service oom-demo -n oom-demo --url

# kind / kubeadm — NodePort 30080
curl http://<node-ip>:30080/status
```

### Удаление

```bash
make k8s-clean
```

## Пороги memwatcher при GOMEMLIMIT=200MiB

| Tier | Порог | ~MB | Действие |
|---|:-:|:-:|---|
| — | < 70% | < 140 MB | мониторинг каждые 30s |
| 1 | ≥ 70% | ≥ 140 MB | polling → 5s, CPU profiler запущен |
| 2 | ≥ 80% | ≥ 160 MB | **первый дамп** всех профилей |
| 3 | ≥ 90% | ≥ 180 MB | **повторный дамп** (cooldown 1m) |

> `limits.memory: 350Mi` > `GOMEMLIMIT: 200MiB` — зазор даёт Go runtime
> время записать дампы и отправить уведомления до OOM kill от k8s.

## Переменные окружения

| Переменная | Default | Описание |
|---|---|---|
| `SERVICE_NAME` | `oom-demo` | Имя в дампах и уведомлениях |
| `PORT` | `8080` | HTTP порт |
| `DUMP_DIR` | `/tmp/dumps` | Директория для дампов |
| `GOMEMLIMIT` | — | Мягкий лимит Go runtime (передаётся снаружи) |
| `SLACK_WEBHOOK_URL` | — | Slack webhook (опционально) |
| `TELEGRAM_BOT_TOKEN` | — | Telegram bot token (опционально) |
| `TELEGRAM_CHAT_ID` | — | Telegram chat ID (опционально) |

Если `SLACK_WEBHOOK_URL` и `TELEGRAM_*` не заданы — дампы всё равно пишутся,
уведомления просто не отправляются (`NoopNotifier`).
