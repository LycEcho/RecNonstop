package com.recnonstop.app;

import android.content.Context;
import android.net.ConnectivityManager;
import android.net.Network;
import android.net.NetworkCapabilities;
import android.net.NetworkRequest;
import android.util.Log;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.BufferedInputStream;
import java.io.BufferedOutputStream;
import java.io.BufferedReader;
import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileInputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.security.MessageDigest;
import java.text.ParsePosition;
import java.text.SimpleDateFormat;
import java.util.Arrays;
import java.util.Comparator;
import java.util.Date;
import java.util.Locale;
import java.util.Set;
import java.util.concurrent.ConcurrentHashMap;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.ScheduledExecutorService;
import java.util.concurrent.TimeUnit;
import java.util.concurrent.atomic.AtomicBoolean;

/**
 * 上传队列。
 *
 * - 上传和结果轮询分成两个线程池：以前上传线程会在 pollResult 里睡最多 120 秒，
 *   一个慢任务就把后面所有段堵住。
 * - 失败状态、重试次数、退避时间、job_id、摘要都在 UploadStore（SQLite）。
 * - Wi-Fi 恢复、每 5 分钟定时扫描都会自动补传，不依赖用户打开 App。
 * - 请求带幂等键，服务端重启/重复上传不会产生重复 job。
 */
public final class Uploader {

    private static final String TAG = "RecNonstopUpload";
    private static final int MAX_JSON_BYTES = 8 * 1024 * 1024;

    static final class HttpError extends IOException {
        final int code;

        HttpError(int code, String body) {
            super("HTTP " + code + ": " + body);
            this.code = code;
        }
    }

    private static final ExecutorService UPLOAD_EXEC = Executors.newSingleThreadExecutor();
    private static final ExecutorService SCAN_EXEC = Executors.newSingleThreadExecutor();
    private static final ScheduledExecutorService POLL_EXEC =
            Executors.newScheduledThreadPool(2);
    private static final ScheduledExecutorService TICK_EXEC =
            Executors.newSingleThreadScheduledExecutor();
    // 电脑端“立即同步”指令：手机定时来取，不必在手机上开端口。
    private static final ScheduledExecutorService HB_EXEC =
            Executors.newSingleThreadScheduledExecutor();
    private static final Set<String> QUEUED = ConcurrentHashMap.newKeySet();
    private static final Set<String> POLLING = ConcurrentHashMap.newKeySet();
    private static final AtomicBoolean SCANNING = new AtomicBoolean(false);

    private static volatile Context appCtx;
    private static volatile boolean started = false;
    private static ConnectivityManager.NetworkCallback networkCallback;

    private Uploader() {
    }

    public static void start(Context c) {
        Context app = c.getApplicationContext();
        appCtx = app;
        if (started) {
            return;
        }
        synchronized (Uploader.class) {
            if (started) {
                return;
            }
            started = true;
            UploadStore.get(app).migrateFromPrefs(app);
            registerNetwork(app);
            Presence.start(app);
            TICK_EXEC.scheduleWithFixedDelay(() -> {
                try {
                    scanPendingAsync(app);
                    pollOutstanding(app);
                } catch (Throwable t) {
                    Log.w(TAG, "periodic scan failed", t);
                }
            }, 5, 5, TimeUnit.MINUTES);
            HB_EXEC.scheduleWithFixedDelay(() -> {
                try {
                    heartbeat(app);
                } catch (Throwable t) {
                    Log.w(TAG, "heartbeat failed", t);
                }
            }, 10, 30, TimeUnit.SECONDS);
        }
    }

