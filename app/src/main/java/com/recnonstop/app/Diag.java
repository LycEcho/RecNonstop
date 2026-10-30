package com.recnonstop.app;

import android.content.Context;
import android.util.Log;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.FileWriter;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.text.SimpleDateFormat;
import java.util.Date;
import java.util.Locale;

/**
 * 手机本地日志。用户出问题时不用连 adb，直接导出 logs/rec.log 即可。
 */
public final class Diag {

    private static final Object LOCK = new Object();
    private static final long MAX_BYTES = 512 * 1024L;
    private static final String NAME = "rec.log";

    private Diag() {
    }

    public static void log(Context c, String tag, String msg) {
        if (c == null) {
            return;
        }
        Log.i(tag, msg);
        synchronized (LOCK) {
            try {
                File dir = dir(c);
                File f = new File(dir, NAME);
                if (f.exists() && f.length() > MAX_BYTES) {
                    File old = new File(dir, NAME + ".1");
                    //noinspection ResultOfMethodCallIgnored
                    old.delete();
                    //noinspection ResultOfMethodCallIgnored
                    f.renameTo(old);
                }
                String line = new SimpleDateFormat("MM-dd HH:mm:ss.SSS", Locale.US)
                        .format(new Date()) + " [" + tag + "] " + msg + "\n";
                try (FileWriter w = new FileWriter(f, true)) {
                    w.write(line);
                }
            } catch (Throwable ignored) {
            }
        }
    }

    public static void error(Context c, String tag, String msg, Throwable t) {
        String extra = t == null ? "" : (" | " + t.getClass().getSimpleName() + ": " + t.getMessage());
        log(c, tag, "E " + msg + extra);
    }

    public static File dir(Context c) {
        File d = new File(c.getFilesDir(), "logs");
        if (!d.exists()) {
            //noinspection ResultOfMethodCallIgnored
            d.mkdirs();
        }
        return d;
    }

    public static File file(Context c) {
        return new File(dir(c), NAME);
    }

    public static String tail(Context c, int maxBytes) {
        File f = file(c);
        if (!f.exists()) {
            return "(no log yet)";
        }
        try (InputStream in = new FileInputStream(f)) {
            long skip = Math.max(0, f.length() - maxBytes);
            long skipped = 0;
            while (skipped < skip) {
                long n = in.skip(skip - skipped);
                if (n <= 0) break;
                skipped += n;
            }
            ByteArrayOutputStream out = new ByteArrayOutputStream();
            byte[] buf = new byte[8192];
            int n;
            while ((n = in.read(buf)) > 0) {
                out.write(buf, 0, n);
            }
            return out.toString("UTF-8");
        } catch (Exception e) {
            return "(read log failed: " + e + ")";
        }
    }

    /** 把日志复制一份到可分享目录，避免直接暴露 filesDir 路径。 */
    public static File export(Context c) {
        File src = file(c);
        File dst = new File(dir(c), "rec-diagnostics.log");
        try (InputStream in = new FileInputStream(src);
             FileOutputStream out = new FileOutputStream(dst)) {
            byte[] buf = new byte[8192];
            int n;
            while ((n = in.read(buf)) > 0) {
                out.write(buf, 0, n);
            }
        } catch (Exception e) {
            try (FileOutputStream out = new FileOutputStream(dst)) {
                out.write(("log unavailable: " + e + "\n").getBytes(StandardCharsets.UTF_8));
            } catch (Exception ignored) {
            }
        }
        return dst;
    }
}
