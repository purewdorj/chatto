package mn.setunari.chatto.mobile;

import android.content.Context;
import android.media.AudioAttributes;
import android.media.AudioManager;
import android.media.Ringtone;
import android.media.RingtoneManager;
import android.net.Uri;
import android.os.Build;
import android.os.Handler;
import android.os.Looper;
import android.os.VibrationEffect;
import android.os.Vibrator;
import com.getcapacitor.Plugin;
import com.getcapacitor.PluginCall;
import com.getcapacitor.PluginMethod;
import com.getcapacitor.annotation.CapacitorPlugin;

/**
 * Rings an incoming call with the device ringtone. Unlike Web Audio in the
 * WebView, it needs no user interaction and follows the ringer mode: silent
 * mode plays nothing, vibrate mode only vibrates. It rings until {@code stop}
 * or the next {@code start}.
 */
@CapacitorPlugin(name = "ChattoRingtone")
public class ChattoRingtonePlugin extends Plugin {

    /** Ringtone restart interval for API levels without looping playback. */
    private static final long REPLAY_MS = 3_000;
    private static final long[] VIBRATION = { 0, 800, 1_200 };

    private final Handler handler = new Handler(Looper.getMainLooper());
    private Ringtone ringtone;
    private Vibrator vibrator;
    private final Runnable replay = new Runnable() {
        @Override
        public void run() {
            if (ringtone == null) return;
            if (!ringtone.isPlaying()) ringtone.play();
            handler.postDelayed(this, REPLAY_MS);
        }
    };

    @PluginMethod
    public void start(PluginCall call) {
        handler.post(() -> {
            stopRinging();
            Context context = getContext();
            AudioManager audio = (AudioManager) context.getSystemService(Context.AUDIO_SERVICE);
            int mode = audio != null ? audio.getRingerMode() : AudioManager.RINGER_MODE_NORMAL;
            if (mode == AudioManager.RINGER_MODE_NORMAL) startSound(context);
            if (mode != AudioManager.RINGER_MODE_SILENT) startVibration(context);
            call.resolve();
        });
    }

    @PluginMethod
    public void stop(PluginCall call) {
        handler.post(() -> {
            stopRinging();
            call.resolve();
        });
    }

    @Override
    protected void handleOnDestroy() {
        stopRinging();
    }

    private void startSound(Context context) {
        Uri uri = RingtoneManager.getActualDefaultRingtoneUri(context, RingtoneManager.TYPE_RINGTONE);
        if (uri == null) uri = RingtoneManager.getDefaultUri(RingtoneManager.TYPE_RINGTONE);
        Ringtone tone = uri != null ? RingtoneManager.getRingtone(context, uri) : null;
        if (tone == null) return;
        tone.setAudioAttributes(
            new AudioAttributes.Builder()
                .setUsage(AudioAttributes.USAGE_NOTIFICATION_RINGTONE)
                .setContentType(AudioAttributes.CONTENT_TYPE_SONIFICATION)
                .build()
        );
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.P) tone.setLooping(true);
        tone.play();
        ringtone = tone;
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.P) handler.postDelayed(replay, REPLAY_MS);
    }

    private void startVibration(Context context) {
        Vibrator device = (Vibrator) context.getSystemService(Context.VIBRATOR_SERVICE);
        if (device == null || !device.hasVibrator()) return;
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            device.vibrate(VibrationEffect.createWaveform(VIBRATION, 0));
        } else {
            device.vibrate(VIBRATION, 0);
        }
        vibrator = device;
    }

    private void stopRinging() {
        handler.removeCallbacks(replay);
        if (ringtone != null) {
            ringtone.stop();
            ringtone = null;
        }
        if (vibrator != null) {
            vibrator.cancel();
            vibrator = null;
        }
    }
}
