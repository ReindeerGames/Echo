.PHONY: build test check run compose-up compose-down local-env local-up local-observe local-down local-logs local-restart local-reset

DOCKER ?= docker
COMPOSE ?= $(DOCKER) compose
LOCAL_COMPOSE = $(COMPOSE) --env-file .env.local -f docker-compose.local.yml

build:
	go build -o bin/echo ./cmd/echo
	go build -o bin/echo-observer ./cmd/echo-observer
	go build -o bin/echo-docker-proxy ./cmd/echo-docker-proxy

test:
	go test -race ./...

check:
	go vet ./...
	go test ./...

run:
	go run ./cmd/echo

compose-up:
	$(COMPOSE) up --build

compose-down:
	$(COMPOSE) down

local-env:
	@test -f .env.local || cp .env.local.example .env.local

local-up: local-env
	$(LOCAL_COMPOSE) up --build --detach --wait echo
	$(LOCAL_COMPOSE) run --rm seed

local-observe:
	@go build -o bin/echo-docker-proxy ./cmd/echo-docker-proxy
	@set -eu; \
		ECHO_DOCKER_PROXY_CONTAINERS=echo-local-test-mock-ai-1 ./bin/echo-docker-proxy & \
		proxy_pid=$$!; \
		trap 'kill "$$proxy_pid" 2>/dev/null || true; wait "$$proxy_pid" 2>/dev/null || true' EXIT INT TERM; \
		ready=0; \
		for attempt in $$(seq 1 20); do \
			if curl --fail --silent --output /dev/null http://127.0.0.1:23750/healthz; then ready=1; break; fi; \
			sleep 0.1; \
		done; \
		kill -0 "$$proxy_pid"; \
		test "$$ready" = 1; \
		ECHO_OBSERVER_WEBHOOK_SECRET=local-monitor-webhook-secret-with-32-chars go run ./cmd/echo-observer -config deploy/local-test/observer.json -once

local-down:
	$(LOCAL_COMPOSE) down

local-logs:
	$(LOCAL_COMPOSE) logs --follow echo mock-ai

local-restart: local-env
	$(LOCAL_COMPOSE) up --build --detach --wait --force-recreate echo

local-reset:
	$(LOCAL_COMPOSE) down --volumes --remove-orphans
