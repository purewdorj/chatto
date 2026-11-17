import AVFoundation
import Capacitor
import CallKit
import CryptoKit
import PushKit
import UIKit

/// The call fields that the web view needs to open or dismiss a call.
/// All values come from the VoIP push payload and are strings.
struct ChattoCallInfo {
    let callId: String
    let roomId: String
    let serverOrigin: String
    let url: String
    let callerName: String

    init(callId: String, payload: [AnyHashable: Any]) {
        func value(_ key: String) -> String { payload[key] as? String ?? "" }
        self.callId = callId
        roomId = value("groupId")
        serverOrigin = value("serverOrigin")
        url = value("url")
        callerName = value("callerName")
    }

    var jsObject: JSObject {
        [
            "callId": callId,
            "roomId": roomId,
            "serverOrigin": serverOrigin,
            "url": url,
            "callerName": callerName
        ]
    }
}

/// Owns PushKit VoIP registration and the CallKit provider for the app's lifetime.
///
/// `AppDelegate` creates the singleton at launch, so PushKit is registered before
/// the web view exists. iOS can launch the app in the background only to deliver a
/// VoIP push. Then no scene and no web view exist.
///
/// Invariants:
/// - Each VoIP push is reported to CallKit with `reportNewIncomingCall` before
///   the PushKit completion handler runs. iOS terminates apps that do not, and
///   stops VoIP delivery to apps that do so repeatedly. Pushes that do not start
///   a ringing call are reported and ended immediately.
/// - Push contents are never logged.
/// - All state is accessed on the main queue.
///
/// The web view does the actual call media through WebRTC in WKWebView. CallKit
/// only rings and records the user's decision. An answer is queued until the web
/// view is ready (see `ChattoCallsPlugin.getPendingAnswers`).
final class CallManager: NSObject {
    static let shared = CallManager()

    /// A ringing call that nobody answers ends after this interval.
    private static let ringTimeout: TimeInterval = 30
    /// Call IDs ended recently. A late or duplicate `call` push for one of these
    /// does not ring again.
    private static let endedMemory = 64

    private enum State {
        case ringing
        case answered
    }

    private struct Entry {
        let info: ChattoCallInfo
        var state: State
        var timeout: DispatchWorkItem?
    }

    private let registry: PKPushRegistry
    private let provider: CXProvider
    private var calls: [UUID: Entry] = [:]
    private var endedCallIds: [String] = []
    private var pendingAnswers: [ChattoCallInfo] = []

    /// The latest PushKit VoIP token as lowercase hex, or nil before registration.
    private(set) var voipToken: String?

    /// The plugin that forwards events to JavaScript. It exists only after the
    /// web view has loaded.
    weak var plugin: ChattoCallsPlugin?

    private override init() {
        registry = PKPushRegistry(queue: .main)
        provider = CXProvider(configuration: Self.providerConfiguration())
        super.init()
        provider.setDelegate(self, queue: .main)
        registry.delegate = self
        registry.desiredPushTypes = [.voIP]
    }

    /// Creates the singleton and registers for VoIP pushes.
    func start() {}

    private static func providerConfiguration() -> CXProviderConfiguration {
        let configuration = CXProviderConfiguration()
        configuration.maximumCallGroups = 1
        configuration.maximumCallsPerCallGroup = 1
        configuration.supportedHandleTypes = [.generic]
        configuration.supportsVideo = true
        configuration.includesCallsInRecents = false
        // A nil ringtone uses the system default ringtone.
        configuration.ringtoneSound = nil
        if let symbol = UIImage(
            systemName: "bubble.left.and.bubble.right.fill",
            withConfiguration: UIImage.SymbolConfiguration(pointSize: 28)
        ) {
            let size = CGSize(width: 40, height: 40)
            let image = UIGraphicsImageRenderer(size: size).image { _ in
                let rect = CGRect(
                    x: (size.width - symbol.size.width) / 2,
                    y: (size.height - symbol.size.height) / 2,
                    width: symbol.size.width, height: symbol.size.height
                )
                symbol.withTintColor(.black, renderingMode: .alwaysOriginal).draw(in: rect)
            }
            configuration.iconTemplateImageData = image.pngData()
        }
        return configuration
    }

    // MARK: - Calls

    /// A stable UUID for a server call ID, so later pushes and JavaScript
    /// requests for the same call find the same CallKit call.
    static func uuid(for callId: String) -> UUID {
        var bytes = Array(SHA256.hash(data: Data(callId.utf8)).prefix(16))
        bytes[6] = (bytes[6] & 0x0F) | 0x50 // Name-based version.
        bytes[8] = (bytes[8] & 0x3F) | 0x80 // RFC 4122 variant.
        return UUID(uuid: (
            bytes[0], bytes[1], bytes[2], bytes[3], bytes[4], bytes[5], bytes[6], bytes[7],
            bytes[8], bytes[9], bytes[10], bytes[11], bytes[12], bytes[13], bytes[14], bytes[15]
        ))
    }

