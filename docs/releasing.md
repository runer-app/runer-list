# Releasing

App and server are versioned separately: the app might be at 1.3.0 while the server is at 0.10.0. Only the git tags carry a prefix, so both can live in one repo. Users see `runer v1.3.0` and the image `runer-api:0.10.0`.

| Tag | What it releases |
|---|---|
| `app-vX.Y.Z` | Installers for macOS, Windows and Linux, as a draft GitHub release. Publishing the draft is what ships it to installed copies |
| `server-vX.Y.Z` | The Docker image `ghcr.io/runer-app/runer-api` as `X.Y.Z`, `X.Y` and `latest` |

Tag from an up-to-date `main`, then push the tags yourself:

```sh
make tag APP=1.3.0 SERVER=0.10.0      # either or both; checks you're on a clean, synced main
git push origin app-v1.3.0 server-v0.10.0
```

Releasing both at once is two tags on the same commit; each starts its own workflow. The image is out as soon as its job ends, while the app waits as a draft, so when the new app needs the new server, deploy the server first and then publish the app. Self-hosted servers update later, so the app has to keep working against the previous server. GitHub starts no workflows when more than three tags are pushed at once.

CI runs only the side whose files changed, and both when `protocol/` changes.

## One-time setup

What the maintainer does once, before the first app build is handed out. Nothing here costs money.

**Where the settings go.** On github.com, open the repository → **Settings** (the tab with the gear, at the top of the repository; you need to be its owner) → in the left column, **Secrets and variables** → **Actions**. The page has two tabs:

- **Secrets**, for what must stay hidden: **New repository secret** → *Name* and *Secret* → **Add secret**. Once saved, GitHub never shows it again, not even to you; you can only replace it.
- **Variables**, for what may be seen: **New repository variable** → *Name* and *Value* → **Add variable**.

Names must be typed exactly as below. To get a file's content into the clipboard for pasting, `pbcopy < <file>` in a terminal.

### 1. The updater key

**What it is.** A pair of keys that belongs to this app only, made on your own Mac with the Tauri tool. It has nothing to do with Apple, Microsoft or GitHub accounts, and it is free. The *private* half signs each release while the release workflow builds it. The *public* half is built into the app. When an installed copy finds an update, it checks the signature against the public half it carries, and installs the update only if it matches.

**Why it matters.** The updater downloads new code and runs it. Without the check, anyone who took over the update address (the update.runer.app redirect, or the GitHub repo) could ship their own build to every tester, and that build could read their keys and lists. With it, they would also need the private half.

**What happens if…**
- *it is lost:* installed copies refuse every update signed with a new key, for good. Every tester has to download and install the app again by hand, and then they're on the new key.
- *it leaks:* whoever has it can sign an update that installed copies accept, if they can also serve it from the update address. Make a new key, publish a release signed with the *old* key that carries the new public half (that moves everyone over), and tell testers what happened.
- *you move to a paid Apple account, or a Windows certificate:* nothing. The updater key is separate from both and stays the same.
- *the repo moves:* nothing; the key isn't tied to the repo. Only the secrets have to be set again in a new repository.

**Make it.**
- In a terminal of your own, from `app/` (it asks for a password twice; choose one and keep it):
  ```sh
  npm run tauri -w @runer/app -- signer generate -w ~/.tauri/runer-updater.key
  ```
  This writes two files: `~/.tauri/runer-updater.key`, the private half, and `~/.tauri/runer-updater.key.pub`, the public half. The public half isn't secret.
- Give the release build both halves, under Settings → Secrets and variables → Actions:
  - Variables tab: `UPDATER_PUBKEY`, with the content of the public half (`pbcopy < ~/.tauri/runer-updater.key.pub`, then paste).
  - Secrets tab: `TAURI_SIGNING_PRIVATE_KEY`, with the content of the private half (`pbcopy < ~/.tauri/runer-updater.key`, then paste).
  - Secrets tab: `TAURI_SIGNING_PRIVATE_KEY_PASSWORD`, with the password you chose.

  GitHub keeps secrets out of the build logs.
- Back up the private file and its password offline. The GitHub secret is not a backup: GitHub never hands it back. Keep the file as an attachment in a password manager and, as a second copy, on a USB stick in a drawer. The file is encrypted with its password, so keeping the password apart from the file (say, the password only in the password manager) means one stolen copy isn't enough. Then delete it from `~/.tauri`: only the release build signs. Never commit it, and never send it to anyone.

A build made without `UPDATER_PUBKEY` works, but has no updater: don't hand one out, since its users would never get a fix.

### 2. The macOS signing certificate

**What it is.** A self-signed code-signing certificate made in Keychain Access. Signing stamps the app with "made by the holder of this certificate". Nobody vouches for a self-signed one, unlike a paid Apple Developer ID, which Apple vouches for.

