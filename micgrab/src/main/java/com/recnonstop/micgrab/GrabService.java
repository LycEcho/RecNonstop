package com.recnonstop.micgrab;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Intent;
import android.content.pm.ServiceInfo;
import android.media.AudioFormat;
import android.media.AudioManager;
import android.media.AudioRecord;
import android.media.MediaRecorder;
import android.os.Build;
import android.os.IBinder;
import android.util.Log;

/**
 * 测试用：持续占用麦克风，并实时上报电平。
 * 用于验证别的 App 抢麦时，录音类 App 会有什么反应。
 */
public class GrabService extends Service {

    public static final String TAG = "MicGrab";
    public static final String ACTION_START = "com.recnonstop.micgrab.START";
    public static final String ACTION_STOP = "com.recnonstop.micgrab.STOP";
    public static final String EXTRA_CALL_MODE = "call_mode";

    private static final String CHANNEL_ID = "grab";
    private static final int NOTIF_ID = 2001;

    public static volatile boolean running = false;
    public static volatile double lastDb = -120;
    public static volatile long lastUpdate = 0L;
    public static volatile String lastError = "";
    public static volatile long samples = 0L;

    private volatile boolean active = false;
    private Thread worker;
    private boolean useCallMode = false;

    @Override
    public void onCreate() {
        super.onCreate();
        NotificationChannel ch = new NotificationChannel(
                CHANNEL_ID, "麦克风占用", NotificationManager.IMPORTANCE_LOW);
        NotificationManager nm = getSystemService(NotificationManager.class);
        if (nm != null) nm.createNotificationChannel(ch);
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        String action = (intent != null && intent.getAction() != null)
                ? intent.getAction() : ACTION_START;

        if (ACTION_STOP.equals(action)) {
            stop();
            stopForeground(true);
            stopSelf();
            return START_NOT_STICKY;
        }

        useCallMode = intent != null && intent.getBooleanExtra(EXTRA_CALL_MODE, false);

        startForeground(NOTIF_ID, buildNotification("准备中"),
                ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE);

        if (!active) start();
        return START_STICKY;
    }

    private void start() {
        active = true;
        running = true;
        lastError = "";
        samples = 0;

        if (useCallMode) {
            try {
                AudioManager am = (AudioManager) getSystemService(AUDIO_SERVICE);
                if (am != null) am.setMode(AudioManager.MODE_IN_COMMUNICATION);
                Log.i(TAG, "audio mode -> MODE_IN_COMMUNICATION");
            } catch (Throwable t) {
                Log.e(TAG, "setMode failed", t);
            }
        }

        worker = new Thread(this::loop, "grab-loop");
        worker.start();
    }

    private void loop() {
        int rate = 16000;
        int min = AudioRecord.getMinBufferSize(rate,
                AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT);
        if (min <= 0) min = 2048;

        AudioRecord rec = null;
        try {
            rec = new AudioRecord.Builder()
                    .setAudioSource(MediaRecorder.AudioSource.MIC)
                    .setAudioFormat(new AudioFormat.Builder()
                            .setEncoding(AudioFormat.ENCODING_PCM_16BIT)
                            .setSampleRate(rate)
                            .setChannelMask(AudioFormat.CHANNEL_IN_MONO)
                            .build())
                    .setBufferSizeInBytes(min * 4)
                    .build();

            if (rec.getState() != AudioRecord.STATE_INITIALIZED) {
                lastError = "AudioRecord 初始化失败";
                running = false;
                return;
            }

            rec.startRecording();
            Log.i(TAG, "grab started, rate=" + rate);

            short[] buf = new short[min];
            long lastLog = 0;

            while (active) {
                int n = rec.read(buf, 0, buf.length);
                if (n <= 0) {
                    lastError = "read 返回 " + n;
                    Log.w(TAG, "read=" + n + " state=" + rec.getRecordingState());
                    sleep(500);
                    continue;
                }

                long sum = 0;
                boolean allZero = true;
                for (int i = 0; i < n; i++) {
                    short s = buf[i];
                    sum += (long) s * s;
                    if (s != 0) allZero = false;
                }
                double rms = Math.sqrt(sum / (double) n);
                double db = 20 * Math.log10(rms / 32768.0 + 1e-12);

                lastDb = db;
                lastUpdate = System.currentTimeMillis();
                samples += n;

                long now = System.currentTimeMillis();
                if (now - lastLog > 2000) {
                    lastLog = now;
                    Log.i(TAG, String.format("level=%.1f dB samples=%d%s",
                            db, samples, allZero ? " [全零/静音]" : ""));
                    notifyText(String.format("%.1f dB", db));
                }
            }
        } catch (Throwable t) {
            lastError = String.valueOf(t);
            Log.e(TAG, "grab error", t);
        } finally {
            if (rec != null) {
                try { rec.stop(); } catch (Throwable ignored) {}
                try { rec.release(); } catch (Throwable ignored) {}
            }
            Log.i(TAG, "grab stopped");
        }
    }

    private void stop() {
        active = false;
        running = false;
        if (worker != null) {
            worker.interrupt();
            worker = null;
        }
        if (useCallMode) {
            try {
                AudioManager am = (AudioManager) getSystemService(AUDIO_SERVICE);
                if (am != null) am.setMode(AudioManager.MODE_NORMAL);
                Log.i(TAG, "audio mode -> MODE_NORMAL");
            } catch (Throwable ignored) {}
        }
    }

    private Notification buildNotification(String text) {
        Intent open = new Intent(this, MainActivity.class);
        PendingIntent pi = PendingIntent.getActivity(this, 1, open,
                PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
        return new Notification.Builder(this, CHANNEL_ID)
                .setContentTitle("麦克风占用测试")
                .setContentText(text)
                .setSmallIcon(R.drawable.ic_launcher)
                .setContentIntent(pi)
                .setOngoing(true)
                .setOnlyAlertOnce(true)
                .build();
    }

    private void notifyText(String text) {
        try {
            NotificationManager nm = getSystemService(NotificationManager.class);
            if (nm != null) nm.notify(NOTIF_ID, buildNotification(text));
        } catch (Throwable ignored) {}
    }

    private static void sleep(long ms) {
        try { Thread.sleep(ms); } catch (InterruptedException ignored) {}
    }

    @Override
    public void onDestroy() {
        stop();
        running = false;
        super.onDestroy();
    }

    @Override
    public IBinder onBind(Intent intent) { return null; }
}
