package com.recnonstop.app;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.util.Log;

import org.json.JSONObject;

/** 定时检查服务是否还活着，被系统杀了就拉起来。 */
public class WatchdogReceiver extends BroadcastReceiver {

    private static final long STALE_MS = 30 * 60 * 1000L;

    @Override
    public void onReceive(Context ctx, Intent intent) {
        SharedPreferences sp = ctx.getSharedPreferences(
                RecorderService.PREFS, Context.MODE_PRIVATE);
        boolean enabled = sp.getBoolean("enabled", false);
        boolean watchdog = Settings.getBool(ctx, Settings.KEY_WATCHDOG, true);

        if (!watchdog) {
            return;
        }

        boolean shouldStart = enabled && !RecState.running;
        if (!shouldStart && enabled && RecState.running && !RecState.paused
                && Health.isStale(ctx, STALE_MS)) {
            // 服务说自己还在跑，但 30 分钟没有成功切段：很可能是线程死了/被冻结。
            shouldStart = true;
            Diag.log(ctx, "RecNonstop", "watchdog: stale heartbeat, restarting");
        }

        long next = RecorderService.nextStartAt(ctx);
        if (shouldStart && System.currentTimeMillis() < next) {
            // 上次启动失败后的退避期，别每分钟硬拉。
            shouldStart = false;
        }

        if (shouldStart) {
            try {
                Intent s = new Intent(ctx, RecorderService.class)
                        .setAction(RecorderService.ACTION_START);
                ctx.startForegroundService(s);
                Diag.log(ctx, "RecNonstop", "watchdog: service restarted");
            } catch (Throwable t) {
                Log.e("RecNonstop", "watchdog restart failed", t);
                RecState.lastError = "看门狗拉起失败: " + t;
                Health.watchdogFailure(ctx, String.valueOf(t));
                Diag.error(ctx, "RecNonstop", "watchdog restart failed", t);
                JSONObject h = Health.read(ctx);
                int fails = h.optInt("watchdog_failures", 0);
                if (fails == 1 || fails % 10 == 0) {
                    Notifications.alert(ctx, 2010, "录不停没能自动恢复",
                            "系统阻止了后台启动（已失败 " + fails + " 次）。"
                                    + "请打开 App 手动开始，并检查自启动/电池白名单。");
                }
            }
        }

        if (enabled) {
            try {
                android.app.AlarmManager am =
                        (android.app.AlarmManager) ctx.getSystemService(Context.ALARM_SERVICE);
                Intent i = new Intent(ctx, WatchdogReceiver.class);
                android.app.PendingIntent pi = android.app.PendingIntent.getBroadcast(
                        ctx, 10, i,
                        android.app.PendingIntent.FLAG_UPDATE_CURRENT
                                | android.app.PendingIntent.FLAG_IMMUTABLE);
                long at = System.currentTimeMillis() + 60_000L;
                if (android.os.Build.VERSION.SDK_INT >= 31 && !am.canScheduleExactAlarms()) {
                    am.setAndAllowWhileIdle(
                            android.app.AlarmManager.RTC_WAKEUP, at, pi);
                } else {
                    am.setExactAndAllowWhileIdle(
                            android.app.AlarmManager.RTC_WAKEUP, at, pi);
                }
            } catch (Throwable t) {
                Log.w("RecNonstop", "watchdog reschedule failed", t);
                Diag.error(ctx, "RecNonstop", "watchdog reschedule failed", t);
            }
        }
    }
}
