package com.recnonstop.app;

import android.app.Activity;
import android.app.Dialog;
import android.content.Context;
import android.graphics.Color;
import android.graphics.drawable.ColorDrawable;
import android.media.AudioAttributes;
import android.media.AudioFocusRequest;
import android.media.AudioManager;
import android.media.MediaPlayer;
import android.os.Handler;
import android.os.Looper;
import android.view.Gravity;
import android.view.View;
import android.view.Window;
import android.view.WindowManager;
import android.widget.SeekBar;
import android.widget.TextView;
import android.widget.Toast;

import java.io.File;
import java.util.Locale;

/** A local recording player whose lifetime is limited to the visible sheet. */
final class AudioPlayerSheet {
    private static AudioPlayerSheet current;

    private final Activity activity;
    private final Dialog dialog;
    private final Handler handler = new Handler(Looper.getMainLooper());
    private final AudioManager audioManager;
    private final AudioFocusRequest focusRequest;
    private final TextView status;
    private final TextView toggle;
    private final TextView elapsed;
    private final TextView durationLabel;
    private final View back;
    private final View forward;
    private final View recordingNote;
    private final SeekBar seek;
    private MediaPlayer player;
    private boolean prepared;
    private boolean closed;
    private boolean dragging;
    private int activeSeek = -1;
    private int queuedSeek = -1;
    private boolean focused;
    private int duration;

    static void show(Activity activity, File file) {
        dismissCurrent();
        if (activity.isFinishing() || activity.isDestroyed()) return;
        if (file == null || !file.isFile() || file.length() == 0) {
            Toast.makeText(activity, "录音文件不存在或为空，请刷新记录列表", Toast.LENGTH_LONG).show();
            return;
        }
        Sheet.dismissCurrent();
        AudioPlayerSheet sheet = new AudioPlayerSheet(activity, file);
        current = sheet;
        try {
            sheet.open(file);
        } catch (RuntimeException e) {
            dismissCurrent();
            Toast.makeText(activity, "暂时无法打开播放器，请返回记录页重试", Toast.LENGTH_LONG).show();
        }
    }

    static void dismissCurrent() {
        AudioPlayerSheet sheet = current;
        current = null;
        if (sheet == null) return;
        // Dismiss callbacks may be queued; stop sound before returning from onPause.
        sheet.release();
        if (sheet.dialog.isShowing()) {
            try { sheet.dialog.dismiss(); }
            catch (RuntimeException ignored) { }
        }
    }

    static boolean isShowing() {
        return current != null && !current.closed && current.dialog.isShowing();
    }

