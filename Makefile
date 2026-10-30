.PHONY: build test check run compose-up compose-down local-env local-up local-down local-logs local-restart local-reset

DOCKER ?= docker
COMPOSE ?= $(DOCKER) compose
LOCAL_COMPOSE = $(COMPOSE) --env-file .env.local -f docker-compose.local.yml

build:
	go build -o bin/echo ./cmd/echo

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

local-down:
	$(LOCAL_COMPOSE) down

local-logs:
	$(LOCAL_COMPOSE) logs --follow echo mock-ai

local-restart: local-env
	$(LOCAL_COMPOSE) up --build --detach --wait --force-recreate echo

local-reset:
	$(LOCAL_COMPOSE) down --volumes --remove-orphans
