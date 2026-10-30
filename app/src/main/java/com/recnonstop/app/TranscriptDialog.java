package com.recnonstop.app;

import android.app.Activity;
import android.app.Dialog;
import android.graphics.Color;
import android.graphics.drawable.ColorDrawable;
import android.util.DisplayMetrics;
import android.view.Gravity;
import android.view.View;
import android.view.Window;
import android.view.WindowManager;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;
import android.widget.Toast;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * 转写详情 + 说话人标记。点说话人标签输入姓名，PC 端保存标签并尝试注册声纹。
 */
final class TranscriptDialog {

    private TranscriptDialog() {
    }

    static void show(Activity activity, String jobId, String title, Runnable onChanged) {
        Dialog dialog = new Dialog(activity);
        dialog.requestWindowFeature(Window.FEATURE_NO_TITLE);
        LinearLayout root = new LinearLayout(activity);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setPadding(dp(activity, 18), dp(activity, 16), dp(activity, 18), dp(activity, 14));
        root.setBackgroundResource(R.drawable.bg_dialog);

        LinearLayout header = new LinearLayout(activity);
        header.setOrientation(LinearLayout.HORIZONTAL);
        header.setGravity(Gravity.CENTER_VERTICAL);
        TextView heading = new TextView(activity);
        heading.setText("转写与说话人");
        heading.setTextColor(activity.getColor(R.color.text_primary));
        heading.setTextSize(18);
        heading.setTypeface(null, android.graphics.Typeface.BOLD);
        header.addView(heading, new LinearLayout.LayoutParams(0, -2, 1));
        TextView close = new TextView(activity);
        close.setText("关闭");
        close.setTextColor(activity.getColor(R.color.brand));
        close.setTextSize(13);
        close.setPadding(dp(activity, 10), dp(activity, 6), dp(activity, 2), dp(activity, 6));
        close.setOnClickListener(v -> dialog.dismiss());
        header.addView(close);
        root.addView(header);

        TextView sub = new TextView(activity);
        sub.setText(title + " · 点说话人标签改名字，保存后会记住声纹");
        sub.setTextColor(activity.getColor(R.color.text_secondary));
        sub.setTextSize(11);
        sub.setPadding(0, dp(activity, 4), 0, dp(activity, 8));
        root.addView(sub);

        TextView status = new TextView(activity);
        status.setText("正在读取转写…");
        status.setTextColor(activity.getColor(R.color.text_secondary));
        status.setTextSize(12);
        status.setPadding(0, dp(activity, 6), 0, dp(activity, 8));
        root.addView(status);

        ScrollView scroll = new ScrollView(activity);
        LinearLayout list = new LinearLayout(activity);
        list.setOrientation(LinearLayout.VERTICAL);
        scroll.addView(list);
        root.addView(scroll, new LinearLayout.LayoutParams(-1, dp(activity, 420)));

        Map<String, List<TextView>> chips = new HashMap<>();
        Map<String, String> names = new HashMap<>();

        dialog.setContentView(root);
        Window w = dialog.getWindow();
        if (w != null) {
            w.setBackgroundDrawable(new ColorDrawable(Color.TRANSPARENT));
            w.addFlags(WindowManager.LayoutParams.FLAG_DIM_BEHIND);
            WindowManager.LayoutParams lp = w.getAttributes();
            lp.width = (int) (activity.getResources().getDisplayMetrics().widthPixels * 0.92);
            lp.height = WindowManager.LayoutParams.WRAP_CONTENT;
            lp.dimAmount = 0.45f;
            w.setAttributes(lp);
        }

        PcApi.getResult(activity, jobId, (body, error) -> {
            if (activity.isFinishing() || activity.isDestroyed()) return;
            if (error != null) {
                status.setText("读取失败：" + error);
                return;
            }
            JSONArray segments = body == null ? null : body.optJSONArray("segments");
            if (segments == null || segments.length() == 0) {
                status.setText("电脑还没返回分段。等分析完成后再试。");
                return;
            }
            status.setText("共 " + segments.length() + " 句。点说话人标签命名。");
            list.removeAllViews();
            chips.clear();
            names.clear();
            for (int i = 0; i < segments.length(); i++) {
                JSONObject seg = segments.optJSONObject(i);
                if (seg == null) continue;
                String sp = seg.optString("speaker", "spk_0");
                String person = seg.optString("person_name", "");
                if (person.isEmpty()) person = seg.optString("person", "");
                if (!person.isEmpty()) names.put(sp, person);
                list.addView(buildRow(activity, dialog, jobId, seg, sp, chips, names, status, onChanged));
            }
        });

        dialog.show();
    }

