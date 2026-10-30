package com.recnonstop.app;

import android.app.Activity;
import android.app.Dialog;
import android.content.Intent;
import android.graphics.Color;
import android.graphics.Typeface;
import android.graphics.drawable.ColorDrawable;
import android.graphics.drawable.GradientDrawable;
import android.media.MediaPlayer;
import android.net.Uri;
import android.os.Bundle;
import android.text.TextUtils;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
import android.view.Window;
import android.view.WindowManager;
import android.widget.HorizontalScrollView;
import android.widget.LinearLayout;
import android.widget.ScrollView;
import android.widget.TextView;

import org.json.JSONArray;
import org.json.JSONObject;

import java.util.ArrayList;
import java.util.HashMap;
import java.util.List;
import java.util.Map;

/**
 * 手机端对话日报。
 *
 * 设计参考 Omi 的 Today / Daily Recap、Limitless 的 Today 时间块、
 * Otter/Fireflies 的会话详情（摘要 / 对话 / 人物）：
 *   首页：今日概览 -> 重点 -> 时间线 -> 人物 -> 待办
 *   会话：一段对话的摘要与逐句上下文
 *   人物：某个人说过的话，按会话分组
 *
 * 数据来自 report.detail，随日报一起下载，断网可看。
 */
public class ReportDetailActivity extends Activity {
    private static final String EXTRA_REPORT_ID = "report_id";
    private static final int MODE_HOME = 0;
    private static final int MODE_SESSION = 1;
    private static final int MODE_PERSON = 2;

    private JSONObject report;
    private JSONObject detail;
    private JSONObject stats;
    private JSONArray sessions;
    private JSONArray people;
    private JSONArray utterances;
    private JSONArray highlights;
    private JSONArray actions;

    private LinearLayout content;
    private ScrollView scroll;
    private TextView tvTitle;
    private TextView tvMeta;
    private int mode = MODE_HOME;
    private int currentSession = -1;
    private String currentPerson = "";
    private MediaPlayer player;

    public static Intent intent(Activity from, String reportId) {
        return new Intent(from, ReportDetailActivity.class).putExtra(EXTRA_REPORT_ID, reportId);
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_report_detail);

        String reportId = getIntent().getStringExtra(EXTRA_REPORT_ID);
        report = DailyReports.get(this, reportId);
        detail = report == null ? null : report.optJSONObject("detail");
        if (detail != null) {
            stats = detail.optJSONObject("stats");
            sessions = detail.optJSONArray("sessions");
            people = detail.optJSONArray("people");
            utterances = detail.optJSONArray("utterances");
            highlights = detail.optJSONArray("highlights");
            actions = detail.optJSONArray("actions");
        }

        content = findViewById(R.id.detailContent);
        scroll = findViewById(R.id.detailScroll);
        tvTitle = findViewById(R.id.tvDetailTitle);
        tvMeta = findViewById(R.id.tvDetailMeta);
        findViewById(R.id.detailTabs).setVisibility(View.GONE);
        findViewById(R.id.btnDetailBack).setOnClickListener(v -> goBack());