    private static void registerNetwork(Context app) {
        try {
            ConnectivityManager cm =
                    (ConnectivityManager) app.getSystemService(Context.CONNECTIVITY_SERVICE);
            if (cm == null || networkCallback != null) {
                return;
            }
            NetworkRequest req = new NetworkRequest.Builder()
                    .addTransportType(NetworkCapabilities.TRANSPORT_WIFI)
                    .addTransportType(NetworkCapabilities.TRANSPORT_ETHERNET)
                    .build();
            networkCallback = new ConnectivityManager.NetworkCallback() {
                @Override
                public void onAvailable(Network network) {
                    Diag.log(app, TAG, "network available, retry uploads");
                    scanPendingAsync(app);
                }
            };
            cm.registerNetworkCallback(req, networkCallback);
        } catch (Throwable t) {
            Diag.error(app, TAG, "registerNetworkCallback failed", t);
        }
    }

    // ---------------- 扫描 / 入队 ----------------

    public static void scanPendingAsync(Context c) {
        scanPendingAsync(c, false);
    }

    public static void scanPendingAsync(Context c, boolean force) {
        Context app = c.getApplicationContext();
        appCtx = app;
        if (!Settings.getBool(app, Settings.KEY_UPLOAD, false)) {
            return;
        }
        if (!SCANNING.compareAndSet(false, true)) {
            return;
        }
        SCAN_EXEC.execute(() -> {
            try {
                scanPendingNow(app, force);
            } catch (Throwable t) {
                Diag.error(app, TAG, "scanPending failed", t);
            } finally {
                SCANNING.set(false);
            }
        });
    }

    private static void scanPendingNow(Context app, boolean force) {
        String token = Settings.getString(app, Settings.KEY_TOKEN, "");
        if (token == null || token.isEmpty()) {
            RecState.uploadStatus = "未连接：请先「发现电脑」并在电脑上点允许";
            return;
        }

        File dir = RecorderService.recDirOf(app);
        File[] files = dir.listFiles((d, n) -> n.endsWith(".aac") || n.endsWith(".m4a"));
        if (files == null) {
            return;
        }
        Arrays.sort(files, Comparator.comparing(File::getName));

        UploadStore store = UploadStore.get(app);
        Set<String> uploaded = store.uploadedNames();
        Set<String> notUploaded = store.pendingNames();
        // 只有到了 next_retry_at 的才算 ready；否则退避/隔离中的文件会被每 5 分钟
        // 的定时扫描重新拉起来，退避就形同虚设。
        Set<String> ready = force
                ? new java.util.HashSet<>()
                : new java.util.HashSet<>(store.pending(200));
        int pending = 0;
        int enqueued = 0;
        for (File f : files) {
            String name = f.getName();
            if (uploaded.contains(name) || !isUploadable(f)) {
                continue;
            }
            if (!notUploaded.contains(name)) {
                // 数据库里还没有这行：新文件，立即入队。
                store.markPending(name);
                ready.add(name);
                notUploaded.add(name);
            }
            pending++;
            if (force) {
                ready.add(name);
            }
            if (ready.contains(name) && enqueued < 200) {
                enqueueInternal(app, f);
                enqueued++;
            }
        }
        RecState.pendingCount = pending;
        int[] counts = store.counts();
        RecState.uploadedCount = counts[0];
        if (pending > 0 && (RecState.uploadStatus == null || RecState.uploadStatus.isEmpty())) {
            RecState.uploadStatus = "待上传 " + pending + " 段";
        }
    }

    public static void enqueue(Context ctx, File f) {
        Context app = ctx.getApplicationContext();
        if (!isUploadable(f)) {
            return;
        }
        String name = f.getName();
        if (!Settings.getBool(app, Settings.KEY_UPLOAD, false)) {
            return;
        }
        String token = Settings.getString(app, Settings.KEY_TOKEN, "");
        if (token == null || token.isEmpty()) {
            RecState.uploadStatus = "未连接：请先「发现电脑」并在电脑上点允许";
            return;
        }
        if (UploadStore.get(app).isUploaded(name)) {
            return;
        }
        enqueueInternal(app, f);
    }

    private static void enqueueInternal(Context app, File f) {
        String name = f.getName();
        if (!QUEUED.add(name)) {
            return;
        }
        UploadStore.get(app).markPending(name);
        RecState.uploadStatus = "排队上传：" + name;
        UPLOAD_EXEC.execute(() -> {
            try {
                uploadOne(app, f);
            } catch (Throwable t) {
                Diag.error(app, TAG, "upload task failed", t);
            } finally {
                QUEUED.remove(name);
            }
        });
    }

