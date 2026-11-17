package mn.setunari.chatto.mobile;

import android.content.Intent;
import com.getcapacitor.JSObject;
import java.util.Map;

/**
 * Immutable description of one incoming call, as the server sends it in a
 * data-only FCM call push. The JavaScript side receives it as {@code CallInfo}
 * with the fields {@code callId}, {@code roomId}, {@code serverOrigin},
 * {@code url}, and {@code callerName}. Missing optional values are empty
 * strings.
 */
final class CallInfo {

    private static final String EXTRA_PREFIX = "mn.setunari.chatto.mobile.call.";
    private static final String EXTRA_CALL_ID = EXTRA_PREFIX + "CALL_ID";
    private static final String EXTRA_ROOM_ID = EXTRA_PREFIX + "ROOM_ID";
    private static final String EXTRA_SERVER_ORIGIN = EXTRA_PREFIX + "SERVER_ORIGIN";
    private static final String EXTRA_URL = EXTRA_PREFIX + "URL";
    private static final String EXTRA_CALLER_NAME = EXTRA_PREFIX + "CALLER_NAME";

    /** Server call ID. Never empty. */
    final String callId;
    /** Room ID of the call (the push key {@code groupId}). */
    final String roomId;
    /** Origin of the server that sent the push. */
    final String serverOrigin;
    /** Frontend URL that opens the call. */
    final String url;
    /** Display name of the caller, for the call screen only. Never log it. */
    final String callerName;

    private CallInfo(String callId, String roomId, String serverOrigin, String url, String callerName) {
        this.callId = callId;
        this.roomId = nonNull(roomId);
        this.serverOrigin = nonNull(serverOrigin);
        this.url = nonNull(url);
        this.callerName = nonNull(callerName);
    }

    /** Reads the FCM data keys of a call push. Returns null without a call ID. */
    static CallInfo fromPushData(Map<String, String> data) {
        String callId = data.get("callId");
        if (callId == null || callId.isEmpty()) return null;
        return new CallInfo(callId, data.get("groupId"), data.get("serverOrigin"), data.get("url"), data.get("callerName"));
    }

    /** Reads the extras that {@link #writeTo(Intent)} wrote. Returns null without a call ID. */
    static CallInfo fromIntent(Intent intent) {
        if (intent == null) return null;
        String callId = intent.getStringExtra(EXTRA_CALL_ID);
        if (callId == null || callId.isEmpty()) return null;
        return new CallInfo(
            callId,
            intent.getStringExtra(EXTRA_ROOM_ID),
            intent.getStringExtra(EXTRA_SERVER_ORIGIN),
            intent.getStringExtra(EXTRA_URL),
            intent.getStringExtra(EXTRA_CALLER_NAME)
        );
    }

    /** Writes this call into intent extras and returns the intent. */
    Intent writeTo(Intent intent) {
        return intent
            .putExtra(EXTRA_CALL_ID, callId)
            .putExtra(EXTRA_ROOM_ID, roomId)
            .putExtra(EXTRA_SERVER_ORIGIN, serverOrigin)
            .putExtra(EXTRA_URL, url)
            .putExtra(EXTRA_CALLER_NAME, callerName);
    }

    /** The JavaScript {@code CallInfo} object. */
    JSObject toJS() {
        JSObject object = new JSObject();
        object.put("callId", callId);
        object.put("roomId", roomId);
        object.put("serverOrigin", serverOrigin);
        object.put("url", url);
        object.put("callerName", callerName);
        return object;
    }

    private static String nonNull(String value) {
        return value != null ? value : "";
    }
}
