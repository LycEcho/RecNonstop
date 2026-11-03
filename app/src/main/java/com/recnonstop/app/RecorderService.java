package com.recnonstop.app;

import android.Manifest;
import android.app.AppOpsManager;
import android.app.Notification;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.app.Service;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.content.pm.ServiceInfo;
import android.media.AudioManager;
import android.media.MediaRecorder;
import android.os.Build;
import android.os.Handler;
import android.os.IBinder;
import android.os.Looper;
import android.os.PowerManager;
import android.os.Process;
import android.os.SystemClock;
import android.util.Log;

import java.io.File;
import java.io.FileWriter;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;
import java.util.Set;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.locks.ReentrantLock;

public class RecorderService extends Service {

    public static final String TAG = "RecNonstop";
    public static final String ACTION_START = "com.recnonstop.app.START";
    public static final String ACTION_STOP = "com.recnonstop.app.STOP";
    public static final String ACTION_RESTART = "com.recnonstop.app.RESTART";
    public static final String PREFS = "rec";
    public static final String KEY_NEXT_START_AT = "next_start_at";

    private static final String CHANNEL_ID = Notifications.CH_STATUS;
    private static final int NOTIF_ID = 1001;

    public static final long DEFAULT_SEGMENT_MS = 3 * 60 * 1000L;

    /** 通知刷新间隔，避免每 500ms 刷一次 */
    private static final long NOTIF_INTERVAL_MS = 5_000L;
    /** 低于该电平视为可能没录到（-70 dB 接近数字静音） */
    private static final double LOW_DB = -70.0;
    /** 连续低电平多久才确认 */
    private static final long LOW_CONFIRM_MS = 30_000L;
    /** 采到纯数字零时，确认时间缩短 */
    private static final long SILENCE_CONFIRM_MS = 10_000L;
    /** 数字零持续多久后自动重启录音器 */
    private static final long ZERO_RECOVER_MS = 10_000L;
    /** 两次自动恢复之间的最小间隔 */
    private static final long ZERO_RECOVER_COOLDOWN_MS = 60_000L;
    /** 连续恢复多少次后暂停自动重试并提醒用户 */
    private static final int ZERO_RECOVER_MAX_ATTEMPTS = 3;
    /** 恢复次数统计窗口 */
    private static final long ZERO_RECOVER_WINDOW_MS = 10 * 60_000L;
    /** 恢复失败后的静默期 */
    private static final long ZERO_RECOVER_PAUSE_MS = 10 * 60_000L;
    /** 暂停超过该时长视为模式卡住，强制尝试恢复 */
    private static final long MAX_PAUSE_MS = 2 * 60 * 60 * 1000L;
    /** 剩余空间低于该值暂停录音（不退出，空间恢复后自动继续） */
    private static final long MIN_FREE_BYTES = 200L * 1024 * 1024;
    /** 低于该值提醒用户，但继续录 */
    private static final long LOW_SPACE_WARN_BYTES = 1024L * 1024 * 1024;
    /** 开段失败最大退避 */
    private static final long MAX_BACKOFF_MS = 5 * 60 * 1000L;
    /** 跨服务实例串行化录音和孤儿恢复，系统销毁服务时旧线程可能仍在关段。 */
    private static final ReentrantLock RECORDING_LOCK = new ReentrantLock();

    private long segmentMs = DEFAULT_SEGMENT_MS;

    private volatile boolean running = false;
    private Thread worker;
    private final Handler mainHandler = new Handler(Looper.getMainLooper());
    private boolean restartAfterStop = false;
    private boolean destroyed = false;
    private volatile boolean stoppedByUser = false;
    private int stopAfterWorkerId = 0;
    private MediaRecorder recorder;
    private File currentFile;
    private File currentPart;
    private long segmentStartMs;
    private PowerManager.WakeLock wakeLock;
    private AudioManager audioManager;
    private final ExecutorService cleanupExec = Executors.newSingleThreadExecutor();

    private boolean gapOpen = false;

    private long lowStartMs = 0L;
    private boolean lowFlagged = false;

    private long zeroStartMs = 0L;
    private volatile boolean zeroRecoverRequested = false;
    private long lastZeroRecoverMs = 0L;
    private long zeroRecoverWindowStart = 0L;
    private int zeroRecoverAttempts = 0;

    private long pauseStartMs = 0L;
    private boolean forcedResume = false;

    private long lastNotifMs = 0L;
    private int consecutiveFailures = 0;
    private boolean storageWarned = false;
    private boolean spaceWarned = false;
    private long lastCleanupMs = 0L;
    private volatile long lastLoopTickMs = 0L;

    @Override
    public void onCreate() {
        super.onCreate();
        audioManager = (AudioManager) getSystemService(AUDIO_SERVICE);
        Notifications.ensure(this);
        UploadStore.get(this).migrateFromPrefs(this);
        Uploader.start(this);
    }

