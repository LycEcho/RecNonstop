package com.recnonstop.app;

import android.content.Context;
import android.graphics.Canvas;
import android.graphics.Paint;
import android.os.SystemClock;
import android.util.AttributeSet;
import android.view.View;

/** Displays recent microphone peaks; silence and paused recording stay flat. */
public final class RecordingWaveformView extends View {
    private final Paint paint = new Paint(Paint.ANTI_ALIAS_FLAG);
    private final float[] peaks = new float[52];
    private long lastSample;
    private boolean active;

    public RecordingWaveformView(Context context, AttributeSet attrs) {
        super(context, attrs);
        setImportantForAccessibility(IMPORTANT_FOR_ACCESSIBILITY_NO);
    }

    public void update(boolean recording) {
        long stamp = RecState.levelSampleMs;
        boolean fresh = recording && stamp > 0
                && SystemClock.elapsedRealtime() - stamp < 2000;
        if (!fresh) {
            if (active) java.util.Arrays.fill(peaks, 0f);
        } else if (stamp != lastSample) {
            System.arraycopy(peaks, 1, peaks, 0, peaks.length - 1);
            peaks[peaks.length - 1] = Math.max(0f, Math.min(1f,
                    (RecState.levelDb + 70f) / 65f));
            lastSample = stamp;
        }
        active = fresh;
        invalidate();
    }

    @Override protected void onDraw(Canvas canvas) {
        super.onDraw(canvas);
        float density = getResources().getDisplayMetrics().density;
        float step = getWidth() / (float) peaks.length;
        float width = Math.min(3f * density, step * .5f);
        float center = getHeight() / 2f;
        paint.setColor(getContext().getColor(R.color.brand));
        for (int i = 0; i < peaks.length; i++) {
            float x = step * (i + .5f);
            float half = Math.max(density, peaks[i] * (center - density * 3));
            paint.setAlpha(active ? 70 + Math.round(185f * (i + 1) / peaks.length) : 55);
            canvas.drawRoundRect(x - width / 2, center - half,
                    x + width / 2, center + half, width, width, paint);
        }
    }
}
