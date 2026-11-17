# FDR-056: Native Push Notifications

**Status:** Experimental
**Last reviewed:** 2026-11-17

## Overview

A server can send notifications to native mobile apps through the Apple Push
Notification service (APNs) for iOS and Firebase Cloud Messaging (FCM) for
Android. The app registers the device token from the operating system with the
server. The server then sends the same notifications that Web Push sends
(FDR-013), and it also rings the other members of a direct message when a call
starts there (FDR-016). On iOS, a call rings through PushKit and CallKit. On
Android, a call push wakes the app, which shows a full-screen call
notification. A second push stops the ring when the call ends or when the
member joins it. Native push is independent of Web Push: a server can
use one, both, or neither. Native push requires operator configuration for
each provider.

## Behavior

- The app calls `PushNotificationService.RegisterNativeDevice` with the
  platform (APNs, APNs VoIP, or FCM), the device token, the app identifier (iOS bundle ID
  or Android package name), the APNs environment flag, an optional device
  name, and a random per-save cleanup token. `UnregisterNativeDevice` removes
  a device by token. Unregistering an unknown token succeeds.
- `ServerRuntimeConfig.native_push_platforms` lists the platforms that the
  server delivers to. An empty list means that the server cannot deliver
  native push. Older servers also send an empty list.
- The server accepts a registration only for an enabled platform
  (`FAILED_PRECONDITION` otherwise) and only for an app identifier in the
  operator allow-list of that platform (`INVALID_ARGUMENT` otherwise). APNs
  and APNs VoIP tokens must be hexadecimal. FCM tokens must contain only letters, digits,
  and `_`, `:`, `.`, `-`.
- An account can have up to 16 active native devices. This limit is separate
  from the Web Push subscription limit.
- A device token is active for only the account that most recently registered
  it on the server. When another account signs in on the same device and
  registers the same token, delivery moves to that account.
- A device stays registered for 180 days after its most recent save. The app
  saves the device again when it starts, at least once a day while it runs, and
  when the operating system issues a new token. Expired devices get no pushes,
  and each read of the account's devices removes them.
- The server removes a device when APNs answers `410`, `BadDeviceToken`,
  `Unregistered`, or `DeviceTokenNotForTopic`, or when FCM answers
  `UNREGISTERED`, `SENDER_ID_MISMATCH`, or `INVALID_ARGUMENT` that names
  `message.token`.
- A cancelled registration can be removed without an account session through
  `PushSubscriptionCleanupService.DeleteNativeDevice` with the device token and
  the cleanup token of that save. A later save replaces the cleanup token, so
  stale cleanup cannot remove it.
- Notification pushes use the same occurrence, policy, visibility, and Do Not
  Disturb checks as Web Push, immediately before the server contacts the
  provider. Once any browser subscription or native device accepts the
  occurrence, the server does not retry it for the other devices.
- APNs VoIP devices: the iOS app registers its PushKit token with the platform
  APNs VoIP and its bundle ID as the app identifier. The APNs topic allow-list
  applies to the bundle ID. The server sends to the APNs topic
  `<bundle ID>.voip`. These devices have the same token rules, limits,
  lifetime, owner claim, removal rules, and cleanup as APNs devices.
  `native_push_platforms` contains APNs VoIP when APNs is enabled.
- APNs VoIP devices receive only call pushes (`call` and `call_end`). iOS
  requires the app to report each VoIP push to CallKit, so message and test
  pushes never go to these devices.
- `SendTestNotification` sends to the caller's browser subscriptions and native
  devices, but not to APNs VoIP devices. It succeeds when one device accepts
  the notification.
- Incoming calls: when a member starts a call in a direct message, the server
  sends a `call` push to each other member who can join the call, is not in
  the call, and is not in Do Not Disturb. Channel calls do not ring. The server
  checks again that the call is active and that the recipient did not join
  immediately before it contacts the provider.
- A `call` push goes to every APNs VoIP and FCM device of the member. It goes
  to a plain APNs device only when the member has no APNs VoIP device with the
  same app identifier. Then it is a time-sensitive alert, as before.
- Before a `call` push to an APNs VoIP or FCM device, the server records the
  ring of that member for that call. The server sends a `call_end` push to the
  APNs VoIP and FCM devices of the member when the call ends (the last
  participant leaves) or when the member joins the call on any device. Each
  recorded ring gets at most one `call_end`. Plain APNs devices get no
  `call_end`. A ring record expires after 60 seconds, so a call that ends later
  sends no `call_end`.
- Each push has custom data: `kind` (`message`, `call`, `call_end`, or
  `test`), `serverOrigin`, `recipientId`, `url` (the same notification URL as
  Web Push, on the server's bundled app route), `tag`, and `groupId` (the room
  ID). Message pushes also have `notificationId` and `attentionLevel`. `call`
  and `call_end` pushes also have `callId`. A `call` push also has
  `callerName`, the display name of the member who started the call.
  `call_end` pushes have no `callerName`. Pushes set no app badge.
