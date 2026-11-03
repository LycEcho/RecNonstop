package com.recnonstop.app;

import android.content.Context;
import android.net.Uri;
import android.os.Handler;
import android.os.Looper;
import android.util.Log;

import org.json.JSONArray;
import org.json.JSONObject;

import java.io.ByteArrayOutputStream;
import java.io.File;
import java.io.FileInputStream;
import java.io.FileOutputStream;
import java.io.IOException;
import java.io.InputStream;
import java.nio.charset.StandardCharsets;
import java.util.ArrayList;
import java.util.Collections;
import java.util.Comparator;
import java.util.HashSet;
import java.util.List;
import java.util.Set;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/**
 * 手机端日报缓存。
 *
 * PC 生成日报后不会主动连手机（手机不开端口），而是在 /api/v1/commands
 * 里带一条待推送记录；Uploader 每 30 秒拉一次，这里负责下载、落盘、发通知、
 * 回 ack。断网/关机不会丢，下一次上线会补推最新一份。
 */
public final class DailyReports {

    private static final String TAG = "RecNonstopReport";
    private static final String DIR = "daily_reports";
    private static final String KEY_READ = "daily_report_read";
    private static final String KEY_NOTIFIED = "daily_report_notified";

    private static final ExecutorService EXEC = Executors.newSingleThreadExecutor();
    private static final Handler MAIN = new Handler(Looper.getMainLooper());

    private DailyReports() {
    }

    public interface ReportCallback {
        void onResult(boolean ok, String message);
    }

    public interface ScheduleCallback {
        void onSchedule(boolean ok, boolean enabled, String time, String message);
    }

    // ---------------- 本地存储 ----------------

    /** 仅用于本机调试包：assets 里如果有 embedded_report.json，就写入本地报告缓存。
     *  这个文件由仓库外的私有构建目录提供，不会进入公开仓库。 */
    static void seedBundled(Context c) {
        try (InputStream in = c.getAssets().open("embedded_report.json")) {
            JSONObject r = new JSONObject(readAll(in));
            String id = r.optString("id", "");
            if (validId(id) && get(c, id) == null) save(c, r);
        } catch (Exception ignored) {
            // 没有嵌入报告时保持正常空状态。
        }
    }

    static File dir(Context c) {
        File d = new File(c.getFilesDir(), DIR);
        if (!d.exists()) d.mkdirs();
        return d;
    }

    static synchronized boolean save(Context c, JSONObject r) {
        if (r == null) return false;
        String id = r.optString("id", "");
        if (!validId(id)) return false;
        File target = new File(dir(c), id + ".json");
        File tmp = new File(dir(c), id + ".json.tmp");
        try (FileOutputStream out = new FileOutputStream(tmp)) {
            out.write(r.toString(2).getBytes(StandardCharsets.UTF_8));
            out.getFD().sync();
        } catch (Exception e) {
            Diag.error(c, TAG, "save report failed", e);
            tmp.delete();
            return false;
        }
        // Android 的同目录 rename 会原子替换；先删旧文件会在崩溃时丢失缓存。
        if (!tmp.renameTo(target)) {
            tmp.delete();
            return false;
        }
        return true;
    }

    static JSONObject get(Context c, String id) {
        if (!validId(id)) return null;
        File f = new File(dir(c), id + ".json");
        if (!f.exists()) return null;
        try (InputStream in = new FileInputStream(f)) {
            return new JSONObject(readAll(in));
        } catch (Exception e) {
            return null;
        }
    }

    static boolean summaryIncomplete(JSONObject report) {
        return report != null && (!report.optString("error", "").trim().isEmpty()
                || "failed".equals(report.optString("status", "")));
    }

    static List<JSONObject> list(Context c) {
        List<JSONObject> out = new ArrayList<>();
        File[] files = dir(c).listFiles((d, n) -> n.endsWith(".json"));
        if (files == null) return out;
        for (File f : files) {
            try (InputStream in = new FileInputStream(f)) {
                out.add(new JSONObject(readAll(in)));
            } catch (Exception ignored) {
            }
        }
        Collections.sort(out, new Comparator<JSONObject>() {
            @Override
            public int compare(JSONObject a, JSONObject b) {
                return stamp(b).compareTo(stamp(a));
            }
        });
        return out;
    }

    static void markRead(Context c, String id) {
        if (!validId(id)) return;
        Set<String> s = readSet(c);
        if (s.add(id)) {
            c.getSharedPreferences(Settings.PREFS, Context.MODE_PRIVATE)
                    .edit().putStringSet(KEY_READ, s).apply();
        }
        Notifications.cancelReport(c, id);
    }