    /** 进程被杀后留下的 .part：能读出 ADTS 头的就恢复成正式段，避免白丢。 */
    private void recoverParts() {
        File dir = recDir();
        File[] parts = dir.listFiles((d, n) -> n.endsWith(".part"));
        if (parts == null) {
            return;
        }
        for (File p : parts) {
            String name = p.getName();
            File dst = new File(dir, name.substring(0, name.length() - 5));
            if (Adts.recover(p)) {
                if (!dst.exists() && p.renameTo(dst)) {
                    Diag.log(this, TAG, "recovered orphan " + name + " -> " + dst.getName());
                    Health.segmentClosed(this, dst);
                } else {
                    Diag.log(this, TAG, "recover rename failed " + name);
                }
            } else if (p.length() == 0L) {
                // 只自动清理空文件；暂时不可读/恢复失败的文件留待下次恢复。
                //noinspection ResultOfMethodCallIgnored
                p.delete();
            }
        }
    }

    @Override
    public int onStartCommand(Intent intent, int flags, int startId) {
        String action = (intent != null && intent.getAction() != null)
                ? intent.getAction() : ACTION_START;

        if (ACTION_RESTART.equals(action)) {
            stopAfterWorkerId = 0;
            // 用户从通知点“恢复”：通知交互属于 Android 的免限制场景，
            // 重新打开 MediaRecorder 可以修复后台启动导致的麦克风静音。
            try {
                startForeground(NOTIF_ID, buildNotification("恢复录音中"),
                        ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE);
            } catch (Throwable e) {
                handleStartFailure(e);
                stopSelf();
                return START_NOT_STICKY;
            }
            getSharedPreferences(PREFS, MODE_PRIVATE).edit()
                    .putBoolean("enabled", true)
                    .putLong(KEY_NEXT_START_AT, 0L)
                    .commit();
            if (running) {
                stopRecording(false);
            }
            startRecording();
            return START_STICKY;
        }
        if (ACTION_STOP.equals(action)) {
            getSharedPreferences(PREFS, MODE_PRIVATE).edit()
                    .putBoolean("enabled", false)
                    .putLong(KEY_NEXT_START_AT, 0L)
                    .commit();
            stopRecording(true);
            stopForeground(true);
            // 关段完成前保留这个服务实例，快速再次开始时不会让新实例抢同一个 .part。
            stopAfterWorkerId = startId;
            if (worker == null || !worker.isAlive()) stopSelfResult(startId);
            return START_NOT_STICKY;
        }

        stopAfterWorkerId = 0;
        try {
            startForeground(NOTIF_ID, buildNotification("准备中"),
                    ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE);
        } catch (Throwable e) {
            handleStartFailure(e);
            stopSelf();
            return START_NOT_STICKY;
        }

        // 只有前台服务真正起来了才认为“用户希望一直录”。
        // 之前是先写 enabled=true 再 startForeground，失败后看门狗会每分钟重拉，
        // 形成永远失败的循环。
        getSharedPreferences(PREFS, MODE_PRIVATE).edit()
                .putBoolean("enabled", true)
                .putLong(KEY_NEXT_START_AT, 0L)
                .commit();
        Diag.log(this, TAG, "foreground started");

        // 进程还活着但录音线程死了时，看门狗发来的 START 要能真正重启循环。
        if (running) {
            boolean dead = worker == null || !worker.isAlive();
            boolean stuck = lastLoopTickMs > 0
                    && SystemClock.elapsedRealtime() - lastLoopTickMs > 5 * 60_000L;
            if (dead) {
                Diag.log(this, TAG, "restart dead loop");
                Health.error(this, "录音线程已退出，正在重启");
                stopRecording(false);
            } else if (stuck) {
                String msg = "录音线程 5 分钟没有心跳，可能被系统冻结；请打开 App 检查。";
                Health.error(this, msg);
                Notifications.alert(this, 2006, "录不停可能已卡住", msg);
            }
        }

        if (!running) {
            startRecording();
        }
        return START_STICKY;
    }

    private void handleStartFailure(Throwable e) {
        String msg = "前台服务启动失败：" + e;
        RecState.lastError = msg;
        Health.error(this, msg);
        Diag.error(this, TAG, "startForeground failed", e);

        boolean permission = e instanceof SecurityException;
        if (permission) {
            getSharedPreferences(PREFS, MODE_PRIVATE).edit()
                    .putBoolean("enabled", false)
                    .commit();
            Notifications.alert(this, 2001, "录不停已停止",
                    "麦克风或通知权限被拒绝，请打开 App 重新授权后再开始。");
            return;
        }

        // 后台启动限制：退避 15 分钟，而不是每分钟硬拉。
        getSharedPreferences(PREFS, MODE_PRIVATE).edit()
                .putLong(KEY_NEXT_START_AT, System.currentTimeMillis() + 15 * 60_000L)
                .commit();
        Notifications.alert(this, 2002, "录不停暂时无法启动",
                "系统限制了后台启动，15 分钟后会自动重试；也可以打开 App 手动开始。");
    }

