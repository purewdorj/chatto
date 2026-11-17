# Chatto Mobile

Experimental iOS and Android client. Capacitor embeds the shared static
frontend without a Chatto backend. Both platforms include the native push
plugin. The frontend registration for push and native calls are not part of
this package.

## Build and run on iOS

Install Xcode with an iOS Simulator runtime. From the repository root:

```sh
mise mobile-ios-build
mise mobile-ios-open
```

`mobile-ios-build` builds the frontend, copies its SPA shell and assets into the
iOS project, and builds an unsigned Simulator app under
`.context/mobile-ios-build/Build/Products/Debug-iphonesimulator/Chatto.app`.
`mobile-ios-open` refreshes the bundle and opens Xcode. Choose an iPhone Simulator
and select Run. For a physical device, select your development team in Xcode's
Signing & Capabilities pane. The project selects the ChattoCorp team by default;
contributors must select a team they can access. Signing secrets are not stored
in this repository.

Run `mise mobile-ios-sync` after frontend changes. Platform commands use the
`mobile-ios-*` and `mobile-android-*` namespaces. The iOS app serves bundled
assets at the stable `capacitor://localhost` origin. The Android app uses
`https://localhost`. Its persistent webview storage owns
server registrations, preferences, and renewable sessions. This prototype does
not add a separate Keychain credential store.

### Push on iOS

The app target has the Push Notifications capability (`App/App.entitlements`,
`aps-environment`) and the `remote-notification` background mode. Automatic
signing adds the capability to the App ID. Xcode sets the APNs environment to
`production` when you export an archive for TestFlight or the App Store. The
server needs an APNs authentication key of the same team. See
[FDR-056](../../docs/fdr/FDR-056-native-push-notifications.md).

## Build and run on Android

Install JDK 21 and the Android SDK (platform 36 and build tools). Set
`ANDROID_HOME` to the SDK. The `mobile-android-build` task gets JDK 21 through
mise. From the repository root:

```sh
mise mobile-android-build
adb install -r apps/mobile/android/app/build/outputs/apk/debug/app-debug.apk
```

`mobile-android-build` builds the frontend, copies it into the Android project,
generates the launcher and notification icons, and builds a debug APK.
`mise mobile-android-open` opens the project in Android Studio. Run
`mise mobile-android-sync` after frontend changes.

The Android shell does these steps:

- It shows the content edge to edge. The page receives the safe-area insets.
  The window moves the page above the keyboard.
- It sets the page scale to 1 and paints the window with the page background
  colour. The system launch screen stays until the page has had a paint
  opportunity, for a maximum of three seconds.
- Sign-in opens a Custom Tab. The callback activity accepts the
  `eu.chattocorp.chatto.mobile` scheme and returns the redirect to the
  `ChattoAuthorization` plugin. The plugin has the same validation, five-minute
  limit, and error messages as on iOS. When you close the tab, the attempt
  stops as cancelled.
- Calls and attachments use the WebView. Android asks for the camera and
  microphone permissions when a page requests them.
- It creates the notification channels `messages` (default importance) and
  `calls` (high importance, ringtone) at app start.
- Incoming calls: a high-priority FCM data message shows a full-screen call
  notification with a looping ringtone, also on the lock screen. Android 14
  and later need the full-screen intent permission (Settings > Apps > Setunari
  Chat). Without it, the call shows as a heads-up notification.

Release builds do not permit WebView debugging. Debug builds permit WebView
debugging and trust user-installed certificate authorities for a local
development server. Both builds permit only HTTPS. The app does not back up
its data.

### Calls on iOS

Incoming calls ring through PushKit and CallKit, also when the app is closed.
The server needs an APNs key (FDR-056). To test without APNs, a Debug build
has `ChattoCalls.simulateIncomingCall({ kind: 'call', callId, groupId,
serverOrigin, url, callerName, delaySeconds })` in the Web Inspector console.
After Answer on a locked phone, unlock and open the app to join the call.

### Push on Android

Push on Android uses Firebase Cloud Messaging. Put `google-services.json` from
the Firebase console, for the package name of the app, in
`apps/mobile/android/app/`. This file is not secret, but it is specific to one
Firebase project. Without the file, the app builds and runs, but push
registration fails. The app gets an FCM token only when the client registers
for push. Firebase Analytics is off.

A release APK or App Bundle needs your own signing key. This repository does
not contain signing keys.

## Connect a server

Use an HTTPS Chatto server that includes the mobile OAuth registration from
[PR #2456](https://github.com/chattocorp/chatto/pull/2456).
Existing releases without that registration cannot complete
mobile sign-in. The client identity is `eu.chattocorp.chatto.mobile`; its only callback is
`eu.chattocorp.chatto.mobile:/oauth/callback`. The native system authentication session
owns sign-in presentation. PKCE, state validation, token exchange, and session
registration remain in the shared frontend. Cancellation does not add a server.
The app does not bypass certificate validation or enable insecure HTTP access.

### Local development

Start `mise dev` in a separate terminal and use the HTTPS server URL that it
prints in the app's Add Server form. Keep that terminal running. The iOS app
contains a frontend bundle; it does not start the development server or load
frontend edits automatically. After frontend changes, run `mise mobile-ios-sync`
and run the app again in Xcode.

The Simulator must trust the certificate authority that issued the local
server certificate. A physical iPhone also needs a server address that it can
reach from its network; `localhost` on the phone refers to the phone itself.
Keep certificate validation enabled on both.

The client contacts only the servers selected by the user and the services
needed by those servers, such as identity providers, asset hosts, and LiveKit.
These systems can see the connecting device's IP address and the data needed
for the requested operation. When the client registers for push, the device
also contacts Apple Push Notification service (iOS) or Firebase Cloud
Messaging (Android) to get a device token. There is no analytics service.
Native bridge logging is disabled.

## Manual TestFlight updates

1. Check out the code to distribute and run `mise mobile-ios-open`. This rebuilds
   the frontend and copies it into the native project. Xcode alone does not
   rebuild the frontend. Sync also generates a 1024-pixel app icon without an
   alpha channel, as required for upload.
2. In Xcode, select the Chatto target and open General. Set Version to the intended
   release, such as `0.5.0`, and increase Build above the latest uploaded build.
3. Run the app on a physical device to check the changes.
4. Select Any iOS Device (arm64), then Product > Archive.
5. In Organizer, select the new archive, then Distribute App > App Store Connect.
   Use automatic signing and upload the build.
6. In App Store Connect > TestFlight, wait for processing, complete any required
   compliance information, and add test notes. Add the build to the internal
   tester group unless automatic distribution is enabled for that group.

External testing requires TestFlight App Review. Uploading a build does not
publish it on the public App Store. TestFlight builds expire after 90 days.
See [Apple's TestFlight guide](https://developer.apple.com/testflight/).

## Current limits

- The native push plugin is installed, but this package does not register for
  push. No incoming-call integration or share extension.
- No Play Store distribution or release automation for Android.
- Calls still use the shared web client. Continuing a call with the phone
  locked is a required follow-up, not a supported property of this build.
- No offline message store, automated release workflow, or in-app updater.
  TestFlight manages beta updates.
- Simulator and debug APK builds do not verify physical-device media, authentication
  with real providers, session restoration after an OS eviction, or networking
  across Wi-Fi and mobile data.

See [FDR-048](../../docs/fdr/FDR-048-chatto-mobile.md) and
[ADR-099](../../docs/adr/ADR-099-capacitor-mobile-client.md).