    static boolean isRead(Context c, String id) {
        return readSet(c).contains(id);
    }

    private static Set<String> readSet(Context c) {
        Set<String> s = c.getSharedPreferences(Settings.PREFS, Context.MODE_PRIVATE)
                .getStringSet(KEY_READ, null);
        return s == null ? new HashSet<>() : new HashSet<>(s);
    }

    private static Set<String> notifiedSet(Context c) {
        Set<String> s = c.getSharedPreferences(Settings.PREFS, Context.MODE_PRIVATE)
                .getStringSet(KEY_NOTIFIED, null);
        return s == null ? new HashSet<>() : new HashSet<>(s);
    }

    private static void markNotified(Context c, String id) {
        Set<String> s = notifiedSet(c);
        if (s.add(id)) {
            c.getSharedPreferences(Settings.PREFS, Context.MODE_PRIVATE)
                    .edit().putStringSet(KEY_NOTIFIED, s).apply();
        }
    }

    private static String stamp(JSONObject r) {
        String s = r.optString("generated_at", "");
        if (s.isEmpty()) s = r.optString("date", "");
        if (s.isEmpty()) s = r.optString("created_at", "");
        return s;
    }

    private static boolean validId(String id) {
        return id != null && id.matches("[A-Za-z0-9_-]{1,96}");
    }

    private static String readAll(InputStream in) throws Exception {
        try (InputStream i = in; ByteArrayOutputStream out = new ByteArrayOutputStream()) {
            byte[] buf = new byte[8192];
            int n;
            while ((n = i.read(buf)) > 0) out.write(buf, 0, n);
            return out.toString("UTF-8");
        }
    }

    // ---------------- 推送 / 拉取 / 历史补齐 ----------------

    /** 由 Uploader 的 30 秒心跳调用，运行在后台线程。 */
    static void handleCommands(Context c, JSONObject commands) {
        JSONArray reports = commands == null ? null : commands.optJSONArray("reports");
        if (reports == null || reports.length() == 0) return;
        try {
            syncFromServer(c, true);
        } catch (Exception e) {
            // 拉取或 ack 失败：保留待推送状态，下一轮心跳重试。
            Log.w(TAG, "report sync failed", e);
        }
    }

    /**
     * 按电脑返回的游标遍历日报历史，再逐份下载本地没有的。
     * 通知只发最新一份，避免离线多日后一次弹很多条。
     * 新协议只确认已落盘的单份报告；旧协议会连带确认更早历史，因此不发送 ack。
     */
    private static int syncFromServer(Context c, boolean notifyNewest) throws Exception {
        String newestId = "";
        int downloaded = 0;
        String cursor = "";
        Set<String> seenCursors = new HashSet<>();
        seenCursors.add(cursor);
        Set<String> syncedIds = new HashSet<>();
        do {
            JSONObject resp;
            try {
                String path = "/api/v1/reports?limit=90";
                if (!cursor.isEmpty()) path += "&cursor=" + Uri.encode(cursor);
                resp = Uploader.apiGet(c, path);
            } catch (Uploader.HttpError e) {
                // 只有首页 404 表示旧版没有历史接口；后续页失败必须报告同步未完成。
                if (e.code != 404 || !cursor.isEmpty()) throw e;
                return syncLatestOnly(c, notifyNewest);
            }
            JSONArray arr = resp.optJSONArray("reports");
            if (arr == null) throw new IOException("日报历史响应缺少列表");
            boolean singleAck = "single".equals(resp.optString("ack_mode", ""));
            for (int i = 0; i < arr.length(); i++) {
                JSONObject meta = arr.optJSONObject(i);
                String id = meta == null ? "" : meta.optString("id", "");
                if (!validId(id)) throw new IOException("日报历史包含无效标识");
                if (newestId.isEmpty()) newestId = id;
                if (!syncedIds.add(id)) continue;
                if (get(c, id) == null) {
                    JSONObject full = Uploader.apiGet(c, "/api/v1/reports/" + id);
                    if (!id.equals(full.optString("id", ""))) {
                        throw new IOException("日报下载标识不匹配");
                    }
                    if (!save(c, full)) throw new IOException("save report failed: " + id);
                    downloaded++;
                }
                // Also ack cached reports: an earlier attempt may have saved the
                // file successfully but lost the acknowledgement to a network error.
                if (singleAck) {
                    Uploader.apiPost(c, "/api/v1/reports/" + id + "/ack", new JSONObject());
                }
            }
            cursor = resp.optString("next_cursor", "");
            if (!cursor.isEmpty() && !seenCursors.add(cursor)) {
                throw new IOException("日报历史游标重复，请稍后重试同步");
            }
        } while (!cursor.isEmpty());

        if (notifyNewest && !newestId.isEmpty()
                && !notifiedSet(c).contains(newestId)) {
            JSONObject full = get(c, newestId);
            if (full != null) notifyReport(c, newestId, full);
        }
        if (downloaded > 0) Diag.log(c, TAG, "reports synced: " + downloaded);

        return downloaded;
    }

