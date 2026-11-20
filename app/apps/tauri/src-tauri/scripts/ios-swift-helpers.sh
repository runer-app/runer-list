#!/bin/sh
# Xcode 27 builds Swift packages in Release with their @_cdecl exports local,
# so the Rust side can't link to them. swift-rs 1.0.8 makes each package's own
# exports global again, but not those of its SwiftRs helper module, nor those of
# Tauri's own Swift package, which every plugin package embeds a copy of. This
# does the rest: in the built library it makes them global and weak, so the
# copies fold into one.
# Run by the Xcode project after "Build Rust Code"; needs `rustup component add llvm-tools`.
set -eu

lib="$1"
# SwiftRs helpers, then Tauri's exports (tauri mobile/ios-api).
helpers="_retain_object _release_object _string_from_bytes _data_from_bytes _log_stdout _register_plugin _on_webview_created _run_plugin_command"

[ -f "$lib" ] || exit 0
# Nothing to do where they are already global (Debug, older Xcode).
nm "$lib" 2>/dev/null | grep -Eq " t _(retain_object|release_object|string_from_bytes|data_from_bytes|log_stdout|register_plugin|on_webview_created|run_plugin_command)$" || exit 0

objcopy="$(rustc --print sysroot)/lib/rustlib/$(rustc -vV | sed -n 's/^host: //p')/bin/llvm-objcopy"
if [ ! -x "$objcopy" ]; then
  echo "error: llvm-objcopy not found; run: rustup component add llvm-tools" >&2
  exit 1
fi

args=""
for s in $helpers; do
  args="$args --globalize-symbol=$s --weaken-symbol=$s"
done
# shellcheck disable=SC2086
"$objcopy" $args "$lib"
