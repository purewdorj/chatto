package mn.setunari.chatto.mobile;

import android.content.ActivityNotFoundException;
import android.net.Uri;
import android.os.Handler;
import android.os.Looper;
import androidx.browser.customtabs.CustomTabsIntent;
import com.getcapacitor.JSObject;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;
import java.lang.ref.WeakReference;
import java.net.URI;
import java.net.URISyntaxException;
import java.util.Collections;
import java.util.List;

/**
 * Opens one time-bounded OAuth authorization in a Custom Tab and returns the
 * redirect to the frontend. This is the Android form of the iOS plugin in
 * {@code ChattoViewController.swift}, with the same JavaScript interface:
 *
 * <pre>
 * ChattoAuthorization.authorize({ url: string }): Promise&lt;{ url: string }&gt;
 * </pre>
 *
 * <p>The frontend owns PKCE, state validation, and the token exchange. This
 * plugin accepts only an HTTPS authorization URL with the exact mobile client
 * ID and callback, and returns only a callback with the exact scheme and path.
 * Rejection messages contain no provider URLs or native errors.
 */
@CapacitorPlugin(name = "ChattoAuthorization")
public class ChattoAuthorizationPlugin extends Plugin {

    static final String CALLBACK_SCHEME = "eu.chattocorp.chatto.mobile";
    static final String CALLBACK_PATH = "/oauth/callback";
    static final String CALLBACK = CALLBACK_SCHEME + ":" + CALLBACK_PATH;
    static final String CLIENT_ID = "eu.chattocorp.chatto.mobile";

    /** Same limit as the iOS authentication session. */
    private static final long TIMEOUT_MS = 300_000;

    /** The plugin instance of the running bridge, for the callback activity. */
    private static WeakReference<ChattoAuthorizationPlugin> active = new WeakReference<>(null);

    private final Handler main = new Handler(Looper.getMainLooper());
    private PluginCall pending;
    private Runnable timeout;
    /** True after the app left the foreground for the Custom Tab. */
    private boolean browserShown;

    @Override
    public void load() {
        active = new WeakReference<>(this);
    }

    @PluginMethod
    public void authorize(PluginCall call) {
        main.post(() -> start(call));
    }

    private void start(PluginCall call) {
        if (pending != null) {
            call.reject("A sign-in attempt is already active.");
            return;
        }
        Uri url = validAuthorizationUrl(call.getString("url"));
        if (url == null || getActivity() == null) {
            call.reject("Sign-in requires an HTTPS server and the mobile callback.");
            return;
        }
        pending = call;
        browserShown = false;
        try {
            new CustomTabsIntent.Builder().setShowTitle(true).build().launchUrl(getActivity(), url);
        } catch (ActivityNotFoundException e) {
            pending = null;
            call.reject("The sign-in session could not be started.");
            return;
        }
        timeout = () -> {
            if (pending != call) return;
            clear();
            call.reject("The sign-in attempt timed out.");
        };
        main.postDelayed(timeout, TIMEOUT_MS);
    }

    /** Called by {@link OAuthCallbackActivity} on the main thread. */
    static void deliverCallback(Uri callback) {
        ChattoAuthorizationPlugin plugin = active.get();
        if (plugin != null) {
            plugin.complete(callback);
        }
    }

    private void complete(Uri callback) {
        PluginCall call = pending;
        if (call == null) return;
        clear();
        if (!isValidCallback(callback)) {
            call.reject("Sign-in was cancelled or could not be completed.");
            return;
        }
        JSObject result = new JSObject();
        result.put("url", callback.toString());
        call.resolve(result);
    }

    @Override
    protected void handleOnPause() {
        super.handleOnPause();
        if (pending != null) browserShown = true;
    }

    /**
     * The callback activity delivers the redirect before the app resumes. If
     * the app resumes with an attempt still active, the user closed the tab.
     */
    @Override
    protected void handleOnResume() {
        super.handleOnResume();
        PluginCall call = pending;
        if (call == null || !browserShown) return;
        clear();
        call.reject("Sign-in was cancelled or could not be completed.");
    }

    @Override
    protected void handleOnDestroy() {
        PluginCall call = pending;
        clear();
        if (call != null) call.reject("Sign-in was cancelled or could not be completed.");
        if (active.get() == this) active = new WeakReference<>(null);
        super.handleOnDestroy();
    }

    private void clear() {
        if (timeout != null) main.removeCallbacks(timeout);
        timeout = null;
        pending = null;
        browserShown = false;
    }

    /**
     * Accept an absolute HTTPS URL with a host, no user information, exactly
     * one {@code client_id} equal to the mobile client, and exactly one
     * {@code redirect_uri} equal to the mobile callback.
     */
    static Uri validAuthorizationUrl(String raw) {
        if (raw == null) return null;
        URI strict;
        try {
            strict = new URI(raw);
        } catch (URISyntaxException e) {
            return null;
        }
        if (
            !"https".equals(strict.getScheme()) ||
            strict.getHost() == null ||
            strict.getHost().isEmpty() ||
            strict.getRawUserInfo() != null
        ) {
            return null;
        }
        Uri url = Uri.parse(raw);
        if (!strict.getHost().equalsIgnoreCase(url.getHost()) || url.getUserInfo() != null) {
            return null;
        }
        if (!exactly(url.getQueryParameters("redirect_uri"), CALLBACK) || !exactly(url.getQueryParameters("client_id"), CLIENT_ID)) {
            return null;
        }
        return url;
    }

    /** Accept {@code eu.chattocorp.chatto.mobile:/oauth/callback} with a query, no host, and no fragment. */
    static boolean isValidCallback(Uri callback) {
        return (
            callback != null &&
            CALLBACK_SCHEME.equals(callback.getScheme()) &&
            callback.isHierarchical() &&
            callback.getEncodedAuthority() == null &&
            CALLBACK_PATH.equals(callback.getEncodedPath()) &&
            callback.getEncodedFragment() == null
        );
    }

    private static boolean exactly(List<String> values, String expected) {
        return values.equals(Collections.singletonList(expected));
    }
}
