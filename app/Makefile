.PHONY: help install dev dev-prod dev-local dev2 ios-dev ios-prod build-dev build-prod macos-icon design test

# Each must equal that server's PUBLIC_URL.
DEV_API   ?= https://api-dev.runer.app
PROD_API  ?= https://api.runer.app
LOCAL_API ?= http://localhost:8080

# Dev builds are their own app, so they install and keep their data and keychain entries beside the release.
# Their icon carries a DEV band (img/runer-icon-macos-dev.svg; regenerate with `tauri icon`), and they leave out the
# release's macOS 26 icon (macos-icon), so macOS shows their icon.icns.
DEV_CONFIG = '{"identifier":"app.runer.dev","productName":"runer dev","bundle":{"icon":["icons-dev/32x32.png","icons-dev/128x128.png","icons-dev/128x128@2x.png","icons-dev/icon.icns","icons-dev/icon.ico"],"macOS":{"files":null,"infoPlist":null}}}'

# iOS gets its own dev server port, so it runs beside `make dev*` (5173).
IOS_CONFIG = '{"build":{"devUrl":"http://localhost:5174","beforeDevCommand":"npm run dev -- --port 5174"}}'

help: ## list targets (default)
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-15s %s\n", $$1, $$2}'

install: ## install npm dependencies
	npm install

dev: ## desktop app with hot reload, against the dev API
	VITE_RUNER_SERVER=$(DEV_API) npm run app:dev -- --config $(DEV_CONFIG)

dev-prod: ## desktop app with hot reload, against the prod API
	VITE_RUNER_SERVER=$(PROD_API) npm run app:dev -- --config $(DEV_CONFIG)

dev-local: ## desktop app with hot reload, against the local compose stack
	VITE_RUNER_SERVER=$(LOCAL_API) npm run app:dev -- --config $(DEV_CONFIG)

dev2: ## a second copy beside a running dev*, as another account (PROFILE=two); join with LINK='<invite link>'
	RUNER_PROFILE=$(or $(PROFILE),two) RUNER_LINK='$(LINK)' target/debug/runer-app

ios-dev: ## iOS app in the simulator, against the dev API; ARGS=--open for Xcode
	VITE_RUNER_SERVER=$(DEV_API) npm run tauri -w @runer/app -- ios dev $(ARGS) --config $(IOS_CONFIG)

ios-prod: ## iOS app in the simulator, against the prod API; ARGS=--open for Xcode
	VITE_RUNER_SERVER=$(PROD_API) npm run tauri -w @runer/app -- ios dev $(ARGS) --config $(IOS_CONFIG)

build-dev: ## installers for this OS, against the dev API
	VITE_RUNER_SERVER=$(DEV_API) npm run app:build -- --config $(DEV_CONFIG)

build-prod: ## installers for this OS, against the prod API
	VITE_RUNER_SERVER=$(PROD_API) npm run app:build

# Built here and committed, so CI needs no particular Xcode: the bundle copies Assets.car in and names it in Info.plist.
macos-icon: ## rebuild the macOS 26 icon (Assets.car) from apps/tauri/src-tauri/icons/Icon.icon; needs Xcode 26 or newer
	o=$$(mktemp -d) && xcrun actool apps/tauri/src-tauri/icons/Icon.icon --compile $$o --app-icon Icon --include-all-app-icons \
		--platform macosx --target-device mac --minimum-deployment-target 10.13 --output-partial-info-plist $$o/p.plist \
		--errors --warnings > /dev/null && cp $$o/Assets.car apps/tauri/src-tauri/icons/Assets.car && rm -r $$o

design: ## the real UI on sample data at http://localhost:5199
	npm run design -w @runer/ui

test: ## everything CI runs: fmt, clippy, lint, types, unit tests, and the e2e suite against the real server (needs Docker and Go)
	cargo fmt --check
	cargo clippy --workspace --all-targets -- -D warnings
	npm run lint
	npm run typecheck
	npm test
	e2e/against-server.sh
