.PHONY: build test check run compose-up compose-down local-env local-up local-observe local-down local-logs local-restart local-reset

DOCKER ?= docker
COMPOSE ?= $(DOCKER) compose
LOCAL_COMPOSE = $(COMPOSE) --env-file .env.local -f docker-compose.local.yml

build:
	go build -o bin/echo ./cmd/echo
	go build -o bin/echo-observer ./cmd/echo-observer
	go build -o bin/echo-docker-proxy ./cmd/echo-docker-proxy
	go build -o bin/echo-process-proxy ./cmd/echo-process-proxy

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
	@go build -o bin/echo-process-proxy ./cmd/echo-process-proxy
	@set -eu; \
		docker_host=$$($(DOCKER) context inspect --format '{{.Endpoints.docker.Host}}'); \
		case "$$docker_host" in \
			unix:///*) docker_socket=$${docker_host#unix://} ;; \
			*) echo "local-observe requires a local Unix-socket Docker endpoint" >&2; exit 1 ;; \
		esac; \
		ECHO_DOCKER_SOCKET="$$docker_socket" ECHO_DOCKER_PROXY_CONTAINERS=echo-local-test-mock-ai-1 ./bin/echo-docker-proxy & \
		docker_proxy_pid=$$!; \
		sleep 30 & \
		process_pid=$$!; \
		ECHO_PROCESS_PROXY_PROCESSES=sleep ./bin/echo-process-proxy & \
		process_proxy_pid=$$!; \
		trap 'kill "$$docker_proxy_pid" "$$process_proxy_pid" "$$process_pid" 2>/dev/null || true; wait "$$docker_proxy_pid" "$$process_proxy_pid" "$$process_pid" 2>/dev/null || true' EXIT INT TERM; \
		docker_ready=0; \
		process_ready=0; \
		for attempt in $$(seq 1 20); do \
			if curl --fail --silent --output /dev/null http://127.0.0.1:23750/healthz; then docker_ready=1; fi; \
			if curl --fail --silent --output /dev/null http://127.0.0.1:23751/healthz; then process_ready=1; fi; \
			if [ "$$docker_ready" = 1 ] && [ "$$process_ready" = 1 ]; then break; fi; \
			sleep 0.1; \
		done; \
		kill -0 "$$docker_proxy_pid"; \
		kill -0 "$$process_proxy_pid"; \
		kill -0 "$$process_pid"; \
		test "$$docker_ready" = 1; \
		test "$$process_ready" = 1; \
		ECHO_OBSERVER_WEBHOOK_SECRET=local-monitor-webhook-secret-with-32-chars go run ./cmd/echo-observer -config deploy/local-test/observer.json -once

local-down:
	$(LOCAL_COMPOSE) down

local-logs:
	$(LOCAL_COMPOSE) logs --follow echo mock-ai

local-restart: local-env
	$(LOCAL_COMPOSE) up --build --detach --wait --force-recreate echo

local-reset:
	$(LOCAL_COMPOSE) down --volumes --remove-orphans
