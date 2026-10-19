SHELL := /bin/bash

COMPOSE      := docker compose -f deploy/compose/docker-compose.yaml
KIND_CLUSTER := temporal-demo
KUBECTL      := kubectl --context kind-$(KIND_CLUSTER) -n temporal-demo
IMAGE        := temporal-demo:dev
API_PORT     ?= 8080
API          ?= http://localhost:$(API_PORT)

# Export to New Relic too when a license key is present.
ifneq ($(strip $(NEW_RELIC_LICENSE_KEY)),)
COLLECTOR_OVERLAY := newrelic
K8S_OVERLAY       := deploy/k8s/newrelic
else
COLLECTOR_OVERLAY := local
K8S_OVERLAY       := deploy/k8s/base
endif
export COLLECTOR_OVERLAY API_PORT

.PHONY: help test build run-worker run-api up down logs ps \
        kind-up kind-down k8s-deploy port-forward \
        demo-order demo-order-fail demo-expense demo-expense-small approve reject status

help: ## Show targets
	@grep -E '^[a-zA-Z0-9_-]+:.*?## ' $(MAKEFILE_LIST) | awk 'BEGIN{FS=":.*?## "}{printf "  %-20s %s\n",$$1,$$2}'

## --- Go ---------------------------------------------------------------
test: ## Run vet + unit tests
	go vet ./...
	go test ./...

build: ## Build binaries into ./bin
	go build -o bin/ ./cmd/...

run-worker: ## Run worker on the host (needs Temporal + collector, e.g. `make up`)
	OTEL_EXPORTER_OTLP_INSECURE=true go run ./cmd/worker

run-api: ## Run API on the host
	OTEL_EXPORTER_OTLP_INSECURE=true LISTEN_ADDR=:$(API_PORT) go run ./cmd/api

## --- Docker Compose ---------------------------------------------------
up: ## Start full stack locally (adds New Relic export if NEW_RELIC_LICENSE_KEY set)
	$(COMPOSE) up -d --build
	@echo "API: $(API)  Temporal UI: http://localhost:8233  Grafana: http://localhost:3000"

down: ## Stop local stack and delete volumes
	$(COMPOSE) down -v

logs: ## Tail worker + api logs
	$(COMPOSE) logs -f worker api

ps: ## Show local stack status
	$(COMPOSE) ps

## --- Kubernetes (kind) ------------------------------------------------
kind-up: ## Create kind cluster and deploy everything
	kind get clusters | grep -qx $(KIND_CLUSTER) || kind create cluster --config deploy/k8s/kind-config.yaml
	$(MAKE) k8s-deploy

kind-down: ## Delete the kind cluster
	kind delete cluster --name $(KIND_CLUSTER)

k8s-deploy: ## Build image, load into kind, apply manifests
	docker build -t $(IMAGE) .
	kind load docker-image $(IMAGE) --name $(KIND_CLUSTER)
	kubectl --context kind-$(KIND_CLUSTER) apply -f deploy/k8s/base/namespace.yaml
ifneq ($(strip $(NEW_RELIC_LICENSE_KEY)),)
	@$(KUBECTL) create secret generic newrelic \
		--from-literal=license-key="$$NEW_RELIC_LICENSE_KEY" \
		$${NEW_RELIC_OTLP_ENDPOINT:+--from-literal=otlp-endpoint="$$NEW_RELIC_OTLP_ENDPOINT"} \
		--dry-run=client -o yaml | $(KUBECTL) apply -f -
endif
	kubectl kustomize --load-restrictor LoadRestrictionsNone $(K8S_OVERLAY) | kubectl --context kind-$(KIND_CLUSTER) apply -f -
	$(KUBECTL) rollout restart deployment/worker deployment/api
	$(KUBECTL) rollout status deployment --timeout=180s

port-forward: ## Forward API :$(API_PORT), Temporal UI :8233, Grafana :3000
	@trap 'kill 0' EXIT; \
	$(KUBECTL) port-forward svc/api $(API_PORT):8080 & \
	$(KUBECTL) port-forward svc/temporal 8233:8233 & \
	$(KUBECTL) port-forward svc/lgtm 3000:3000 & \
	wait

## --- Demo scenarios (works against compose or port-forwarded kind) ------
ID ?= $(shell date +%s)

demo-order: ## Order saga, happy path (with random transient failures + retries)
	curl -s -XPOST $(API)/orders -d '{"order_id":"$(ID)","items":["widget"],"amount":42}'; echo

demo-order-fail: ## Order saga, payment declined -> compensation (FAIL=inventory|payment|shipping)
	curl -s -XPOST $(API)/orders -d '{"order_id":"$(ID)","amount":42,"fail_step":"$(or $(FAIL),payment)"}'; echo

demo-expense: ## Expense needing approval (TIMEOUT seconds, default 120)
	curl -s -XPOST $(API)/expenses -d '{"expense_id":"$(ID)","requester":"sam","amount":500,"timeout_seconds":$(or $(TIMEOUT),120)}'; echo

demo-expense-small: ## Expense under 100, auto-approved
	curl -s -XPOST $(API)/expenses -d '{"expense_id":"$(ID)","requester":"sam","amount":25}'; echo

approve: ## Approve expense ID=<id>
	curl -s -XPOST $(API)/expenses/$(ID)/approve -d '{"approver":"pat","comment":"looks good"}'; echo

reject: ## Reject expense ID=<id>
	curl -s -XPOST $(API)/expenses/$(ID)/reject -d '{"approver":"pat","comment":"missing receipt"}'; echo

status: ## Show workflow state, TYPE=orders|expenses ID=<id>
	curl -s $(API)/$(or $(TYPE),orders)/$(ID); echo
