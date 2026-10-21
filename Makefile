.PHONY: help install test test-app test-server check up down dev dev-local versions bump-app bump-server

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

APP_CONF := app/apps/tauri/src-tauri/tauri.conf.json
APP_CARGO := app/apps/tauri/src-tauri/Cargo.toml
APP_VERSION = $(shell sed -n 's/^  "version": "\(.*\)",$$/\1/p' $(APP_CONF))
# The server's version is only its newest tag.
SERVER_VERSION = v=$$(git tag -l 'server-v*' --sort=-v:refname | head -1); echo "$${v\#server-v}"
# $(call next,PART) reads X.Y.Z and prints the bumped version; with no PART, all three
next = awk -F. -v p=$(1) '$$0 ~ /^[0-9]+\.[0-9]+\.[0-9]+$$/ { \
	a = $$1 "." $$2 "." $$3+1; b = $$1 "." $$2+1 ".0"; c = $$1+1 ".0.0"; \
	if (p == "patch") print a; else if (p == "minor") print b; else if (p == "major") print c; \
	else if (p == "") printf "patch %-9s minor %-9s major %s\n", a, b, c }'

define on_synced_main
	@test "$$(git branch --show-current)" = main || { echo "not on main"; exit 1; }
	@test -z "$$(git status --porcelain)" || { echo "working tree not clean"; exit 1; }
	@git fetch -q --tags origin main && test "$$(git rev-parse HEAD)" = "$$(git rev-parse origin/main)" || { echo "main is not in sync with origin/main"; exit 1; }
endef

versions: ## each side's current version and what bump-app / bump-server would make it; changes nothing
	@git fetch -q --tags origin
	@printf "app     %-9s " "$(APP_VERSION)"; echo "$(APP_VERSION)" | $(call next,)
	@cur=$$($(SERVER_VERSION)); printf "server  %-9s " "$$cur"; echo "$$cur" | $(call next,)

bump-app: ## bump the app's version (PART=patch, minor or major; patch by default), commit it and tag it; push yourself
	$(on_synced_main)
	@new=$$(echo "$(APP_VERSION)" | $(call next,$(or $(PART),patch))); \
	test -n "$$new" || { echo "can't bump $(APP_VERSION) by PART=$(PART): want patch, minor or major"; exit 1; }; \
	! git rev-parse -q --verify refs/tags/app-v$$new >/dev/null || { echo "app-v$$new already exists"; exit 1; }; \
	sed -i.bak 's/^  "version": "$(APP_VERSION)",$$/  "version": "'$$new'",/' $(APP_CONF) && \
	sed -i.bak 's/^version = "$(APP_VERSION)"$$/version = "'$$new'"/' $(APP_CARGO) && \
	rm $(APP_CONF).bak $(APP_CARGO).bak && \
	(cd app && cargo update --workspace --offline -q) && \
	git commit -q -m "app v$$new" $(APP_CONF) $(APP_CARGO) app/Cargo.lock && \
	git tag -a app-v$$new -m "app v$$new" && \
	echo "app $(APP_VERSION) -> $$new; push with: git push origin main app-v$$new"

bump-server: ## tag HEAD with the server's next version (PART=patch, minor or major; patch by default); push yourself
	$(on_synced_main)
	@t=$$(git tag --points-at HEAD 'server-v*'); test -z "$$t" || { echo "HEAD is already $$t"; exit 1; }
	@cur=$$($(SERVER_VERSION)); new=$$(echo "$${cur:-0.0.0}" | $(call next,$(or $(PART),patch))); \
	test -n "$$new" || { echo "can't bump $$cur by PART=$(PART): want patch, minor or major"; exit 1; }; \
	git tag -a server-v$$new -m "server v$$new" && \
	echo "server $$cur -> $$new; push with: git push origin server-v$$new"
