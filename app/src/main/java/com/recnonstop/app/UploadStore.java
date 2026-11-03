package com.recnonstop.app;

import android.content.ContentValues;
import android.content.Context;
import android.database.Cursor;
import android.database.sqlite.SQLiteDatabase;
import android.database.sqlite.SQLiteOpenHelper;

import java.util.ArrayList;
import java.util.HashSet;
import java.util.List;
import java.util.Set;

/**
 * 上传状态与结果索引。
 *
 * 之前的实现把已上传文件名塞进 SharedPreferences 的 StringSet：
 * 3 分钟一段 ≈ 480 段/天，一个月 1.4 万条，每次上传都要重写整份 XML，
 * 一年后单次写入几 MB，既卡顿又伤闪存。这里改成 SQLite，顺带把
 * 重试次数、退避时间、job_id、摘要都存下来。
 */
public final class UploadStore extends SQLiteOpenHelper {

    private static final String DB_NAME = "upload.db";
    private static final int DB_VERSION = 2;

    private static final Object LOCK = new Object();
    private static UploadStore instance;

    public static UploadStore get(Context c) {
        synchronized (LOCK) {
            if (instance == null) {
                instance = new UploadStore(c.getApplicationContext());
            }
            return instance;
        }
    }

    private UploadStore(Context c) {
        super(c, DB_NAME, null, DB_VERSION);
    }

    @Override
    public void onCreate(SQLiteDatabase db) {
        db.execSQL("CREATE TABLE files ("
                + "name TEXT PRIMARY KEY,"
                + "state TEXT NOT NULL,"                 // pending | uploaded | failed | quarantine
                + "attempts INTEGER NOT NULL DEFAULT 0,"
                + "next_retry_at INTEGER NOT NULL DEFAULT 0,"
                + "job_id TEXT,"
                + "summary TEXT,"
                + "error TEXT,"
                + "updated_at INTEGER NOT NULL)");
        db.execSQL("CREATE INDEX idx_files_state ON files(state, next_retry_at)");
        db.execSQL("CREATE TABLE meta (k TEXT PRIMARY KEY, v TEXT)");
    }

    @Override
    public void onUpgrade(SQLiteDatabase db, int oldVersion, int newVersion) {
        if (oldVersion < 2) {
            // v1 会把长期离线也当成坏文件隔离；升级后再尝试一次，
            // 明确无效的文件会由服务端重新拒收并隔离。
            db.execSQL("UPDATE files SET state='failed', next_retry_at=0 WHERE state='quarantine'");
        }
    }

    /** 把旧版 SharedPreferences 里的已上传集合搬进 SQLite，只跑一次。 */
    public void migrateFromPrefs(Context c) {
        Set<String> old = Settings.getUploaded(c);
        if (old.isEmpty()) {
            return;
        }
        SQLiteDatabase db = getWritableDatabase();
        long now = System.currentTimeMillis();
        db.beginTransaction();
        try {
            for (String name : old) {
                ContentValues v = new ContentValues();
                v.put("name", name);
                v.put("state", "uploaded");
                v.put("updated_at", now);
                db.insertWithOnConflict("files", null, v, SQLiteDatabase.CONFLICT_IGNORE);
            }
            db.setTransactionSuccessful();
        } finally {
            db.endTransaction();
        }
        Settings.clearUploaded(c);
    }

    public boolean isUploaded(String name) {
        try (Cursor c = getReadableDatabase().rawQuery(
                "SELECT 1 FROM files WHERE name=? AND state='uploaded' LIMIT 1",
                new String[]{name})) {
            return c.moveToFirst();
        }
    }

    public String state(String name) {
        try (Cursor c = getReadableDatabase().rawQuery(
                "SELECT state FROM files WHERE name=? LIMIT 1", new String[]{name})) {
            return c.moveToFirst() ? c.getString(0) : "";
        }
    }

    public int attempts(String name) {
        try (Cursor c = getReadableDatabase().rawQuery(
                "SELECT attempts FROM files WHERE name=? LIMIT 1", new String[]{name})) {
            return c.moveToFirst() ? c.getInt(0) : 0;
        }
    }

    public void markUploaded(String name, String jobId) {
        ContentValues v = new ContentValues();
        v.put("state", "uploaded");
        v.put("attempts", 0);
        v.put("next_retry_at", 0L);
        v.put("error", (String) null);
        v.put("updated_at", System.currentTimeMillis());
        if (jobId != null && !jobId.isEmpty()) {
            v.put("job_id", jobId);
        }
        upsert(name, v);
    }

    /**
     * 失败退避：30s, 60s, 120s ... 最多 30 分钟。
     * 网络故障持续退避重试；只有服务端明确拒收的坏文件进入 quarantine。
     */
    public void markFailed(String name, String error) {
        markFailed(name, error, false);
    }

    public void markFailed(String name, String error, boolean permanent) {
        int n = Math.min(attempts(name), 30) + 1;
        long delay = Math.min(30L * 60_000L, 30_000L * (1L << Math.min(n - 1, 6)));
        ContentValues v = new ContentValues();
        v.put("state", permanent ? "quarantine" : "failed");
        v.put("attempts", n);
        v.put("next_retry_at", System.currentTimeMillis() + delay);
        v.put("error", error == null ? "" : error);
        v.put("updated_at", System.currentTimeMillis());
        upsert(name, v);
    }

    /** 新文件入队时写一行，方便 UI 统计和清理时保护。 */
    public void markPending(String name) {
        ContentValues v = new ContentValues();
        v.put("state", "pending");
        v.put("next_retry_at", 0L);
        v.put("updated_at", System.currentTimeMillis());
        int n = getWritableDatabase().update("files", v,
                "name=? AND state!='uploaded'", new String[]{name});
        if (n == 0 && !isUploaded(name)) {
            upsert(name, v);
        }
    }

