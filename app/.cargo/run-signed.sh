#!/bin/sh
# Cargo's runner on macOS. The keychain lets an app read the items it stored only while its signature stays the
# same, and an unsigned build gets a new one each time it is compiled, so macOS asks for the password again after
# every rebuild. Signed with the same certificate each time (a self-signed one will do), "Always Allow" lasts.
# Set RUNER_SIGN_IDENTITY to the certificate's name; without it nothing is signed. Tests are never signed.
if [ -n "$RUNER_SIGN_IDENTITY" ] && [ "$(basename "$1")" = runer-app ]; then
  codesign --force --sign "$RUNER_SIGN_IDENTITY" --identifier app.runer.dev "$1" || echo "run-signed: not signed, the keychain will ask again" >&2
fi
exec "$@"
