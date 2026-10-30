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

/** 用 6 位配对码换设备 token。 */
public final class PairingClient {

    private static final ExecutorService EXEC = Executors.newSingleThreadExecutor();

    public interface Callback {
        void onResult(boolean ok, String message);
    }

    private PairingClient() {
    }

    public static void pair(Context ctx, String host, int port, String code, Callback cb) {
        Context app = ctx.getApplicationContext();
        EXEC.execute(() -> {
            try {
                JSONObject req = new JSONObject();
                req.put("device_name", Settings.deviceName(app));
                req.put("code", code);

                String body = post("http://" + host + ":" + port + "/api/v1/pair", req.toString());
                JSONObject o = new JSONObject(body);
                String token = o.optString("token", "");
                if (token.isEmpty()) throw new IOException("电脑没有返回 token");

                Settings.setString(app, Settings.KEY_TOKEN, token);
                Settings.setString(app, Settings.KEY_PC_URL, "http://" + host + ":" + port);
                Settings.setString(app, Settings.KEY_PC_NAME, o.optString("name", host));
                Settings.setString(app, Settings.KEY_DEVICE_ID, o.optString("device_id", ""));
                cb.onResult(true, "配对成功");
            } catch (Exception e) {
                cb.onResult(false, e.getMessage());
            }
        });
    }

    public static void unpair(Context ctx) {
        Settings.setString(ctx, Settings.KEY_TOKEN, "");
        Settings.setString(ctx, Settings.KEY_PC_NAME, "");
        Settings.setString(ctx, Settings.KEY_DEVICE_ID, "");
        Settings.setString(ctx, Settings.KEY_PC_ID, "");
        Settings.setString(ctx, Settings.KEY_PC_FP, "");
    }

    private static String post(String url, String json) throws IOException {
        HttpURLConnection c = (HttpURLConnection) new URL(url).openConnection();
        c.setConnectTimeout(10000);
        c.setReadTimeout(20000);
        c.setRequestMethod("POST");
        c.setDoOutput(true);
        c.setRequestProperty("Content-Type", "application/json");
        try (OutputStream os = c.getOutputStream()) {
            os.write(json.getBytes(StandardCharsets.UTF_8));
        }
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
