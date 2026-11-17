package mn.setunari.chatto.mobile;

import android.content.ActivityNotFoundException;
import android.content.Intent;
import android.net.Uri;
import android.os.Build;
import android.provider.Settings;
import com.getcapacitor.JSArray;
import com.getcapacitor.JSObject;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;
import org.json.JSONObject;

/**
 * JavaScript interface of native incoming calls ({@code ChattoCalls}).
 *
 * <p>Methods:
 * <ul>
 *   <li>{@code getVoipToken(): Promise<{ token: null }>} — Android has no VoIP
 *       token; call pushes use the normal FCM token.</li>
 *   <li>{@code getPendingAnswers(): Promise<{ calls: CallInfo[] }>} — returns
 *       and clears answers that no plugin instance received.</li>
 *   <li>{@code endCall({ callId }): Promise<void>} — stops the native ring of
 *       that call; no-op for an unknown call.</li>
 *   <li>{@code canUseFullScreenIntent(): Promise<{ value: boolean }>} and
 *       {@code openFullScreenIntentSettings(): Promise<void>} — Android 14+
 *       full-screen call screen permission.</li>
 * </ul>
 *
 * <p>Events: {@code callAnswered} (CallInfo, retained until a listener
 * consumes it) and {@code callDeclined} (CallInfo). Consumers must ignore a
 * second answer for the same call ID.
 */
@CapacitorPlugin(name = "ChattoCalls")
public class ChattoCallsPlugin extends Plugin {

    static final String EVENT_ANSWERED = "callAnswered";
    static final String EVENT_DECLINED = "callDeclined";

    private static volatile ChattoCallsPlugin instance;

    @Override
    public void load() {
        instance = this;
    }

    @Override
    protected void handleOnDestroy() {
        if (instance == this) instance = null;
    }

    /** Sends an answer to the frontend. Returns false when no plugin instance exists. */
    static boolean deliverAnswer(CallInfo call) {
        ChattoCallsPlugin plugin = instance;
        if (plugin == null) return false;
        plugin.notifyListeners(EVENT_ANSWERED, call.toJS(), true);
        return true;
    }

    /** Sends a decline to a running frontend. Drops it when no plugin instance exists. */
    static void deliverDecline(CallInfo call) {
        ChattoCallsPlugin plugin = instance;
        if (plugin != null) plugin.notifyListeners(EVENT_DECLINED, call.toJS());
    }

    @PluginMethod
    public void getVoipToken(PluginCall call) {
        JSObject result = new JSObject();
        result.put("token", JSONObject.NULL);
        call.resolve(result);
    }

    @PluginMethod
    public void getPendingAnswers(PluginCall call) {
        JSArray calls = new JSArray();
        for (CallInfo answer : IncomingCalls.takePendingAnswers()) calls.put(answer.toJS());
        JSObject result = new JSObject();
        result.put("calls", calls);
        call.resolve(result);
    }

    @PluginMethod
    public void endCall(PluginCall call) {
        String callId = call.getString("callId");
        if (callId == null || callId.isEmpty()) {
            call.reject("callId is required");
            return;
        }
        IncomingCalls.end(getContext(), callId);
        call.resolve();
    }

    @PluginMethod
    public void canUseFullScreenIntent(PluginCall call) {
        JSObject result = new JSObject();
        result.put("value", IncomingCalls.canUseFullScreenIntent(getContext()));
        call.resolve(result);
    }

    @PluginMethod
    public void openFullScreenIntentSettings(PluginCall call) {
        Intent intent;
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
            intent = new Intent(Settings.ACTION_MANAGE_APP_USE_FULL_SCREEN_INTENT);
        } else {
            intent = new Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS);
        }
        intent.setData(Uri.fromParts("package", getContext().getPackageName(), null));
        intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK);
        try {
            getContext().startActivity(intent);
            call.resolve();
        } catch (ActivityNotFoundException missing) {
            call.reject("settings unavailable");
        }
    }
}