    public static void test(Context ctx) {
        Context app = ctx.getApplicationContext();
        String base = Settings.getPcUrl(app);
        RecState.uploadStatus = "测试连接：" + base;
        UPLOAD_EXEC.execute(() -> {
            try {
                String body = get(app, base + "/health");
                RecState.uploadStatus = "电脑连接正常：" + body;
            } catch (Exception e) {
                RecState.uploadStatus = "电脑连接失败：" + e.getMessage();
            }
        });
    }

    // ---------------- 上传 ----------------

    private static void uploadOne(Context ctx, File f) {
        // 排队后用户可能已关闭上传；执行时再次检查，避免继续发送积压录音。
        if (!Settings.getBool(ctx, Settings.KEY_UPLOAD, false)) return;
        String base = Settings.getPcUrl(ctx);
        if (base.isEmpty()) {
            RecState.uploadStatus = "未设置电脑地址";
            return;
        }
        if (Settings.getBool(ctx, Settings.KEY_WIFI_ONLY, true) && !isWifi(ctx)) {
            // 不是失败，等 Wi-Fi；不清 attempts，下一轮扫描会再来。
            RecState.uploadStatus = "等待 Wi-Fi：" + f.getName();
            return;
        }

        UploadStore store = UploadStore.get(ctx);
        String name = f.getName();
        String key = idempotencyKey(f);
        try {
            RecState.uploadStatus = "上传中：" + name;
            String resp = postFile(ctx, base + "/api/v1/jobs", f, key);
            String jobId = new JSONObject(resp).optString("job_id", "");
            if (jobId.isEmpty()) {
                throw new IOException("电脑未确认接收任务，请稍后重试");
            }

            store.markUploaded(name, jobId);
            Health.uploadOk(ctx, name);
            RecState.uploadedCount++;
            RecState.pendingCount = Math.max(0, RecState.pendingCount - 1);
            Diag.log(ctx, TAG, "uploaded " + name + " job=" + jobId);

            RecState.lastJobId = jobId;
            RecState.uploadStatus = "已上传，分析中：" + name;
            pollAsync(ctx, base, jobId, name);
        } catch (Exception e) {
            int code = e instanceof HttpError ? ((HttpError) e).code : 0;
            boolean permanent = code == 400 || code == 413 || code == 415;
            store.markFailed(name, e.getMessage(), permanent);
            RecState.uploadStatus = (permanent ? "文件无法上传：" : "上传失败，稍后重试：")
                    + name + " · " + e.getMessage();
            Diag.error(ctx, TAG, "upload failed " + name, e);
        }
    }

    private static void pollAsync(Context ctx, String base, String jobId, String name) {
        if (!POLLING.add(jobId)) {
            return;
        }
        POLL_EXEC.execute(() -> {
            try {
                pollResult(ctx, base, jobId, name);
            } finally {
                POLLING.remove(jobId);
            }
        });
    }

