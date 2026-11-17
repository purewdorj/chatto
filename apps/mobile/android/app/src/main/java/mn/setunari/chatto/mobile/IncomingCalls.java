package mn.setunari.chatto.mobile;

import android.app.Notification;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;
import android.media.AudioManager;
import android.media.RingtoneManager;
import android.net.Uri;
import android.os.Build;
import androidx.core.app.NotificationCompat;
import androidx.core.app.NotificationManagerCompat;
import androidx.core.app.Person;
import androidx.core.content.ContextCompat;
import java.lang.ref.WeakReference;
import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;

/**
 * Native incoming-call state of the process: the ringing notification, the
 * full-screen {@link IncomingCallActivity}, and answers that wait for the
 * frontend.
 *
 * <p>When the app is in the foreground (MainActivity resumed), the frontend
 * rings the call itself, so no notification is shown. Otherwise the call rings
 * as a looping {@code calls} channel notification for at most
 * {@link #RING_TIMEOUT_MS}, with a full-screen intent when the system allows
 * one.
 *
 * <p>All state is per process. Methods are safe to call from any thread.
 */
final class IncomingCalls {

    /** Ring duration. Same as the server call push expiry and the in-app banner. */
    static final long RING_TIMEOUT_MS = 30_000;

    /** MainActivity action: the user answered the call in the intent extras. */
    static final String ACTION_ANSWER = "mn.setunari.chatto.mobile.action.ANSWER_CALL";
    /** {@link CallActionReceiver} action: the user declined the call in the intent extras. */
    static final String ACTION_DECLINE = "mn.setunari.chatto.mobile.action.DECLINE_CALL";
    /** Wall-clock start of the ring, for the remaining time of the call screen. */
    static final String EXTRA_RING_STARTED = "mn.setunari.chatto.mobile.call.RING_STARTED";

    /** Notification ID of call notifications. The tag is the call ID. */
    private static final int NOTIFICATION_ID = 0x43414c4c;
    private static final long[] VIBRATION = { 0, 800, 1_200 };

    private static final Object lock = new Object();
    /** Call IDs that ring natively in this process. */
    private static final Set<String> ringing = new HashSet<>();
    /** Answers that happened while no plugin instance existed. */
    private static final List<CallInfo> pendingAnswers = new ArrayList<>();
    private static volatile boolean appInForeground = false;
    private static WeakReference<IncomingCallActivity> screen = new WeakReference<>(null);

    private IncomingCalls() {}

    /** MainActivity reports resume (true) and pause (false). */
    static void setAppInForeground(boolean foreground) {
        appInForeground = foreground;
    }

    /**
     * Rings an incoming call. Does nothing visible while the app is in the
     * foreground, because the frontend rings the call itself.
     */
    static void ring(Context context, CallInfo call) {
        if (appInForeground) return;
        Context app = context.getApplicationContext();
        NotificationChannels.create(app);
        synchronized (lock) {
            ringing.add(call.callId);
        }
        long started = System.currentTimeMillis();
        Notification notification = buildNotification(app, call, started);
        try {
            NotificationManagerCompat.from(app).notify(call.callId, NOTIFICATION_ID, notification);
        } catch (SecurityException denied) {
            // POST_NOTIFICATIONS is not granted (Android 13+). Nothing can ring.
            synchronized (lock) {
                ringing.remove(call.callId);
            }
        }
    }

    /**
     * Stops the ring of a call: cancels its notification and closes its call
     * screen. Does nothing for an unknown call ID.
     */
    static void end(Context context, String callId) {
        synchronized (lock) {
            ringing.remove(callId);
        }
        NotificationManagerCompat.from(context.getApplicationContext()).cancel(callId, NOTIFICATION_ID);
        IncomingCallActivity activity = screen.get();
        if (activity != null) activity.finishFor(callId);
    }

    /**
     * Records an answer: stops the ring and gives the call to the frontend.
     * MainActivity calls it on the main thread when it receives
     * {@link #ACTION_ANSWER}.
     */
    static void answer(Context context, CallInfo call) {
        end(context, call.callId);
        if (!ChattoCallsPlugin.deliverAnswer(call)) {
            synchronized (lock) {
                pendingAnswers.add(call);
            }
        }
    }

