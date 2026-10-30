package com.recnonstop.app;

import android.content.Intent;
import android.service.quicksettings.Tile;
import android.service.quicksettings.TileService;

/** 下拉快捷开关：一键开始/停止，不用打开 App。 */
public final class RecorderTileService extends TileService {

    @Override
    public void onStartListening() {
        super.onStartListening();
        updateTile();
    }

    @Override
    public void onClick() {
        super.onClick();
        try {
            Intent i = new Intent(this, RecorderService.class);
            if (RecState.running) {
                i.setAction(RecorderService.ACTION_STOP);
                startService(i);
            } else {
                i.setAction(RecorderService.ACTION_START);
                startForegroundService(i);
            }
        } catch (Throwable t) {
            Diag.error(this, "RecNonstop", "tile action failed", t);
            Health.error(this, "快捷开关启动失败：" + t);
            Notifications.alert(this, 2101, "录不停",
                    "快捷开关没能启动录音，请打开 App 检查权限。");
        }
        updateTile();
    }

    private void updateTile() {
        Tile t = getQsTile();
        if (t == null) {
            return;
        }
        t.setState(RecState.running ? Tile.STATE_ACTIVE : Tile.STATE_INACTIVE);
        t.setLabel(RecState.running ? "录不停 · 录音中" : "录不停");
        t.updateTile();
    }
}