    // ---------------- 录音主循环 ----------------

    private void startRecording() {
        if (destroyed) return;
        if (worker != null && worker.isAlive()) {
            // 旧线程可能仍在 prepare/stop；等它完成关段后再开始，不能共享录音器。
            restartAfterStop = true;
            return;
        }
        restartAfterStop = false;
        stoppedByUser = false;
        running = true;
        segmentMs = Settings.segmentMs(this);

        lowStartMs = 0L;
        lowFlagged = false;
        zeroStartMs = 0L;
        zeroRecoverRequested = false;
        lastZeroRecoverMs = 0L;
        zeroRecoverWindowStart = 0L;
        zeroRecoverAttempts = 0;
        pauseStartMs = 0L;
        forcedResume = false;
        lastNotifMs = 0L;
        consecutiveFailures = 0;
        storageWarned = false;
        spaceWarned = false;
        lastCleanupMs = 0L;
        lastLoopTickMs = SystemClock.elapsedRealtime();
        worker = new Thread(this::runRecordingLoop, "rec-loop");
        worker.start();

        if (Settings.getBool(this, Settings.KEY_WATCHDOG, true)) {
            scheduleWatchdog(60_000L);
        }
        Diag.log(this, TAG, "recording started, segment=" + segmentMs + "ms");
    }

    private void runRecordingLoop() {
        try {
            RECORDING_LOCK.lockInterruptibly();
            try {
                if (running) loop();
            } finally {
                RECORDING_LOCK.unlock();
            }
        } catch (InterruptedException ignored) {
            // 等待旧服务收尾时也可以取消，不会开启新的录音器。
        } finally {
            running = false;
            Thread finished = Thread.currentThread();
            mainHandler.post(() -> {
                if (worker != finished) return;
                worker = null;
                if (restartAfterStop && !destroyed) {
                    startRecording();
                } else if (stopAfterWorkerId != 0) {
                    stopSelfResult(stopAfterWorkerId);
                }
            });
        }
    }

    /** 按保留天数清理旧录音：保护未上传文件，且放到独立线程，不阻塞切段。 */
    private void cleanupOldAsync() {
        long now = SystemClock.elapsedRealtime();
        if (lastCleanupMs > 0L && now - lastCleanupMs < 30_000L) {
            return;
        }
        lastCleanupMs = now;
        cleanupExec.execute(() -> cleanupOld(recDir()));
    }

    private void cleanupOld(File dir) {
        int days = Settings.getInt(this, Settings.KEY_KEEP_DAYS, 7);
        if (days <= 0) {
            return;
        }
        long now = System.currentTimeMillis();
        long cutoff = now - days * 86_400_000L;
        long graceCutoff = now - (days + 3L) * 86_400_000L;
        boolean uploadOn = Settings.getBool(this, Settings.KEY_UPLOAD, false)
                && !Settings.getString(this, Settings.KEY_TOKEN, "").isEmpty();

        Set<String> pending = UploadStore.get(this).pendingNames();
        File[] fs = dir.listFiles();
        if (fs == null) {
            return;
        }
        int n = 0;
        for (File f : fs) {
            if (!f.isFile() || f.getName().endsWith(".part")) {
                continue;
            }
            if (!f.getName().endsWith(".aac") && !f.getName().endsWith(".m4a")) {
                continue;
            }
            long mtime = f.lastModified();
            if (mtime >= cutoff) {
                continue;
            }
            boolean inFlight = pending.contains(f.getName());
            if (uploadOn && inFlight && mtime >= graceCutoff) {
                // 还没上传成功：保留到宽限期，避免清理和上传互相打架。
                continue;
            }
            if (uploadOn && inFlight) {
                Diag.log(this, TAG, "cleanup: dropping unsent file after grace " + f.getName());
                Health.error(this, "上传积压超过保留期，已丢弃：" + f.getName());
            }
            if (f.delete()) {
                n++;
            }
        }

        // 已上传的旧索引也按保留期清掉，避免数据库无限长。
        UploadStore.get(this).prune(now - Math.max(days, 7) * 86_400_000L);
        if (n > 0) {
            Diag.log(this, TAG, "cleanup removed " + n + " files older than " + days + "d");
        }
    }

