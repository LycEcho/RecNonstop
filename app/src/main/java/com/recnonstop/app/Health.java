package com.recnonstop.app;

import android.content.Context;

import org.json.JSONObject;

import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;

/**
 * 持久化健康心跳。
 *
 * 以前的 RecState 全是进程内 volatile：服务一死，用户打开 App 只能看到“已停止”，
 * 不知道是刚停还是停了三天。这里把最近一次成功切段、上传、错误、空档写进
 * filesDir/health.json，供 UI 和看门狗判断“是不是静默停录了”。
 */
public final class Health {

    private static final Object LOCK = new Object();
    private static final String FILE = "health.json";

    private Health() {
    }

    public static JSONObject read(Context c) {
        synchronized (LOCK) {
            return load(c);
        }
    }

    public static void serviceStart(Context c) {
        synchronized (LOCK) {
            JSONObject o = load(c);
            long now = System.currentTimeMillis();
            try {
                o.put("service_started_at", now);
                o.put("service_starts", o.optInt("service_starts", 0) + 1);
                o.put("boot_blocked_at", 0L);
                o.put("enabled_at", now);
            } catch (Exception ignored) {
            }
            save(c, o);
        }
    }

    public static void serviceStopped(Context c, boolean userStopped) {
        synchronized (LOCK) {
            JSONObject o = load(c);
            try {
                o.put("service_stopped_at", System.currentTimeMillis());
                o.put("stopped_by_user", userStopped);
            } catch (Exception ignored) {
            }
            save(c, o);
        }
    }

    public static void segmentClosed(Context c, File f) {
        if (f == null || !f.exists()) {
            return;
        }
        synchronized (LOCK) {
            JSONObject o = load(c);
            long now = System.currentTimeMillis();
            String day = day(now);
            try {
                if (!day.equals(o.optString("day"))) {
                    o.put("day", day);
                    o.put("segments_today", 0);
                    o.put("bytes_today", 0L);
                    o.put("gaps_today", 0);
                }
                o.put("last_segment_at", now);
                o.put("last_segment_name", f.getName());
                o.put("last_segment_bytes", f.length());
                o.put("segments_today", o.optInt("segments_today", 0) + 1);
                o.put("bytes_today", o.optLong("bytes_today", 0L) + f.length());
            } catch (Exception ignored) {
            }
            save(c, o);
        }
    }

    public static void noteGap(Context c, String type) {
        synchronized (LOCK) {
            JSONObject o = load(c);
            long now = System.currentTimeMillis();
            String day = day(now);
            try {
                if (!day.equals(o.optString("day"))) {
                    o.put("day", day);
                    o.put("segments_today", 0);
                    o.put("bytes_today", 0L);
                    o.put("gaps_today", 0);
                }
                o.put("last_gap_at", now);
                o.put("last_gap_type", type == null ? "" : type);
                if (type == null || !type.endsWith("_end")) {
                    o.put("gaps_today", o.optInt("gaps_today", 0) + 1);
                }
            } catch (Exception ignored) {
            }
            save(c, o);
        }
    }

    public static void uploadOk(Context c, String name) {
        synchronized (LOCK) {
            JSONObject o = load(c);
            long now = System.currentTimeMillis();
            String day = day(now);
            try {
                if (!day.equals(o.optString("upload_day"))) {
                    o.put("upload_day", day);
                    o.put("uploads_today", 0);
                }
                o.put("last_upload_at", now);
                o.put("last_upload_name", name == null ? "" : name);
                o.put("uploads_today", o.optInt("uploads_today", 0) + 1);
            } catch (Exception ignored) {
            }
            save(c, o);
        }
    }

    public static void error(Context c, String msg) {
        synchronized (LOCK) {
            JSONObject o = load(c);
            try {
                o.put("last_error", msg == null ? "" : msg);
                o.put("last_error_at", System.currentTimeMillis());
            } catch (Exception ignored) {
            }
            save(c, o);
        }
    }

    public static void bootBlocked(Context c) {
        synchronized (LOCK) {
            JSONObject o = load(c);
            try {
                o.put("boot_blocked_at", System.currentTimeMillis());
            } catch (Exception ignored) {
            }
            save(c, o);
        }
    }

    public static void watchdogFailure(Context c, String msg) {
        synchronized (LOCK) {
            JSONObject o = load(c);
            try {
                o.put("watchdog_failures", o.optInt("watchdog_failures", 0) + 1);
                o.put("watchdog_last_error", msg == null ? "" : msg);
                o.put("watchdog_last_error_at", System.currentTimeMillis());
            } catch (Exception ignored) {
            }
            save(c, o);
        }
    }

    public static boolean isStale(Context c, long thresholdMs) {
        JSONObject o = read(c);
        // 新会话尚未关闭第一段时，从本次启动计时，不能立刻判定心跳过期。
        long last = Math.max(o.optLong("last_segment_at", 0L),
                o.optLong("service_started_at", 0L));
        return last > 0L && System.currentTimeMillis() - last > thresholdMs;
    }

    private static JSONObject load(Context c) {
        File f = new File(c.getFilesDir(), FILE);
        if (!f.exists()) {
            return new JSONObject();
        }
        try (InputStream in = new FileInputStream(f)) {
            byte[] buf = new byte[(int) Math.min(f.length(), 64 * 1024)];
            int n = in.read(buf);
            if (n <= 0) {
                return new JSONObject();
            }
            return new JSONObject(new String(buf, 0, n, StandardCharsets.UTF_8));
        } catch (Exception e) {
            return new JSONObject();
        }
    }

    private static void save(Context c, JSONObject o) {
        File f = new File(c.getFilesDir(), FILE);
        File tmp = new File(c.getFilesDir(), FILE + ".tmp");
        try (FileOutputStream out = new FileOutputStream(tmp)) {
            out.write(o.toString().getBytes(StandardCharsets.UTF_8));
            out.getFD().sync();
        } catch (Exception ignored) {
            return;
        }
        if (!tmp.renameTo(f)) {
            // rename 失败时退化为覆盖写，至少别把旧文件弄丢。
            try (FileOutputStream out = new FileOutputStream(f)) {
                out.write(o.toString().getBytes(StandardCharsets.UTF_8));
            } catch (Exception ignored) {
            }
            //noinspection ResultOfMethodCallIgnored
            tmp.delete();
        }
    }

    private static String day(long ms) {
        return new SimpleDateFormat("yyyyMMdd", Locale.US).format(new Date(ms));
    }
}
