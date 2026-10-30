package com.recnonstop.app;

import android.app.Activity;
import android.app.Dialog;
import android.content.Context;
import android.graphics.Color;
import android.graphics.drawable.ColorDrawable;
import android.text.TextUtils;
import android.view.Gravity;
import android.view.LayoutInflater;
import android.view.View;
import android.view.Window;
import android.view.WindowManager;
import android.view.inputmethod.InputMethodManager;
import android.widget.EditText;
import android.widget.FrameLayout;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.TextView;

import java.util.List;
import java.util.function.Consumer;
import java.util.function.IntConsumer;

/**
 * 底部操作面板 / 选项面板 / 输入框 / 进度框。
 *
 * 之前的界面所有操作都是系统 AlertDialog，层级混乱也没有动作反馈。
 * 这里统一成从底部滑出的面板，危险操作标红，选择项有明确选中态。
 */
final class Sheet {

    static final class Action {
        final int icon;
        final String title;
        final String subtitle;
        final int tint;
        final Runnable run;

        Action(int icon, String title, String subtitle, int tint, Runnable run) {
            this.icon = icon;
            this.title = title;
            this.subtitle = subtitle;
            this.tint = tint;
            this.run = run;
        }
    }

    private static Dialog current;

    private Sheet() {
    }

    static boolean isShowing() {
        return current != null && current.isShowing();
    }

    static void dismissCurrent() {
        Dialog d = current;
        current = null;
        if (d != null && d.isShowing()) {
            try {
                d.dismiss();
            } catch (Exception ignored) {
            }
        }
    }

    static Dialog show(Activity a, String title, String subtitle, List<Action> actions) {
        Dialog d = new Dialog(a);
        d.requestWindowFeature(Window.FEATURE_NO_TITLE);
        LinearLayout root = base(a, title, subtitle);

        for (Action action : actions) {
            View row = LayoutInflater.from(a).inflate(R.layout.item_sheet_action, root, false);
            ImageView icon = row.findViewById(R.id.sheetIcon);
            FrameLayout box = row.findViewById(R.id.sheetIconBox);
            TextView tvTitle = row.findViewById(R.id.sheetTitle);
            TextView tvSub = row.findViewById(R.id.sheetSubtitle);
            ImageView chevron = row.findViewById(R.id.sheetChevron);

            icon.setImageResource(action.icon);
            icon.setColorFilter(action.tint);
            box.setBackgroundResource(softBackground(a, action.tint));
            tvTitle.setText(action.title);
            tvTitle.setTextColor(action.tint == a.getColor(R.color.danger)
                    ? a.getColor(R.color.danger)
                    : a.getColor(R.color.text_primary));
            if (TextUtils.isEmpty(action.subtitle)) {
                tvSub.setVisibility(View.GONE);
            } else {
                tvSub.setVisibility(View.VISIBLE);
                tvSub.setText(action.subtitle);
            }
            if (action.tint == a.getColor(R.color.danger)) {
                chevron.setColorFilter(a.getColor(R.color.danger));
            }
            row.setOnClickListener(v -> {
                d.dismiss();
                if (action.run != null) action.run.run();
            });
            root.addView(row);
        }

        root.addView(cancelRow(a, d));
        return showDialog(a, d, root, false);
    }

    static Dialog choices(Activity a, String title, String subtitle,
                          String[] labels, String[] details, int selected,
                          IntConsumer onPick) {
        Dialog d = new Dialog(a);
        d.requestWindowFeature(Window.FEATURE_NO_TITLE);
        LinearLayout root = base(a, title, subtitle);

        for (int i = 0; i < labels.length; i++) {
            View row = LayoutInflater.from(a).inflate(R.layout.item_sheet_choice, root, false);
            TextView tvTitle = row.findViewById(R.id.choiceTitle);
            TextView tvSub = row.findViewById(R.id.choiceSubtitle);
            ImageView check = row.findViewById(R.id.choiceCheck);
            boolean on = i == selected;

            tvTitle.setText(labels[i]);
            if (details != null && i < details.length && !TextUtils.isEmpty(details[i])) {
                tvSub.setVisibility(View.VISIBLE);
                tvSub.setText(details[i]);
            } else {
                tvSub.setVisibility(View.GONE);
            }
            row.setBackgroundResource(on
                    ? R.drawable.bg_choice_selected
                    : R.drawable.bg_choice_normal);
            check.setVisibility(on ? View.VISIBLE : View.INVISIBLE);
            check.setColorFilter(a.getColor(R.color.brand));
            if (on) tvTitle.setTextColor(a.getColor(R.color.brand));

            final int index = i;
            row.setOnClickListener(v -> {
                d.dismiss();
                if (onPick != null) onPick.accept(index);
            });
            root.addView(row);
        }

        root.addView(cancelRow(a, d));
        return showDialog(a, d, root, false);
    }