    private void loop() {
        try {
            RecState.running = true;
            RecState.paused = false;
            RecState.lowLevel = false;
            RecState.recordStartMs = System.currentTimeMillis();
            RecState.pausedAt = 0L;
            RecState.pausedTotalMs = 0L;
            RecState.segmentCount = 0;
            RecState.lastError = "";
            Health.serviceStart(this);
            acquireWake();
            recoverParts();
            Uploader.start(this);
            Uploader.scanPendingAsync(this);
            File dir = recDir();
            if (!dir.exists() && !dir.mkdirs()) {
                String msg = "无法创建录音目录：" + dir;
                RecState.lastError = msg;
                Health.error(this, msg);
                Notifications.alert(this, 2003, "录不停无法开始", msg);
                running = false;
                RecState.running = false;
                getSharedPreferences(PREFS, MODE_PRIVATE).edit()
                        .putLong(KEY_NEXT_START_AT, System.currentTimeMillis() + 15 * 60_000L)
                        .commit();
                return;
            }
            cleanupOldAsync();
            lastLoopTickMs = SystemClock.elapsedRealtime();

            while (running) {
                try {
                    lastLoopTickMs = SystemClock.elapsedRealtime();
                    segmentMs = Settings.segmentMs(this);

                    // 1. 通话中：暂停
                    if (callBlocking()) {
                        closeSegment();
                        markPauseStart();
                        openGap();
                        checkPauseTimeout();
                        sleepInterruptible(1000L);
                        continue;
                    }

                    pauseStartMs = 0L;

                    if (zeroRecoverRequested) {
                        zeroRecoverRequested = false;
                        recoverFromSilence();
                        sleepInterruptible(500L);
                    }

                    // 2. 空间检查：不退出循环，空间恢复后自动继续
                    long free = dir.getUsableSpace();
                    if (free < MIN_FREE_BYTES) {
                        closeSegment();
                        if (!storageWarned) {
                            storageWarned = true;
                            String msg = "存储空间不足（剩余 " + (free / 1048576)
                                    + " MB），录音已暂停；腾出空间后会自动继续。";
                            RecState.lastError = msg;
                            Health.error(this, msg);
                            notifyText(msg);
                            Notifications.alert(this, 2004, "录不停已暂停", msg);
                            Diag.log(this, TAG, msg);
                        }
                        sleepInterruptible(5000L);
                        continue;
                    }
                    if (storageWarned) {
                        storageWarned = false;
                        RecState.lastError = "";
                        notifyText("空间已恢复，继续录音");
                        Diag.log(this, TAG, "storage recovered");
                    }
                    if (!spaceWarned && free < LOW_SPACE_WARN_BYTES) {
                        spaceWarned = true;
                        Notifications.alert(this, 2005, "录不停空间偏低",
                                "剩余 " + (free / 1048576) + " MB，建议清理旧录音或缩短保留天数。");
                    } else if (spaceWarned && free > LOW_SPACE_WARN_BYTES * 3 / 2) {
                        spaceWarned = false;
                    }

                    // 3. 开新段：失败退避，永不因临时错误自杀
                    if (!openSegment(dir)) {
                        consecutiveFailures++;
                        long backoff = Math.min(MAX_BACKOFF_MS,
                                5000L * (1L << Math.min(consecutiveFailures, 5)));
                        if (consecutiveFailures == 3 || consecutiveFailures == 10
                                || consecutiveFailures % 30 == 0) {
                            String msg = "无法开始录音（第 " + consecutiveFailures + " 次）："
                                    + RecState.lastError + "；" + (backoff / 1000) + " 秒后重试";
                            Health.error(this, msg);
                            notifyText(msg);
                            Diag.log(this, TAG, msg);
                        }
                        sleepInterruptible(backoff);
                        continue;
                    }
                    consecutiveFailures = 0;

                    // 定期清理旧文件（大约每小时一次）
                    if (RecState.segmentCount % 20 == 0) {
                        cleanupOldAsync();
                    }

                    closeGap();
                    long start = SystemClock.elapsedRealtime();

                    // 4. 录制直到超时 / 来电
                    while (running && !callBlocking()
                            && SystemClock.elapsedRealtime() - start < segmentMs) {
                        sleepInterruptible(500L);
                        sampleLevel();
                        maybeNotify();
                        if (zeroRecoverRequested) {
                            // 先关掉这一段，回到外层循环重新声明前台并重建 MediaRecorder。
                            break;
                        }
                    }

                    boolean callNow = callBlocking();
                    closeSegment();
                    if (callNow) {
                        markPauseStart();
                        openGap();
                    }

                } catch (InterruptedException ie) {
                    break;
                } catch (Throwable t) {
                    RecState.lastError = String.valueOf(t);
                    Health.error(this, "录音循环异常：" + t);
                    Diag.error(this, TAG, "loop error", t);
                    closeSegment();
                    try {
                        sleepInterruptible(1000L);
                    } catch (InterruptedException ignored) {
                        break;
                    }
                }
            }
        } finally {
            boolean unexpected = running;
            running = false;
            try {
                // MediaRecorder 的关闭与采样/开段都只由这一个线程执行。
                closeSegment();
                if (gapOpen) closeGap();
            } finally {
                RecState.running = false;
                RecState.paused = false;
                releaseWake();
                Health.serviceStopped(this, stoppedByUser);
                Diag.log(this, TAG, "recording stopped user=" + stoppedByUser);
            }
            if (unexpected) {
                // 线程意外退出：把状态翻过来，让看门狗能重新拉起。
                Health.error(this, "录音循环意外退出");
                Diag.log(this, TAG, "loop exited unexpectedly");
            }
        }
    }

