.PHONY: help install test test-app test-server check up down dev dev-local tag

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

dev: ## the desktop app against the dev API
	$(MAKE) -C app dev

dev-local: up ## the desktop app against the local server stack
	$(MAKE) -C app dev-local

tag: ## tag HEAD of an up-to-date main as a release: make tag APP=1.3.0 SERVER=0.10.0 (either or both); push the tags yourself
	@test -n "$(APP)$(SERVER)" || { echo "usage: make tag APP=X.Y.Z SERVER=X.Y.Z (either or both)"; exit 1; }
	@for v in $(APP) $(SERVER); do echo "$$v" | grep -Eq '^[0-9]+\.[0-9]+\.[0-9]+$$' || { echo "$$v: want X.Y.Z, no v"; exit 1; }; done
	@test "$$(git branch --show-current)" = main || { echo "not on main"; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "working tree not clean"; exit 1; }
	@git fetch -q origin main && test "$$(git rev-parse HEAD)" = "$$(git rev-parse origin/main)" || { echo "main is not in sync with origin/main"; exit 1; }
	$(if $(APP),git tag -a app-v$(APP) -m "app v$(APP)")
	$(if $(SERVER),git tag -a server-v$(SERVER) -m "server v$(SERVER)")
	@echo "push with: git push origin$(if $(APP), app-v$(APP))$(if $(SERVER), server-v$(SERVER))"