    static Dialog input(Activity a, String title, String message, String initial,
                        String hint, String positive, Consumer<String> onOk) {
        Dialog d = new Dialog(a);
        d.requestWindowFeature(Window.FEATURE_NO_TITLE);
        View root = LayoutInflater.from(a).inflate(R.layout.dialog_input, null, false);

        TextView tvTitle = root.findViewById(R.id.dlgTitle);
        TextView tvMessage = root.findViewById(R.id.dlgMessage);
        EditText input = root.findViewById(R.id.dlgInput);
        TextView cancel = root.findViewById(R.id.dlgCancel);
        TextView ok = root.findViewById(R.id.dlgOk);

        tvTitle.setText(title);
        if (TextUtils.isEmpty(message)) {
            tvMessage.setVisibility(View.GONE);
        } else {
            tvMessage.setVisibility(View.VISIBLE);
            tvMessage.setText(message);
        }
        input.setHint(hint);
        input.setText(initial == null ? "" : initial);
        if (!TextUtils.isEmpty(initial)) {
            input.setSelection(input.getText().length());
        }
        ok.setText(positive);

        cancel.setOnClickListener(v -> d.dismiss());
        ok.setOnClickListener(v -> {
            String value = input.getText().toString();
            d.dismiss();
            if (onOk != null) onOk.accept(value);
        });
        input.setOnEditorActionListener((v, actionId, event) -> {
            ok.performClick();
            return true;
        });

        d.setContentView(root);
        d.setCanceledOnTouchOutside(true);
        Window w = d.getWindow();
        if (w != null) {
            w.setBackgroundDrawable(new ColorDrawable(Color.TRANSPARENT));
            w.addFlags(WindowManager.LayoutParams.FLAG_DIM_BEHIND);
            w.setDimAmount(0.45f);
            w.setGravity(Gravity.CENTER);
            w.setSoftInputMode(WindowManager.LayoutParams.SOFT_INPUT_ADJUST_RESIZE
                    | WindowManager.LayoutParams.SOFT_INPUT_STATE_VISIBLE);
        }
        d.setOnShowListener(di -> {
            input.requestFocus();
            InputMethodManager imm = (InputMethodManager)
                    a.getSystemService(Context.INPUT_METHOD_SERVICE);
            if (imm != null) {
                input.postDelayed(() -> imm.showSoftInput(input, InputMethodManager.SHOW_IMPLICIT), 120);
            }
        });

        current = d;
        d.setOnDismissListener(di -> {
            if (current == d) current = null;
        });
        d.show();
        if (w != null) {
            int width = a.getResources().getDisplayMetrics().widthPixels - dp(a, 48);
            w.setLayout(Math.max(dp(a, 260), width), WindowManager.LayoutParams.WRAP_CONTENT);
        }
        return d;
    }

    static Dialog progress(Activity a, String title, String message,
                           boolean cancelable, Runnable onCancel) {
        Dialog d = new Dialog(a);
        d.requestWindowFeature(Window.FEATURE_NO_TITLE);
        View root = LayoutInflater.from(a).inflate(R.layout.dialog_progress, null, false);

        TextView tvTitle = root.findViewById(R.id.dlgTitle);
        TextView tvMessage = root.findViewById(R.id.dlgMessage);
        TextView cancel = root.findViewById(R.id.dlgCancel);
        tvTitle.setText(title);
        tvMessage.setText(message);

        if (cancelable) {
            cancel.setVisibility(View.VISIBLE);
            cancel.setOnClickListener(v -> d.cancel());
            d.setOnCancelListener(di -> {
                if (onCancel != null) onCancel.run();
            });
        } else {
            cancel.setVisibility(View.GONE);
            d.setCanceledOnTouchOutside(false);
        }

        d.setContentView(root);
        d.setCancelable(cancelable);
        Window w = d.getWindow();
        if (w != null) {
            w.setBackgroundDrawable(new ColorDrawable(Color.TRANSPARENT));
            w.addFlags(WindowManager.LayoutParams.FLAG_DIM_BEHIND);
            w.setDimAmount(0.45f);
            w.setGravity(Gravity.CENTER);
        }

        current = d;
        d.setOnDismissListener(di -> {
            if (current == d) current = null;
        });
        d.show();
        if (w != null) {
            w.setLayout(dp(a, 300), WindowManager.LayoutParams.WRAP_CONTENT);
        }
        return d;
    }