    /// Reports a VoIP push payload to CallKit. `completion` runs after CallKit
    /// has accepted or rejected the report.
    func handlePush(_ payload: [AnyHashable: Any], completion: @escaping () -> Void) {
        let kind = payload["kind"] as? String
        guard let callId = payload["callId"] as? String, !callId.isEmpty else {
            reportEndedCall(callerName: "", completion: completion)
            return
        }
        let info = ChattoCallInfo(callId: callId, payload: payload)
        switch kind {
        case "call" where !endedCallIds.contains(callId):
            reportIncoming(info, completion: completion)
        case "call_end":
            remember(ended: callId)
            let uuid = Self.uuid(for: callId)
            if calls[uuid] != nil {
                end(uuid, reason: .remoteEnded)
                completion()
            } else {
                reportEndedCall(callerName: info.callerName, completion: completion)
            }
        default:
            reportEndedCall(callerName: info.callerName, completion: completion)
        }
    }

    private func reportIncoming(_ info: ChattoCallInfo, completion: @escaping () -> Void) {
        let uuid = Self.uuid(for: info.callId)
        if calls[uuid] != nil {
            // A duplicate push for a known call. iOS still requires a report;
            // CallKit rejects the duplicate UUID and the existing call continues.
            provider.reportNewIncomingCall(with: uuid, update: Self.update(info.callerName)) { _ in completion() }
            return
        }
        provider.reportNewIncomingCall(with: uuid, update: Self.update(info.callerName)) { [weak self] error in
            defer { completion() }
            guard let self, error == nil else { return }
            let timeout = DispatchWorkItem { [weak self] in
                guard let self, self.calls[uuid]?.state == .ringing else { return }
                self.end(uuid, reason: .unanswered)
            }
            self.calls[uuid] = Entry(info: info, state: .ringing, timeout: timeout)
            DispatchQueue.main.asyncAfter(deadline: .now() + Self.ringTimeout, execute: timeout)
        }
    }

    /// Satisfies the PushKit rule for a push that must not ring.
    private func reportEndedCall(callerName: String, completion: @escaping () -> Void) {
        let uuid = UUID()
        provider.reportNewIncomingCall(with: uuid, update: Self.update(callerName)) { [weak self] error in
            if error == nil {
                self?.provider.reportCall(with: uuid, endedAt: nil, reason: .remoteEnded)
            }
            completion()
        }
    }

    private static func update(_ callerName: String) -> CXCallUpdate {
        let update = CXCallUpdate()
        let name = callerName.isEmpty ? "Setunari Chat" : callerName
        update.remoteHandle = CXHandle(type: .generic, value: name)
        update.localizedCallerName = name
        update.hasVideo = false
        update.supportsHolding = false
        update.supportsGrouping = false
        update.supportsUngrouping = false
        update.supportsDTMF = false
        return update
    }

    /// Ends a known call without a user action, for example when the server
    /// reports the end or the call was answered on another device.
    private func end(_ uuid: UUID, reason: CXCallEndedReason) {
        guard let entry = calls.removeValue(forKey: uuid) else { return }
        entry.timeout?.cancel()
        remember(ended: entry.info.callId)
        pendingAnswers.removeAll { $0.callId == entry.info.callId }
        provider.reportCall(with: uuid, endedAt: nil, reason: reason)
    }

    private func remember(ended callId: String) {
        endedCallIds.removeAll { $0 == callId }
        endedCallIds.append(callId)
        if endedCallIds.count > Self.endedMemory {
            endedCallIds.removeFirst(endedCallIds.count - Self.endedMemory)
        }
    }

    // MARK: - JavaScript interface

    /// Ends the CallKit call for `callId` if one exists. A ringing call ends as
    /// answered elsewhere; an answered call ends as remotely ended.
    func endCall(callId: String) {
        let uuid = Self.uuid(for: callId)
        guard let entry = calls[uuid] else { return }
        end(uuid, reason: entry.state == .ringing ? .answeredElsewhere : .remoteEnded)
    }

    /// Returns and clears answers that JavaScript has not received yet.
    func takePendingAnswers() -> [ChattoCallInfo] {
        defer { pendingAnswers.removeAll() }
        return pendingAnswers
    }

    #if DEBUG
    /// Simulates a VoIP push without APNs. A delay lets the tester lock the
    /// phone or leave the app first; a background task keeps the app running.
    func simulatePush(_ payload: [AnyHashable: Any], delay: TimeInterval) {
        var task = UIBackgroundTaskIdentifier.invalid
        task = UIApplication.shared.beginBackgroundTask {
            UIApplication.shared.endBackgroundTask(task)
            task = .invalid
        }
        DispatchQueue.main.asyncAfter(deadline: .now() + max(0, delay)) { [weak self] in
            self?.handlePush(payload) {
                if task != .invalid { UIApplication.shared.endBackgroundTask(task) }
            }
        }
    }
    #endif
}

// MARK: - PushKit

extension CallManager: PKPushRegistryDelegate {
    func pushRegistry(_ registry: PKPushRegistry, didUpdate pushCredentials: PKPushCredentials, for type: PKPushType) {
        guard type == .voIP else { return }
        let token = pushCredentials.token.map { String(format: "%02x", $0) }.joined()
        voipToken = token
        plugin?.emitToken(token)
    }

