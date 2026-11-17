package mn.setunari.chatto.mobile;

import androidx.annotation.NonNull;
import com.capacitorjs.plugins.pushnotifications.MessagingService;
import com.google.firebase.messaging.RemoteMessage;
import java.util.Map;

/**
 * Replaces the FCM service of the Capacitor push plugin (see the app
 * manifest). Data-only call pushes ({@code kind} {@code call} or
 * {@code call_end}) ring or stop the native incoming call. All other messages
 * and token changes go to the Capacitor plugin unchanged.
 */
public class ChattoMessagingService extends MessagingService {

    @Override
    public void onMessageReceived(@NonNull RemoteMessage message) {
        Map<String, String> data = message.getData();
        String kind = data.get("kind");
        if ("call".equals(kind)) {
            CallInfo call = CallInfo.fromPushData(data);
            // FCM can deliver a push late, for example after the device was
            // offline. Do not ring for a call that stopped ringing already.
            long sent = message.getSentTime();
            boolean stale = sent > 0 && System.currentTimeMillis() - sent > IncomingCalls.RING_TIMEOUT_MS;
            if (call != null && !stale) IncomingCalls.ring(this, call);
            return;
        }
        if ("call_end".equals(kind)) {
            String callId = data.get("callId");
            if (callId != null && !callId.isEmpty()) IncomingCalls.end(this, callId);
            return;
        }
        super.onMessageReceived(message);
    }

    /** Passes token changes to the Capacitor plugin, which sends {@code registration}. */
    @Override
    public void onNewToken(@NonNull String token) {
        super.onNewToken(token);
    }
}