    private static LinearLayout base(Activity a, String title, String subtitle) {
        LinearLayout root = new LinearLayout(a);
        root.setOrientation(LinearLayout.VERTICAL);
        root.setBackgroundResource(R.drawable.bg_sheet);
        root.setPadding(dp(a, 20), dp(a, 10), dp(a, 20), dp(a, 24));

        View handle = new View(a);
        LinearLayout.LayoutParams hp = new LinearLayout.LayoutParams(dp(a, 38), dp(a, 4));
        hp.gravity = Gravity.CENTER_HORIZONTAL;
        hp.topMargin = dp(a, 2);
        hp.bottomMargin = dp(a, 12);
        handle.setLayoutParams(hp);
        handle.setBackgroundResource(R.drawable.bg_sheet_handle);
        root.addView(handle);

        if (!TextUtils.isEmpty(title)) {
            TextView tvTitle = new TextView(a);
            tvTitle.setText(title);
            tvTitle.setTextSize(18);
            tvTitle.setTextColor(a.getColor(R.color.text_primary));
            tvTitle.setTypeface(android.graphics.Typeface.DEFAULT_BOLD);
            tvTitle.setIncludeFontPadding(false);
            root.addView(tvTitle);
        }
        if (!TextUtils.isEmpty(subtitle)) {
            TextView tvSub = new TextView(a);
            tvSub.setText(subtitle);
            tvSub.setTextSize(12);
            tvSub.setTextColor(a.getColor(R.color.text_secondary));
            tvSub.setLineSpacing(dp(a, 2), 1f);
            LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(
                    LinearLayout.LayoutParams.MATCH_PARENT,
                    LinearLayout.LayoutParams.WRAP_CONTENT);
            lp.topMargin = dp(a, 5);
            lp.bottomMargin = dp(a, 6);
            tvSub.setLayoutParams(lp);
            root.addView(tvSub);
        } else {
            View spacer = new View(a);
            spacer.setLayoutParams(new LinearLayout.LayoutParams(
                    LinearLayout.LayoutParams.MATCH_PARENT, dp(a, 6)));
            root.addView(spacer);
        }
        return root;
    }

    private static TextView cancelRow(Activity a, Dialog d) {
        TextView cancel = new TextView(a);
        cancel.setText("取消");
        cancel.setTextSize(14);
        cancel.setTextColor(a.getColor(R.color.text_secondary));
        cancel.setGravity(Gravity.CENTER);
        cancel.setBackgroundResource(R.drawable.bg_ripple_transparent);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(
                LinearLayout.LayoutParams.MATCH_PARENT, dp(a, 46));
        lp.topMargin = dp(a, 6);
        cancel.setLayoutParams(lp);
        cancel.setClickable(true);
        cancel.setFocusable(true);
        cancel.setOnClickListener(v -> d.dismiss());
        return cancel;
    }

    private static Dialog showDialog(Activity a, Dialog d, View content, boolean wrapWidth) {
        d.setContentView(content);
        d.setCanceledOnTouchOutside(true);
        Window w = d.getWindow();
        if (w != null) {
            w.setBackgroundDrawable(new ColorDrawable(Color.TRANSPARENT));
            w.addFlags(WindowManager.LayoutParams.FLAG_DIM_BEHIND);
            w.setDimAmount(0.45f);
            w.setGravity(Gravity.BOTTOM);
            w.setWindowAnimations(R.style.SheetAnimation);
        }
        current = d;
        d.setOnDismissListener(di -> {
            if (current == d) current = null;
        });
        d.show();
        if (w != null) {
            w.setLayout(wrapWidth
                    ? WindowManager.LayoutParams.WRAP_CONTENT
                    : WindowManager.LayoutParams.MATCH_PARENT,
                    WindowManager.LayoutParams.WRAP_CONTENT);
        }
        return d;
    }

    private static int softBackground(Activity a, int tint) {
        if (tint == a.getColor(R.color.danger)) return R.drawable.bg_icon_soft_red;
        if (tint == a.getColor(R.color.warning)) return R.drawable.bg_icon_soft_amber;
        if (tint == a.getColor(R.color.success)) return R.drawable.bg_icon_soft_green;
        if (tint == a.getColor(R.color.text_secondary)
                || tint == a.getColor(R.color.text_tertiary)) {
            return R.drawable.bg_icon_soft_gray;
        }
        return R.drawable.bg_icon_soft_blue;
    }

    private static int dp(Context c, int value) {
        return Math.round(value * c.getResources().getDisplayMetrics().density);
    }
}
