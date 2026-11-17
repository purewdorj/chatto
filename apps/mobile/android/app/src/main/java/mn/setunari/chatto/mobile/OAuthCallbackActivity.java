package mn.setunari.chatto.mobile;

import android.app.Activity;
import android.content.Intent;
import android.os.Bundle;

/**
 * Receives the OAuth redirect from the browser, gives it to
 * {@link ChattoAuthorizationPlugin}, and returns to the app. Bringing
 * {@link MainActivity} to the front with CLEAR_TOP also closes the Custom Tab
 * that runs in the app task.
 *
 * <p>Any app can send this intent. The plugin accepts a callback only while a
 * sign-in attempt is active, and the frontend validates its state and PKCE.
 */
public class OAuthCallbackActivity extends Activity {

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        Intent received = getIntent();
        if (received != null && Intent.ACTION_VIEW.equals(received.getAction())) {
            ChattoAuthorizationPlugin.deliverCallback(received.getData());
        }
        Intent main = new Intent(this, MainActivity.class);
        main.addFlags(Intent.FLAG_ACTIVITY_CLEAR_TOP | Intent.FLAG_ACTIVITY_SINGLE_TOP);
        startActivity(main);
        finish();
    }
}
