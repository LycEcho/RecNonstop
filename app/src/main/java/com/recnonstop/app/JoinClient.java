package com.recnonstop.app;

import android.content.Context;

import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;
import java.util.concurrent.atomic.AtomicInteger;

/**
 * 在 app 内完成连接：手机发申请，电脑页面点“允许”，手机拿到 token。
 * 不需要 6 位配对码，也不需要去系统设置里做蓝牙配对。
 */
public final class JoinClient {

    private static final ExecutorService EXEC = Executors.newCachedThreadPool();
    private static final AtomicInteger GEN = new AtomicInteger();
    private static final long WAIT_MS = 180_000L;

    public interface Callback {
        void onResult(boolean ok, String message);
    }

    private JoinClient() {
    }

    /** 取消当前等待（用户点了取消）。 */
    public static void cancel() {
        GEN.incrementAndGet();
    }

    public static void join(Context ctx, Discovery.Device dev, Callback cb) {
        joinHost(ctx, dev.host, dev.port, cb);
    }

    public static void joinHost(Context ctx, String host, int port, Callback cb) {
        join(ctx, host, port, "", cb);
    }

    /** 电脑先点了“连接”，手机凭一次性邀请直接完成，不用再等对方确认。 */
    public static void acceptInvite(Context ctx, String host, int port, String inviteToken, Callback cb) {
        join(ctx, host, port, inviteToken, cb);
    }

    private static void join(Context ctx, String host, int port, String inviteToken, Callback cb) {
        Context app = ctx.getApplicationContext();
        final int gen = GEN.incrementAndGet();
        EXEC.execute(() -> {
            try {
                String base = "http://" + host + ":" + port;

                JSONObject req = new JSONObject();
                req.put("device_id", Settings.installId(app));
                req.put("device_name", Settings.deviceName(app));
                req.put("fp", Settings.fingerprint(Settings.installId(app)));
                if (inviteToken != null && !inviteToken.isEmpty()) {
                    req.put("invite", inviteToken);
                }

                JSONObject created = new JSONObject(post(base + "/api/v1/join", req.toString()));
                String rid = created.optString("request_id", "");
                if (rid.isEmpty()) {
                    throw new IOException("电脑没有返回请求编号");
                }
                JSONObject pc = created.optJSONObject("pc");
                String pcId = pc == null ? "" : pc.optString("id", "");
                String pcName = pc == null ? host : pc.optString("name", host);
                String pcFp = pc == null ? "" : pc.optString("fp", "");

                long deadline = System.currentTimeMillis() + WAIT_MS;
                while (System.currentTimeMillis() < deadline) {
                    if (GEN.get() != gen) {
                        cb.onResult(false, "已取消");
                        return;
                    }

                    JSONObject st;
                    try {
                        st = new JSONObject(get(base + "/api/v1/join/" + rid));
                    } catch (Exception e) {
                        // 电脑短暂不可达/wifi 切换，继续等到超时。
                        Thread.sleep(1500);
                        continue;
                    }
                    String status = st.optString("status", "");
                    if ("approved".equals(status)) {
                        String token = st.optString("token", "");
                        if (token.isEmpty()) {
                            throw new IOException("电脑没有返回 token");
                        }
                        Settings.setString(app, Settings.KEY_TOKEN, token);
                        Settings.setString(app, Settings.KEY_PC_URL, base);
                        Settings.setString(app, Settings.KEY_PC_NAME, pcName);
                        Settings.setString(app, Settings.KEY_DEVICE_ID,
                                st.optString("device_id", Settings.installId(app)));
                        Settings.setString(app, Settings.KEY_PC_ID, pcId);
                        Settings.setString(app, Settings.KEY_PC_FP, pcFp);
                        cb.onResult(true, pcName);
                        return;
                    }
                    if ("rejected".equals(status)) {
                        cb.onResult(false, "电脑拒绝了连接请求");
                        return;
                    }
                    if ("expired".equals(status)) {
                        cb.onResult(false, "等待超时，请重新点一次连接");
                        return;
                    }
                    Thread.sleep(1500);
                }
                cb.onResult(false, "等待电脑确认超时");
            } catch (Exception e) {
                cb.onResult(false, e.getMessage() == null ? e.toString() : e.getMessage());
            }
        });
    }

    private static String post(String url, String json) throws IOException {
        HttpURLConnection c = (HttpURLConnection) new URL(url).openConnection();
        c.setConnectTimeout(8000);
        c.setReadTimeout(15000);
        c.setRequestMethod("POST");
        c.setDoOutput(true);
        c.setRequestProperty("Content-Type", "application/json");
        try (OutputStream os = c.getOutputStream()) {
            os.write(json.getBytes(StandardCharsets.UTF_8));
        }
        int code = c.getResponseCode();
        InputStream in = (code >= 200 && code < 300) ? c.getInputStream() : c.getErrorStream();
        String body = readAll(in);
        if (code == 404) {
            throw new IOException("电脑端版本太旧，请更新 pc-server");
        }
        if (code < 200 || code >= 300) {
            throw new IOException("HTTP " + code + ": " + body);
        }
        return body;
    }

    private static String get(String url) throws IOException {
        HttpURLConnection c = (HttpURLConnection) new URL(url).openConnection();
        c.setConnectTimeout(5000);
        c.setReadTimeout(10000);
        int code = c.getResponseCode();
        InputStream in = (code >= 200 && code < 300) ? c.getInputStream() : c.getErrorStream();
        String body = readAll(in);
        if (code < 200 || code >= 300) {
            throw new IOException("HTTP " + code + ": " + body);
        }
        return body;
    }

    private static String readAll(InputStream in) throws IOException {
        if (in == null) return "";
        try (InputStream i = in; ByteArrayOutputStream out = new ByteArrayOutputStream()) {
            byte[] buf = new byte[4096];
            int n;
            while ((n = i.read(buf)) > 0) out.write(buf, 0, n);
            return out.toString("UTF-8");
        }
    }
}
