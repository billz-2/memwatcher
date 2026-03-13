# oom-demo Makefile
# Загружает переменные из .env если файл существует.
# Скопируй .env.example в .env и заполни нужные значения.
-include .env
export

IMAGE     ?= oom-demo:latest
PORT      ?= 8080
CONTAINER ?= oom-demo
GOMEMLIMIT ?= 200MiB
MB        ?= 20

# BASE_URL для curl команд (локальный Docker)
BASE_URL  := http://localhost:$(PORT)

# Корень модуля memwatcher (два уровня вверх от examples/oom-demo)
REPO_ROOT := $(shell cd ../.. && pwd)

# DUMPS_DIR — локальная папка куда монтируются дампы из контейнера.
# После make run дампы доступны прямо на хосте: ls ./dumps/
DUMPS_DIR ?= $(CURDIR)/dumps

.PHONY: help build run stop logs status leak oom reset dumps \
        k8s-secret k8s-deploy k8s-status k8s-leak k8s-oom k8s-dumps k8s-logs k8s-clean \
        tidy

help:
	@echo "=== Local (Docker) ==="
	@echo "  make build              — собрать Docker образ"
	@echo "  make run                — запустить контейнер"
	@echo "  make stop               — остановить и удалить контейнер"
	@echo "  make logs               — показать логи контейнера"
	@echo ""
	@echo "  make status             — текущее состояние памяти"
	@echo "  make leak [MB=N]        — аллоцировать N MB (default: 20)"
	@echo "  make oom                — сценарий OOM: 10 x $(MB) MB до срабатывания memwatcher"
	@echo "  make reset              — освободить всю память"
	@echo "  make dumps              — список директорий с дампами"
	@echo ""
	@echo "=== Kubernetes ==="
	@echo "  make k8s-secret         — создать/обновить Secret с notifier-настройками"
	@echo "  make k8s-deploy         — задеплоить в k8s"
	@echo "  make k8s-status         — curl /status внутри pod"
	@echo "  make k8s-leak [MB=N]    — /leak внутри pod"
	@echo "  make k8s-oom            — сценарий OOM в k8s"
	@echo "  make k8s-dumps          — список дампов в pod"
	@echo "  make k8s-logs           — логи pod"
	@echo "  make k8s-clean          — удалить все k8s ресурсы"

# ── Build ──────────────────────────────────────────────────────────────────

build:
	docker build \
	  -f $(CURDIR)/Dockerfile \
	  -t $(IMAGE) \
	  $(REPO_ROOT)

# ── Local Docker ───────────────────────────────────────────────────────────

run: build
	@mkdir -p $(DUMPS_DIR)
	@docker rm -f $(CONTAINER) 2>/dev/null || true
	docker run -d \
	  --name $(CONTAINER) \
	  -p $(PORT):8080 \
	  -m 300m \
	  -e GOMEMLIMIT=$(GOMEMLIMIT) \
	  -e SERVICE_NAME=$(SERVICE_NAME) \
	  -e DUMP_DIR=/dumps \
	  -e POLL_INTERVAL=$(POLL_INTERVAL) \
	  -e SLACK_WEBHOOK_URL=$(SLACK_WEBHOOK_URL) \
	  -e TELEGRAM_BOT_TOKEN=$(TELEGRAM_BOT_TOKEN) \
	  -e TELEGRAM_CHAT_ID=$(TELEGRAM_CHAT_ID) \
	  -v $(DUMPS_DIR):/dumps \
	  $(IMAGE)
	@echo "→ running at $(BASE_URL)"
	@echo "→ dumps mounted at: $(DUMPS_DIR)"
	@echo "→ try: make status"

stop:
	docker rm -f $(CONTAINER) 2>/dev/null && echo "stopped" || echo "not running"

logs:
	docker logs -f $(CONTAINER)

# ── Demo actions (Local) ───────────────────────────────────────────────────

status:
	@curl -s $(BASE_URL)/status | python3 -m json.tool 2>/dev/null || curl -s $(BASE_URL)/status

leak:
	@curl -s -X POST "$(BASE_URL)/leak?mb=$(MB)"

