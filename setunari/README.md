# Setunari build of Chatto

This fork runs Setunari Chat (https://chat.setunari-group.com). Branch `setunari/main` is
upstream [chattocorp/chatto](https://github.com/chattocorp/chatto) release **v0.5.0** plus the
changes below. Deployment lives in the private repo `Setunari/chat` (`deploy/versions.env` pins the
commit of this branch that production runs).

## Changes

| Area | Files | What |
|---|---|---|
| Header search bar | `apps/frontend/src/lib/ui/AppHeader.svelte` | Slack-style "Search ⌘K" bar in the centre of the header (wide screens); opens the Quick Switcher. Narrow screens keep the icon. |
| Search all people + messages | `apps/frontend/src/lib/components/quickSwitcherModel.svelte.ts` | Ordinary Quick Switcher queries also ask each server's user directory (`UserService.ListUsers`) and message search; results: destinations, then people, then up to 5 messages. `?` still means messages only, `#` channels only. |
| Placeholder text | `apps/frontend/messages/en-GB/quick_switcher.json` | "Search channels, people, and messages..." (other locales keep upstream text). |
| AGPL source link | `apps/frontend/src/routes/chat/modals/AboutChattoModal.svelte` | About dialog links to this fork. |
| Image build | `setunari/Dockerfile` | Builds frontend + server from source, packaged like `docker/Dockerfile.goreleaser`. |
| Desktop app | `apps/desktop/build_profile.mjs`, `native/.../CaptureProbe.swift` | Release build is **Setunari Chat** (`mn.setunari.chatto`, data in `Setunari Chat Desktop`), installs next to the official Chatto app; no auto-update. Screen sharing excludes it like the official app. |

| Mac app download | `apps/frontend/src/lib/ui/AppHeader.svelte`, `messages/*/ui.json` (`ui.download_mac_app`) | **Get the Mac app** link in the header for Mac browsers (hidden in the desktop app and on narrow screens) → `https://chat.setunari-group.com/downloads/Setunari-Chat-mac.dmg`. |
| Default server | `apps/frontend/src/lib/setunari.ts`, `serverCatalogue.ts` (`ensureSetunariServer`), `routes/+page.ts` | A page without its own Chatto origin (the desktop app) starts with `https://chat.setunari-group.com` when no server is registered. |
| DMG | `apps/desktop/scripts/macos-dmg.mjs` | Packages the release profile (`Setunari Chat`). |
| Mac app CI | `.github/workflows/setunari-desktop.yml` | Signed and notarized production Mac build and GitHub Release. |

Search for `Setunari` in the source to find each change.

## Update to a new upstream release

```bash
git fetch upstream --tags
git checkout setunari/main
git merge vX.Y.Z            # resolve conflicts in the files above
mise test-frontend          # or at least: QuickSwitcher, AppHeader, ModalContainer specs
git push origin setunari/main
```

Then bump `CHATTO_REF` / `CHATTO_VERSION` in `Setunari/chat` `deploy/versions.env` and run its Deploy workflow.

## Build the Mac app

Production builds run on GitHub Actions (`.github/workflows/setunari-desktop.yml`, macOS runner):

```bash
gh workflow run setunari-desktop.yml -R Setunari/chatto -f version=0.5.0-setunari.N
```

The workflow accepts only commits on `setunari/main`. It tests the desktop shell, builds the app,
signs it with Developer ID, notarizes the app and the DMG, verifies both, and publishes the GitHub
Release `desktop-v0.5.0-setunari.N` with `Setunari-Chat-mac.dmg` and `SHA256SUMS`. Set `release=false`
to get only a workflow artifact. To put the DMG on the download link, run **Publish desktop app** in
`Setunari/chat` (see its `deploy/RUNBOOK.md` → "Desktop app downloads").

The signing secrets are in the `setunari-desktop-signing` environment of this repository:
`SETUNARI_MACOS_CERTIFICATE_BASE64` and `SETUNARI_MACOS_CERTIFICATE_PASSWORD` (Developer ID
Application `.p12`), and `SETUNARI_MACOS_NOTARY_API_KEY_BASE64`, `SETUNARI_MACOS_NOTARY_API_KEY_ID`,
`SETUNARI_MACOS_NOTARY_API_ISSUER_ID` (App Store Connect API key). The release tag does not start with
`v`, so the upstream `release` workflow does not start.

Local build (same result; signing settings in the gitignored `mise.local.toml`):

```bash
CHATTO_BUILD_VERSION=0.5.0-setunari.N CHATTO_FRONTEND_PRECOMPRESS=1 mise desktop-build
mise x -- pnpm --filter chatto-desktop build:macos-dmg --release
```

Without the signing variables the app is ad-hoc signed and other Macs must allow it once in System
Settings → Privacy & Security.

## Build the server image locally

```bash
docker build -f setunari/Dockerfile --build-arg VERSION=0.5.0-setunari.1 -t setunari-chatto:dev .
```