- APNs alerts: alert push type, priority 10, sound `default`, `thread-id` set
  to the room ID, a title and body, and `apns-collapse-id` set to the Web Push
  tag. Message pushes use the `active` interruption level and expire at the end
  of the two-minute notification delivery window. Call alerts use
  `time-sensitive` and expire after 30 seconds, the ring duration of the
  incoming-call banner. A device registered with the APNs environment flag
  receives pushes through the APNs development environment. This flag also
  applies to APNs VoIP devices.
- APNs VoIP: push type `voip`, priority 10, an `apns-expiration` 30 seconds
  after the send, no collapse ID, and an empty `aps` dictionary. The custom
  data keys are at the top level of the payload.
- FCM messages and test pushes: notification messages with a title and body,
  Android priority `HIGH`, sound `default`, notification tag set to the Web
  Push tag, channel `messages`, the same TTL rules as APNs, and
  `restricted_package_name` set to the registered app identifier.
- FCM `call` and `call_end` pushes: data-only messages with no notification
  block, Android priority `HIGH`, TTL 30 seconds, and
  `restricted_package_name`. Android always gives a data-only message to the
  app, so the app shows the full-screen call notification or stops the ring.
- When an operator sets `include_previews = false` for a provider, pushes
  through that provider contain no message preview, sender name, or room name.
  Message pushes show "New notification". Call alerts show "Incoming call",
  and `callerName` is "Incoming call". Test pushes keep their fixed text.
- Deleting an account removes its native devices together with its browser
  subscriptions, through the same durable cleanup and reconciliation.
- Server logs identify devices only by a hash of the token. Provider response
  bodies are not returned through the API or written to logs.

## External Systems And Privacy

Native push contacts systems outside the operator's control:

- **APNs** (`api.push.apple.com` and `api.sandbox.push.apple.com`) receives the
  device token, the bundle ID, the title, body, and custom data of each push,
  and the server's IP address. APNs pushes are not end-to-end encrypted, so
  Apple can read the notification content.
- **FCM** (`fcm.googleapis.com`) and **Google OAuth** (`oauth2.googleapis.com`)
  receive the registration token, the package name, the title, body, and
  custom data of each push, the service account identity, and the server's IP
  address. Google can read the notification content.

Web Push encrypts the content for the browser (FDR-013); native push does not.
Operators who do not accept this can disable previews per provider. The custom
data always contains opaque IDs and the server URL, but no message text.

## Design Decisions

### 1. Direct delivery to APNs and FCM

**Decision:** The server sends to APNs over HTTP/2 with token-based
authentication (an ES256 JWT from a `.p8` key), and to the FCM HTTP v1 API with
an OAuth 2.0 service account. There is no relay.
**Why:** Each operator controls its own provider credentials, the same as with
VAPID keys for Web Push. No third Chatto-operated system receives notification
content.
**Tradeoff:** Each operator who ships or supports a native app needs Apple and
Firebase credentials for that app's bundle ID or package name. A self-hosted
server can deliver only to apps whose signing team or Firebase project it has
credentials for.

### 2. Separate records with exclusive token ownership

**Decision:** Each native device is its own `RUNTIME_STATE` record. A separate
owner claim, protected with KV OCC and the record revision, makes the token
active for one account. Keys contain only an HMAC of the token. Native devices
share the claim protocol, the 180-day lifetime, and account-deletion cleanup
with Web Push subscriptions.
**Why:** The same phone can sign in to more than one account. The owner claim
prevents a previous account's pushes from reaching the device. Shared rules
keep both transports equally safe.
**Tradeoff:** Records of a previous account remain stored but inert until
cleanup or expiry.

### 3. Operator allow-list of app identifiers

**Decision:** A registration must name an app identifier that the operator
configured for the platform.
**Why:** APNs and FCM credentials belong to specific apps. A registration for
another app would always fail at the provider and could make the server send
pushes on behalf of an app that the operator does not support.
**Tradeoff:** Operators must update the allow-list when they add an app build
with a different identifier.

### 4. Remove only on proof that the token is invalid

**Decision:** The server removes a device only for provider answers that refer
to the token. FCM `INVALID_ARGUMENT` counts only when Google names
`message.token`.
**Why:** `INVALID_ARGUMENT` can also mean a bad message. A payload fault must
not remove every device.
**Tradeoff:** A token that FCM rejects with an unusual error stays registered
until it expires or the app registers again.

### 5. Best-effort incoming-call pushes

