package com.recnonstop.app;

import android.content.BroadcastReceiver;
import android.content.Context;
import android.content.Intent;
import android.util.Log;

/**
 * 调试用控制通道。
 * 受 android.permission.DUMP 保护：shell 有该权限，普通第三方 App 没有。
 *
 * adb shell am broadcast -a com.recnonstop.app.CONTROL --es cmd start -n com.recnonstop.app/.ControlReceiver
 */
public class ControlReceiver extends BroadcastReceiver {

    @Override
    public void onReceive(Context ctx, Intent intent) {
        String cmd = intent.getStringExtra("cmd");
        if (cmd == null) cmd = "";
        Log.i("RecNonstop", "control cmd=" + cmd);

        Intent s = new Intent(ctx, RecorderService.class);
        switch (cmd) {
            case "start":
                s.setAction(RecorderService.ACTION_START);
                ctx.startForegroundService(s);
                break;
            case "stop":
                s.setAction(RecorderService.ACTION_STOP);
                ctx.startService(s);
                break;
            default:
                Log.w("RecNonstop", "unknown cmd: " + cmd);
        }
    }
}