        if (detail == null || utterances == null) {
            tvTitle.setText("对话日报");
            tvMeta.setText("暂无结构化详情");
            addEmpty("电脑端还没有这份日报的逐句结构。更新电脑端后重新生成日报即可。");
            return;
        }
        renderHome();
    }

    @Override
    protected void onDestroy() {
        stopPlayer();
        super.onDestroy();
    }

    @Override
    public void onBackPressed() {
        goBack();
    }

    private void goBack() {
        if (mode != MODE_HOME) {
            renderHome();
        } else {
            finish();
        }
    }

    // ---------------- 首页 ----------------

    private void renderHome() {
        mode = MODE_HOME;
        currentSession = -1;
        currentPerson = "";
        String title = report == null ? "对话日报" : report.optString("title", "对话日报");
        String date = report == null ? "" : report.optString("date", "");
        tvTitle.setText("对话日报");
        tvMeta.setText((date.isEmpty() ? "今天" : date) + " · " + utterances.length() + " 句 · " + peopleCount() + " 人");
        content.removeAllViews();

        addHero(title);
        addKpiRow();
        addHighlights();
        addTimeline();
        addPeople();
        addActions();
        addPending();
        addFootNote();
    }

    private void addHero(String title) {
        LinearLayout hero = new LinearLayout(this);
        hero.setOrientation(LinearLayout.VERTICAL);
        hero.setPadding(dp(18), dp(16), dp(18), dp(16));
        hero.setBackgroundResource(R.drawable.bg_hero);
        content.addView(hero);

        TextView kicker = text("TODAY · " + (report == null ? "" : report.optString("date", "")), 10, getColor(R.color.hero_text_dim), true);
        kicker.setLetterSpacing(0.08f);
        hero.addView(kicker);
        hero.addView(text("今天聊了 " + sessionCount() + " 段对话", 22, Color.WHITE, true));
        String overview = detail.optString("overview", "");
        if (overview.isEmpty()) overview = report == null ? "" : report.optString("summary", "");
        TextView sub = text(overview, 13, getColor(R.color.hero_text_soft), false);
        sub.setPadding(0, dp(5), 0, 0);
        sub.setLineSpacing(dp(2), 1.0f);
        hero.addView(sub);
    }

    private void addKpiRow() {
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        row.setPadding(0, dp(9), 0, 0);
        addKpi(row, String.valueOf(sessionCount()), "段对话");
        addKpi(row, fmt(stats == null ? 0 : stats.optDouble("talk_minutes", 0)) + " min", "有效说话");
        addKpi(row, String.valueOf(peopleCount()), "个人");
        content.addView(row);
    }

    private void addKpi(LinearLayout row, String value, String label) {
        LinearLayout box = new LinearLayout(this);
        box.setOrientation(LinearLayout.VERTICAL);
        box.setPadding(dp(11), dp(8), dp(11), dp(8));
        box.setBackgroundResource(R.drawable.bg_card);
        box.addView(text(value, 16, getColor(R.color.text_primary), true));
        box.addView(text(label, 10, getColor(R.color.text_secondary), false));
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1);
        lp.rightMargin = dp(7);
        row.addView(box, lp);
    }

    private void addHighlights() {
        if (highlights == null || highlights.length() == 0) return;
        addSection("今天发生了什么", "先看重点，再进时间线。");
        HorizontalScrollView view = new HorizontalScrollView(this);
        view.setHorizontalScrollBarEnabled(false);
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        view.addView(row);

        for (int i = 0; i < highlights.length(); i++) {
            JSONObject h = highlights.optJSONObject(i);
            if (h == null) continue;
            LinearLayout card = new LinearLayout(this);
            card.setOrientation(LinearLayout.VERTICAL);
            card.setPadding(dp(13), dp(11), dp(13), dp(11));
            card.setBackgroundResource(R.drawable.bg_card);
            card.setElevation(dp(1));
            TextView time = text(h.optString("time", ""), 10, getColor(R.color.brand), true);
            card.addView(time);
            TextView t = text(h.optString("title", ""), 14, getColor(R.color.text_primary), true);
            t.setPadding(0, dp(4), 0, 0);
            card.addView(t);
            TextView d = text(h.optString("detail", ""), 11, getColor(R.color.text_secondary), false);
            d.setPadding(0, dp(3), 0, 0);
            d.setMaxLines(3);
            card.addView(d);
            TextView open = text("打开这段 →", 11, getColor(R.color.brand), true);
            open.setPadding(0, dp(7), 0, 0);
            card.addView(open);
            final int sid = h.optInt("session", -1);
            card.setOnClickListener(v -> openSession(sid));
            LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(dp(215), ViewGroup.LayoutParams.WRAP_CONTENT);
            lp.rightMargin = dp(9);
            row.addView(card, lp);
        }
        content.addView(view);
    }

    private void addTimeline() {
        addSection("时间线", "点一段对话，看它的摘要和完整上下文。");
        if (sessions == null) return;
        for (int i = 0; i < sessions.length(); i++) {
            JSONObject s = sessions.optJSONObject(i);
            if (s == null) continue;
            // 早晨碎片在人物页仍可见，但首页只放主会话。
            if (s.optInt("id", i) >= 3) continue;
            addSessionCard(s, false);
        }
    }

    private void addPeople() {
        if (people == null || people.length() == 0) return;
        addSection("人物", "点一个人，只看 TA 说过的话。");
        for (int i = 0; i < people.length(); i++) {
            JSONObject p = people.optJSONObject(i);
            if (p != null) addPersonRow(p);
        }
    }

    private void addActions() {
        if (actions == null || actions.length() == 0) return;
        addSection("待办 / 决定", "只列录音里明确提到的事项。");
        LinearLayout card = card();
        for (int i = 0; i < actions.length(); i++) {
            JSONObject a = actions.optJSONObject(i);
            if (a == null) continue;
            LinearLayout row = new LinearLayout(this);
            row.setOrientation(LinearLayout.HORIZONTAL);
            row.setPadding(0, dp(4), 0, dp(4));
            TextView dot = text("●", 9, getColor(R.color.brand), true);
            dot.setPadding(0, dp(3), dp(8), 0);
            row.addView(dot);
            LinearLayout body = new LinearLayout(this);
            body.setOrientation(LinearLayout.VERTICAL);
            body.addView(text(a.optString("text", ""), 13, getColor(R.color.text_primary), false));
            body.addView(text(a.optString("source", ""), 10, getColor(R.color.text_tertiary), false));
            row.addView(body);
            final int sid = a.optInt("session", -1);
            row.setOnClickListener(v -> openSession(sid));
            card.addView(row);
        }
        content.addView(card);
    }

    private void addPending() {
        String pending = detail.optString("pending", "");
        if (pending.isEmpty()) return;
        addSection("待确认", "");
        LinearLayout card = new LinearLayout(this);
        card.setOrientation(LinearLayout.VERTICAL);
        card.setPadding(dp(14), dp(11), dp(14), dp(11));
        card.setBackgroundResource(R.drawable.bg_warn_card);
        card.addView(text("有相似声音，等你确认", 13, getColor(R.color.warning), true));
        TextView t = text(pending, 11, getColor(R.color.text_secondary), false);
        t.setPadding(0, dp(3), 0, 0);
        card.addView(t);
        content.addView(card);
    }

    private void addFootNote() {
        TextView t = text("只统计有有效转写的录音；静音和太短的碎片没有进入时间线。",
                10, getColor(R.color.text_tertiary), false);
        t.setPadding(dp(2), dp(16), dp(2), dp(4));
        content.addView(t);
    }

    // ---------------- 会话 ----------------

    private void openSession(int sid) {
        JSONObject s = findSession(sid);
        if (s == null) return;
        mode = MODE_SESSION;
        currentSession = s.optInt("id", sid);
        currentPerson = "";
        tvTitle.setText(s.optString("start", "") + "–" + s.optString("end", ""));
        tvMeta.setText(s.optString("title", "对话"));
        content.removeAllViews();
        scroll.scrollTo(0, 0);

        LinearLayout head = card();
        TextView topic = text(s.optString("topic", "对话"), 11, getColor(R.color.brand), true);
        head.addView(topic);
        TextView title = text(s.optString("title", "对话"), 17, getColor(R.color.text_primary), true);
        title.setPadding(0, dp(4), 0, 0);
        head.addView(title);
        TextView meta = text(join(s.optJSONArray("people"), " / ") + "  ·  "
                + s.optInt("turns", 0) + " 轮", 11, getColor(R.color.text_secondary), false);
        meta.setPadding(0, dp(4), 0, 0);
        head.addView(meta);
        TextView sm = text(s.optString("summary", ""), 13, getColor(R.color.text_primary), false);
        sm.setPadding(0, dp(9), 0, 0);
        sm.setLineSpacing(dp(2), 1.0f);
        head.addView(sm);
        content.addView(head);

        addSection("对话", "每句旁边有主题；点“上下文”看前后接话。");
        int session = s.optInt("id", sid);
        int count = 0;
        for (int i = 0; i < utterances.length(); i++) {
            JSONObject u = utterances.optJSONObject(i);
            if (u != null && u.optInt("session", -1) == session) {
                addUtterance(u);
                count++;
            }
        }
        if (count == 0) addEmpty("这一段没有有效转写。");
    }

    // ---------------- 人物 ----------------

    private void openPerson(String name) {
        JSONObject p = findPerson(name);
        if (p == null) return;
        mode = MODE_PERSON;
        currentPerson = name;
        currentSession = -1;
        tvTitle.setText(displayName(name) + " 说过的话");
        tvMeta.setText(p.optString("status", "") + " · " + p.optString("turns", "0") + " 轮");
        content.removeAllViews();
        scroll.scrollTo(0, 0);

        LinearLayout head = card();
        head.addView(text(name, 21, getColor(R.color.text_primary), true));
        head.addView(text(p.optString("status", "") + " · " + p.optString("turns", "0") + " 轮 · "
                + p.optString("talk", "0") + " min", 11, getColor(R.color.text_secondary), false));
        TextView topics = text(p.optString("topics", ""), 12, getColor(R.color.brand), false);
        topics.setPadding(0, dp(8), 0, 0);
        head.addView(topics);
        content.addView(head);

        if (sessions == null) return;
        for (int i = 0; i < sessions.length(); i++) {
            JSONObject s = sessions.optJSONObject(i);
            if (s == null) continue;
            int sid = s.optInt("id", i);
            List<JSONObject> list = new ArrayList<>();
            for (int j = 0; j < utterances.length(); j++) {
                JSONObject u = utterances.optJSONObject(j);
                if (u != null && u.optInt("session", -1) == sid && name.equals(u.optString("person", ""))) {
                    list.add(u);
                }
            }
            if (list.isEmpty()) continue;
            addSessionHeader(s, list.size());
            for (JSONObject u : list) addUtterance(u);
        }
    }

    private void addSessionHeader(JSONObject s, int count) {
        LinearLayout line = new LinearLayout(this);
        line.setOrientation(LinearLayout.VERTICAL);
        line.setPadding(dp(4), dp(12), dp(4), dp(4));
        TextView time = text(s.optString("start", "") + "–" + s.optString("end", ""), 11, getColor(R.color.brand), true);
        line.addView(time);
        line.addView(text(s.optString("title", "对话") + "  ·  " + count + " 句", 13, getColor(R.color.text_primary), true));
        final int sid = s.optInt("id", -1);
        line.setOnClickListener(v -> openSession(sid));
        content.addView(line);
    }

    // ---------------- 公共组件 ----------------

    private void addSessionCard(JSONObject s, boolean compact) {
        LinearLayout card = card();
        LinearLayout head = new LinearLayout(this);
        head.setOrientation(LinearLayout.HORIZONTAL);
        head.setGravity(Gravity.CENTER_VERTICAL);
        TextView time = text(s.optString("start", "") + "–" + s.optString("end", ""), 11, getColor(R.color.brand), true);
        head.addView(time);
        TextView chip = text(s.optString("topic", "对话"), 10, getColor(R.color.text_secondary), false);
        chip.setBackground(round(0xFFEEF3EF, 99));
        chip.setPadding(dp(7), dp(1), dp(7), dp(1));
        chip.setSingleLine(true);
        chip.setEllipsize(TextUtils.TruncateAt.END);
        chip.setMaxWidth(dp(150));
        LinearLayout.LayoutParams clp = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        clp.leftMargin = dp(8);
        head.addView(chip, clp);
        card.addView(head);

        TextView title = text(s.optString("title", "对话"), 16, getColor(R.color.text_primary), true);
        title.setPadding(0, dp(5), 0, 0);
        card.addView(title);
        TextView meta = text(join(s.optJSONArray("people"), " / ") + "  ·  " + s.optInt("turns", 0) + " 轮", 11, getColor(R.color.text_secondary), false);
        meta.setPadding(0, dp(3), 0, 0);
        card.addView(meta);
        String summary = s.optString("summary", "");
        if (!summary.isEmpty()) {
            TextView sm = text(summary, 12, getColor(R.color.text_secondary), false);
            sm.setPadding(0, dp(6), 0, 0);
            sm.setMaxLines(2);
            card.addView(sm);
        }
        TextView open = text("打开会话 →", 11, getColor(R.color.brand), true);
        open.setPadding(0, dp(7), 0, 0);
        card.addView(open);
        final int sid = s.optInt("id", -1);
        card.setOnClickListener(v -> openSession(sid));
        content.addView(card);
    }

    private void addPersonRow(JSONObject p) {
        LinearLayout card = card();
        LinearLayout top = new LinearLayout(this);
        top.setOrientation(LinearLayout.HORIZONTAL);
        top.setGravity(Gravity.CENTER_VERTICAL);
        TextView avatar = text(p.optString("name", "?"), 17, Color.WHITE, true);
        avatar.setGravity(Gravity.CENTER);
        avatar.setBackground(round(personColor(p.optString("name")), 99));
        LinearLayout.LayoutParams alp = new LinearLayout.LayoutParams(dp(38), dp(38));
        alp.rightMargin = dp(10);
        top.addView(avatar, alp);
        LinearLayout names = new LinearLayout(this);
        names.setOrientation(LinearLayout.VERTICAL);
        names.addView(text(p.optString("name", "?") + "  ·  " + p.optString("status", ""), 15, getColor(R.color.text_primary), true));
        names.addView(text(p.optString("turns", "0") + " 轮 · " + p.optString("talk", "0") + " min", 11, getColor(R.color.text_secondary), false));
        top.addView(names);
        card.addView(top);
        TextView topics = text(p.optString("topics", ""), 11, getColor(R.color.brand), false);
        topics.setPadding(0, dp(7), 0, 0);
        card.addView(topics);
        addRoleLine(card, p);
        addSampleControls(card, p);
        TextView open = text("只看 " + displayName(p.optString("name", "TA")) + " 说过的话 →", 11, getColor(R.color.brand), true);
        open.setPadding(0, dp(6), 0, 0);
        card.addView(open);
        card.setOnClickListener(v -> openPerson(p.optString("name")));
        content.addView(card);
    }

    private void addUtterance(JSONObject u) {
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.VERTICAL);
        row.setPadding(0, dp(9), 0, dp(8));

        LinearLayout head = new LinearLayout(this);
        head.setOrientation(LinearLayout.HORIZONTAL);
        head.setGravity(Gravity.CENTER_VERTICAL);
        String person = u.optString("person", "?");
        TextView who = text(person, 10, Color.WHITE, true);
        who.setGravity(Gravity.CENTER);
        who.setPadding(dp(6), dp(1), dp(6), dp(1));
        who.setBackground(round(personColor(person), 5));
        who.setOnClickListener(v -> openPerson(person));
        head.addView(who);

        TextView topic = text(u.optString("topic", ""), 10, getColor(R.color.text_secondary), false);
        topic.setBackground(round(0xFFEEF3EF, 99));
        topic.setPadding(dp(7), dp(1), dp(7), dp(1));
        topic.setSingleLine(true);
        topic.setEllipsize(TextUtils.TruncateAt.END);
        topic.setMaxWidth(dp(126));
        LinearLayout.LayoutParams tlp = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        tlp.leftMargin = dp(6);
        head.addView(topic, tlp);

        TextView time = text(u.optString("hm", ""), 10, getColor(R.color.text_tertiary), false);
        LinearLayout.LayoutParams mlp = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        mlp.leftMargin = dp(7);
        head.addView(time, mlp);

        TextView ctx = text("上下文", 10, getColor(R.color.brand), false);
        ctx.setGravity(Gravity.CENTER);
        ctx.setBackground(round(0xFFF0F0EA, 6));
        ctx.setPadding(dp(7), dp(2), dp(7), dp(2));
        ctx.setClickable(true);
        LinearLayout.LayoutParams clp = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        clp.leftMargin = dp(6);
        head.addView(ctx, clp);
        row.addView(head);

        TextView body = text(u.optString("text", ""), 14, getColor(R.color.text_primary), false);
        body.setPadding(0, dp(4), 0, 0);
        body.setLineSpacing(dp(2), 1.0f);
        row.addView(body);
        ctx.setOnClickListener(v -> showContext(u));
        content.addView(row);
    }

    private void showContext(JSONObject target) {
        if (utterances == null) return;
        int id = target.optInt("id", -1);
        int session = target.optInt("session", -1);
        if (id < 0) return;

        Dialog dialog = new Dialog(this);
        dialog.requestWindowFeature(Window.FEATURE_NO_TITLE);
        LinearLayout box = new LinearLayout(this);
        box.setOrientation(LinearLayout.VERTICAL);
        box.setPadding(dp(18), dp(14), dp(18), dp(12));
        box.setBackgroundResource(R.drawable.bg_sheet);

        LinearLayout titleRow = new LinearLayout(this);
        titleRow.setOrientation(LinearLayout.HORIZONTAL);
        titleRow.setGravity(Gravity.CENTER_VERTICAL);
        LinearLayout titles = new LinearLayout(this);
        titles.setOrientation(LinearLayout.VERTICAL);
        titles.addView(text("对话上下文", 17, getColor(R.color.text_primary), true));
        titles.addView(text(target.optString("topic", "") + "  ·  " + target.optString("time", ""), 10, getColor(R.color.text_secondary), false));
        titleRow.addView(titles, new LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1));
        TextView close = text("关闭", 12, getColor(R.color.brand), true);
        close.setPadding(dp(8), dp(6), 0, dp(6));
        close.setOnClickListener(v -> dialog.dismiss());
        titleRow.addView(close);
        box.addView(titleRow);

        ScrollView sv = new ScrollView(this);
        LinearLayout thread = new LinearLayout(this);
        thread.setOrientation(LinearLayout.VERTICAL);
        int from = Math.max(0, id - 2);
        int to = Math.min(utterances.length(), id + 3);
        for (int i = from; i < to; i++) {
            JSONObject u = utterances.optJSONObject(i);
            if (u == null || u.optInt("session", -1) != session) continue;
            LinearLayout line = new LinearLayout(this);
            line.setOrientation(LinearLayout.HORIZONTAL);
            line.setPadding(dp(6), dp(5), dp(6), dp(5));
            if (i == id) line.setBackground(round(0xFFFFEAB8, 6));
            TextView tm = text(u.optString("hm"), 10, getColor(R.color.text_tertiary), false);
            tm.setWidth(dp(54));
            line.addView(tm);
            TextView who = text(u.optString("person"), 10, Color.WHITE, true);
            who.setGravity(Gravity.CENTER);
            who.setPadding(dp(5), dp(1), dp(5), dp(1));
            who.setBackground(round(personColor(u.optString("person")), 4));
            LinearLayout.LayoutParams wlp = new LinearLayout.LayoutParams(
                    ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT);
            wlp.rightMargin = dp(6);
            line.addView(who, wlp);
            TextView tx = text(u.optString("text"), 12, getColor(R.color.text_primary), false);
            tx.setLineSpacing(dp(1), 1.0f);
            line.addView(tx, new LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1));
            thread.addView(line);
        }
        sv.addView(thread);
        box.addView(sv, new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT));
        dialog.setContentView(box);
        Window w = dialog.getWindow();
        if (w != null) {
            w.setBackgroundDrawable(new ColorDrawable(Color.TRANSPARENT));
            w.setLayout(WindowManager.LayoutParams.MATCH_PARENT, WindowManager.LayoutParams.WRAP_CONTENT);
            w.setGravity(Gravity.BOTTOM);
        }
        dialog.show();
    }

    private void addRoleLine(LinearLayout parent, JSONObject p) {
        String role = p.optString("role_guess", "").trim();
        if (role.isEmpty()) return;
        String conf = p.optString("role_confidence", "");
        TextView t = text("推测身份：" + role + (conf.isEmpty() ? "" : " · " + conf), 11, getColor(R.color.warning), false);
        t.setPadding(0, dp(6), 0, 0);
        parent.addView(t);
        String ev = p.optString("role_evidence", "");
        if (!ev.isEmpty()) {
            TextView e = text(ev, 10, getColor(R.color.text_tertiary), false);
            e.setPadding(0, dp(2), 0, 0);
            parent.addView(e);
        }
    }

    private void addSampleControls(LinearLayout parent, JSONObject p) {
        JSONObject sample = p.optJSONObject("sample");
        if (sample == null) return;
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        row.setGravity(Gravity.CENTER_VERTICAL);
        row.setPadding(0, dp(8), 0, 0);

        TextView play = text("▶ 听一段", 11, getColor(R.color.brand), true);
        play.setPadding(dp(9), dp(4), dp(9), dp(4));
        play.setBackground(round(0xFFE8F0FE, 7));
        play.setClickable(true);
        play.setOnClickListener(v -> playSample(sample, play));
        row.addView(play);

        TextView mark = text("标记姓名", 11, getColor(R.color.text_secondary), false);
        mark.setPadding(dp(9), dp(4), dp(9), dp(4));
        mark.setBackground(round(0xFFF0F0EA, 7));
        mark.setClickable(true);
        LinearLayout.LayoutParams mlp = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        mlp.leftMargin = dp(7);
        row.addView(mark, mlp);
        String alias = alias(p.optString("name", ""));
        mark.setText(alias.isEmpty() ? "标记姓名" : "已标记：" + alias + "（本地）");
        mark.setOnClickListener(v -> Sheet.input(this,
                "标记这个人", "先听一段声音，再输入你确认的姓名/称呼。当前只保存本地标记，不改声纹 ID。",
                alias, "例如：张三 / 技术同事", "保存", value -> {
                    String n = value == null ? "" : value.trim();
                    if (n.isEmpty()) return;
                    saveAlias(p.optString("name", ""), n);
                    mark.setText("已标记：" + n + "（本地）");
                    if (mode == MODE_PERSON) openPerson(p.optString("name", ""));
                    else renderHome();
                }));
        parent.addView(row);
    }

    private void playSample(JSONObject sample, TextView label) {
        String job = sample.optString("job", "");
        if (job.isEmpty()) {
            label.setText("没有试听片段");
            return;
        }
        stopPlayer();
        String base = Settings.getPcUrl(this);
        String token = Settings.getString(this, Settings.KEY_TOKEN, "");
        if (base == null || base.isEmpty() || token == null || token.isEmpty()) {
            label.setText("先连接电脑");
            return;
        }
        double start = sample.optDouble("start", 0);
        double end = sample.optDouble("end", start + 6);
        String url = base + "/api/v1/jobs/" + Uri.encode(job)
                + "/clip?start=" + start + "&end=" + end;
        try {
            MediaPlayer mp = new MediaPlayer();
            Map<String, String> headers = new HashMap<>();
            headers.put("Authorization", "Bearer " + token);
            mp.setDataSource(this, Uri.parse(url), headers);
            player = mp;
            label.setText("正在加载…");
            mp.setOnPreparedListener(x -> {
                x.start();
                label.setText("■ 停止");
            });
            mp.setOnCompletionListener(x -> {
                label.setText("▶ 听一段");
                stopPlayer();
            });
            mp.setOnErrorListener((x, what, extra) -> {
                label.setText("播放失败");
                stopPlayer();
                return true;
            });
            mp.prepareAsync();
        } catch (Exception e) {
            label.setText("播放失败");
            stopPlayer();
        }
    }

    private void stopPlayer() {
        MediaPlayer p = player;
        player = null;
        if (p != null) {
            try { p.stop(); } catch (Exception ignored) {}
            try { p.release(); } catch (Exception ignored) {}
        }
    }

    private String alias(String name) {
        return getSharedPreferences(Settings.PREFS, MODE_PRIVATE)
                .getString("person_alias_" + name, "");
    }

    private void saveAlias(String name, String value) {
        getSharedPreferences(Settings.PREFS, MODE_PRIVATE).edit()
                .putString("person_alias_" + name, value).apply();
    }

    private String displayName(String name) {
        String a = alias(name);
        return a.isEmpty() ? name : name + " / " + a;
    }

    // ---------------- 数据辅助 ----------------

    private int sessionCount() {
        if (sessions == null) return 0;
        int n = 0;
        for (int i = 0; i < sessions.length(); i++) {
            JSONObject s = sessions.optJSONObject(i);
            if (s != null && s.optInt("id", i) < 3) n++;
        }
        return n;
    }

    private int peopleCount() {
        return people == null ? 0 : people.length();
    }

    private JSONObject findSession(int sid) {
        if (sessions == null) return null;
        for (int i = 0; i < sessions.length(); i++) {
            JSONObject s = sessions.optJSONObject(i);
            if (s != null && s.optInt("id", i) == sid) return s;
        }
        return null;
    }

    private JSONObject findPerson(String name) {
        if (people == null) return null;
        for (int i = 0; i < people.length(); i++) {
            JSONObject p = people.optJSONObject(i);
            if (p != null && name.equals(p.optString("name"))) return p;
        }
        return null;
    }

    private String join(JSONArray a, String sep) {
        if (a == null) return "";
        List<String> out = new ArrayList<>();
        for (int i = 0; i < a.length(); i++) {
            String name = a.optString(i);
            if (!name.isEmpty()) out.add(name);
        }
        out.sort((x, y) -> {
            int ox = personOrder(x), oy = personOrder(y);
            if (ox != oy) return Integer.compare(ox, oy);
            return x.compareTo(y);
        });
        return TextUtils.join(sep, out);
    }

    private int personOrder(String n) {
        if ("A".equals(n)) return 0;
        if ("B".equals(n)) return 1;
        if ("C".equals(n)) return 2;
        return 9;
    }

    private void addSection(String title, String sub) {
        TextView t = text(title, 17, getColor(R.color.text_primary), true);
        t.setPadding(0, dp(18), 0, dp(2));
        content.addView(t);
        if (sub != null && !sub.isEmpty()) {
            TextView s = text(sub, 11, getColor(R.color.text_secondary), false);
            s.setPadding(0, 0, 0, dp(7));
            content.addView(s);
        }
    }

    private void addEmpty(String text) {
        TextView t = text(text, 13, getColor(R.color.text_secondary), false);
        t.setGravity(Gravity.CENTER);
        t.setPadding(dp(12), dp(20), dp(12), dp(20));
        content.addView(t);
    }

    private LinearLayout card() {
        LinearLayout c = new LinearLayout(this);
        c.setOrientation(LinearLayout.VERTICAL);
        c.setPadding(dp(14), dp(12), dp(14), dp(12));
        c.setBackgroundResource(R.drawable.bg_card);
        c.setElevation(dp(1));
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        lp.bottomMargin = dp(9);
        c.setLayoutParams(lp);
        return c;
    }

    private TextView text(String s, int sp, int color, boolean bold) {
        TextView t = new TextView(this);
        t.setText(s == null ? "" : s);
        t.setTextSize(sp);
        t.setTextColor(color);
        t.setIncludeFontPadding(false);
        if (bold) t.setTypeface(Typeface.DEFAULT, Typeface.BOLD);
        return t;
    }

    private GradientDrawable round(int color, int radiusDp) {
        GradientDrawable d = new GradientDrawable();
        d.setColor(color);
        d.setCornerRadius(dp(radiusDp));
        return d;
    }

    private int personColor(String p) {
        if ("B".equals(p)) return 0xFF456B9C;
        if ("C".equals(p)) return 0xFFA45D42;
        return 0xFF2F6F5E;
    }

    private String fmt(double v) {
        if (Math.abs(v - Math.rint(v)) < 0.05) return String.valueOf((long) Math.rint(v));
        return String.format(java.util.Locale.ROOT, "%.1f", v);
    }

    private int dp(int v) {
        return Math.round(v * getResources().getDisplayMetrics().density);
    }
}
