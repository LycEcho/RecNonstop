package com.recnonstop.app;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.content.SharedPreferences;
import android.util.Log;

/** 开机自启（Android 15 起系统不允许开机拉起 microphone 类型前台服务，失败时提醒用户） */
public class BootReceiver extends BroadcastReceiver {

    @Override
    public void onReceive(Context ctx, Intent intent) {
        SharedPreferences sp = ctx.getSharedPreferences(
                RecorderService.PREFS, Context.MODE_PRIVATE);
        if (!sp.getBoolean("enabled", false)) {
            return;
        }

        // 开机后清掉上次失败退避，允许立即尝试一次。
        sp.edit().putLong(RecorderService.KEY_NEXT_START_AT, 0L).apply();

        try {
            Intent s = new Intent(ctx, RecorderService.class)
                    .setAction(RecorderService.ACTION_START);
            ctx.startForegroundService(s);
            Diag.log(ctx, "RecNonstop", "boot: service started");
        } catch (Throwable t) {
            Log.w("RecNonstop", "boot start blocked: " + t);
            Health.bootBlocked(ctx);
            Diag.error(ctx, "RecNonstop", "boot start blocked", t);
            Notifications.alert(ctx, 2020, "录不停没有自动开始",
                    "系统限制了开机后台录音（Android 15+ 的常见限制）。"
                            + "请打开 App 手动开始；之后只要不被系统清理就会继续录。");
        }
    }
}
