package com.recnonstop.app;

import android.content.ContentProvider;
import android.content.ContentValues;
import android.database.Cursor;
import android.database.MatrixCursor;
import android.net.Uri;
import android.os.ParcelFileDescriptor;
import android.provider.OpenableColumns;

import java.io.File;
import java.io.FileNotFoundException;

/**
 * 零依赖的文件共享 Provider：只暴露 rec/ 和 logs/ 两个目录，
 * 用来播放/分享录音、导出诊断日志。避免为 FileProvider 引入 AndroidX。
 */
public final class RecFileProvider extends ContentProvider {

    public static final String AUTHORITY = "com.recnonstop.app.files";

    public static Uri uriForRec(File f) {
        return Uri.parse("content://" + AUTHORITY + "/rec/" + Uri.encode(f.getName()));
    }

    public static Uri uriForLog(File f) {
        return Uri.parse("content://" + AUTHORITY + "/logs/" + Uri.encode(f.getName()));
    }

    @Override
    public boolean onCreate() {
        return true;
    }

    @Override
    public ParcelFileDescriptor openFile(Uri uri, String mode) throws FileNotFoundException {
        File f = resolve(uri);
        if (!f.exists() || !f.isFile()) {
            throw new FileNotFoundException(f.getAbsolutePath());
        }
        return ParcelFileDescriptor.open(f, ParcelFileDescriptor.MODE_READ_ONLY);
    }

    @Override
    public String getType(Uri uri) {
        String name = uri.getLastPathSegment();
        if (name == null) {
            return "application/octet-stream";
        }
        if (name.endsWith(".aac")) {
            return "audio/aac";
        }
        if (name.endsWith(".m4a") || name.endsWith(".mp4")) {
            return "audio/mp4";
        }
        if (name.endsWith(".log") || name.endsWith(".txt")) {
            return "text/plain";
        }
        if (name.endsWith(".json")) {
            return "application/json";
        }
        return "application/octet-stream";
    }

    @Override
    public Cursor query(Uri uri, String[] projection, String selection,
                        String[] selectionArgs, String sortOrder) {
        try {
            File f = resolve(uri);
            String[] cols = projection != null ? projection
                    : new String[]{OpenableColumns.DISPLAY_NAME, OpenableColumns.SIZE};
            MatrixCursor c = new MatrixCursor(cols, 1);
            Object[] row = new Object[cols.length];
            for (int i = 0; i < cols.length; i++) {
                if (OpenableColumns.SIZE.equals(cols[i])) {
                    row[i] = f.length();
                } else {
                    row[i] = f.getName();
                }
            }
            c.addRow(row);
            return c;
        } catch (Exception e) {
            return null;
        }
    }

    @Override
    public Uri insert(Uri uri, ContentValues values) {
        return null;
    }

    @Override
    public int delete(Uri uri, String selection, String[] selectionArgs) {
        return 0;
    }

    @Override
    public int update(Uri uri, ContentValues values, String selection, String[] selectionArgs) {
        return 0;
    }

    private File resolve(Uri uri) throws FileNotFoundException {
        String path = uri.getPath();
        if (path == null) {
            throw new FileNotFoundException("no path");
        }
        String[] parts = path.split("/");
        if (parts.length != 3) {
            throw new FileNotFoundException("bad path: " + path);
        }
        File base;
        if ("rec".equals(parts[1])) {
            base = RecorderService.recDirOf(getContext());
        } else if ("logs".equals(parts[1])) {
            base = Diag.dir(getContext());
        } else {
            throw new FileNotFoundException("unknown root: " + parts[1]);
        }
        String name = parts[2];
        if (name.contains("..") || name.contains("/") || name.contains("\\")) {
            throw new FileNotFoundException("bad name");
        }
        try {
            File f = new File(base, name);
            String canonical = f.getCanonicalPath();
            String baseCanonical = base.getCanonicalPath();
            if (!canonical.startsWith(baseCanonical + File.separator)) {
                throw new FileNotFoundException("outside root");
            }
            return f;
        } catch (FileNotFoundException e) {
            throw e;
        } catch (Exception e) {
            throw new FileNotFoundException(e.getMessage());
        }
    }
}
