package mn.setunari.chatto.mobile;

import android.app.Activity;
import android.app.KeyguardManager;
import android.content.Intent;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.view.WindowManager;
import android.widget.Button;
import android.widget.TextView;

/**
 * Full-screen call screen that the incoming-call notification opens over the
 * lock screen. It shows the caller with Answer and Decline buttons and closes
 * when the ring times out, the call ends, or the user acts. The notification
 * keeps the ringtone; this screen plays no sound.
 *
 * <p>Answer asks the system to dismiss the keyguard first, so that
 * MainActivity, which does not show over the lock screen, opens in front.
 */
public class IncomingCallActivity extends Activity {

    private final Handler handler = new Handler(Looper.getMainLooper());
    private final Runnable timeout = this::finish;
    private CallInfo call;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O_MR1) {
            setShowWhenLocked(true);
            setTurnScreenOn(true);
        } else {
            getWindow().addFlags(
                WindowManager.LayoutParams.FLAG_SHOW_WHEN_LOCKED | WindowManager.LayoutParams.FLAG_TURN_SCREEN_ON
            );
        }
        getWindow().addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON);
        setContentView(R.layout.activity_incoming_call);
        findViewById(R.id.call_answer).setOnClickListener(view -> answer());
        findViewById(R.id.call_decline).setOnClickListener(view -> decline());
        show(getIntent());
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        show(intent);
    }

    @Override
    protected void onDestroy() {
        handler.removeCallbacks(timeout);
        IncomingCalls.detachScreen(this);
        super.onDestroy();
    }

    /** Closes this screen if it shows the given call. Safe from any thread. */
    void finishFor(String callId) {
        runOnUiThread(() -> {
            if (call != null && call.callId.equals(callId)) finish();
        });
    }

    private void show(Intent intent) {
        CallInfo next = CallInfo.fromIntent(intent);
        long started = intent.getLongExtra(IncomingCalls.EXTRA_RING_STARTED, 0);
        long remaining = IncomingCalls.RING_TIMEOUT_MS - (System.currentTimeMillis() - started);
        if (next == null || started <= 0 || remaining <= 0) {
            finish();
            return;
        }
        call = next;
        IncomingCalls.attachScreen(this);
        String caller = call.callerName.isEmpty() ? getString(R.string.call_unknown_caller) : call.callerName;
        ((TextView) findViewById(R.id.call_caller)).setText(caller);
        handler.removeCallbacks(timeout);
        handler.postDelayed(timeout, remaining);
    }

    private void answer() {
        if (call == null) return;
        Button answer = findViewById(R.id.call_answer);
        KeyguardManager keyguard = getSystemService(KeyguardManager.class);
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O && keyguard != null && keyguard.isKeyguardLocked()) {
            answer.setEnabled(false);
            keyguard.requestDismissKeyguard(
                this,
                new KeyguardManager.KeyguardDismissCallback() {
                    @Override
                    public void onDismissSucceeded() {
                        openApp();
                    }

                    @Override
                    public void onDismissCancelled() {
                        answer.setEnabled(true);
                    }

                    @Override
                    public void onDismissError() {
                        answer.setEnabled(true);
                    }
                }
            );
            return;
        }
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
            getWindow().addFlags(WindowManager.LayoutParams.FLAG_DISMISS_KEYGUARD);
        }
        openApp();
    }

    private void openApp() {
        if (call == null) return;
        startActivity(IncomingCalls.answerIntent(this, call));
        finish();
    }

    private void decline() {
        if (call != null) IncomingCalls.decline(this, call);
        finish();
    }
}