    private AudioPlayerSheet(Activity activity, File file) {
        this.activity = activity;
        dialog = new Dialog(activity);
        dialog.requestWindowFeature(Window.FEATURE_NO_TITLE);
        dialog.setContentView(R.layout.dialog_audio_player);
        dialog.setCanceledOnTouchOutside(true);
        dialog.setOnDismissListener(d -> release());
        ((TextView) dialog.findViewById(R.id.playerFilename)).setText(file.getName());
        dialog.findViewById(R.id.playerClose).setOnClickListener(v -> dialog.dismiss());
        status = dialog.findViewById(R.id.playerStatus);
        toggle = dialog.findViewById(R.id.playerToggle);
        elapsed = dialog.findViewById(R.id.playerElapsed);
        durationLabel = dialog.findViewById(R.id.playerDuration);
        seek = dialog.findViewById(R.id.playerSeek);
        back = dialog.findViewById(R.id.playerBack);
        forward = dialog.findViewById(R.id.playerForward);
        recordingNote = dialog.findViewById(R.id.playerRecordingNote);
        audioManager = (AudioManager) activity.getSystemService(Context.AUDIO_SERVICE);
        focusRequest = new AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT)
                .setAudioAttributes(audioAttributes())
                .setWillPauseWhenDucked(true)
                .setOnAudioFocusChangeListener(change -> {
                    if (change < 0 && usable()) pause("其他应用正在使用声音，播放已暂停");
                }, handler)
                .build();
        toggle.setOnClickListener(v -> {
            if (!usable()) return;
            try {
                if (player.isPlaying()) pause("已暂停");
                else play();
            } catch (Exception e) {
                fail("播放暂时中断，请关闭后重试");
            }
        });
        back.setOnClickListener(v -> jump(-15_000));
        forward.setOnClickListener(v -> jump(15_000));
        seek.setOnSeekBarChangeListener(new SeekBar.OnSeekBarChangeListener() {
            @Override public void onProgressChanged(SeekBar bar, int progress, boolean fromUser) {
                if (fromUser) elapsed.setText(formatTime(progress));
            }
            @Override public void onStartTrackingTouch(SeekBar bar) { dragging = true; }
            @Override public void onStopTrackingTouch(SeekBar bar) {
                dragging = false;
                seekTo(bar.getProgress());
            }
        });
    }

    private static AudioAttributes audioAttributes() {
        return new AudioAttributes.Builder()
                .setUsage(AudioAttributes.USAGE_MEDIA)
                .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build();
    }

    private void open(File file) {
        Window window = dialog.getWindow();
        if (window != null) {
            window.setBackgroundDrawable(new ColorDrawable(Color.TRANSPARENT));
            window.addFlags(WindowManager.LayoutParams.FLAG_DIM_BEHIND);
            window.setDimAmount(0.32f);
            window.setGravity(Gravity.BOTTOM);
            window.setWindowAnimations(R.style.SheetAnimation);
        }
        dialog.show();
        if (window != null) window.setLayout(WindowManager.LayoutParams.MATCH_PARENT,
                WindowManager.LayoutParams.WRAP_CONTENT);
        updateRecordingNote();
        toggle.setAlpha(0.5f);
        try {
            MediaPlayer media = new MediaPlayer();
            player = media;
            media.setAudioAttributes(audioAttributes());
            media.setDataSource(file.getAbsolutePath());
            media.setOnPreparedListener(p -> {
                if (closed || player != p || !dialog.isShowing()) return;
                try {
                    prepared = true;
                    duration = Math.max(0, p.getDuration());
                    durationLabel.setText(formatTime(duration));
                    seek.setMax(Math.max(1, duration));
                    setControls(true);
                    // Recording continues independently. Let the user see the headphone
                    // hint before intentionally playing sound beside a live microphone.
                    if (RecState.running) {
                        status.setText("录音任务保持开启，点播放开始试听");
                        toggle.setText("播放");
                    } else {
                        play();
                    }
                    if (usable()) handler.post(tick);
                } catch (Exception e) {
                    fail("无法读取录音时长，请关闭后重试");
                }
            });
            media.setOnSeekCompleteListener(p -> {
                if (!usable() || player != p) return;
                activeSeek = -1;
                if (queuedSeek >= 0) startNextSeek();
                else if (!dragging) updatePosition();
            });
            media.setOnCompletionListener(p -> {
                if (!usable() || player != p) return;
                elapsed.setText(formatTime(duration));
                seek.setProgress(duration);
                status.setText("播放结束");
                toggle.setText("重播");
                abandonFocus();
            });
            media.setOnErrorListener((p, what, extra) -> {
                if (!closed && player == p) fail("暂时无法播放这段录音，请关闭后重试");
                return true;
            });
            media.prepareAsync();
        } catch (Exception e) {
            fail("无法读取这段录音，请检查文件是否仍在手机上");
        }
    }

    private boolean usable() {
        return !closed && prepared && player != null && dialog.isShowing()
                && !activity.isFinishing() && !activity.isDestroyed();
    }

    private void play() {
        if (!usable()) return;
        try {
            if (audioManager != null && !focused) {
                focused = audioManager.requestAudioFocus(focusRequest) == AudioManager.AUDIOFOCUS_REQUEST_GRANTED;
                if (!focused) {
                    status.setText("当前无法播放，请结束通话或稍后再试");
                    toggle.setText("播放");
                    return;
                }
            }
            if (activeSeek < 0 && queuedSeek < 0 && duration > 0
                    && player.getCurrentPosition() >= duration) seekTo(0);
            if (!usable()) return;
            player.start();
            toggle.setText("暂停");
            status.setText("正在播放");
        } catch (Exception e) {
            fail("播放暂时中断，请关闭后重试");
        }
    }

    private void pause(String message) {
        if (!usable()) return;
        try {
            if (player.isPlaying()) player.pause();
            toggle.setText("播放");
            status.setText(message);
            abandonFocus();
        } catch (Exception e) {
            fail("播放暂时中断，请关闭后重试");
        }
    }

    private void jump(int delta) {
        if (!usable()) return;
        try {
            int position = queuedSeek >= 0 ? queuedSeek
                    : activeSeek >= 0 ? activeSeek : player.getCurrentPosition();
            seekTo((long) position + delta);
        }
        catch (Exception e) { fail("暂时无法调整播放位置"); }
    }

    private void seekTo(long position) {
        if (!usable()) return;
        queuedSeek = (int) Math.max(0L, Math.min((long) duration, position));
        seek.setProgress(queuedSeek);
        elapsed.setText(formatTime(queuedSeek));
        // MediaPlayer seeks asynchronously. Accumulate rapid +/-15s presses and
        // issue the latest target only after the current seek completes.
        if (activeSeek < 0) startNextSeek();
    }

    private void startNextSeek() {
        if (!usable() || queuedSeek < 0) return;
        try {
            activeSeek = queuedSeek;
            queuedSeek = -1;
            player.seekTo(activeSeek, MediaPlayer.SEEK_CLOSEST);
            if (!player.isPlaying()) toggle.setText("播放");
        } catch (Exception e) {
            activeSeek = queuedSeek = -1;
            fail("暂时无法调整播放位置");
        }
    }

    private final Runnable tick = new Runnable() {
        @Override public void run() {
            if (!usable()) return;
            updateRecordingNote();
            if (!dragging && activeSeek < 0) updatePosition();
            if (usable()) handler.postDelayed(this, 500);
        }
    };

    private void updateRecordingNote() {
        recordingNote.setVisibility(RecState.running ? View.VISIBLE : View.GONE);
    }

    private void updatePosition() {
        if (!usable()) return;
        try {
            int position = player.getCurrentPosition();
            seek.setProgress(position);
            elapsed.setText(formatTime(position));
        } catch (Exception e) {
            fail("播放暂时中断，请关闭后重试");
        }
    }

    private void setControls(boolean enabled) {
        toggle.setEnabled(enabled);
        toggle.setAlpha(enabled ? 1f : 0.5f);
        seek.setEnabled(enabled && duration > 0);
        back.setEnabled(enabled && duration > 0);
        forward.setEnabled(enabled && duration > 0);
    }

    private void fail(String message) {
        prepared = false;
        handler.removeCallbacks(tick);
        setControls(false);
        toggle.setText("无法播放");
        status.setText(message);
        releasePlayer();
    }

    private void abandonFocus() {
        if (focused && audioManager != null) {
            try { audioManager.abandonAudioFocusRequest(focusRequest); }
            catch (Exception ignored) { }
        }
        focused = false;
    }

    private void releasePlayer() {
        MediaPlayer media = player;
        player = null;
        if (media != null) {
            try {
                media.setOnPreparedListener(null);
                media.setOnCompletionListener(null);
                media.setOnSeekCompleteListener(null);
                media.setOnErrorListener(null);
            } catch (RuntimeException ignored) {
            } finally {
                try { media.release(); }
                catch (RuntimeException ignored) { }
                abandonFocus();
            }
        } else {
            abandonFocus();
        }
    }

    private void release() {
        if (closed) return;
        closed = true;
        prepared = false;
        activeSeek = queuedSeek = -1;
        handler.removeCallbacksAndMessages(null);
        if (current == this) current = null;
        releasePlayer();
    }

    private static String formatTime(int milliseconds) {
        int seconds = Math.max(0, milliseconds / 1000);
        if (seconds >= 3600) return String.format(Locale.ROOT, "%d:%02d:%02d",
                seconds / 3600, seconds / 60 % 60, seconds % 60);
        return String.format(Locale.ROOT, "%02d:%02d", seconds / 60, seconds % 60);
    }
}