    private void sleepInterruptible(long ms) throws InterruptedException {
        long end = SystemClock.elapsedRealtime() + ms;
        while (running && SystemClock.elapsedRealtime() < end) {
            lastLoopTickMs = SystemClock.elapsedRealtime();
            Thread.sleep(Math.min(500L, Math.max(1L, end - SystemClock.elapsedRealtime())));
        }
        if (!running) {
            throw new InterruptedException("stopped");
        }
    }

    /** 通话是否应当阻止录音（含"暂停超时后强制恢复"逻辑） */
    private boolean callBlocking() {
        if (!inCall()) {
            forcedResume = false;
            return false;
        }
        return !forcedResume;
    }

    private void markPauseStart() {
        if (pauseStartMs == 0L) pauseStartMs = SystemClock.elapsedRealtime();
    }

    /** 暂停超过 2 小时，判定为模式卡住，强制恢复 */
    private void checkPauseTimeout() {
        if (pauseStartMs == 0L) return;
        long paused = SystemClock.elapsedRealtime() - pauseStartMs;
        if (!forcedResume && paused > MAX_PAUSE_MS) {
            forcedResume = true;
            writeGap("pause_timeout");
            String msg = "音频模式持续 " + (paused / 60000) + " 分钟未恢复，已强制尝试恢复录音";
            RecState.lastError = msg;
            Health.error(this, msg);
            Diag.log(this, TAG, msg);
            notifyText("长时间暂停，尝试恢复录音");
        }
    }

    /** 当前是否处于通话 / 语音通话 */
    private boolean inCall() {
        try {
            if (audioManager == null) return false;
            int mode = audioManager.getMode();
            return mode == AudioManager.MODE_IN_CALL
                    || mode == AudioManager.MODE_IN_COMMUNICATION
                    || mode == AudioManager.MODE_CALL_SCREENING;
        } catch (Exception e) {
            return false;
        }
    }

    /**
     * 采样电平，检测"被别的 App 抢麦"。
     * MediaRecorder.getMaxAmplitude() 返回上次调用以来的峰值（0~32767）。
     */
    private void sampleLevel() {
        MediaRecorder mr = recorder;
        if (mr == null) return;

        int amp;
        try {
            amp = mr.getMaxAmplitude();
        } catch (Throwable t) {
            return;
        }

        double db = 20 * Math.log10(Math.max(amp, 1) / 32767.0);
        long now = SystemClock.elapsedRealtime();
        RecState.levelDb = (float) db;
        RecState.levelSampleMs = now;

        // amp==0 是数字静音，正常房间的麦克风自噪声也不会是 0。
        // 这种情况多半是权限/AppOps/前台状态被系统拦了，光写日志没用，必须重启录音器。
        if (amp == 0) {
            if (zeroStartMs == 0L) {
                zeroStartMs = now;
            } else if (!zeroRecoverRequested
                    && now - zeroStartMs >= ZERO_RECOVER_MS
                    && (lastZeroRecoverMs == 0L
                            || now - lastZeroRecoverMs >= ZERO_RECOVER_COOLDOWN_MS)) {
                zeroRecoverRequested = true;
                Diag.log(this, TAG, "digital silence for "
                        + (now - zeroStartMs) / 1000 + " s, requesting recorder restart");
            }
        } else {
            zeroStartMs = 0L;
            // 连续正常一段时间后清空恢复计数，避免历史失败影响下一次。
            if (lastZeroRecoverMs > 0 && now - lastZeroRecoverMs > 5 * 60_000L) {
                zeroRecoverAttempts = 0;
                zeroRecoverWindowStart = 0L;
            }
        }

        long confirmMs = (amp == 0) ? SILENCE_CONFIRM_MS : LOW_CONFIRM_MS;

        if (db < LOW_DB) {
            if (lowStartMs == 0L) {
                lowStartMs = now;
            } else if (!lowFlagged && now - lowStartMs >= confirmMs) {
                lowFlagged = true;
                RecState.lowLevel = true;
                writeGap("low_level_start");
                if (amp == 0) {
                    Notifications.alert(this, 2007, "录不停麦克风无输入",
                            "录音还在继续，但系统没有收到声音。点这里打开 App，或点通知里的「恢复录音」。");
                }
                Diag.log(this, TAG, String.format(Locale.US,
                        "low level %.1f dB (amp=%d) for %d s", db, amp, confirmMs / 1000));
                notifyText(amp == 0
                        ? "麦克风无输入，正在尝试恢复"
                        : "音量异常低，可能被其他应用占用");
            }
        } else {
            if (lowFlagged) {
                lowFlagged = false;
                RecState.lowLevel = false;
                writeGap("low_level_end");
                Diag.log(this, TAG, "level recovered");
            }
            lowStartMs = 0L;
        }
    }