    private static void pollResult(Context ctx, String base, String jobId, String name) {
        for (int i = 0; i < 20; i++) {
            if (!Settings.getBool(ctx, Settings.KEY_UPLOAD, false)) {
                return;
            }
            try {
                Thread.sleep(3000L);
            } catch (InterruptedException e) {
                return;
            }
            try {
                JSONObject o = new JSONObject(get(ctx, base + "/api/v1/jobs/" + jobId));
                String status = o.optString("status");
                if ("done".equals(status) || "failed".equals(status)) {
                    String summary = "";
                    try {
                        JSONObject r = new JSONObject(
                                get(ctx, base + "/api/v1/jobs/" + jobId + "/result"));
                        summary = r.optString("summary", "");
                        if (summary.isEmpty()) {
                            summary = r.optString("error", "");
                        }
                    } catch (HttpError e) {
                        // 失败任务可能从未产生结果；其他错误（断网、5xx、解析失败）
                        // 必须继续重试，不能把占位文本当成永久摘要存下来。
                        if (e.code != 409 || !"failed".equals(status)) throw e;
                        summary = o.optString("error", "");
                    }
                    if (summary.isEmpty()) {
                        summary = "done".equals(status) ? "（无有效语音）" : "分析失败（无结果）";
                    }
                    UploadStore.get(ctx).setSummary(name, summary);
                    if (!summary.isEmpty()) {
                        RecState.lastSummary = summary;
                    }
                    RecState.uploadStatus = ("done".equals(status) ? "分析完成：" : "分析失败：")
                            + name;
                    return;
                }
                RecState.uploadStatus = "分析中：" + name + " (" + status + ")";
            } catch (Exception e) {
                if (e instanceof HttpError && ((HttpError) e).code == 404) {
                    // PC 端任务被清掉了：标记失败，下一轮扫描会重新上传。
                    UploadStore.get(ctx).markFailed(name, "job not found on PC");
                    RecState.uploadStatus = "电脑端任务丢失，稍后重传：" + name;
                    return;
                }
                Log.w(TAG, "poll failed", e);
            }
        }
        RecState.uploadStatus = "已上传，分析较慢：" + name;
    }

    /**
     * 每 30 秒问一次电脑有没有指令/日报。电脑页面点“同步”后，
     * 手机在这里收到 sync，立刻扫一遍待上传的分段；日报生成后也在这里推送。
     */
    private static void heartbeat(Context app) {
        if (!Settings.getBool(app, Settings.KEY_UPLOAD, false)) return;
        if (!Settings.connected(app)) return;
        if (Settings.getBool(app, Settings.KEY_WIFI_ONLY, true) && !isWifi(app)) return;
        try {
            JSONObject o = apiGet(app, "/api/v1/commands");
            if (o.optBoolean("sync", false)) {
                Diag.log(app, TAG, "sync requested by pc");
                scanPendingAsync(app, true);
            }
            DailyReports.handleCommands(app, o);
        } catch (Exception ignored) {
            // 电脑没开或网络切换中，下一轮再试。
        }
    }

    static JSONObject apiGet(Context ctx, String path) throws IOException {
        try {
            return new JSONObject(get(ctx, Settings.getPcUrl(ctx) + path));
        } catch (org.json.JSONException e) {
            throw new IOException("电脑返回了无法解析的数据", e);
        }
    }

    static JSONObject apiPost(Context ctx, String path, JSONObject body) throws IOException {
        HttpURLConnection c = (HttpURLConnection) new URL(
                Settings.getPcUrl(ctx) + path).openConnection();
        try {
            c.setConnectTimeout(10000);
            c.setReadTimeout(30000);
            c.setRequestMethod("POST");
            c.setDoOutput(true);
            c.setRequestProperty("Content-Type", "application/json; charset=utf-8");
            addAuth(ctx, c);
            try (OutputStream out = c.getOutputStream()) {
                out.write((body == null ? "{}" : body.toString())
                        .getBytes(StandardCharsets.UTF_8));
            }
            String text = readResponse(c);
            if (text.isEmpty()) {
                return new JSONObject();
            }
            try {
                return new JSONObject(text);
            } catch (org.json.JSONException e) {
                throw new IOException("电脑返回了无法解析的数据", e);
            }
        } finally {
            c.disconnect();
        }
    }

    /** 补轮询：上传成功但摘要为空的任务（PC 重启/超时）。 */
    private static void pollOutstanding(Context app) {
        if (!Settings.getBool(app, Settings.KEY_UPLOAD, false)) return;
        String token = Settings.getString(app, Settings.KEY_TOKEN, "");
        if (token == null || token.isEmpty()) {
            return;
        }
        String base = Settings.getPcUrl(app);
        for (UploadStore.JobRef ref : UploadStore.get(app).outstanding(20)) {
            pollAsync(app, base, ref.jobId, ref.name);
        }
    }

