package com.recnonstop.micgrab;

import android.Manifest;
import android.app.Activity;
import android.content.Intent;
import android.content.pm.PackageManager;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.widget.Button;
import android.widget.Switch;
import android.widget.TextView;

import java.util.Locale;

public class MainActivity extends Activity {

    private static final int REQ = 1;

    private TextView status;
    private Switch swCallMode;
    private final Handler handler = new Handler(Looper.getMainLooper());

    private final Runnable ticker = new Runnable() {
        @Override public void run() {
            refresh();
            handler.postDelayed(this, 500);
        }
    };

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_main);

        status = findViewById(R.id.status);
        swCallMode = findViewById(R.id.swCallMode);
        Button btnStart = findViewById(R.id.btnStart);
        Button btnStop = findViewById(R.id.btnStop);

        btnStart.setOnClickListener(v -> start());
        btnStop.setOnClickListener(v -> stop());

        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO)
                != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[]{Manifest.permission.RECORD_AUDIO}, REQ);
        }
        if (Build.VERSION.SDK_INT >= 33
                && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS)
                != PackageManager.PERMISSION_GRANTED) {
            requestPermissions(new String[]{Manifest.permission.POST_NOTIFICATIONS}, REQ);
        }
    }

    @Override
    protected void onResume() {
        super.onResume();
        handler.post(ticker);
    }

    @Override
    protected void onPause() {
        super.onPause();
        handler.removeCallbacks(ticker);
    }

    private void start() {
        Intent i = new Intent(this, GrabService.class)
                .setAction(GrabService.ACTION_START)
                .putExtra(GrabService.EXTRA_CALL_MODE, swCallMode.isChecked());
        startForegroundService(i);
    }

    private void stop() {
        Intent i = new Intent(this, GrabService.class)
                .setAction(GrabService.ACTION_STOP);
        startService(i);
    }

    private void refresh() {
        StringBuilder sb = new StringBuilder();
        sb.append(GrabService.running ? "状态：占用中\n" : "状态：未占用\n");
        sb.append(String.format(Locale.US, "电平：%.1f dB\n", GrabService.lastDb));
        sb.append("采样数：").append(GrabService.samples).append('\n');
        if (!GrabService.lastError.isEmpty()) {
            sb.append("错误：").append(GrabService.lastError).append('\n');
        }
        if (GrabService.running && GrabService.lastDb < -80) {
            sb.append("\n⚠ 当前几乎静音，可能已被别的 App 抢走");
        }
        status.setText(sb.toString());
    }
}