    /**
     * 数字静音恢复：重新确认前台服务状态，然后让外层循环重建 MediaRecorder。
     * 只重启 MediaRecorder 修不了 AppOps 拦截，所以先重新 startForeground。
     */
    private void recoverFromSilence() {
        long now = SystemClock.elapsedRealtime();
        if (zeroRecoverWindowStart == 0L || now - zeroRecoverWindowStart > ZERO_RECOVER_WINDOW_MS) {
            zeroRecoverWindowStart = now;
            zeroRecoverAttempts = 0;
        }
        zeroRecoverAttempts++;
        lastZeroRecoverMs = now;

        boolean permitted = checkSelfPermission(Manifest.permission.RECORD_AUDIO)
                == PackageManager.PERMISSION_GRANTED;
        int opMode = Integer.MIN_VALUE;
        try {
            AppOpsManager ops = getSystemService(AppOpsManager.class);
            if (ops != null) {
                opMode = ops.unsafeCheckOpNoThrow(AppOpsManager.OPSTR_RECORD_AUDIO,
                        Process.myUid(), getPackageName());
            }
        } catch (Throwable t) {
            Diag.error(this, TAG, "read RECORD_AUDIO appop failed", t);
        }
        Diag.log(this, TAG, "mic recover attempt=" + zeroRecoverAttempts
                + " permission=" + permitted + " appop=" + opMode);

        if (!permitted) {
            failMic("麦克风权限被拒绝，录音没有声音；请打开 App 重新授权。",
                    2007, "录不停没有麦克风权限");
            return;
        }
        if (opMode == AppOpsManager.MODE_IGNORED || opMode == AppOpsManager.MODE_ERRORED) {
            failMic("系统隐私设置阻止了麦克风（AppOps=" + opMode + "），请检查系统权限/隐私开关。",
                    2007, "录不停麦克风被系统阻止");
            return;
        }

        try {
            startForeground(NOTIF_ID, buildNotification("恢复录音中"),
                    ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE);
        } catch (Throwable t) {
            Diag.error(this, TAG, "recover startForeground failed", t);
        }

        if (zeroRecoverAttempts >= ZERO_RECOVER_MAX_ATTEMPTS) {
            failMic("连续 " + zeroRecoverAttempts + " 次检测到麦克风无输入，"
                            + "已暂停自动重启 10 分钟；请检查系统隐私开关、权限或其他占用麦克风的应用。",
                    2008, "录不停麦克风无声");
            return;
        }

        RecState.lastError = "";
        notifyText("检测到麦克风无输入，正在重启录音");
        Diag.log(this, TAG, "mic recover: recorder will be reopened");
    }

    private void failMic(String msg, int id, String title) {
        RecState.lastError = msg;
        Health.error(this, msg);
        Notifications.alert(this, id, title, msg);
        lastZeroRecoverMs = SystemClock.elapsedRealtime() + ZERO_RECOVER_PAUSE_MS;
        Diag.log(this, TAG, msg);
    }

