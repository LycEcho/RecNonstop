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
import android.text.method.LinkMovementMethod;
import android.view.Gravity;
import android.view.View;
import android.view.ViewGroup;
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
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

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
    private static final ExecutorService LOAD_EXEC = Executors.newSingleThreadExecutor();

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
    private int homeScrollY;
    private MediaPlayer player;
    private TextView playingLabel;
    private TextView refreshButton;
    private String reportId;
    private boolean loading;
    private final Map<Integer, View> utteranceRows = new HashMap<>();
    private int highlightedUtterance = -1;
    private boolean sourcesExpanded;

    public static Intent intent(Activity from, String reportId) {
        return new Intent(from, ReportDetailActivity.class).putExtra(EXTRA_REPORT_ID, reportId);
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_report_detail);

        reportId = getIntent().getStringExtra(EXTRA_REPORT_ID);
        content = findViewById(R.id.detailContent);
        scroll = findViewById(R.id.detailScroll);
        tvTitle = findViewById(R.id.tvDetailTitle);
        tvMeta = findViewById(R.id.tvDetailMeta);
        findViewById(R.id.detailTabs).setVisibility(View.GONE);
        findViewById(R.id.btnDetailBack).setOnClickListener(v -> goBack());
        refreshButton = findViewById(R.id.btnDetailRefresh);
        refreshButton.setOnClickListener(v -> syncReport());
        int savedScroll = 0;
        if (savedInstanceState != null) {
            mode = savedInstanceState.getInt("mode", MODE_HOME);
            currentSession = savedInstanceState.getInt("session", -1);
            highlightedUtterance = savedInstanceState.getInt("utterance", -1);
            sourcesExpanded = savedInstanceState.getBoolean("sources_expanded", false);
            currentPerson = savedInstanceState.getString("person", "");
            homeScrollY = savedInstanceState.getInt("home_scroll", 0);
            savedScroll = savedInstanceState.getInt("scroll", 0);
        }
        tvTitle.setText("录音日报");
        tvMeta.setText("正在读取…");
        loadReport(savedScroll);
    }

    private void loadReport(int scrollY) {
        int previousMode = mode;
        int previousSession = currentSession;
        int previousUtterance = highlightedUtterance;
        String previousPerson = currentPerson;
        int previousHomeScroll = homeScrollY;
        loading = true;
        refreshButton.setEnabled(false);
        LOAD_EXEC.execute(() -> {
            JSONObject cached = DailyReports.get(getApplicationContext(), reportId);
            runOnUiThread(() -> {
                if (isFinishing() || isDestroyed()) return;
                loading = false;
                refreshButton.setEnabled(true);
                refreshButton.setText("同步");
                report = cached;
                detail = report == null ? null : report.optJSONObject("detail");
                stats = detail == null ? null : detail.optJSONObject("stats");
                sessions = detail == null ? null : detail.optJSONArray("sessions");
                people = detail == null ? null : detail.optJSONArray("people");
                utterances = detail == null ? null : detail.optJSONArray("utterances");
                highlights = detail == null ? null : detail.optJSONArray("highlights");
                actions = detail == null ? null : detail.optJSONArray("actions");
                renderHome();
                if (utterances != null && previousMode == MODE_SESSION) {
                    openSession(previousSession);
                    highlightUtterance(previousUtterance, false);
                }
                if (utterances != null && previousMode == MODE_PERSON) openPerson(previousPerson);
                homeScrollY = previousHomeScroll;
                scroll.post(() -> scroll.scrollTo(0, scrollY));
            });
        });
    }

    private void syncReport() {
        if (loading) return;
        if (!Settings.connected(this)) {
            Toast.makeText(this, "尚未连接电脑，已保存的日报仍可离线阅读", Toast.LENGTH_LONG).show();
            return;
        }
        loading = true;
        refreshButton.setEnabled(false);
        refreshButton.setText("同步中…");
        DailyReports.syncHistory(this, (ok, message) -> {
            if (isFinishing() || isDestroyed()) return;
            if (!ok) Toast.makeText(this, "同步失败，保留本地日报；稍后可重试", Toast.LENGTH_LONG).show();
            loadReport(scroll.getScrollY());
        });
    }

    @Override
    protected void onSaveInstanceState(Bundle state) {
        super.onSaveInstanceState(state);
        state.putInt("mode", mode);
        state.putInt("session", currentSession);
        state.putInt("utterance", highlightedUtterance);
        state.putBoolean("sources_expanded", sourcesExpanded);
        state.putString("person", currentPerson);
        state.putInt("home_scroll", homeScrollY);
        state.putInt("scroll", scroll.getScrollY());
    }

    @Override
    protected void onStop() {
        stopPlayer();
        super.onStop();
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
        if (loading && report == null) {
            finish();
            return;
        }
        if (mode != MODE_HOME) {
            renderHome();
            scroll.post(() -> {
                if (mode == MODE_HOME) scroll.scrollTo(0, homeScrollY);
            });
        } else {
            finish();
        }
    }

    // ---------------- 首页 ----------------

    private void renderHome() {
        stopPlayer();
        mode = MODE_HOME;
        highlightedUtterance = -1;
        utteranceRows.clear();
        currentSession = -1;
        currentPerson = "";
        String title = report == null ? "对话日报" : report.optString("title", "对话日报");
        String date = report == null ? "" : report.optString("date", "");
        tvTitle.setText("录音日报");
        tvMeta.setText((date.isEmpty() ? "" : date + " · ") + "已保存在手机，可离线阅读");
        content.removeAllViews();
        scroll.scrollTo(0, 0);

        if (report == null) {
            tvMeta.setText("这份日报尚未保存在手机");
            refreshButton.setText("重试");
            addEmpty("连接电脑后，点右上角「重试」下载日报。已有日报可在「日报」页离线查看。");
            return;
        }
        DailyReports.markRead(this, reportId);
        if (DailyReports.summaryIncomplete(report)) {
            tvMeta.setText((date.isEmpty() ? "" : date + " · ") + "汇总未完成 · 分段材料已保存在手机");
            addEmpty("本次汇总未完成，可先查看已保留的分段材料；有原始转写时可回看原文。右上角「同步」只下载电脑端已有日报，不会重新生成。");
        }
        if (detail == null || utterances == null) {
            addSection(title, report.optInt("job_count", 0) + " 段录音 · "
                    + Math.round(report.optDouble("duration_seconds", 0) / 60) + " 分钟");
            addReportBody();
            return;
        }

        addHero(title);
        addKpiRow();
        addReportBody();
        addSourceList();
        addHighlights();
        addActions();
        addPending();
        addTimeline();
        addPeople();
        addFootNote();
    }

    private void addReportBody() {
        String body = report.optString("content", "").trim();
        if (body.isEmpty()) body = report.optString("summary", "").trim();
        if (body.isEmpty()) {
            if (detail == null || utterances == null) addEmpty("这份日报还没有正文，稍后同步重试。");
            return;
        }
        LinearLayout card = card();
        card.setPadding(dp(22), dp(22), dp(22), dp(22));
        TextView text = text("", 16, getColor(R.color.text_primary), false);
        text.setText(ReportMarkdown.render(this, body));
        text.setLineSpacing(dp(7), 1.05f);
        text.setTextIsSelectable(true);
        text.setBreakStrategy(android.text.Layout.BREAK_STRATEGY_HIGH_QUALITY);
        card.addView(text);
        content.addView(card);
    }

    /** Only labels carried by structured detail are links; body dates stay plain text. */
    private void addSourceList() {
        if (utterances == null) return;
        Map<String, Integer> uniqueLabels = new HashMap<>();
        for (int i = 0; i < utterances.length(); i++) {
            JSONObject u = utterances.optJSONObject(i);
            if (u == null || !u.has("recording_index") || !u.has("segment_index")) continue;
            String label = u.optString("source_label", "");
            int id = u.optInt("id", -1);
            if (label.isEmpty() || id < 0) continue;
            uniqueLabels.put(label, uniqueLabels.containsKey(label) ? -1 : id);
        }
        if (uniqueLabels.isEmpty()) return; // Old reports retain their existing timeline.
        LinearLayout sourceCard = card();
        TextView toggle = text("原始录音来源 · 展开", 15, getColor(R.color.brand), true);
        toggle.setMinHeight(dp(48));
        toggle.setGravity(Gravity.CENTER_VERTICAL);
        toggle.setFocusable(true);
        sourceCard.addView(toggle);
        LinearLayout sourceRows = new LinearLayout(this);
        sourceRows.setOrientation(LinearLayout.VERTICAL);
        sourceRows.setVisibility(View.GONE);
        sourceCard.addView(sourceRows);
        toggle.setOnClickListener(v -> {
            boolean opening = sourceRows.getVisibility() != View.VISIBLE;
            if (opening && sourceRows.getChildCount() == 0) {
                sourceRows.addView(text("点来源定位原文。相同标签无法唯一定位时，仅打开所在对话范围。", 12, getColor(R.color.text_secondary), false));
                for (int i = 0; i < utterances.length(); i++) {
                    JSONObject u = utterances.optJSONObject(i);
                    if (u == null || !u.has("recording_index") || !u.has("segment_index")) continue;
                    String label = u.optString("source_label", "");
                    Integer target = uniqueLabels.get(label);
                    if (target == null) continue;
                    TextView source = text("", 13, getColor(R.color.brand), false);
                    source.setMinHeight(dp(48));
                    source.setPadding(0, dp(10), 0, dp(10));
                    source.setLineSpacing(dp(3), 1f);
                    final int id = target;
                    final int sid = u.optInt("session", -1);
                    String display = "录音 " + u.optInt("recording_index") + " · 原文 "
                            + (u.optInt("segment_index") + 1) + "\n" + label;
                    if (id < 0) display += " · 查看对话范围";
                    source.setText(ReportMarkdown.sourceLink(this, display, () -> {
                        if (id >= 0) openUtterance(id);
                        else openSession(sid);
                    }));
                    source.setMovementMethod(LinkMovementMethod.getInstance());
                    source.setFocusable(true);
                    source.setContentDescription(display + (id >= 0 ? "，定位原文" : ""));
                    sourceRows.addView(source);
                }
            }
            sourcesExpanded = opening;
            sourceRows.setVisibility(opening ? View.VISIBLE : View.GONE);
            toggle.setText(opening ? "原始录音来源 · 收起" : "原始录音来源 · 展开");
        });
        content.addView(sourceCard);
        if (sourcesExpanded) toggle.performClick();
    }

    private JSONObject findUtterance(int id) {
        if (id < 0 || utterances == null) return null;
        for (int i = 0; i < utterances.length(); i++) {
            JSONObject u = utterances.optJSONObject(i);
            if (u != null && u.optInt("id", -1) == id) return u;
        }
        return null;
    }

    private void openUtterance(int id) {
        JSONObject target = findUtterance(id);
        if (target == null) return;
        openSession(target.optInt("session", -1));
        highlightUtterance(id, true);
    }

    private void highlightUtterance(int id, boolean move) {
        View row = utteranceRows.get(id);
        if (mode != MODE_SESSION || row == null) return;
        highlightedUtterance = id;
        row.setBackground(round(getColor(R.color.surface_tint), 12));
        if (move) scroll.post(() -> {
            if (isFinishing() || isDestroyed() || mode != MODE_SESSION
                    || highlightedUtterance != id || utteranceRows.get(id) != row) return;
            scroll.smoothScrollTo(0, Math.max(0, row.getTop() - dp(12)));
            row.announceForAccessibility("已定位来源原文");
        });
    }

    private void addHero(String title) {
        LinearLayout hero = new LinearLayout(this);
        hero.setOrientation(LinearLayout.VERTICAL);
        hero.setPadding(dp(2), 0, dp(2), dp(24));
        content.addView(hero);

        TextView kicker = text(report == null ? "" : report.optString("date", ""), 12, getColor(R.color.brand), true);
        kicker.setLetterSpacing(0.08f);
        hero.addView(kicker);
        TextView heading = text(title, 27, getColor(R.color.text_primary), true);
        heading.setPadding(0, dp(12), 0, 0);
        heading.setAccessibilityHeading(true);
        heading.setLineSpacing(dp(3), 1.0f);
        hero.addView(heading);
        String overview = detail.optString("overview", "");
        if (overview.isEmpty()) overview = report == null ? "" : report.optString("summary", "");
        TextView sub = text(overview, 15, getColor(R.color.text_secondary), false);
        sub.setMaxLines(3);
        sub.setEllipsize(android.text.TextUtils.TruncateAt.END);
        sub.setPadding(0, dp(12), 0, 0);
        sub.setLineSpacing(dp(5), 1.05f);
        hero.addView(sub);
    }

    private void addKpiRow() {
        LinearLayout row = new LinearLayout(this);
        row.setOrientation(LinearLayout.HORIZONTAL);
        row.setPadding(dp(10), dp(14), dp(10), dp(14));
        row.setBackgroundResource(R.drawable.bg_summary);
        addKpi(row, String.valueOf(sessionCount()), "段对话");
        addKpi(row, fmt(stats == null ? 0 : stats.optDouble("talk_minutes", 0)), "发言分钟");
        addKpi(row, String.valueOf(peopleCount()), "说话人标签");
        LinearLayout.LayoutParams rowParams = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        rowParams.bottomMargin = dp(24);
        content.addView(row, rowParams);
    }

    private void addKpi(LinearLayout row, String value, String label) {
        LinearLayout box = new LinearLayout(this);
        box.setOrientation(LinearLayout.VERTICAL);
        box.setPadding(dp(8), dp(4), dp(8), dp(4));
        box.addView(text(value, 23, getColor(R.color.text_primary), true));
        TextView caption = text(label, 12, getColor(R.color.text_secondary), false);
        caption.setPadding(0, dp(7), 0, 0);
        box.addView(caption);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1);
        row.addView(box, lp);
    }

    private void addHighlights() {
        if (highlights == null || highlights.length() == 0) return;
        addSection("重点回顾", "点开查看原始对话");

        for (int i = 0; i < highlights.length(); i++) {
            JSONObject h = highlights.optJSONObject(i);
            if (h == null) continue;
            LinearLayout card = card();
            TextView time = text(h.optString("time", ""), 12, getColor(R.color.brand), true);
            card.addView(time);
            TextView t = text(h.optString("title", ""), 17, getColor(R.color.text_primary), true);
            t.setPadding(0, dp(9), 0, 0);
            card.addView(t);
            TextView d = text(h.optString("detail", ""), 14, getColor(R.color.text_secondary), false);
            d.setPadding(0, dp(9), 0, 0);
            d.setLineSpacing(dp(4), 1.0f);
            card.addView(d);
            TextView open = text("查看对话 →", 13, getColor(R.color.brand), true);
            open.setPadding(0, dp(14), 0, 0);
            card.addView(open);
            final int sid = h.optInt("session", -1);
            card.setOnClickListener(v -> openSession(sid));
            card.setFocusable(true);
            content.addView(card);
        }
    }

    private void addTimeline() {
        addSection("对话时间线", "按发生顺序，回到当时的上下文");
        if (sessions == null) return;
        for (int i = 0; i < sessions.length(); i++) {
            JSONObject s = sessions.optJSONObject(i);
            if (s == null) continue;
            addSessionCard(s, false);
        }
    }

    private void addPeople() {
        if (people == null || people.length() == 0) return;
        addSection("说话人", "点标签查看相关发言。未确认的编号仅属于当前片段，不代表跨段是同一个人。");
        for (int i = 0; i < people.length(); i++) {
            JSONObject p = people.optJSONObject(i);
            if (p != null) addPersonRow(p);
        }
    }

    private void addActions() {
        if (actions == null || actions.length() == 0) return;
        addSection("分段待办候选", "来自分段自动分析，需结合原文确认；点开回看录音起点，不代表任务原句。");
        LinearLayout card = card();
        for (int i = 0; i < actions.length(); i++) {
            JSONObject a = actions.optJSONObject(i);
            if (a == null) continue;
            LinearLayout row = new LinearLayout(this);
            row.setOrientation(LinearLayout.HORIZONTAL);
            row.setPadding(0, dp(8), 0, dp(8));
            row.setMinimumHeight(dp(48));
            TextView dot = text("□", 18, getColor(R.color.brand), false);
            dot.setPadding(0, dp(3), dp(8), 0);
            row.addView(dot);
            LinearLayout body = new LinearLayout(this);
            body.setOrientation(LinearLayout.VERTICAL);
            body.addView(text(a.optString("text", ""), 15, getColor(R.color.text_primary), false));
            body.addView(text(a.optString("source", ""), 10, getColor(R.color.text_tertiary), false));
            row.addView(body, new LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1));
            final int sid = a.optInt("session", -1);
            final int firstUtterance = a.optInt("first_utterance_id", -1);
            row.setFocusable(true);
            row.setContentDescription(a.optString("text", "") + "，回看录音起点");
            row.setOnClickListener(v -> {
                if (findUtterance(firstUtterance) != null) openUtterance(firstUtterance);
                else openSession(sid);
            });
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
        card.setPadding(dp(18), dp(16), dp(18), dp(16));
        card.setBackgroundResource(R.drawable.bg_warn_card);
        card.addView(text("需要核实的内容", 15, getColor(R.color.warning), true));
        TextView t = text(pending, 11, getColor(R.color.text_secondary), false);
        t.setPadding(0, dp(3), 0, 0);
        card.addView(t);
        content.addView(card);
    }

    private void addFootNote() {
        TextView t = text("来源于已完成转写的录音，可从时间线查看原文。",
                10, getColor(R.color.text_tertiary), false);
        t.setPadding(dp(2), dp(16), dp(2), dp(4));
        content.addView(t);
    }

    // ---------------- 会话 ----------------

    private void openSession(int sid) {
        JSONObject s = findSession(sid);
        if (s == null) return;
        if (mode == MODE_HOME) homeScrollY = scroll.getScrollY();
        stopPlayer();
        mode = MODE_SESSION;
        currentSession = s.optInt("id", sid);
        highlightedUtterance = -1;
        utteranceRows.clear();
        currentPerson = "";
        tvTitle.setText(s.optString("start", "") + "–" + s.optString("end", ""));
        tvMeta.setText(s.optString("title", "对话"));
        content.removeAllViews();
        scroll.scrollTo(0, 0);

        LinearLayout head = card();
        TextView topic = text(s.optString("topic", "对话"), 11, getColor(R.color.brand), true);
        head.addView(topic);
        TextView title = text(s.optString("title", "对话"), 22, getColor(R.color.text_primary), true);
        title.setPadding(0, dp(10), 0, 0);
        head.addView(title);
        TextView meta = text(join(s.optJSONArray("people"), " / ") + "  ·  "
                + s.optInt("turns", 0) + " 轮", 11, getColor(R.color.text_secondary), false);
        meta.setPadding(0, dp(4), 0, 0);
        head.addView(meta);
        TextView sm = text(s.optString("summary", ""), 15, getColor(R.color.text_primary), false);
        sm.setPadding(0, dp(16), 0, 0);
        sm.setLineSpacing(dp(5), 1.0f);
        head.addView(sm);
        content.addView(head);

        addSection("逐句记录", "点「上下文」查看前后的发言");
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
        if (mode == MODE_HOME) homeScrollY = scroll.getScrollY();
        stopPlayer();
        mode = MODE_PERSON;
        highlightedUtterance = -1;
        utteranceRows.clear();
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
        line.setFocusable(true);
        line.setMinimumHeight(dp(48));
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
        chip.setBackground(round(getColor(R.color.surface_tint), 8));
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
        card.setFocusable(true);
        content.addView(card);
    }

    private void addPersonRow(JSONObject p) {
        LinearLayout card = card();
        LinearLayout top = new LinearLayout(this);
        top.setOrientation(LinearLayout.HORIZONTAL);
        top.setGravity(Gravity.CENTER_VERTICAL);
        String name = p.optString("name", "").trim();
        boolean unconfirmed = name.isEmpty() || name.startsWith("未确认")
                || p.optString("status", "").contains("未确认");
        String initial = unconfirmed ? "？" : new String(Character.toChars(name.codePointAt(0)));
        TextView avatar = text(initial, 17, Color.WHITE, true);
        avatar.setGravity(Gravity.CENTER);
        avatar.setBackground(round(personColor(p.optString("name")), 14));
        avatar.setImportantForAccessibility(View.IMPORTANT_FOR_ACCESSIBILITY_NO);
        LinearLayout.LayoutParams alp = new LinearLayout.LayoutParams(dp(48), dp(48));
        alp.rightMargin = dp(10);
        top.addView(avatar, alp);
        LinearLayout names = new LinearLayout(this);
        names.setOrientation(LinearLayout.VERTICAL);
        TextView nameView = text(name + "  ·  " + p.optString("status", ""), 15, getColor(R.color.text_primary), true);
        nameView.setMaxLines(2);
        nameView.setEllipsize(TextUtils.TruncateAt.END);
        nameView.setContentDescription(name + "，" + p.optString("status", ""));
        names.addView(nameView);
        names.addView(text(p.optString("turns", "0") + " 轮 · " + p.optString("talk", "0") + " min", 11, getColor(R.color.text_secondary), false));
        top.addView(names, new LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1));
        card.addView(top);
        TextView topics = text(p.optString("topics", ""), 11, getColor(R.color.brand), false);
        topics.setPadding(0, dp(7), 0, 0);
        card.addView(topics);
        addRoleLine(card, p);
        addSampleControls(card, p);
        TextView open = text("查看相关发言 →", 11, getColor(R.color.brand), true);
        open.setPadding(0, dp(6), 0, 0);
        card.addView(open);
        card.setOnClickListener(v -> openPerson(p.optString("name")));
        card.setFocusable(true);
        card.setMinimumHeight(dp(48));
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
        who.setBackground(round(personColor(person), 10));
        who.setMaxLines(2);
        who.setEllipsize(TextUtils.TruncateAt.END);
        who.setMinHeight(dp(48));
        who.setFocusable(true);
        who.setContentDescription("查看" + person + "的发言");
        who.setOnClickListener(v -> openPerson(person));
        head.addView(who, new LinearLayout.LayoutParams(0, ViewGroup.LayoutParams.WRAP_CONTENT, 1));

        TextView topic = text(u.optString("topic", ""), 10, getColor(R.color.text_secondary), false);
        topic.setBackground(round(getColor(R.color.surface_tint), 8));
        topic.setPadding(dp(7), dp(1), dp(7), dp(1));
        topic.setSingleLine(true);
        topic.setEllipsize(TextUtils.TruncateAt.END);
        topic.setMaxWidth(dp(80));
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
        ctx.setBackground(round(getColor(R.color.neutral_light), 12));
        ctx.setPadding(dp(7), dp(2), dp(7), dp(2));
        ctx.setClickable(true);
        ctx.setFocusable(true);
        ctx.setMinHeight(dp(48));
        ctx.setMinWidth(dp(48));
        LinearLayout.LayoutParams clp = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        clp.leftMargin = dp(6);
        head.addView(ctx, clp);
        row.addView(head);

        TextView body = text(u.optString("text", ""), 16, getColor(R.color.text_primary), false);
        body.setPadding(0, dp(10), 0, dp(8));
        body.setLineSpacing(dp(5), 1.05f);
        body.setTextIsSelectable(true);
        row.addView(body);
        ctx.setOnClickListener(v -> showContext(u));
        content.addView(row);
        int utteranceId = u.optInt("id", -1);
        if (utteranceId >= 0) utteranceRows.put(utteranceId, row);
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
        close.setMinHeight(dp(48));
        close.setMinWidth(dp(64));
        close.setGravity(Gravity.CENTER);
        close.setFocusable(true);
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
            if (i == id) line.setBackground(round(getColor(R.color.accent), 10));
            TextView tm = text(u.optString("hm"), 10, getColor(R.color.text_tertiary), false);
            tm.setWidth(dp(54));
            line.addView(tm);
            TextView who = text(u.optString("person"), 10, Color.WHITE, true);
            who.setGravity(Gravity.CENTER);
            who.setPadding(dp(5), dp(1), dp(5), dp(1));
            who.setBackground(round(personColor(u.optString("person")), 8));
            who.setMaxLines(2);
            who.setMaxWidth(dp(96));
            who.setEllipsize(TextUtils.TruncateAt.END);
            who.setContentDescription(u.optString("person"));
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
        play.setBackground(round(getColor(R.color.surface_tint), 12));
        play.setClickable(true);
        play.setFocusable(true);
        play.setMinHeight(dp(48));
        play.setGravity(Gravity.CENTER);
        play.setOnClickListener(v -> playSample(sample, play));
        row.addView(play);

        TextView mark = text("标记姓名", 11, getColor(R.color.text_secondary), false);
        mark.setPadding(dp(9), dp(4), dp(9), dp(4));
        mark.setBackground(round(getColor(R.color.neutral_light), 12));
        mark.setClickable(true);
        mark.setFocusable(true);
        mark.setMinHeight(dp(48));
        mark.setGravity(Gravity.CENTER);
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
        if (playingLabel == label && player != null) {
            stopPlayer();
            return;
        }
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
            player = mp;
            playingLabel = label;
            Map<String, String> headers = new HashMap<>();
            headers.put("Authorization", "Bearer " + token);
            mp.setDataSource(this, Uri.parse(url), headers);
            label.setText("正在加载…");
            mp.setOnPreparedListener(x -> {
                if (player != x || isFinishing() || isDestroyed()) return;
                x.start();
                label.setText("■ 停止");
            });
            mp.setOnCompletionListener(x -> {
                if (player == x) stopPlayer();
            });
            mp.setOnErrorListener((x, what, extra) -> {
                if (player == x) {
                    stopPlayer();
                    label.setText("重试播放");
                    Toast.makeText(this, "试听需要连接电脑，请检查连接后重试", Toast.LENGTH_LONG).show();
                }
                return true;
            });
            mp.prepareAsync();
        } catch (Exception e) {
            stopPlayer();
            label.setText("重试播放");
        }
    }

    private void stopPlayer() {
        MediaPlayer p = player;
        player = null;
        if (playingLabel != null) playingLabel.setText("▶ 听一段");
        playingLabel = null;
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
            if (s != null) n++;
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
        TextView t = text(title, 20, getColor(R.color.text_primary), true);
        t.setAccessibilityHeading(true);
        t.setPadding(0, content.getChildCount() == 0 ? 0 : dp(28), 0, dp(8));
        content.addView(t);
        if (sub != null && !sub.isEmpty()) {
            TextView s = text(sub, 13, getColor(R.color.text_secondary), false);
            s.setPadding(0, 0, 0, dp(16));
            s.setLineSpacing(dp(3), 1.0f);
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
        c.setPadding(dp(18), dp(18), dp(18), dp(18));
        c.setBackgroundResource(R.drawable.bg_card);
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, ViewGroup.LayoutParams.WRAP_CONTENT);
        lp.bottomMargin = dp(12);
        c.setLayoutParams(lp);
        return c;
    }

    private TextView text(String s, int sp, int color, boolean bold) {
        TextView t = new TextView(this);
        t.setText(s == null ? "" : s);
        t.setTextSize(Math.max(12, sp));
        t.setTextColor(color);
        t.setIncludeFontPadding(false);
        t.setLineSpacing(dp(2), 1.0f);
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
        return getColor(p != null && p.startsWith("未确认")
                ? R.color.text_secondary : R.color.brand);
    }

    private String fmt(double v) {
        if (Math.abs(v - Math.rint(v)) < 0.05) return String.valueOf((long) Math.rint(v));
        return String.format(java.util.Locale.ROOT, "%.1f", v);
    }

    private int dp(int v) {
        return Math.round(v * getResources().getDisplayMetrics().density);
    }
}