    /** Records a decline: stops the ring and tells a running frontend. */
    static void decline(Context context, CallInfo call) {
        end(context, call.callId);
        ChattoCallsPlugin.deliverDecline(call);
    }

    /** Returns and clears the answers that no plugin instance received. */
    static List<CallInfo> takePendingAnswers() {
        synchronized (lock) {
            List<CallInfo> answers = new ArrayList<>(pendingAnswers);
            pendingAnswers.clear();
            return answers;
        }
    }

    /**
     * Whether the system shows full-screen intents of this app. Android 14 and
     * later let the user (or the Play Store policy) revoke it; earlier versions
     * grant it with the manifest permission.
     */
    static boolean canUseFullScreenIntent(Context context) {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.UPSIDE_DOWN_CAKE) return true;
        NotificationManager manager = context.getSystemService(NotificationManager.class);
        return manager != null && manager.canUseFullScreenIntent();
    }

    static void attachScreen(IncomingCallActivity activity) {
        screen = new WeakReference<>(activity);
    }

    static void detachScreen(IncomingCallActivity activity) {
        if (screen.get() == activity) screen = new WeakReference<>(null);
    }

    /** Intent that opens MainActivity and answers the call. */
    static Intent answerIntent(Context context, CallInfo call) {
        return call
            .writeTo(new Intent(context, MainActivity.class))
            .setAction(ACTION_ANSWER)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_SINGLE_TOP);
    }

    private static Notification buildNotification(Context context, CallInfo call, long started) {
        int requestCode = call.callId.hashCode();
        int immutable = PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE;

        Intent screenIntent = call
            .writeTo(new Intent(context, IncomingCallActivity.class))
            .putExtra(EXTRA_RING_STARTED, started)
            .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_NO_USER_ACTION);
        PendingIntent screenPending = PendingIntent.getActivity(context, requestCode, screenIntent, immutable);
        // An activity intent, not a broadcast trampoline: Android 12+ blocks
        // activity starts from notification broadcast receivers.
        PendingIntent answerPending = PendingIntent.getActivity(context, requestCode, answerIntent(context, call), immutable);
        Intent declineIntent = call
            .writeTo(new Intent(context, CallActionReceiver.class))
            .setAction(ACTION_DECLINE);
        PendingIntent declinePending = PendingIntent.getBroadcast(context, requestCode, declineIntent, immutable);

        String caller = call.callerName.isEmpty() ? context.getString(R.string.call_unknown_caller) : call.callerName;
        boolean fullScreen = canUseFullScreenIntent(context);

        NotificationCompat.Builder builder = new NotificationCompat.Builder(context, NotificationChannels.CALLS)
            .setSmallIcon(R.drawable.ic_stat_chatto)
            .setColor(ContextCompat.getColor(context, R.color.notification_accent))
            .setContentTitle(caller)
            .setContentText(context.getString(R.string.call_incoming))
            .setCategory(NotificationCompat.CATEGORY_CALL)
            .setPriority(NotificationCompat.PRIORITY_MAX)
            .setVisibility(NotificationCompat.VISIBILITY_PUBLIC)
            .setOngoing(true)
            .setAutoCancel(false)
            .setOnlyAlertOnce(false)
            .setShowWhen(false)
            .setTimeoutAfter(RING_TIMEOUT_MS)
            .setContentIntent(screenPending);

        // Android 7 has no channels: put the ringtone and vibration on the
        // notification. Android 8+ uses the settings of the calls channel.
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.O) {
            Uri tone = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_RINGTONE);
            builder.setSound(tone, AudioManager.STREAM_RING).setVibrate(VIBRATION);
        }

        if (fullScreen) {
            builder.setFullScreenIntent(screenPending, true);
            // Android 12+ rejects a CallStyle notification that has neither a
            // full-screen intent nor a foreground service, so use it only here.
            Person person = new Person.Builder().setName(caller).setImportant(true).build();
            builder.setStyle(NotificationCompat.CallStyle.forIncomingCall(person, declinePending, answerPending));
        } else {
            builder
                .addAction(0, context.getString(R.string.call_decline), declinePending)
                .addAction(0, context.getString(R.string.call_answer), answerPending);
        }

        Notification notification = builder.build();
        // Repeat the channel sound and vibration until the user acts or the
        // notification times out.
        notification.flags |= Notification.FLAG_INSISTENT;
        return notification;
    }
}