    private boolean openSegment(File dir) {
        long timestamp = System.currentTimeMillis();
        String name;
        File f;
        File part;
        do {
            name = "rec_" + stamp(timestamp++) + ".aac";
            f = new File(dir, name);
            part = new File(dir, name + ".part");
        } while (f.exists() || part.exists());

        MediaRecorder mr = null;
        try {
            mr = new MediaRecorder();
            mr.setAudioSource(MediaRecorder.AudioSource.MIC);
            mr.setOutputFormat(MediaRecorder.OutputFormat.AAC_ADTS);
            mr.setAudioEncoder(MediaRecorder.AudioEncoder.AAC);
            int q = Settings.quality(this);
            mr.setAudioSamplingRate(Settings.sampleRate(q));
            mr.setAudioEncodingBitRate(Settings.bitRate(q));
            mr.setAudioChannels(1);
            // 先写 .part，close 成功后再 rename。这样扫描永远不会上传正在写的段，
            // 崩溃后也能一眼区分“已关闭”和“孤儿”。
            mr.setOutputFile(part.getAbsolutePath());
            mr.prepare();
            mr.start();

            recorder = mr;
            currentFile = f;
            currentPart = part;
            segmentStartMs = System.currentTimeMillis();
            RecState.currentFile = f.getName();
            RecState.segmentStartMs = segmentStartMs;
            RecState.segmentCount++;
            RecState.lastError = "";
            Diag.log(this, TAG, "segment opened " + name
                    + " quality=" + Settings.qualityLabel(Settings.quality(this)));
            return true;

        } catch (Throwable t) {
            RecState.lastError = "开段失败: " + t;
            Diag.error(this, TAG, "openSegment failed", t);
            if (mr != null) {
                try { mr.reset(); } catch (Exception ignored) {}
                try { mr.release(); } catch (Exception ignored) {}
            }
            recorder = null;
            currentFile = null;
            currentPart = null;
            // 编码器失败也可能已经写出部分音频，非空 .part 留待下次恢复。
            if (part.length() == 0L) {
                //noinspection ResultOfMethodCallIgnored
                part.delete();
            }
            return false;
        }
    }

    private void closeSegment() {
        MediaRecorder mr = recorder;
        recorder = null;
        File finalFile = currentFile;
        File part = currentPart;
        currentFile = null;
        currentPart = null;
        RecState.currentFile = "";
        RecState.segmentStartMs = 0L;

        boolean stoppedCleanly = false;
        if (mr != null) {
            try {
                mr.stop();
                stoppedCleanly = true;
            } catch (Throwable t) { Diag.log(this, TAG, "stop: " + t); }
            try { mr.reset(); } catch (Throwable ignored) {}
            try { mr.release(); } catch (Throwable ignored) {}
        }

        File closed = null;
        if (part != null && part.exists()) {
            long len = part.length();
            if (stoppedCleanly ? Adts.isValid(part) : Adts.recover(part)) {
                if (finalFile != null && !finalFile.exists() && part.renameTo(finalFile)) {
                    closed = finalFile;
                } else {
                    // rename 失败（极少见）：保留 .part，下次启动可恢复。
                    Diag.log(this, TAG, "rename part failed: " + part.getName());
                }
            } else {
                Diag.log(this, TAG, "unusable segment retained for recovery " + part.getName()
                        + " size=" + len);
                if (len == 0L) {
                    //noinspection ResultOfMethodCallIgnored
                    part.delete();
                }
            }
        } else if (finalFile != null && finalFile.exists()) {
            // 兼容旧版本直接写 .aac 的文件
            closed = finalFile;
        }

        // 段结束后重置电平检测窗口
        lowStartMs = 0L;
        if (lowFlagged) {
            lowFlagged = false;
            RecState.lowLevel = false;
            writeGap("low_level_end");
        }

        if (closed != null && closed.exists()) {
            Health.segmentClosed(this, closed);
            Uploader.enqueue(this, closed);
            Diag.log(this, TAG, "segment closed " + closed.getName()
                    + " size=" + closed.length());
        }
    }

    // ---------------- 通话空档标记 ----------------

    private void openGap() {
        if (gapOpen) return;
        gapOpen = true;
        RecState.paused = true;
        RecState.pausedAt = System.currentTimeMillis();
        writeGap("gap_start");
        Diag.log(this, TAG, "call detected, recording paused");
        notifyText("通话中，已暂停");
    }

    private void closeGap() {
        if (!gapOpen) return;
        gapOpen = false;
        if (RecState.pausedAt > 0L) {
            RecState.pausedTotalMs += Math.max(0L, System.currentTimeMillis() - RecState.pausedAt);
            RecState.pausedAt = 0L;
        }
        RecState.paused = false;
        writeGap("gap_end");
        Diag.log(this, TAG, "call ended, recording resumed");
    }

    private synchronized void writeGap(String type) {
        try {
            File f = new File(recDir(), "gaps.jsonl");
            FileWriter w = new FileWriter(f, true);
            w.write("{\"type\":\"" + type + "\",\"ts\":" + System.currentTimeMillis() + "}\n");
            w.close();
            Health.noteGap(this, type);
        } catch (Exception e) {
            Diag.error(this, TAG, "writeGap failed", e);
        }
    }

    // ---------------- 通知 ----------------