    public void setJobId(String name, String jobId) {
        ContentValues v = new ContentValues();
        v.put("job_id", jobId);
        v.put("updated_at", System.currentTimeMillis());
        upsert(name, v);
    }

    public void setSummary(String name, String summary) {
        ContentValues v = new ContentValues();
        v.put("summary", summary == null ? "" : summary);
        v.put("updated_at", System.currentTimeMillis());
        upsert(name, v);
    }

    public String summary(String name) {
        try (Cursor c = getReadableDatabase().rawQuery(
                "SELECT summary FROM files WHERE name=? LIMIT 1", new String[]{name})) {
            return c.moveToFirst() ? c.getString(0) : "";
        }
    }

    public String jobId(String name) {
        try (Cursor c = getReadableDatabase().rawQuery(
                "SELECT job_id FROM files WHERE name=? LIMIT 1", new String[]{name})) {
            return c.moveToFirst() ? c.getString(0) : "";
        }
    }

    /** 返回 name 列表：未上传、到了重试时间、按时间正序。 */
    public List<String> pending(int limit) {
        List<String> out = new ArrayList<>();
        long now = System.currentTimeMillis();
        try (Cursor c = getReadableDatabase().rawQuery(
                "SELECT name FROM files WHERE state IN ('pending','failed') "
                        + "AND next_retry_at<=? ORDER BY updated_at ASC LIMIT ?",
                new String[]{String.valueOf(now), String.valueOf(limit)})) {
            while (c.moveToNext()) {
                out.add(c.getString(0));
            }
        }
        return out;
    }

    /** 清理时保护：所有还没确认上传的文件名。 */
    public Set<String> pendingNames() {
        Set<String> out = new HashSet<>();
        try (Cursor c = getReadableDatabase().rawQuery(
                "SELECT name FROM files WHERE state IN ('pending','failed','quarantine')", null)) {
            while (c.moveToNext()) {
                out.add(c.getString(0));
            }
        }
        return out;
    }

    /** 一次查出所有已上传文件名，避免扫描目录时逐文件查询。 */
    public Set<String> uploadedNames() {
        Set<String> out = new HashSet<>();
        try (Cursor c = getReadableDatabase().rawQuery(
                "SELECT name FROM files WHERE state='uploaded'", null)) {
            while (c.moveToNext()) {
                out.add(c.getString(0));
            }
        }
        return out;
    }

    /** 已上传但还没有摘要的 job，用于定时补轮询。 */
    public List<JobRef> outstanding(int limit) {
        List<JobRef> out = new ArrayList<>();
        try (Cursor c = getReadableDatabase().rawQuery(
                "SELECT name, job_id FROM files WHERE state='uploaded' "
                        + "AND job_id IS NOT NULL AND job_id!='' "
                        + "AND (summary IS NULL OR summary='') "
                        + "ORDER BY updated_at DESC LIMIT ?",
                new String[]{String.valueOf(limit)})) {
            while (c.moveToNext()) {
                JobRef r = new JobRef();
                r.name = c.getString(0);
                r.jobId = c.getString(1);
                out.add(r);
            }
        }
        return out;
    }

    /** {uploaded, pending, failed, quarantine} */
    public int[] counts() {
        int[] out = new int[4];
        try (Cursor c = getReadableDatabase().rawQuery(
                "SELECT state, COUNT(*) FROM files GROUP BY state", null)) {
            while (c.moveToNext()) {
                switch (c.getString(0)) {
                    case "uploaded": out[0] += c.getInt(1); break;
                    case "pending": out[1] += c.getInt(1); break;
                    case "failed": out[2] += c.getInt(1); break;
                    case "quarantine": out[3] += c.getInt(1); break;
                    default: break;
                }
            }
        }
        return out;
    }

    /** 已上传的旧行按保留期清掉，避免库无限长。 */
    public void prune(long cutoffMs) {
        try {
            getWritableDatabase().delete("files",
                    "state IN ('uploaded','quarantine') AND updated_at<?",
                    new String[]{String.valueOf(cutoffMs)});
        } catch (Throwable ignored) {
        }
    }

    public List<SummaryRow> recentSummaries(int limit) {
        List<SummaryRow> out = new ArrayList<>();
        try (Cursor c = getReadableDatabase().rawQuery(
                "SELECT name, state, job_id, summary, updated_at FROM files "
                        + "WHERE summary IS NOT NULL AND summary!='' "
                        + "ORDER BY updated_at DESC LIMIT ?",
                new String[]{String.valueOf(limit)})) {
            while (c.moveToNext()) {
                SummaryRow r = new SummaryRow();
                r.name = c.getString(0);
                r.state = c.getString(1);
                r.jobId = c.getString(2);
                r.summary = c.getString(3);
                r.updatedAt = c.getLong(4);
                out.add(r);
            }
        }
        return out;
    }

    private void upsert(String name, ContentValues v) {
        SQLiteDatabase db = getWritableDatabase();
        int n = db.update("files", v, "name=?", new String[]{name});
        if (n == 0) {
            v.put("name", name);
            if (!v.containsKey("state")) {
                v.put("state", "pending");
            }
            if (!v.containsKey("updated_at")) {
                v.put("updated_at", System.currentTimeMillis());
            }
            db.insert("files", null, v);
        }
    }

    public static final class SummaryRow {
        public String name;
        public String state;
        public String jobId;
        public String summary;
        public long updatedAt;
    }

    public static final class JobRef {
        public String name;
        public String jobId;
    }
}