**Why it matters.** The app keeps its keys in the macOS keychain, and macOS lets an app read its keychain items without asking only while the app's signature stays the same. An unsigned build gets a new throwaway signature every time it is built, so after every update macOS sees a different app and asks for the password, once per item. Signed with the same certificate each time, "Always Allow" lasts from one release to the next.

**What it doesn't do.** It doesn't make macOS trust the app. Testers still get the warning on first install and use "Open Anyway" (System Settings → Privacy & Security), and the app can't be notarised. Only a paid Apple account removes that.

**Can other people use it?**
- *Testers:* yes, and they need nothing. The certificate is inside the app you give them; they don't install it and never see it.
- *Other developers building the app from source:* not yours, and they shouldn't have it. They sign their own builds with a self-signed certificate of their own ([app/README.md](../app/README.md), Commands), or don't sign and get the prompts. Their builds are a different app to the keychain, which is right: a build someone else made shouldn't read your testers' keys.
- *Why it stays private:* whoever holds it can sign a program that macOS treats as this app, and that program could read the app's keychain items on a tester's Mac without a prompt, if the tester runs it.

**What happens if…**
- *you move to a paid Apple account (Developer ID):* the app's signature changes to the new certificate. The first update signed with it makes macOS ask once more for each keychain item; testers click "Always Allow" and that's it. Nothing is lost: the items stay, only who may read them changes. From then on there's no "Open Anyway", since the app is notarised, and the self-signed certificate is no longer needed. The updater key stays the same (above).
- *it expires:* you made it for 10 years. A new one has the same effect as the move to Apple: one more round of prompts.
- *it is lost:* the same as expiry: make a new one, and testers get one more round of prompts.
- *it leaks:* make a new one and release with it (one round of prompts), and warn testers not to run builds from anywhere but the release page.

**Give it to the build.**
- Keychain Access → login → My Certificates → right-click your certificate → Export → save as `runer-signing.p12`, with a password. The file holds the private key as well as the certificate.
- Hand it to the release build, under Settings → Secrets and variables → Actions:
  - Variables tab: `MACOS_SIGNING_IDENTITY`, with the certificate's name.
  - Secrets tab: `MACOS_CERTIFICATE`, with the file as text: run `base64 -i runer-signing.p12 | pbcopy` where the file is, then paste.
  - Secrets tab: `MACOS_CERTIFICATE_PASSWORD`, with the password you gave the export.
- Back up the `.p12` and its password offline, then delete the exported file from the disk.
- For your own dev builds, add `export RUNER_SIGN_IDENTITY="<certificate name>"` to `~/.zshrc` ([app/README.md](../app/README.md), Commands).

Without these the macOS build is still made, signed ad hoc, and testers get the keychain prompts after each update.

### 3. The server the app suggests

- Variables tab: `RUNER_SERVER`, with the server's address (`https://…`), which must equal that server's `PUBLIC_URL`.
  The release build stops with a message when this is missing.

### 4. The update address

Installed copies ask `https://update.runer.app/latest.json` for updates (`UPDATE_FEED` in `.github/workflows/app-release.yml`). Each release has a `latest.json` among its files.

- Make `update.runer.app` redirect (301 or 302) to `https://github.com/runer-app/runer-list/releases/latest/download/latest.json`, with the registrar's URL forwarding or a redirect rule where runer.app is hosted. It must answer over HTTPS with a valid certificate (`.app` is HTTPS-only) and be a plain redirect, not masked or framed forwarding, which returns an HTML page the updater can't read. Check with `curl -sIL https://update.runer.app/latest.json`: a redirect, then `200` (`404` until the first app release is published). The `latest` link works only while app releases are the repo's only GitHub releases (the server publishes Docker images, not releases); if the server ever makes releases too, "latest" can be a server one, so point the redirect at the newest `app-v*` release instead.

Because the address is ours, the repo can move later: only the redirect changes. Changing `UPDATE_FEED` itself strands the copies already installed.

### 5. Making an app release

1. Set the new version in `app/apps/tauri/src-tauri/tauri.conf.json` (`version`), commit and push.
2. `make tag APP=0.1.0` at the root, then `git push origin app-v0.1.0`. The tag is only the trigger: the release is named after the version in the file.
3. The `app-release` workflow builds macOS, Windows and Linux and makes a **draft** release with the installers. To try a build without a tag: the repository's **Actions** tab → **app-release** → **Run workflow**.
4. Try the installers. Then publish the draft on GitHub: from that moment installed copies update to it at their next start.

What testers meet on first install, since nothing is signed by Apple or Microsoft: on macOS "Open Anyway" under System Settings → Privacy & Security; on Windows "More info" → "Run anyway".
