package com.recnonstop.app;

import android.content.Context;

import java.util.HashSet;
import java.util.Set;

/** 设置项统一读写 */
public class Settings {

    public static final String PREFS = "settings";

    public static final String KEY_AUTO_START  = "auto_start";   // 打开软件自动录音
    public static final String KEY_BRIGHT_CUSTOM = "bright_custom"; // 自定义窗口亮度
    public static final String KEY_BRIGHT_PCT  = "bright_pct";   // 窗口亮度百分比 1-100
    public static final String KEY_WATCHDOG    = "watchdog";     // 后台被杀自动拉起
    public static final String KEY_SEGMENT_MIN = "segment_min";  // 分段时长（分钟）
    public static final String KEY_QUALITY     = "quality";      // 音质档位 0-3，0 最高
    public static final String KEY_KEEP_DAYS   = "keep_days";    // 保留天数，0 = 不清理

    public static final String KEY_PC_URL       = "pc_url";       // 电脑服务地址
    public static final String KEY_PC_NAME      = "pc_name";      // 已连接电脑名称
    public static final String KEY_DEVICE_ID    = "device_id";    // 电脑端分配的 device_id
    public static final String KEY_TOKEN        = "device_token"; // 设备令牌
    public static final String KEY_UPLOAD       = "upload_enabled"; // 自动上传到电脑
    public static final String KEY_WIFI_ONLY    = "upload_wifi_only";
    public static final String KEY_UPLOADED     = "uploaded_files";

    public static final String KEY_INSTALL_ID   = "install_id";   // 本机随机 ID（不是硬件标识）
    public static final String KEY_DEVICE_NAME  = "device_name";  // 局域网里显示的名字
    public static final String KEY_PC_ID        = "pc_id";        // 已连接电脑的稳定 ID
    public static final String KEY_PC_FP        = "pc_fp";        // 已连接电脑的短指纹
    public static final String KEY_PENDING_INVITE = "pending_invite"; // 电脑主动发来的连接邀请

    public static final String KEY_REPORT_ENABLED = "daily_report_enabled"; // 每日日报开关
    public static final String KEY_REPORT_TIME    = "daily_report_time";    // HH:MM

    public static final String DEFAULT_PC_URL = "http://192.168.1.100:18082";

    public static boolean getBool(Context c, String k, boolean def) {
        return c.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getBoolean(k, def);
    }

    public static void setBool(Context c, String k, boolean v) {
        c.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
                .edit().putBoolean(k, v).apply();
    }

    public static int getInt(Context c, String k, int def) {
        return c.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getInt(k, def);
    }

    public static void setInt(Context c, String k, int v) {
        c.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
                .edit().putInt(k, v).apply();
    }

    public static String getString(Context c, String k, String def) {
        return c.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getString(k, def);
    }

    public static void setString(Context c, String k, String v) {
        c.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
                .edit().putString(k, v).apply();
    }

    public static String getPcUrl(Context c) {
        String s = getString(c, KEY_PC_URL, DEFAULT_PC_URL);
        if (s == null) s = DEFAULT_PC_URL;
        s = s.trim();
        if (s.isEmpty()) s = DEFAULT_PC_URL;
        if (!s.startsWith("http://") && !s.startsWith("https://")) {
            s = "http://" + s;
        }
        while (s.endsWith("/")) s = s.substring(0, s.length() - 1);
        return s;
    }

    public static Set<String> getUploaded(Context c) {
        Set<String> s = c.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
                .getStringSet(KEY_UPLOADED, null);
        return s == null ? new HashSet<>() : new HashSet<>(s);
    }

    public static void markUploaded(Context c, String name) {
        Set<String> s = getUploaded(c);
        s.add(name);
        c.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
                .edit().putStringSet(KEY_UPLOADED, s).apply();
    }

    /** 迁移到 UploadStore 后清空旧集合，避免继续膨胀。 */
    public static void clearUploaded(Context c) {
        c.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
                .edit().remove(KEY_UPLOADED).commit();
    }

    /** 0=最高 1=高 2=标准 3=省空间；默认省空间（16k/32k），语音 ASR 足够，且 7 天只要 ~2.4GB */
    public static int quality(Context c) {        int q = getInt(c, KEY_QUALITY, 3);
        return (q < 0 || q > 3) ? 3 : q;
    }

    public static int sampleRate(int q) {
        switch (q) {
            case 1: return 44100;
            case 2: return 32000;
            case 3: return 16000;
            default: return 48000;
        }
    }

    public static int bitRate(int q) {
        switch (q) {
            case 1: return 96000;
            case 2: return 64000;
            case 3: return 32000;
            default: return 128000;
        }
    }

    public static String qualityLabel(int q) {
        switch (q) {
            case 1: return "高（44.1kHz / 96kbps）";
            case 2: return "标准（32kHz / 64kbps）";
            case 3: return "省空间（16kHz / 32kbps）";
            default: return "最高（48kHz / 128kbps）";
        }
    }

    public static long segmentMs(Context c) {
        int m = getInt(c, KEY_SEGMENT_MIN, 3);
        if (m < 1) m = 1;
        return m * 60_000L;
    }

    // ---------------- 设备身份 ----------------

    /** 本机在局域网里的稳定 ID。首次随机生成，不使用硬件标识。 */
    public static String installId(Context c) {
        String s = getString(c, KEY_INSTALL_ID, "");
        if (s != null && s.matches("[0-9a-f]{16}")) {
            return s;
        }
        byte[] b = new byte[8];
        new java.security.SecureRandom().nextBytes(b);
        StringBuilder sb = new StringBuilder(16);
        for (byte x : b) {
            sb.append(String.format(java.util.Locale.ROOT, "%02x", x));
        }
        s = sb.toString();
        setString(c, KEY_INSTALL_ID, s);
        return s;
    }

    /** 4 位短指纹，只用于界面上区分设备。 */
    public static String fingerprint(String id) {
        try {
            java.security.MessageDigest md = java.security.MessageDigest.getInstance("SHA-256");
            byte[] d = md.digest(id.getBytes(java.nio.charset.StandardCharsets.UTF_8));
            return String.format(java.util.Locale.ROOT, "%02X%02X", d[0], d[1]);
        } catch (Exception e) {
            return "";
        }
    }

    public static String deviceName(Context c) {
        String s = getString(c, KEY_DEVICE_NAME, "");
        if (s == null || s.trim().isEmpty()) {
            return "手机 " + fingerprint(installId(c));
        }
        return s.trim();
    }

    public static void setDeviceName(Context c, String name) {
        String v = name == null ? "" : name.trim();
        if (v.length() > 32) v = v.substring(0, 32);
        setString(c, KEY_DEVICE_NAME, v);
    }

    public static boolean connected(Context c) {
        String token = getString(c, KEY_TOKEN, "");
        return token != null && !token.isEmpty();
    }
}