    private static String postFile(Context ctx, String urlStr, File f, String idemKey)
            throws IOException {
        String boundary = "----RecNonstop" + System.currentTimeMillis();
        HttpURLConnection c = (HttpURLConnection) new URL(urlStr).openConnection();
        try {
            c.setConnectTimeout(15000);
            c.setReadTimeout(60000);
            c.setRequestMethod("POST");
            c.setDoOutput(true);
            c.setChunkedStreamingMode(8192);
            c.setRequestProperty("Content-Type", "multipart/form-data; boundary=" + boundary);
            c.setRequestProperty("X-Idempotency-Key", idemKey);
            addAuth(ctx, c);

            try (OutputStream raw = new BufferedOutputStream(c.getOutputStream())) {
                writeField(raw, boundary, "device", Settings.deviceName(ctx));
                writeField(raw, boundary, "device_id",
                        Settings.getString(ctx, Settings.KEY_DEVICE_ID, ""));
                writeField(raw, boundary, "recorded_at", String.valueOf(recordedAt(f)));
                writeField(raw, boundary, "segment_name", uploadName(f));
                String gaps = gapsForSegment(ctx, f);
                if (!gaps.isEmpty()) {
                    writeField(raw, boundary, "gaps", gaps);
                }
                writeFile(raw, boundary, "file", f);
                raw.write(("--" + boundary + "--\r\n").getBytes(StandardCharsets.UTF_8));
            }

            return readResponse(c);
        } finally {
            c.disconnect();
        }
    }

    private static void writeField(OutputStream os, String boundary, String name, String value)
            throws IOException {
        String s = "--" + boundary + "\r\n"
                + "Content-Disposition: form-data; name=\"" + name + "\"\r\n\r\n"
                + value + "\r\n";
        os.write(s.getBytes(StandardCharsets.UTF_8));
    }

    private static void writeFile(OutputStream os, String boundary, String field, File f)
            throws IOException {
        String name = uploadName(f);
        String mime = name.endsWith(".aac") ? "audio/aac" : "audio/mp4";
        String head = "--" + boundary + "\r\n"
                + "Content-Disposition: form-data; name=\"" + field + "\"; filename=\""
                + name + "\"\r\n"
                + "Content-Type: " + mime + "\r\n\r\n";
        os.write(head.getBytes(StandardCharsets.UTF_8));
        try (InputStream in = new BufferedInputStream(new FileInputStream(f))) {
            byte[] buf = new byte[8192];
            int n;
            while ((n = in.read(buf)) > 0) {
                os.write(buf, 0, n);
            }
        }
        os.write("\r\n".getBytes(StandardCharsets.UTF_8));
    }

    private static String uploadName(File f) {
        return f.getName();
    }

    private static boolean isUploadable(File f) {
        if (f == null || !f.isFile()) return false;
        String name = f.getName();
        if (name.endsWith(".aac")) return Adts.isValid(f);
        return name.endsWith(".m4a") && f.length() >= 1024;
    }

    /** 文件修改时间是段结束时间；标准文件名保存的是实际开始时间。 */
    private static long recordedAt(File f) {
        String name = f.getName();
        if (name.matches("rec_[0-9]{8}_[0-9]{6}(_[0-9]{3})?\\.(aac|m4a)")) {
            String stamp = name.substring(4, name.lastIndexOf('.'));
            String pattern = stamp.length() == 19 ? "yyyyMMdd_HHmmss_SSS" : "yyyyMMdd_HHmmss";
            SimpleDateFormat format = new SimpleDateFormat(pattern, Locale.US);
            format.setLenient(false);
            ParsePosition pos = new ParsePosition(0);
            Date parsed = format.parse(stamp, pos);
            if (parsed != null && pos.getIndex() == stamp.length()) return parsed.getTime();
        }
        return f.lastModified();
    }

