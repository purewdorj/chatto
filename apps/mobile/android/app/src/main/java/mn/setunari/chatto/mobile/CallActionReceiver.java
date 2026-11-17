package mn.setunari.chatto.mobile;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;

/**
 * Receives the Decline action of the incoming-call notification. It stops the
 * ring and tells a running frontend. It does not contact the server: when the
 * app is not running, the caller keeps ringing until the ring times out.
 */
public class CallActionReceiver extends BroadcastReceiver {

    @Override
    public void onReceive(Context context, Intent intent) {
        if (!IncomingCalls.ACTION_DECLINE.equals(intent.getAction())) return;
        CallInfo call = CallInfo.fromIntent(intent);
        if (call != null) IncomingCalls.decline(context, call);
    }
}
