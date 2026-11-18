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
| iPhone app | `apps/mobile/capacitor.config.json`, `ios/App/App.xcodeproj`, `ios/App/App/Info.plist` | iOS app is **Setunari Chat** (`mn.setunari.chatto.mobile`, team Vibe Pay LLC `83QS64JBQ3`). It keeps the upstream OAuth client and callback (`eu.chattocorp.chatto.mobile`), so sign-in works with the production server. It starts with the default server like the desktop app. |
| iPhone push | `apps/mobile/ios/App/App/App.entitlements`, `AppDelegate.swift`, `Info.plist` | Push Notifications capability (`aps-environment`), `remote-notification` background mode, and APNs token forwarding to `@capacitor/push-notifications` 8.1.3. Foreground notifications show a banner with sound and badge. |
| Android app | `apps/mobile/android/`, `apps/mobile/scripts/prepare.mjs`, `mise.toml` (`mobile-android-*`) | Android app is **Setunari Chat** (`mn.setunari.chatto.mobile`). Same frontend and the same upstream OAuth client and callback as the iPhone app; sign-in uses a Custom Tab. Firebase push plugin, notification channels `messages` and `calls`. |
| Native push and calls | `cli/internal/push/` (`apns.go`, `fcm.go`, `native.go`), `apps/frontend/src/lib/notifications/nativePushNotifications.svelte.ts`, `apps/mobile/ios/App/App/CallManager.swift`, `apps/mobile/android/.../ChattoMessagingService.java` | Server sends notifications through APNs and FCM ([FDR-056](../docs/fdr/FDR-056-native-push-notifications.md)); configure `[push.apns]` (`CHATTO_PUSH_APNS_*`, topics `mn.setunari.chatto.mobile`) and `[push.fcm]`. Direct-message calls ring like phone calls: CallKit on iOS, a full-screen call notification on Android, also when the app is closed. Turn on in the app: Settings → Your account → Notifications. |
| AD sync | `setunari/ad-sync/` | Deactivates Chatto accounts of people who are no longer current employees in Active Directory, and reactivates them when they return. Runs outside the server with a bot API key; see its [README](ad-sync/README.md). |

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

## Run the iPhone app on a device

Connect and unlock the iPhone (Developer Mode on). From the repository root:

```bash
mise mobile-ios-sync
cd apps/mobile
xcodebuild -project ios/App/App.xcodeproj -scheme Chatto -configuration Debug \
  -destination 'id=<device UDID>' -derivedDataPath ../../.context/mobile-ios-device \
  -allowProvisioningUpdates build
xcrun devicectl device install app --device <device UDID> \
  ../../.context/mobile-ios-device/Build/Products/Debug-iphoneos/Chatto.app
xcrun devicectl device process launch --device <device UDID> mn.setunari.chatto.mobile
```

`xcrun devicectl list devices` shows the UDID. You can also use `mise mobile-ios-open` and select Run in Xcode.

## Build and install the Android app

Install JDK 21 (mise installs it for the build task) and the Android SDK with platform 36. Set
`ANDROID_HOME` to the SDK, for example `/opt/homebrew/share/android-commandlinetools`. From the
repository root:

```bash
mise mobile-android-build
adb devices                     # the phone must show as "device" (USB debugging on)
adb install -r apps/mobile/android/app/build/outputs/apk/debug/app-debug.apk
adb shell am start -n mn.setunari.chatto.mobile/.MainActivity
```

`mise mobile-android-open` opens the project in Android Studio.

Push on Android needs Firebase. In the Firebase console, add an Android app with the package name
`mn.setunari.chatto.mobile` and put its `google-services.json` in `apps/mobile/android/app/`. The
file is not secret; it is not in the repository. Without it, the app builds and runs, but push
registration fails. The server needs a service account key of the same Firebase project.

A release build for other people needs a signing keystore. Keep it outside the repository.

## Push on the iPhone app

The Chatto target has the Push Notifications capability. Automatic signing with team `83QS64JBQ3`
adds it to the App ID `mn.setunari.chatto.mobile`. Debug builds use the APNs sandbox; an archive
for TestFlight or the App Store uses production. The server needs an APNs authentication key
(`.p8`, key ID, and team ID) from the same Apple team.
