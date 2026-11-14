.PHONY: help install dev dev2 ios-dev ios-phone ios-log build-dev build-prod macos-icon design screenshots test

# API is the server the app talks to and must equal its PUBLIC_URL. dev and ios-dev fall back to the local compose
# stack; builds never do, so they need API set.
LOCAL_API := http://localhost:8080

# Dev builds are their own app, so they install and keep their data and keychain entries beside the release.
# Their icon carries a DEV band (img/runer-icon-macos-dev.svg; regenerate with `tauri icon`), and they leave out the
# release's macOS 26 icon (macos-icon), so macOS shows their icon.icns.
DEV_CONFIG = '{"identifier":"app.runer.dev","productName":"runer dev","bundle":{"icon":["icons-dev/32x32.png","icons-dev/128x128.png","icons-dev/128x128@2x.png","icons-dev/icon.icns","icons-dev/icon.ico"],"macOS":{"files":null,"infoPlist":null}}}'

# iOS gets its own dev server port, so it runs beside `make dev` (5173).
IOS_CONFIG = '{"build":{"devUrl":"http://localhost:5174","beforeDevCommand":"npm run dev -- --port 5174"}}'

# The paired iPhone's UDID, over USB or Wi-Fi.
PHONE = $$(xcrun devicectl list devices | sed -nE 's/.* ([0-9A-F-]+) \(UDID\) +(connected|available).*physical.*/\1/p' | head -1)

help: ## list targets (default)
	@grep -E '^[a-z0-9-]+:.*## ' $(MAKEFILE_LIST) | awk -F':.*## ' '{printf "  %-15s %s\n", $$1, $$2}'

install: ## install npm dependencies and the browsers the layout test runs in
	npm install
	npx playwright install chromium webkit

dev: ## desktop app with hot reload, against API (the local compose stack by default)
	VITE_RUNER_SERVER=$(or $(API),$(LOCAL_API)) npm run app:dev -- --config $(DEV_CONFIG)

dev2: ## a second copy beside a running dev, as another account (PROFILE=two); join with LINK='<invite link>'
	RUNER_PROFILE=$(or $(PROFILE),two) RUNER_LINK='$(LINK)' target/debug/runer-app

ios-dev: ## iOS app in the simulator, against API (the local compose stack by default); ARGS=--open for Xcode
	VITE_RUNER_SERVER=$(or $(API),$(LOCAL_API)) npm run tauri -w @runer/app -- ios dev $(ARGS) --config $(IOS_CONFIG)

ios-phone: ## build and install on the connected iPhone, against API (required: the phone can't reach localhost)
	VITE_RUNER_SERVER=$(API) npm run tauri -w @runer/app -- ios build --debug
	xcrun devicectl device install app apps/tauri/src-tauri/gen/apple/build/arm64/runer.ipa --device "$(PHONE)"

# Copying the file doesn't launch or attach to the app, so background refresh behaves as without it.
ios-log: ## print the debug log of the ios-phone build, copied from the phone
	@d=$$(mktemp -d) && trap 'rm -r $$d' EXIT && \
		xcrun devicectl device copy from --device "$(PHONE)" --domain-type appDataContainer --domain-identifier app.runer \
			--source "Library/Application Support/app.runer/debug.log" --destination $$d/log > /dev/null && \
		perl -MPOSIX -pe 's/^(\d+)\d{3} /strftime("%F %T ", localtime $$1)/e' $$d/log

build-dev: ## installers for this OS as the separate app "runer dev", against API (required)
	VITE_RUNER_SERVER=$(API) npm run app:build -- --config $(DEV_CONFIG)

build-prod: ## installers for this OS as the release app, against API (required)
	VITE_RUNER_SERVER=$(API) npm run app:build

# Built here and committed, so CI needs no particular Xcode: the bundle copies Assets.car in and names it in Info.plist.
macos-icon: ## rebuild the macOS 26 icon (Assets.car) from apps/tauri/src-tauri/icons/Icon.icon; needs Xcode 26 or newer
	o=$$(mktemp -d) && xcrun actool apps/tauri/src-tauri/icons/Icon.icon --compile $$o --app-icon Icon --include-all-app-icons \
		--platform macosx --target-device mac --minimum-deployment-target 10.13 --output-partial-info-plist $$o/p.plist \
		--errors --warnings > /dev/null && cp $$o/Assets.car apps/tauri/src-tauri/icons/Assets.car && rm -r $$o

design: ## the real UI on sample data at http://localhost:5199
	npm run design -w @runer/ui

screenshots: ## the marketing screenshots, from the design preview, into img/marketing/ (installs Playwright's chromium if missing)
	npx playwright install chromium
	npm run screenshots -w @runer/ui

test: ## everything CI runs: fmt, clippy, lint, types, unit tests, and the e2e suite against the real server (needs Docker and Go)
	cargo fmt --check
	cargo clippy --workspace --all-targets -- -D warnings
	npm run lint
	npm run typecheck
	npm test
	npm run test:layout -w @runer/ui
	e2e/against-server.sh