    private static String gapsForSegment(Context ctx, File f) {
        File gaps = new File(RecorderService.recDirOf(ctx), "gaps.jsonl");
        if (!gaps.exists()) {
            return "";
        }
        long from = recordedAt(f) - 2000L;
        long to = f.lastModified() + 2000L;
        JSONArray arr = new JSONArray();
        try (InputStream raw = new FileInputStream(gaps)) {
            // gaps.jsonl 跑一年可能到 MB 级；只读尾部 256KB，避免每段上传都全量扫。
            long skip = Math.max(0, gaps.length() - 256 * 1024L);
            long skipped = 0;
            while (skipped < skip) {
                long n = raw.skip(skip - skipped);
                if (n <= 0) break;
                skipped += n;
            }
            BufferedReader r = new BufferedReader(new InputStreamReader(
                    raw, StandardCharsets.UTF_8));
            String line;
            int n = 0;
            while ((line = r.readLine()) != null && n < 500) {
                try {
                    JSONObject o = new JSONObject(line);
                    long ts = o.optLong("ts", 0L);
                    if (ts >= from && ts <= to) {
                        arr.put(o);
                        n++;
                    }
                } catch (Exception ignored) {
                }
            }
        } catch (Exception ignored) {
        }
        return arr.length() == 0 ? "" : arr.toString();
    }

    private static String get(Context ctx, String urlStr) throws IOException {
        HttpURLConnection c = (HttpURLConnection) new URL(urlStr).openConnection();
        try {
            c.setConnectTimeout(10000);
            c.setReadTimeout(30000);
            addAuth(ctx, c);
            return readResponse(c);
        } finally {
            c.disconnect();
        }
    }

    private static String readResponse(HttpURLConnection c) throws IOException {
        int code = c.getResponseCode();
        InputStream in = (code >= 200 && code < 300) ? c.getInputStream() : c.getErrorStream();
        String body = readAll(in, MAX_JSON_BYTES);
        if (code == 401) {
            throw new HttpError(code, "未连接或连接已失效");
        }
        if (code == 429) {
            throw new HttpError(code, "电脑端队列已满，稍后重试");
        }
        if (code < 200 || code >= 300) {
            throw new HttpError(code, body);
        }
        return body;
    }

    private static void addAuth(Context ctx, HttpURLConnection c) {
        String token = Settings.getString(ctx, Settings.KEY_TOKEN, "");
        if (token != null && !token.isEmpty()) {
            c.setRequestProperty("Authorization", "Bearer " + token);
        }
    }

    private static String readAll(InputStream in, int max) throws IOException {
        if (in == null) return "";
        try (InputStream i = in; ByteArrayOutputStream out = new ByteArrayOutputStream()) {
            byte[] buf = new byte[8192];
            int n;
            int total = 0;
            while ((n = i.read(buf)) > 0) {
                if (n > max - total) throw new IOException("电脑返回的数据过大");
                out.write(buf, 0, n);
                total += n;
            }
            return out.toString("UTF-8");
        }
    }

    private static boolean isWifi(Context ctx) {
        try {
            ConnectivityManager cm =
                    (ConnectivityManager) ctx.getSystemService(Context.CONNECTIVITY_SERVICE);
            if (cm == null) return true;
            Network n = cm.getActiveNetwork();
            if (n == null) return false;
            NetworkCapabilities cap = cm.getNetworkCapabilities(n);
            return cap != null && (cap.hasTransport(NetworkCapabilities.TRANSPORT_WIFI)
                    || cap.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET));
        } catch (Exception e) {
            return true;
        }
    }

    private static String idempotencyKey(File f) {
        String raw = f.getName() + ":" + f.length() + ":" + f.lastModified();
        try {
            MessageDigest md = MessageDigest.getInstance("SHA-256");
            byte[] d = md.digest(raw.getBytes(StandardCharsets.UTF_8));
            StringBuilder sb = new StringBuilder(d.length * 2);
            for (byte b : d) {
                sb.append(String.format(Locale.US, "%02x", b));
            }
            return sb.toString();
        } catch (Exception e) {
            return Integer.toHexString(raw.hashCode());
        }
    }
}