**Decision:** The replica that commits the start of a call sends the call
pushes in the background. There is no durable consumer and no retry.
**Why:** A ring is useful for only 30 seconds. A durable retry after a crash or
a provider outage would ring after the call is over.
**Tradeoff:** A replica crash just after the call starts, or a provider
failure, skips the ring for that call. Members still see the call in the app.

### 6. Visible alerts for messages, wake-up pushes only for calls

**Decision:** Message and test pushes are visible alerts. The server sends no
silent or background pushes for them, and no push to remove a message
notification. Only call pushes wake the app: VoIP pushes on iOS and data-only
messages on Android.
**Why:** Silent pushes need platform-specific background handling, and iOS
throttles them. Visible alerts work while the app is not running. A call must
ring like a phone call, also when the phone is locked and the app is closed.
Only PushKit with CallKit on iOS, and a high-priority data message with a
full-screen notification on Android, can do this.
**Tradeoff:** Reading a notification on another device does not remove the
system notification on the phone. The app can remove its delivered
notifications when it runs and confirms the read state, as Web Push clients do
(FDR-013, Decision 5). The apps must handle every call push correctly: iOS
stops VoIP delivery to an app that does not report a VoIP push to CallKit.

### 7. VoIP devices are separate registrations

**Decision:** The iOS app registers its PushKit token as a second device with
the platform APNs VoIP and the same bundle ID. The server derives the
`.voip` topic. A `call` push goes to the VoIP device instead of the APNs alert
device of the same app.
**Why:** PushKit tokens differ from alert tokens and need a different topic and
push type. One allow-list entry per app keeps operator configuration simple.
Without the rule, the phone would show the CallKit screen and a call alert.
**Tradeoff:** Each iPhone uses two of the 16 device slots of the account.

### 8. Ring records in runtime state stop rings across replicas

**Decision:** Before a `call` push to a device that can stop its ring, the
server stores a short-lived `call_ring` record. The replica that commits a
join or a call end removes the matching records with a revision-checked
delete, and only the replica whose delete succeeds sends `call_end`.
**Why:** The join or the call end can happen on a replica other than the one
that rang. The record lets any replica find the rings to stop. The
revision-checked delete makes sure that one replica sends each `call_end`. The
server never sends `call_end` to a member whose devices did not ring, which
iOS would otherwise show as a short CallKit call.
**Tradeoff:** Like the ring itself, `call_end` is best-effort. A replica crash
after the commit, or a provider failure, skips it. Then the device stops
ringing by its own timeout.

## Mobile App Behavior

- The shared frontend uses native push only when the host has the Capacitor
  `PushNotifications` plugin and the native shell supplies the app identity
  (`window.chattoNativeApp`). The browser and the desktop app continue to use
  Web Push.
- The iOS shell sets the APNs environment flag from the `aps-environment`
  value of its embedded provisioning profile. Builds without an embedded
  profile, such as App Store builds, use the production environment. Android
  always sends `false`.
- Notification settings show one native push control for all servers. Turning
  it on asks the operating system for permission when it has not decided yet,
  then registers the device with each signed-in server whose
  `native_push_platforms` contains the platform. Turning it off removes the
  device from those servers. The choice is kept on the device.
- The app saves registrations again when it starts, when it comes back to the
  foreground, and once an hour when the last save is a day old or failed.
  Signing out of a server or removing it first removes the device from that
  server.
- When the user taps a notification, the app finds the registered server
  through `serverOrigin` and opens the room, thread, and message from `url`.
  The app ignores a URL that has a different origin, a path outside the chat,
  or an unknown server.
- If the device cannot get a token, for example an Android build without
  Firebase configuration, settings show the reason and a retry action.

## Limits And Open Questions

- The iOS app must report each VoIP push to CallKit, also a `call_end` push
  for a call that it does not know, and then end that call immediately. The
  apps must stop ringing after 30 seconds without a `call_end`.
- The server sends no `call_end` when the member declines the call on the
  device; the app handles a decline locally.
- A plain APNs alert for a call stays visible after the call ends until the
  user dismisses it or the app removes it.
- Pushes set no app badge.
- The server does not check whether a token belongs to the APNs development
  environment; the app sends the flag. A wrong flag causes `BadDeviceToken`, and
  the server removes the device until the app registers again.
- Web Push does not ring for calls; only native devices receive call pushes.

## Permissions

There is no dedicated permission. The operating-system notification permission
and the device registration are the opt-in. Notification pushes need the same
conditions as Web Push (FDR-013). Call pushes need DM membership and the
`call.join` permission, evaluated without privileged mode.

## Related

- **FDRs:** FDR-012 (Notifications), FDR-013 (Web Push Notifications), FDR-016
  (Voice Calls), FDR-048 (Chatto Mobile)
- **ADRs:** ADR-036 (Runtime state KV boundary), ADR-076 (Deterministic
  notification occurrences), ADR-099 (Capacitor mobile client)