    private static View buildRow(Activity activity, Dialog dialog, String jobId, JSONObject seg,
                                 String sp, Map<String, List<TextView>> chips, Map<String, String> names,
                                 TextView status, Runnable onChanged) {
        LinearLayout row = new LinearLayout(activity);
        row.setOrientation(LinearLayout.VERTICAL);
        row.setPadding(0, dp(activity, 8), 0, dp(activity, 8));

        LinearLayout top = new LinearLayout(activity);
        top.setOrientation(LinearLayout.HORIZONTAL);
        top.setGravity(Gravity.CENTER_VERTICAL);

        TextView time = new TextView(activity);
        time.setText(formatTime(seg.optDouble("start", 0)));
        time.setTextColor(activity.getColor(R.color.text_secondary));
        time.setTextSize(11);
        time.setPadding(0, 0, dp(activity, 8), 0);
        top.addView(time);

        TextView chip = new TextView(activity);
        String label = names.get(sp);
        if (label == null || label.isEmpty()) label = displaySpeaker(sp);
        chip.setText(label);
        chip.setTextSize(12);
        chip.setTextColor(activity.getColor(R.color.brand));
        chip.setBackgroundResource(R.drawable.bg_chip_blue);
        chip.setPadding(dp(activity, 9), dp(activity, 4), dp(activity, 9), dp(activity, 4));
        chip.setOnClickListener(v -> {
            String cur = names.get(sp);
            Sheet.input(activity, "这是谁？",
                    "给「" + displaySpeaker(sp) + "」命名。保存后会尝试把这段声音加入声纹库。",
                    cur == null ? "" : cur, "例如：小王", "保存", value -> {
                        String name = value == null ? "" : value.trim();
                        if (name.isEmpty()) return;
                        if (name.length() > 24) name = name.substring(0, 24);
                        chip.setText("保存中…");
                        final String finalName = name;
                        PcApi.markSpeaker(activity, jobId, sp, finalName, (res, err) -> {
                            if (err != null) {
                                chip.setText(cur == null || cur.isEmpty() ? displaySpeaker(sp) : cur);
                                status.setText("保存失败：" + err);
                                Toast.makeText(activity, "保存失败：" + err, Toast.LENGTH_LONG).show();
                                return;
                            }
                            names.put(sp, finalName);
                            for (TextView c : chips.getOrDefault(sp, new ArrayList<>())) {
                                c.setText(finalName);
                            }
                            boolean voice = res != null && res.optBoolean("voiceprint", false);
                            status.setText(voice
                                    ? "已保存「" + finalName + "」，声纹已加入记忆"
                                    : "已保存「" + finalName + "」，声纹服务未启用，只保存了名字");
                            Toast.makeText(activity, voice ? "已记住这个名字和声纹" : "已保存名字", Toast.LENGTH_SHORT).show();
                            if (onChanged != null) onChanged.run();
                        });
                    });
        });
        top.addView(chip);
        row.addView(top);

        TextView text = new TextView(activity);
        text.setText(seg.optString("text", ""));
        text.setTextColor(activity.getColor(R.color.text_primary));
        text.setTextSize(14);
        text.setPadding(0, dp(activity, 5), 0, 0);
        text.setLineSpacing(dp(activity, 2), 1f);
        row.addView(text);

        List<TextView> list = chips.get(sp);
        if (list == null) {
            list = new ArrayList<>();
            chips.put(sp, list);
        }
        list.add(chip);

        View divider = new View(activity);
        divider.setBackgroundColor(activity.getColor(R.color.divider));
        row.addView(divider, new LinearLayout.LayoutParams(-1, dp(activity, 1)));
        return row;
    }

    private static String displaySpeaker(String sp) {
        if (sp == null || sp.isEmpty()) return "说话人";
        String s = sp.replace("spk_", "").replace("spk", "").trim();
        if (s.isEmpty()) s = "1";
        return "说话人 " + (Integer.parseInt(s) + 1);
    }

    private static String formatTime(double sec) {
        int s = (int) Math.max(0, sec);
        return String.format(java.util.Locale.ROOT, "%02d:%02d", s / 60, s % 60);
    }

    private static int dp(Activity a, int v) {
        return (int) (v * a.getResources().getDisplayMetrics().density + 0.5f);
    }
}