    private Notification buildNotification(String text) {
        Intent open = new Intent(this, MainActivity.class);
        PendingIntent piOpen = PendingIntent.getActivity(this, 1, open,
                PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);

        Intent stop = new Intent(this, RecorderService.class).setAction(ACTION_STOP);
        PendingIntent piStop = PendingIntent.getService(this, 2, stop,
                PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
        Intent restart = new Intent(this, RecorderService.class).setAction(ACTION_RESTART);
        PendingIntent piRestart = PendingIntent.getService(this, 3, restart,
                PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);

        return new Notification.Builder(this, CHANNEL_ID)
                .setContentTitle("录不停 · 正在录音")
                .setContentText(text)
                .setSmallIcon(R.drawable.ic_notification)
                .setContentIntent(piOpen)
                .setOngoing(true)
                .setOnlyAlertOnce(true)
                .addAction(new Notification.Action.Builder(
                        android.R.drawable.ic_media_pause, "停止", piStop).build())
                .addAction(new Notification.Action.Builder(
                        android.R.drawable.ic_media_play, "恢复录音", piRestart).build())
                .build();
    }

    private void maybeNotify() {
        long now = SystemClock.elapsedRealtime();
        if (now - lastNotifMs < NOTIF_INTERVAL_MS) return;
        lastNotifMs = now;
        updateNotification();
    }

    private void updateNotification() {
        long now = System.currentTimeMillis();
        long paused = RecState.pausedTotalMs;
        if (RecState.paused && RecState.pausedAt > 0L) {
            paused += Math.max(0L, now - RecState.pausedAt);
        }
        long ms = Math.max(0L, now - RecState.recordStartMs - paused);
        String text = "已录 " + fmt(ms) + " · 第 " + RecState.segmentCount + " 段";
        if (RecState.lowLevel) text += " · 音量异常";
        notifyText(text);
    }

    private void notifyText(String text) {
        try {
            NotificationManager nm = getSystemService(NotificationManager.class);
            if (nm != null) nm.notify(NOTIF_ID, buildNotification(text));
        } catch (Exception ignored) {}
    }

    // ---------------- 看门狗 / 唤醒锁 ----------------

    private void scheduleWatchdog(long delayMs) {
        try {
            android.app.AlarmManager am =
                    (android.app.AlarmManager) getSystemService(ALARM_SERVICE);
            Intent i = new Intent(this, WatchdogReceiver.class);
            PendingIntent pi = PendingIntent.getBroadcast(this, 10, i,
                    PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
            long at = System.currentTimeMillis() + delayMs;
            if (Build.VERSION.SDK_INT >= 31 && !am.canScheduleExactAlarms()) {
                am.setAndAllowWhileIdle(android.app.AlarmManager.RTC_WAKEUP, at, pi);
            } else {
                am.setExactAndAllowWhileIdle(android.app.AlarmManager.RTC_WAKEUP, at, pi);
            }
        } catch (Exception e) {
            Diag.error(this, TAG, "scheduleWatchdog failed", e);
        }
    }

    private void acquireWake() {
        try {
            PowerManager pm = (PowerManager) getSystemService(POWER_SERVICE);
            if (pm != null) {
                wakeLock = pm.newWakeLock(PowerManager.PARTIAL_WAKE_LOCK,
                        "RecNonstop::rec");
                wakeLock.setReferenceCounted(false);
                wakeLock.acquire();
            }
        } catch (Exception e) {
            Diag.error(this, TAG, "wakelock failed", e);
        }
    }

    private void stopRecording(boolean userStopped) {
        restartAfterStop = false;
        if (userStopped) stoppedByUser = true;
        running = false;
        if (worker != null) {
            worker.interrupt();
        }
    }

    private void releaseWake() {
        if (wakeLock != null && wakeLock.isHeld()) {
            wakeLock.release();
        }
        wakeLock = null;
    }

    @Override
    public void onDestroy() {
        destroyed = true;
        stopRecording(false);
        cleanupExec.shutdownNow();
        super.onDestroy();
    }

    @Override
    public void onTaskRemoved(Intent rootIntent) {
        Diag.log(this, TAG, "task removed");
        if (Settings.getBool(this, Settings.KEY_WATCHDOG, true)) {
            scheduleWatchdog(5_000L);
        }
        super.onTaskRemoved(rootIntent);
    }

    @Override
    public IBinder onBind(Intent intent) { return null; }

    // ---------------- 工具 ----------------

    public static File recDirOf(Context ctx) {
        File base = ctx.getExternalFilesDir("rec");
        if (base == null) base = new File(ctx.getFilesDir(), "rec");
        return base;
    }

    public static long nextStartAt(Context c) {
        SharedPreferences sp = c.getSharedPreferences(PREFS, Context.MODE_PRIVATE);
        return sp.getLong(KEY_NEXT_START_AT, 0L);
    }

    private File recDir() { return recDirOf(this); }

    private static String stamp(long timestamp) {
        return new SimpleDateFormat("yyyyMMdd_HHmmss_SSS", Locale.US)
                .format(new Date(timestamp));
    }

    private static String fmt(long ms) {
        long s = ms / 1000;
        return String.format(Locale.US, "%02d:%02d:%02d",
                s / 3600, (s % 3600) / 60, s % 60);
    }
}
