package mn.setunari.chatto.mobile;

import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.content.Context;
import android.media.AudioAttributes;
import android.media.RingtoneManager;
import android.os.Build;

/**
 * Creates the notification channels before the frontend runs, so that push
 * messages can name a channel on the first start.
 *
 * <p>Android keeps the user's settings for an existing channel. Changing the
 * importance or sound here does not change an installed channel; use a new
 * channel ID for that.
 */
final class NotificationChannels {

    /** Channel for new messages and mentions. Default importance. */
    static final String MESSAGES = "messages";

    /** Channel for incoming calls. High importance with the ringtone. */
    static final String CALLS = "calls";

    private NotificationChannels() {}

    static void create(Context context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) return;
        NotificationManager manager = context.getSystemService(NotificationManager.class);
        if (manager == null) return;

        NotificationChannel messages = new NotificationChannel(
            MESSAGES,
            context.getString(R.string.notification_channel_messages),
            NotificationManager.IMPORTANCE_DEFAULT
        );
        messages.setDescription(context.getString(R.string.notification_channel_messages_description));

        NotificationChannel calls = new NotificationChannel(
            CALLS,
            context.getString(R.string.notification_channel_calls),
            NotificationManager.IMPORTANCE_HIGH
        );
        calls.setDescription(context.getString(R.string.notification_channel_calls_description));
        calls.setSound(
            RingtoneManager.getDefaultUri(RingtoneManager.TYPE_RINGTONE),
            new AudioAttributes.Builder()
                .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
                .setUsage(AudioAttributes.USAGE_NOTIFICATION_RINGTONE)
                .build()
        );
        calls.enableVibration(true);

        manager.createNotificationChannel(messages);
        manager.createNotificationChannel(calls);
    }
}