    /** 旧版电脑端只有 /reports/latest，能拿一份是一份。 */
    private static int syncLatestOnly(Context c, boolean notifyNewest) throws Exception {
        JSONObject full = Uploader.apiGet(c, "/api/v1/reports/latest");
        String id = full.optString("id", "");
        if (!validId(id)) return 0;
        int downloaded = 0;
        if (get(c, id) == null) {
            if (!save(c, full)) throw new IOException("save report failed: " + id);
            downloaded = 1;
        }
        if (notifyNewest && !notifiedSet(c).contains(id)) notifyReport(c, id, full);
        // This fallback is an old server without a history/capability response.
        // Its acknowledgement may include undownloaded reports, so leave it pending.
        return downloaded;
    }

    private static void notifyReport(Context c, String id, JSONObject full) {
        String title = full.optString("title", "录音日报");
        String text = full.optString("summary", "");
        if (text.isEmpty()) text = "日报已生成，点击查看";
        Notifications.report(c, id, title, text);
        markNotified(c, id);
    }

    /** 同步电脑端已有日报；不会触发重新生成。旧版电脑只能提供其列表范围。 */
    static void syncHistory(Context c, ReportCallback cb) {
        EXEC.execute(() -> {
            String msg = "";
            boolean ok = false;
            try {
                int n = syncFromServer(c, false);
                ok = true;
                if (n > 0) {
                    msg = "已同步 " + n + " 份日报";
                } else {
                    msg = list(c).isEmpty() ? "电脑上还没有可同步的日报" : "同步完成，没有新增日报";
                }
            } catch (Exception e) {
                Diag.error(c, TAG, "report history sync incomplete", e);
                msg = "同步未完成，已保存的日报仍可阅读；请稍后重试";
            }
            final boolean fok = ok;
            final String fmsg = msg;
            MAIN.post(() -> cb.onResult(fok, fmsg));
        });
    }

    // ---------------- 生成时间设置 ----------------

    static void fetchSchedule(Context c, ScheduleCallback cb) {
        EXEC.execute(() -> {
            boolean ok = false;
            boolean enabled = Settings.getBool(c, Settings.KEY_REPORT_ENABLED, true);
            String time = Settings.getString(c, Settings.KEY_REPORT_TIME, "18:00");
            String msg = "";
            try {
                JSONObject o = Uploader.apiGet(c, "/api/v1/report-settings");
                enabled = o.optBoolean("enabled", true);
                time = o.optString("generate_at", "18:00");
                Settings.setBool(c, Settings.KEY_REPORT_ENABLED, enabled);
                Settings.setString(c, Settings.KEY_REPORT_TIME, time);
                ok = true;
            } catch (Exception e) {
                msg = e.getMessage() == null ? "" : e.getMessage();
            }
            final boolean fok = ok;
            final boolean fen = enabled;
            final String ftime = time;
            final String fmsg = msg;
            MAIN.post(() -> cb.onSchedule(fok, fen, ftime, fmsg));
        });
    }

    static void updateSchedule(Context c, boolean enabled, String time, ScheduleCallback cb) {
        EXEC.execute(() -> {
            boolean ok = false;
            String msg = "";
            boolean outEnabled = enabled;
            String outTime = time;
            try {
                JSONObject body = new JSONObject();
                body.put("enabled", enabled);
                body.put("generate_at", time);
                JSONObject o = Uploader.apiPost(c, "/api/v1/report-settings", body);
                outEnabled = o.optBoolean("enabled", enabled);
                outTime = o.optString("generate_at", time);
                Settings.setBool(c, Settings.KEY_REPORT_ENABLED, outEnabled);
                Settings.setString(c, Settings.KEY_REPORT_TIME, outTime);
                ok = true;
            } catch (Exception e) {
                msg = e.getMessage() == null ? "" : e.getMessage();
            }
            final boolean fok = ok;
            final boolean fen = outEnabled;
            final String ftime = outTime;
            final String fmsg = msg;
            MAIN.post(() -> cb.onSchedule(fok, fen, ftime, fmsg));
        });
    }
}
