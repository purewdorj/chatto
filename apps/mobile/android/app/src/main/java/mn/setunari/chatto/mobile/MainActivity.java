package mn.setunari.chatto.mobile;

import android.content.Intent;
import android.content.res.Configuration;
import android.graphics.Color;
import android.os.Bundle;
import android.os.SystemClock;
import android.view.View;
import androidx.activity.EdgeToEdge;
import androidx.core.splashscreen.SplashScreen;
import androidx.core.view.WindowCompat;
import androidx.core.view.WindowInsetsControllerCompat;
import com.getcapacitor.BridgeActivity;

/**
 * Hosts the bundled frontend and applies the native shell policy: edge-to-edge
 * layout, fixed page scale, safe-area styles, launch colour, notification
 * channels, system sign-in, and answers of native incoming calls.
 */
public class MainActivity extends BridgeActivity {

    /** Upper limit for the launch screen when the page cannot report a paint. */
    private static final long LAUNCH_COVER_LIMIT_MS = 3000;

    private volatile boolean webContentReady = false;
    private Integer webBackground;

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        // Keep the system launch screen until the bundled HTML has had a paint
        // opportunity, without a minimum display time.
        SplashScreen splash = SplashScreen.installSplashScreen(this);
        long launchStarted = SystemClock.uptimeMillis();
        splash.setKeepOnScreenCondition(
            () -> !webContentReady && SystemClock.uptimeMillis() - launchStarted < LAUNCH_COVER_LIMIT_MS
        );

        registerPlugin(ChattoShellPlugin.class);
        registerPlugin(ChattoAuthorizationPlugin.class);
        registerPlugin(ChattoRingtonePlugin.class);
        registerPlugin(ChattoCallsPlugin.class);
        EdgeToEdge.enable(this);
        super.onCreate(savedInstanceState);
        NotificationChannels.create(this);
        if (bridge == null) {
            webContentReady = true;
        }
        // A restored activity gets its old intent again; answer only once.
        if (savedInstanceState == null) handleCallIntent(getIntent());
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        handleCallIntent(intent);
    }

    @Override
    public void onResume() {
        super.onResume();
        IncomingCalls.setAppInForeground(true);
    }

    @Override
    public void onPause() {
        IncomingCalls.setAppInForeground(false);
        super.onPause();
    }

    /** Gives an Answer from the call notification or call screen to the frontend. */
    private void handleCallIntent(Intent intent) {
        if (intent == null || !IncomingCalls.ACTION_ANSWER.equals(intent.getAction())) return;
        CallInfo call = CallInfo.fromIntent(intent);
        // Clear the action so that a later delivery of this intent does not answer again.
        intent.setAction(null);
        if (call != null) IncomingCalls.answer(this, call);
    }

    @Override
    public void onConfigurationChanged(Configuration newConfig) {
        super.onConfigurationChanged(newConfig);
        // The SystemBars plugin resets the window colour and bar style.
        if (webBackground != null) {
            applyWindowBackground(webBackground);
        }
    }

    /** Paint the window exposed around the keyboard and behind transparent system bars. */
    private void applyWindowBackground(int colour) {
        View decor = getWindow().getDecorView();
        decor.setBackgroundColor(colour);
        WindowInsetsControllerCompat bars = WindowCompat.getInsetsController(getWindow(), decor);
        boolean light = Color.luminance(colour) > 0.5f;
        bars.setAppearanceLightStatusBars(light);
        bars.setAppearanceLightNavigationBars(light);
    }

    /**
     * Called by {@link ChattoShellPlugin} on the main thread when the bundled
     * page has painted, with its background colour if the page reported one.
     */
    void onWebContentReady(Integer colour) {
        webContentReady = true;
        if (colour != null) {
            webBackground = colour;
            applyWindowBackground(colour);
        }
    }

    /** Release the launch screen when the shell scripts cannot run. */
    void releaseLaunchScreen() {
        webContentReady = true;
    }
}
