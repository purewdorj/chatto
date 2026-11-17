package mn.setunari.chatto.mobile;

import android.graphics.Color;
import android.webkit.WebView;
import androidx.webkit.WebViewCompat;
import androidx.webkit.WebViewFeature;
import com.getcapacitor.Plugin;
import com.getcapacitor.annotation.CapacitorPlugin;
import java.util.Collections;
import java.util.Set;
import org.json.JSONArray;
import org.json.JSONObject;

/**
 * Installs the shell scripts of the bundled frontend. Capacitor calls
 * {@link #load()} before the bridge loads the first page, so the scripts and
 * the message listener apply to the first document. This plugin has no
 * JavaScript methods.
 */
@CapacitorPlugin(name = "ChattoShell")
public class ChattoShellPlugin extends Plugin {

    /** Only the bundled frontend may run the shell scripts or post to the shell. */
    private static final Set<String> BUNDLED_ORIGIN = Collections.singleton("https://localhost");

    /**
     * Runs at document start in the main frame of the bundled origin.
     *
     * <p>The frontend viewport already disables zoom. The shell repeats the
     * limits so that a later change to the web page does not enable focus or
     * double-tap zoom in the app. The shared layout owns the top inset. The
     * shell reserves the bottom inset so that the inner background continues
     * behind the gesture bar. The page reports its background colour after its
     * first paint and when the theme changes.
     */
    private static final String SHELL_SCRIPT = """
        (() => {
          if (window.top !== window) return;
          function applyShellPolicy() {
            const viewport = document.querySelector('meta[name="viewport"]');
            if (viewport) {
              viewport.content += ', minimum-scale=1, maximum-scale=1, user-scalable=no';
            }
            const safeAreaStyle = document.createElement('style');
            safeAreaStyle.textContent = `
              :root { --mobile-sidebar-safe-bottom: env(safe-area-inset-bottom, 0px); }
              body { padding-bottom: env(safe-area-inset-bottom, 0px); background-color: var(--color-background); }
              [data-safari-bottom-edge] { display: none; }
            `;
            document.head.appendChild(safeAreaStyle);
            // Resolve CSS colours (including OKLCH) to sRGB for the native window.
            function syncNativeBackground() {
              const canvas = document.createElement('canvas');
              canvas.width = canvas.height = 1;
              const context = canvas.getContext('2d');
              context.fillStyle = getComputedStyle(document.body).backgroundColor;
              context.fillRect(0, 0, 1, 1);
              chattoLaunchReady.postMessage(JSON.stringify(Array.from(context.getImageData(0, 0, 1, 1).data)));
            }
            requestAnimationFrame(() => requestAnimationFrame(syncNativeBackground));
            new MutationObserver(syncNativeBackground).observe(document.documentElement, {
              attributes: true, attributeFilter: ['data-theme', 'style']
            });
          }
          if (document.readyState === 'loading') {
            document.addEventListener('DOMContentLoaded', applyShellPolicy, { once: true });
          } else {
            applyShellPolicy();
          }
        })();
        """;

    @Override
    public void load() {
        MainActivity activity = getActivity() instanceof MainActivity main ? main : null;
        WebView webView = getBridge().getWebView();
        if (
            !WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT) ||
            !WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER)
        ) {
            // Old WebView: keep the default layout and do not hold the launch screen.
            if (activity != null) activity.releaseLaunchScreen();
            return;
        }
        webView.setBackgroundColor(getContext().getColor(R.color.launch_background));
        WebViewCompat.addWebMessageListener(webView, "chattoLaunchReady", BUNDLED_ORIGIN, (view, message, sourceOrigin, isMainFrame, replyProxy) -> {
            if (!isMainFrame || activity == null) return;
            activity.onWebContentReady(parseColour(message.getData()));
        });
        WebViewCompat.addDocumentStartJavaScript(webView, SHELL_SCRIPT, BUNDLED_ORIGIN);
        WebViewCompat.addDocumentStartJavaScript(webView, appIdentityScript(getContext().getPackageName()), BUNDLED_ORIGIN);
    }

    /**
     * Native push registration needs the app identity. FCM has no separate
     * development environment, so {@code apnsSandbox} is always false here.
     */
    static String appIdentityScript(String packageName) {
        return "window.chattoNativeApp = Object.freeze({ appId: " +
            JSONObject.quote(packageName) +
            ", apnsSandbox: false });";
    }

    /** Accept only four sRGB channels from 0 to 255. */
    static Integer parseColour(String data) {
        if (data == null) return null;
        try {
            JSONArray rgba = new JSONArray(data);
            if (rgba.length() != 4) return null;
            int[] channel = new int[4];
            for (int i = 0; i < 4; i++) {
                double value = rgba.getDouble(i);
                if (!Double.isFinite(value) || value < 0 || value > 255) return null;
                channel[i] = (int) Math.round(value);
            }
            return Color.argb(channel[3], channel[0], channel[1], channel[2]);
        } catch (Exception ignored) {
            return null;
        }
    }
}