    func pushRegistry(_ registry: PKPushRegistry, didInvalidatePushTokenFor type: PKPushType) {
        guard type == .voIP else { return }
        voipToken = nil
        plugin?.emitToken(nil)
    }

    func pushRegistry(_ registry: PKPushRegistry, didReceiveIncomingPushWith payload: PKPushPayload,
                      for type: PKPushType, completion: @escaping () -> Void) {
        guard type == .voIP else {
            completion()
            return
        }
        handlePush(payload.dictionaryPayload, completion: completion)
    }
}

// MARK: - CallKit

extension CallManager: CXProviderDelegate {
    func providerDidReset(_ provider: CXProvider) {
        for entry in calls.values { entry.timeout?.cancel() }
        calls.removeAll()
        pendingAnswers.removeAll()
    }

    func provider(_ provider: CXProvider, perform action: CXAnswerCallAction) {
        guard var entry = calls[action.callUUID] else {
            action.fail()
            return
        }
        entry.timeout?.cancel()
        entry.timeout = nil
        entry.state = .answered
        calls[action.callUUID] = entry
        action.fulfill()
        // The web view joins the call. When JavaScript is not listening yet
        // (for example after a background launch), queue the answer.
        if let plugin, plugin.hasListeners("callAnswered") {
            plugin.emit("callAnswered", entry.info)
        } else {
            pendingAnswers.append(entry.info)
        }
    }

    func provider(_ provider: CXProvider, perform action: CXEndCallAction) {
        guard let entry = calls.removeValue(forKey: action.callUUID) else {
            action.fulfill()
            return
        }
        entry.timeout?.cancel()
        remember(ended: entry.info.callId)
        pendingAnswers.removeAll { $0.callId == entry.info.callId }
        action.fulfill()
        plugin?.emit("callDeclined", entry.info)
    }

    // WebRTC runs inside WKWebView, which configures and activates its own
    // audio session. Do not change the session here: changes conflict with
    // WebKit's capture and playback.
    func provider(_ provider: CXProvider, didActivate audioSession: AVAudioSession) {}

    func provider(_ provider: CXProvider, didDeactivate audioSession: AVAudioSession) {}
}

/// Exposes CallKit incoming calls to the bundled frontend.
///
/// JavaScript name: `ChattoCalls`. Events: `voipToken` `{ token }`,
/// `callAnswered` (CallInfo) and `callDeclined` (CallInfo). Answers that occur
/// before a `callAnswered` listener exists are queued; read them with
/// `getPendingAnswers` after adding the listener.
@objc(ChattoCallsPlugin)
public class ChattoCallsPlugin: CAPPlugin, CAPBridgedPlugin {
    public let identifier = "ChattoCallsPlugin"
    public let jsName = "ChattoCalls"
    public let pluginMethods: [CAPPluginMethod] = {
        var methods: [CAPPluginMethod] = [
            CAPPluginMethod(name: "getVoipToken", returnType: CAPPluginReturnPromise),
            CAPPluginMethod(name: "getPendingAnswers", returnType: CAPPluginReturnPromise),
            CAPPluginMethod(name: "endCall", returnType: CAPPluginReturnPromise)
        ]
        #if DEBUG
        methods.append(CAPPluginMethod(name: "simulateIncomingCall", returnType: CAPPluginReturnPromise))
        #endif
        return methods
    }()

    override public func load() {
        DispatchQueue.main.async {
            CallManager.shared.plugin = self
        }
    }

    @objc func getVoipToken(_ call: CAPPluginCall) {
        DispatchQueue.main.async {
            call.resolve(["token": CallManager.shared.voipToken ?? NSNull()])
        }
    }

    @objc func getPendingAnswers(_ call: CAPPluginCall) {
        DispatchQueue.main.async {
            call.resolve(["calls": CallManager.shared.takePendingAnswers().map(\.jsObject)])
        }
    }

    @objc func endCall(_ call: CAPPluginCall) {
        guard let callId = call.getString("callId"), !callId.isEmpty else {
            call.reject("A call ID is required.")
            return
        }
        DispatchQueue.main.async {
            CallManager.shared.endCall(callId: callId)
            call.resolve()
        }
    }

    #if DEBUG
    /// Debug builds only. Takes the VoIP payload fields as the call options,
    /// plus an optional `delaySeconds`.
    @objc func simulateIncomingCall(_ call: CAPPluginCall) {
        var payload: [AnyHashable: Any] = [:]
        for key in ["kind", "callId", "groupId", "serverOrigin", "url", "recipientId", "callerName"] {
            if let value = call.getString(key) { payload[key] = value }
        }
        let delay = call.getDouble("delaySeconds") ?? 0
        DispatchQueue.main.async {
            CallManager.shared.simulatePush(payload, delay: delay)
            call.resolve()
        }
    }
    #endif

    func emitToken(_ token: String?) {
        notifyListeners("voipToken", data: ["token": token ?? NSNull()])
    }

    func emit(_ event: String, _ info: ChattoCallInfo) {
        notifyListeners(event, data: info.jsObject)
    }
}
