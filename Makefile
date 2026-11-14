.PHONY: help install test test-app test-server check up down dev

help: ## list targets (default); app/ and server/ have their own Makefiles with more
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-12s %s\n", $$1, $$2}'

install: ## install the app's npm dependencies
	$(MAKE) -C app install

test: test-app test-server ## both sides' full suites, against the shared protocol/ data

test-app: ## fmt, clippy, lint, types, unit tests, and the e2e suite against the real server
	$(MAKE) -C app test

test-server: ## the server's unit and e2e tests (e2e needs Docker)
	$(MAKE) -C server test

check: test-app ## everything CI gates on, both sides
	$(MAKE) -C server check

up: ## the server and PostgreSQL in local Docker on :8080
	$(MAKE) -C server up

down: ## stop the local server stack
	$(MAKE) -C server down

dev: up ## the desktop app against the local server stack
	$(MAKE) -C app dev