# Сценарий OOM: постепенно аллоцируем память до срабатывания memwatcher.
# GOMEMLIMIT=200MiB → пороги: Tier1=140MB (70%), Tier2=160MB (80%), Tier3=180MB (90%)
# 10 итераций по 20MB = 200MB суммарно
oom:
	@echo "Starting OOM scenario: 10 x $(MB) MB (GOMEMLIMIT=$(GOMEMLIMIT))"
	@echo "Tier1 (70%) ≈ at step 5, Tier2 (80%) ≈ at step 6, Tier3 (90%) ≈ at step 8"
	@echo ""
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
	  echo -n "step $$i → "; \
	  curl -s -X POST "$(BASE_URL)/leak?mb=$(MB)"; \
	  sleep 1; \
	done
	@echo ""
	@echo "Check status:"
	@$(MAKE) status
	@echo ""
	@echo "Check dumps:"
	@$(MAKE) dumps

reset:
	@curl -s -X POST $(BASE_URL)/reset

dumps:
	@curl -s $(BASE_URL)/debug/dumps/ | python3 -m json.tool 2>/dev/null || curl -s $(BASE_URL)/debug/dumps/

# Скачать конкретный файл из дампа:
# make download DUMP=memdump-oom-demo-20260312-100523 FILE=heap.pprof
download:
	curl -s "$(BASE_URL)/debug/dumps/$(DUMP)/$(FILE)" -o $(FILE)
	@echo "saved: $(FILE)"
	@echo "analyze: go tool pprof $(FILE)"

# ── Kubernetes ─────────────────────────────────────────────────────────────

K8S_NS      := oom-demo
K8S_POD     := $(shell kubectl get pod -n $(K8S_NS) -l app=oom-demo -o jsonpath='{.items[0].metadata.name}' 2>/dev/null)
K8S_SVC_URL := $(shell kubectl get svc -n $(K8S_NS) oom-demo -o jsonpath='http://{.status.loadBalancer.ingress[0].ip}:{.spec.ports[0].port}' 2>/dev/null)

k8s-secret:
	kubectl create secret generic oom-demo-notifiers \
	  --namespace=$(K8S_NS) \
	  --from-literal=SLACK_WEBHOOK_URL="$(SLACK_WEBHOOK_URL)" \
	  --from-literal=TELEGRAM_BOT_TOKEN="$(TELEGRAM_BOT_TOKEN)" \
	  --from-literal=TELEGRAM_CHAT_ID="$(TELEGRAM_CHAT_ID)" \
	  --dry-run=client -o yaml | kubectl apply -f -

k8s-deploy:
	kubectl apply -f k8s/namespace.yaml
	$(MAKE) k8s-secret
	kubectl apply -f k8s/
	@echo "→ waiting for pod..."
	kubectl rollout status deployment/oom-demo -n $(K8S_NS) --timeout=60s

k8s-status:
	kubectl exec -n $(K8S_NS) $(K8S_POD) -- wget -qO- http://localhost:8080/status

k8s-leak:
	kubectl exec -n $(K8S_NS) $(K8S_POD) -- wget -qO- --post-data="" "http://localhost:8080/leak?mb=$(MB)"

k8s-oom:
	@echo "Starting OOM scenario in k8s pod $(K8S_POD)..."
	@for i in 1 2 3 4 5 6 7 8 9 10; do \
	  echo -n "step $$i → "; \
	  kubectl exec -n $(K8S_NS) $(K8S_POD) -- wget -qO- --post-data="" "http://localhost:8080/leak?mb=$(MB)"; \
	  sleep 1; \
	done

k8s-dumps:
	kubectl exec -n $(K8S_NS) $(K8S_POD) -- wget -qO- http://localhost:8080/debug/dumps/

k8s-logs:
	kubectl logs -n $(K8S_NS) -l app=oom-demo -f

k8s-clean:
	kubectl delete -f k8s/ --ignore-not-found

# ── Dev ────────────────────────────────────────────────────────────────────

tidy:
	cd $(REPO_ROOT) && go mod tidy
	go mod tidy
