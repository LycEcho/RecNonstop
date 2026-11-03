package com.recnonstop.app;

import android.content.Context;
import android.os.Handler;
import android.os.Looper;

import org.json.JSONObject;

import java.io.BufferedReader;
import java.io.InputStream;
import java.io.InputStreamReader;
import java.io.OutputStream;
import java.net.HttpURLConnection;
import java.net.URL;
import java.nio.charset.StandardCharsets;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/**
 * 手机 -> PC 的轻量 API。只用于读取转写和标记说话人，不把音频/声纹放到第三方。
 */
final class PcApi {

    interface JsonCallback {
        void onResult(JSONObject body, String error);
    }

    private static final ExecutorService EXEC = Executors.newFixedThreadPool(2);
    private static final Handler MAIN = new Handler(Looper.getMainLooper());

    private PcApi() {
    }

    static void getResult(Context c, String jobId, JsonCallback cb) {
        request(c, "GET", "/api/v1/jobs/" + jobId + "/result", null, cb);
    }

    static void markSpeaker(Context c, String jobId, String speaker, String name, JsonCallback cb) {
        JSONObject body = new JSONObject();
        try {
            body.put("speaker", speaker);
            body.put("name", name);
        } catch (Exception ignored) {
        }
        request(c, "POST", "/api/v1/jobs/" + jobId + "/speaker", body, cb);
    }

    private static void request(Context c, String method, String path, JSONObject body, JsonCallback cb) {
        final Context app = c.getApplicationContext();
        EXEC.execute(() -> {
            HttpURLConnection conn = null;
            try {
                String base = Settings.getPcUrl(app);
                String token = Settings.getString(app, Settings.KEY_TOKEN, "");
                if (token == null || token.isEmpty()) {
                    finish(cb, null, "未连接电脑");
                    return;
                }
                conn = (HttpURLConnection) new URL(base + path).openConnection();
                conn.setRequestMethod(method);
                conn.setConnectTimeout(8000);
                conn.setReadTimeout(30000);
                conn.setRequestProperty("Authorization", "Bearer " + token);
                conn.setRequestProperty("Accept", "application/json");
                if (body != null) {
                    byte[] raw = body.toString().getBytes(StandardCharsets.UTF_8);
                    conn.setDoOutput(true);
                    conn.setRequestProperty("Content-Type", "application/json; charset=utf-8");
                    conn.setFixedLengthStreamingMode(raw.length);
                    try (OutputStream out = conn.getOutputStream()) {
                        out.write(raw);
                    }
                }
                int code = conn.getResponseCode();
                InputStream in = code >= 200 && code < 300 ? conn.getInputStream() : conn.getErrorStream();
                String text = readAll(in);
                JSONObject obj;
                try {
                    obj = text.isEmpty() ? new JSONObject() : new JSONObject(text);
                } catch (Exception e) {
                    finish(cb, null, "电脑返回了无效数据（HTTP " + code + "）");
                    return;
                }
                if (code < 200 || code >= 300) {
                    finish(cb, null, obj.optString("error", "HTTP " + code));
                    return;
                }
                finish(cb, obj, null);
            } catch (Exception e) {
                String msg = e.getMessage();
                finish(cb, null, msg == null || msg.isEmpty() ? "连接电脑失败" : msg);
            } finally {
                if (conn != null) conn.disconnect();
            }
        });
    }

    private static void finish(JsonCallback cb, JSONObject body, String error) {
        MAIN.post(() -> {
            if (cb != null) cb.onResult(body, error);
        });
    }

    private static String readAll(InputStream in) throws Exception {
        if (in == null) return "";
        StringBuilder sb = new StringBuilder();
        try (BufferedReader r = new BufferedReader(new InputStreamReader(in, StandardCharsets.UTF_8))) {
            char[] buf = new char[2048];
            int n;
            while ((n = r.read(buf)) > 0) sb.append(buf, 0, n);
        }
        return sb.toString();
    }
}
