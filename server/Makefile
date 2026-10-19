.PHONY: up down reset run build test test-unit test-e2e fmt vet lint vuln fuzz check tidy

DATABASE_URL ?= postgres://postgres:postgres@localhost:5432/runer?sslmode=disable
ORIGIN ?= tauri://localhost
PUBLIC_URL ?= http://localhost:8080
LISTEN_ADDR ?= :8080

## run: start the server against DATABASE_URL/ORIGIN (override on the command line, e.g. make run DATABASE_URL=...)
run:
	DATABASE_URL="$(DATABASE_URL)" ORIGIN="$(ORIGIN)" PUBLIC_URL="$(PUBLIC_URL)" LISTEN_ADDR="$(LISTEN_ADDR)" go run ./cmd/server

## build: compile the server binary to bin/server
build:
	go build -o bin/server ./cmd/server

## test: run everything (unit + e2e). e2e spins up real Postgres containers via testcontainers — needs Docker running.
test: test-unit test-e2e

## test-unit: run tests outside e2e/ (no Docker required)
test-unit:
	go test -race $(shell go list ./... | grep -v '/e2e$$')

## test-e2e: run the real-server, real-Postgres scenarios (needs Docker running)
test-e2e:
	go test -race -timeout 120s ./e2e/...

## fmt: format all Go source
fmt:
	gofmt -w .

## vet: go vet ./...
vet:
	go vet ./...

## lint: golangci-lint (code must pass gofmt and golangci-lint)
lint:
	golangci-lint run ./...

## vuln: report known vulnerabilities in code paths we actually call
vuln:
	go run golang.org/x/vuln/cmd/govulncheck@latest ./...

## fuzz: fuzz the membership-log parser/validator (FUZZTIME=30s by default)
FUZZTIME ?= 30s
fuzz:
	go test -run '^$$' -fuzz FuzzParseAndValidate -fuzztime $(FUZZTIME) ./internal/grouplog

## check: everything CI should gate on
check: fmt vet lint vuln test

## tidy: sync go.mod/go.sum with imports
tidy:
	go mod tidy

## up: build the compose stack (dev API on port 8080) and start it in local Docker; settings come from optional .env.local (ORIGIN, PUBLIC_URL, DB_PORT)
up:
	docker compose $(if $(wildcard .env.local),--env-file .env.local) up -d --build

## down: stop the local compose stack (data volume is kept)
down:
	docker compose down

## reset: stop the local compose stack, DELETE its database volume, and start it fresh (needed after editing an applied migration)
reset:
	docker compose down -v
	$(MAKE) up
