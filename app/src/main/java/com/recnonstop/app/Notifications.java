package com.recnonstop.app;

import android.app.Notification;
import android.app.NotificationChannel;
import android.app.NotificationManager;
import android.app.PendingIntent;
import android.content.Context;
import android.content.Intent;

/** 通知渠道与“需要用户注意”的告警。 */
public final class Notifications {

    public static final String CH_STATUS = "rec_channel";
    public static final String CH_ALERT = "rec_alert2";
    public static final String CH_REPORT = "rec_report2";

    private Notifications() {
    }

    public static void ensure(Context c) {
        NotificationManager nm = c.getSystemService(NotificationManager.class);
        if (nm == null) {
            return;
        }
        NotificationChannel status = new NotificationChannel(
                CH_STATUS, "录音状态", NotificationManager.IMPORTANCE_LOW);
        status.setShowBadge(false);
        nm.createNotificationChannel(status);

        NotificationChannel alert = new NotificationChannel(
                CH_ALERT, "录音告警", NotificationManager.IMPORTANCE_HIGH);
        // 调试机/夜间不播放声音，但保留高优先级横幅。
        alert.setSound(null, null);
        alert.enableVibration(false);
        alert.setShowBadge(true);
        alert.setDescription("录音中断、空间不足、权限丢失等需要处理的问题");
        nm.createNotificationChannel(alert);

        NotificationChannel report = new NotificationChannel(
                CH_REPORT, "录音日报", NotificationManager.IMPORTANCE_HIGH);
        // 调试机/夜间都可能有通知：只弹横幅，不播放提示音、不振动。
        report.setSound(null, null);
        report.enableVibration(false);
        report.setShowBadge(true);
        report.setDescription("电脑生成每日日报后推送");
        nm.createNotificationChannel(report);
    }

    public static void alert(Context c, int id, String title, String text) {
        ensure(c);
        try {
            Intent open = new Intent(c, MainActivity.class)
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_CLEAR_TOP);
            PendingIntent pi = PendingIntent.getActivity(c, id, open,
                    PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
            Notification n = new Notification.Builder(c, CH_ALERT)
                    .setContentTitle(title)
                    .setContentText(text)
                    .setStyle(new Notification.BigTextStyle().bigText(text))
                    .setSmallIcon(R.drawable.ic_notification)
                    .setContentIntent(pi)
                    .setAutoCancel(true)
                    .setPriority(Notification.PRIORITY_HIGH)
                    .build();
            NotificationManager nm = c.getSystemService(NotificationManager.class);
            if (nm != null) {
                nm.notify(id, n);
            }
        } catch (Throwable t) {
            Diag.error(c, "RecNonstop", "alert notification failed", t);
        }
    }

    /** 每日日报生成后推送；点击直接打开日报页。 */
    public static void report(Context c, String reportId, String title, String text) {
        ensure(c);
        try {
            Intent open = new Intent(c, MainActivity.class)
                    .putExtra("report_id", reportId)
                    .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK | Intent.FLAG_ACTIVITY_CLEAR_TOP);
            PendingIntent pi = PendingIntent.getActivity(c, reportId(reportId), open,
                    PendingIntent.FLAG_UPDATE_CURRENT | PendingIntent.FLAG_IMMUTABLE);
            Notification n = new Notification.Builder(c, CH_REPORT)
                    .setContentTitle(title)
                    .setContentText(text)
                    .setStyle(new Notification.BigTextStyle().bigText(text))
                    .setSmallIcon(R.drawable.ic_notification)
                    .setContentIntent(pi)
                    .setAutoCancel(true)
                    .setPriority(Notification.PRIORITY_HIGH)
                    .build();
            NotificationManager nm = c.getSystemService(NotificationManager.class);
            if (nm != null) {
                nm.notify(reportId(reportId), n);
            }
        } catch (Throwable t) {
            Diag.error(c, "RecNonstop", "report notification failed", t);
        }
    }

    public static void cancelReport(Context c, String reportId) {
        try {
            NotificationManager nm = c.getSystemService(NotificationManager.class);
            if (nm != null) nm.cancel(reportId(reportId));
        } catch (Throwable ignored) {
        }
    }

    private static int reportId(String id) {
        int h = id == null ? 0 : id.hashCode();
        return 3000 + (h & 0x3FFFF);
    }

    /** 电脑主动发起连接：通知用户回 App 里点允许。 */
    public static void invite(Context c, String pcName) {
        alert(c, 1001, "电脑「" + pcName + "」想连接",
                "点这里回到 App，确认后就能同步录音。");
    }

    public static void cancelInvite(Context c) {
        try {
            NotificationManager nm = c.getSystemService(NotificationManager.class);
            if (nm != null) {
                nm.cancel(1001);
            }
        } catch (Throwable ignored) {
        }
    }
}
