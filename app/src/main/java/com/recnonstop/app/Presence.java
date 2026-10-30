package com.recnonstop.app;

import android.content.Context;

import org.json.JSONObject;

import java.net.DatagramPacket;
import java.net.DatagramSocket;
import java.net.InetSocketAddress;
import java.net.SocketTimeoutException;
import java.nio.charset.StandardCharsets;

/**
 * 电脑扫描时回应自己的身份，让电脑能列出“要同步到哪台手机”。
 * 录音服务在运行时就回应，方便电脑主动发起连接；数据仍要用户确认。
 */
public final class Presence {

    private static final String TAG = "RecNonstopPresence";
    private static final String MAGIC_PREFIX = "RECNONSTOP_DISCOVER_V";

    private static volatile boolean running = false;
    private static volatile DatagramSocket socket;
    private static Thread thread;

    private Presence() {
    }

    public static synchronized void start(Context ctx) {
        if (running) return;
        final Context app = ctx.getApplicationContext();
        running = true;

        thread = new Thread(() -> {
            try {
                DatagramSocket s = new DatagramSocket(null);
                s.setReuseAddress(true);
                s.bind(new InetSocketAddress(Discovery.DISCOVERY_PORT));
                s.setSoTimeout(2000);
                socket = s;

                byte[] buf = new byte[512];
                while (running) {
                    DatagramPacket p = new DatagramPacket(buf, buf.length);
                    try {
                        s.receive(p);
                    } catch (SocketTimeoutException e) {
                        continue;
                    } catch (Exception e) {
                        break;
                    }
                    String probe = new String(p.getData(), 0, p.getLength(), StandardCharsets.UTF_8);
                    if (probe.startsWith("{")) {
                        handleInvite(app, probe, p.getAddress().getHostAddress());
                        continue;
                    }
                    if (!probe.startsWith(MAGIC_PREFIX)) continue;

                    try {
                        String id = Settings.installId(app);
                        JSONObject o = new JSONObject();
                        o.put("service", "recnonstop");
                        o.put("v", 2);
                        o.put("kind", "phone");
                        o.put("id", id);
                        o.put("name", Settings.deviceName(app));
                        o.put("fp", Settings.fingerprint(id));
                        o.put("port", 0);
                        byte[] out = o.toString().getBytes(StandardCharsets.UTF_8);
                        s.send(new DatagramPacket(out, out.length, p.getAddress(), p.getPort()));
                    } catch (Exception ignored) {
                    }
                }
            } catch (Exception e) {
                Diag.error(app, TAG, "presence failed", e);
            } finally {
                running = false;
                DatagramSocket s = socket;
                socket = null;
                if (s != null) {
                    try {
                        s.close();
                    } catch (Exception ignored) {
                    }
                }
            }
        }, "recnonstop-presence");
        thread.setDaemon(true);
        thread.start();
    }

    public static synchronized void stop() {
        running = false;
        DatagramSocket s = socket;
        if (s != null) {
            try {
                s.close();
            } catch (Exception ignored) {
            }
        }
        socket = null;
    }

    /** 电脑主动点「连接」后会发来一条邀请，存下来等用户回 App 确认。 */
    private static void handleInvite(Context app, String raw, String host) {
        try {
            JSONObject o = new JSONObject(raw);
            if (!"recnonstop".equals(o.optString("service"))) return;
            if (!"join".equals(o.optString("cmd"))) return;
            String token = o.optString("token", "");
            JSONObject pc = o.optJSONObject("pc");
            if (token.isEmpty() || pc == null || pc.optString("id", "").isEmpty()) return;

            JSONObject inv = new JSONObject();
            inv.put("host", host);
            inv.put("token", token);
            inv.put("pc", pc);
            String s = inv.toString();
            if (s.equals(Settings.getString(app, Settings.KEY_PENDING_INVITE, ""))) {
                return; // 同一邀请重复到达
            }
            Settings.setString(app, Settings.KEY_PENDING_INVITE, s);
            Notifications.invite(app, pc.optString("name", host));
        } catch (Exception ignored) {
        }
    }
}
