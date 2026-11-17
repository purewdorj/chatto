# FDR-048: Chatto Mobile

**Status:** Experimental
**Last reviewed:** 2026-11-17

## Overview

Chatto Mobile is an experimental iOS and Android application that bundles the
shared Chatto client. It connects to independent servers without running a
local backend.

## Behavior

- The app presents the shared multi-server interface and preferences.
- Native iOS defaults to Flat surface depth. A saved depth choice takes
  precedence. Browser and PWA defaults are unchanged.
- The web page paints the top iOS safe area with the client frame colour and
  the bottom safe area with the inner background colour. Native content
  insets are disabled; the shared layout reserves the top inset and
  the iOS shell adds bottom padding for the home indicator.
- The mobile sidebar columns and backdrop start below the toolbar and top
  safe area, and end above the home-indicator safe area.
- The iOS launch screen shows the muted CHATTO wordmark with system light or
  dark colours. The shell keeps this view visible until the bundled HTML has
  had a rendering opportunity, then fades into the web loading screen without
  a minimum display time. Reduced Motion disables the fade. The saved client
  theme takes effect when the web content loads.
- The iOS shell fixes the page scale at 1 to prevent gesture and input-focus
  zoom from cropping the interface. Page zoom is unavailable in this shell.
- The iOS keyboard hides the form-navigation accessory bar. UIKit's keyboard
  layout guide positions the webview above the keyboard during its movement;
  the plugin's delayed frame resizing is disabled. The shared client adapts to
  the smaller viewport so the composer remains visible.
- Native iOS resizing keeps the app and room headers visible. The native
  window follows the web background colour, including behind the keyboard's
  rounded corners. The Safari keyboard workaround does not run in the iOS app.
- Server sign-in opens the system authentication session. The selected server
  owns sign-in and consent. Cancellation leaves the client in place.
- Server registrations and sessions use persistent application webview storage.
- The shared client keeps no chat data in webview storage. It loads chat data
  from the server after it verifies the session. It does not provide offline
  reading or message sending.
- Sign-in requires HTTPS and a server with the mobile client registration.
- Registered servers recover from failed startup discovery or session loading
  through the shared retry loop. Returning to the foreground triggers an
  immediate retry. Network failure does not start a new sign-in flow.
- The Android app shows the content edge to edge. The page receives the
  safe-area insets, and the window moves the page above the keyboard. The
  shell applies the same page scale and bottom safe-area styles as on iOS.
  The window and the system bars follow the web background colour. The system
  launch screen stays until the bundled page has had a paint opportunity, for
  a maximum of three seconds.
- Android sign-in opens a Custom Tab. A callback activity accepts the mobile
  callback scheme. The plugin has the same JavaScript interface, input and
  callback validation, single active attempt, five-minute limit, and error
  messages as on iOS. When the user closes the tab, the attempt stops as
  cancelled.
- The Android WebView asks for camera and microphone permissions when a page
  requests media, and supports file selection for attachments. The app permits
  only HTTPS and does not back up its data. Release builds do not permit
  WebView debugging.
- Both platforms include the Capacitor push plugin. iOS has the push
  entitlement and the `remote-notification` background mode, and shows
  foreground notifications as a banner with sound and badge. Android creates
  the notification channels `messages` and `calls` at app start and gets an
  FCM token only when the client registers. Registration and delivery are in
  FDR-056.
- Incoming direct-message calls ring natively, also when the app is closed or
  the phone is locked. iOS reports a PushKit VoIP push to CallKit, which rings
  with the system ringtone. Android shows a full-screen call notification on
  the `calls` channel with a looping ringtone; Android 14 and later need the
  full-screen intent permission, else the call shows as a heads-up
  notification. The `ChattoCalls` plugin gives the shared frontend the iOS
  VoIP token and the user's Answer or Decline. After Answer, the frontend
  joins the call when the app is in front, because the web view can use the
  microphone only then. On a locked phone, the user unlocks to join.
- While the app is open, the in-app call banner rings through the
  `ChattoRingtone` plugin: a native alert sound on iOS and the device
  ringtone on Android. Both follow the device's silent setting. Web Audio in
  the web view cannot start a ring without user interaction.
- Decline is local to the device. The server does not get it, so the caller's
  ring stops only at its time limit. Continuing calls with the phone locked
  remains a requirement for a later stage.
- iOS distribution is through local Xcode builds and manual TestFlight
  uploads. Android distribution is through debug APKs. Public store
  distribution and release automation are deferred.

## Design Decisions

### 1. Share the frontend and add narrow native capabilities

**Decision:** Bundle the existing frontend with Capacitor, starting with iOS.
Use native system authentication as the first host capability.
Keep the iOS Flat surface default separate from native capability detection;
it changes presentation and does not grant access to a native operation.
**Why:** This retains the shared interface and session behaviour while allowing
later native media support. See ADR-072 and ADR-099.
**Tradeoff:** WebKit behaviour still applies to the shared interface. Native
integration requires device verification in addition to browser tests.

### 2. Keep the first implementation small

**Decision:** Retain webview session storage. Defer native call ownership, a
native offline store, and public store distribution. Add native push plumbing
only; the server and client parts are in FDR-056.
**Why:** First establish a working shell and authentication boundary.
**Tradeoff:** This is a development prototype, not a complete mobile release.

### 3. Port the iOS shell to Android with the same plugin contract

**Decision:** Add an Android app with the same application identity and the
same `ChattoAuthorization` JavaScript interface. Use a Custom Tab and a
callback activity for system sign-in. Keep the official OAuth client and
callback.
**Why:** The shared frontend detects the plugin and needs no Android-specific
sign-in code. The server accepts only the registered mobile client.
**Tradeoff:** Another app can also register the callback scheme. The frontend
state and PKCE checks keep a redirect to another app unusable. If Android
stops the app process during sign-in, the user must sign in again.

## Related

- **ADRs:** ADR-072, ADR-099, ADR-107
- **FDRs:** FDR-016, FDR-023, FDR-034, FDR-056

## Open Questions

- Native media ownership for locked-phone calls, audio routing, and interruptions.
- Credential storage and recovery after OS process termination.
- Mobile release automation and store distribution.
