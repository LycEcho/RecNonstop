package com.recnonstop.app;

import android.Manifest;
import android.animation.ObjectAnimator;
import android.app.Activity;
import android.app.Dialog;
import android.app.TimePickerDialog;
import android.content.ClipData;
import android.content.ClipboardManager;
import android.content.ComponentName;
import android.content.Intent;
import android.content.SharedPreferences;
import android.content.pm.PackageManager;
import android.graphics.Insets;
import android.net.Uri;
import android.os.Build;
import android.os.Bundle;
import android.os.Handler;
import android.os.Looper;
import android.os.PowerManager;
import android.view.GestureDetector;
import android.view.Gravity;
import android.view.HapticFeedbackConstants;
import android.view.LayoutInflater;
import android.view.MotionEvent;
import android.view.View;
import android.view.ViewGroup;
import android.view.WindowInsets;
import android.view.WindowManager;
import android.view.animation.AccelerateDecelerateInterpolator;
import android.view.animation.OvershootInterpolator;
import android.widget.FrameLayout;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.ProgressBar;
import android.widget.ScrollView;
import android.widget.SeekBar;
import android.widget.Switch;
import android.widget.TextView;

import org.json.JSONObject;

import java.io.File;
import java.text.SimpleDateFormat;
import java.util.ArrayList;
import java.util.Arrays;
import java.util.Calendar;
import java.util.Date;
import java.util.HashMap;
import java.util.List;
import java.util.Locale;
import java.util.Map;
import java.util.Set;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

/**
 * 主界面：录音 / 记录 / 日报，顶部进入电脑同步与设置。
 *
 * 旧界面把所有开关、按钮、状态和文件列表堆在一个 ScrollView 里，
 * 这里把主操作和低频设置拆开，所有反馈尽量留在界面内（底部面板 / 页面内提示），
 * 不再满屏系统 AlertDialog。
 */
public class MainActivity extends Activity {

    private static final int REQ_PERM = 100;

    private static final int TAB_RECORDING = 0;
    private static final int TAB_FILES = 1;
    private static final int TAB_REPORT = 2;
    private static final int TAB_PC = 3;
    private static final int TAB_SETTINGS = 4;

    private static final int ATTENTION_NONE = 0;
    private static final int ATTENTION_PERMISSION = 1;
    private static final int ATTENTION_BATTERY = 2;
    private static final int ATTENTION_AUTOSTART = 3;
    private static final int ATTENTION_DIAG = 4;
    private static final int ATTENTION_STORAGE = 5;

    private static final ExecutorService FILES_EXEC = Executors.newSingleThreadExecutor();

    private FrameLayout root;
    private View[] pages;
    private LinearLayout[] navItems;
    private FrameLayout[] navBoxes;
    private ImageView[] navIcons;
    private TextView[] navLabels;
    private int currentTab = -1;
    private int lastPrimaryTab = TAB_RECORDING;
    private int initialTab = TAB_RECORDING;
    private int initialFilter = 0;

    private TextView tvTopStatus;

    private View heroDot;
    private TextView tvHeroStatus;
    private TextView tvHeroBadge;
    private TextView tvHeroTime;
    private TextView tvHeroHint;
    private TextView tvSegmentInfo;
    private TextView tvHeroSub;
    private ProgressBar segmentProgress;
    private FrameLayout recordButton;
    private ImageView recordIcon;
    private TextView tvRecordAction;
    private RecordingWaveformView recordingWaveform;
    private View pulseRing;

    private TextView tvStatSegments;
    private TextView tvStatBytes;
    private TextView tvStatGaps;
    private LinearLayout attentionCard;
    private TextView tvAttentionTitle;
    private TextView tvAttentionDesc;
    private int attentionAction = ATTENTION_NONE;

    private LinearLayout recentContainer;

    private TextView tvFilesSummary;
    private TextView tvFilesRecordingHint;
    private LinearLayout filesContainer;
    private TextView btnLoadMore;
    private View btnRefreshFiles;
    private TextView btnCleanupFiles;
    private TextView[] filterChips;
    private int fileFilter = 0;
    private int filesLimit = 30;
    private boolean filesLoaded = false;
    private boolean filesLoading = false;
    private boolean filesRefreshAgain = false;
    private boolean filesRefreshFeedback = false;
    private String filesError = "";
    private long filesSig = 0L;
    private int filesTotalCount = 0;
    private long filesTotalBytes = 0L;
    private int filesPendingCount = 0;

    private final List<FileItem> filesCache = new ArrayList<>();

    private TextView tvPcTitle;
    private TextView tvPcSub;
    private TextView tvPcBadge;
    private TextView tvPcStatus;
    private TextView tvPcStats;
    private TextView tvPcSummary;
    private TextView btnPcAction;
    private TextView btnManualPair;
    private Switch swUpload;
    private Switch swWifiOnly;

    private TextView tvReportTitle;
    private TextView tvReportMeta;
    private TextView tvReportContent;
    private TextView tvReportEmpty;
    private TextView btnReportRefresh;
    private TextView btnReportDetail;
    private LinearLayout reportChips;
    private String currentReportId = "";
    private long lastReportSyncMs = 0L;
    private boolean reportSyncing = false;

    private Switch swAutoStart;
    private Switch swBright;
    private Switch swWatchdog;
    private Switch swDailyReport;
    private TextView tvReportTimeValue;
    private TextView tvReportSettingSub;
    private boolean reportSettingLoading = false;
    private long lastReportSettingsMs = 0L;
    private SeekBar sbBright;
    private TextView tvBrightValue;
    private TextView tvSegmentValue;
    private TextView tvSegmentSub;
    private TextView tvQualityValue;
    private TextView tvQualitySub;
    private TextView tvKeepValue;
    private TextView tvKeepSub;
    private TextView tvTimeoutValue;
    private TextView tvBatteryValue;
    private TextView tvVersion;
    private LinearLayout brightnessPanel;

    private TextView snackbar;

    private boolean skipAutoStart = false;
    private boolean startAfterPermission = false;
    private boolean awaitingStart = false;
    private boolean awaitingStop = false;
    private long startRequestMs = 0L;
    private long stopRequestMs = 0L;

    private long lastFilesMs = 0L;
    private int filesGen = 0;
    private long lastHealthMs = 0L;
    private JSONObject cachedHealth = new JSONObject();
    private long lastCountsMs = 0L;
    private int[] cachedCounts = new int[4];
    private long lastInviteCheckMs = 0L;
    private long lastAttentionMs = 0L;

    private ObjectAnimator pulseAnimator;
    private ObjectAnimator dotAnimator;

    private final Handler handler = new Handler(Looper.getMainLooper());
    private final Runnable ticker = new Runnable() {
        @Override public void run() {
            refreshUi();
            handler.postDelayed(this, 1000);
        }
    };

    // ---------------- 生命周期 ----------------

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        if (savedInstanceState != null) {
            initialTab = Math.max(TAB_RECORDING,
                    Math.min(TAB_SETTINGS, savedInstanceState.getInt("tab", TAB_RECORDING)));
            initialFilter = savedInstanceState.getInt("file_filter", 0);
            lastPrimaryTab = Math.max(TAB_RECORDING, Math.min(TAB_REPORT,
                    savedInstanceState.getInt("primary_tab", TAB_RECORDING)));
            currentReportId = savedInstanceState.getString("report_id", "");
        }
        setContentView(R.layout.activity_main);

        Notifications.ensure(this);
        Uploader.start(this);
        UploadStore.get(this).migrateFromPrefs(this);
        DailyReports.seedBundled(this);

        bindViews();
        setupWindowInsets();
        setupTabs();
        setupActions();
        loadSettings();
        bindSettings();
        selectFilter(initialFilter);
        renderRecent();
        applyBrightness();
        setVersionText();

        ensurePermissions();
        handleIntent(getIntent());
        maybeAutoStart();
        refreshUi();
    }

    @Override
    protected void onNewIntent(Intent intent) {
        super.onNewIntent(intent);
        setIntent(intent);
        handleIntent(intent);
    }

    @Override
    protected void onResume() {
        super.onResume();
        applyBrightness();
        updateTimeoutText();
        Uploader.start(this);
        Uploader.scanPendingAsync(this);
        lastFilesMs = System.currentTimeMillis();
        refreshFiles(false);
        if (System.currentTimeMillis() - lastReportSettingsMs > 30000) {
            fetchReportSchedule();
        }
        if (currentTab == TAB_REPORT) {
            refreshReportPage(false);
        }
        handler.removeCallbacks(ticker);
        handler.post(ticker);
    }

    @Override
    protected void onPause() {
        super.onPause();
        AudioPlayerSheet.dismissCurrent();
        handler.removeCallbacks(ticker);
        if (pulseAnimator != null) {
            pulseAnimator.cancel();
            pulseAnimator = null;
        }
        if (dotAnimator != null) {
            dotAnimator.cancel();
            dotAnimator = null;
        }
    }

    @Override
    protected void onDestroy() {
        handler.removeCallbacksAndMessages(null);
        if (pulseAnimator != null) pulseAnimator.cancel();
        if (dotAnimator != null) dotAnimator.cancel();
        Sheet.dismissCurrent();
        super.onDestroy();
    }

    @Override
    protected void onSaveInstanceState(Bundle outState) {
        super.onSaveInstanceState(outState);
        outState.putInt("tab", currentTab);
        outState.putInt("file_filter", fileFilter);
        outState.putInt("primary_tab", lastPrimaryTab);
        outState.putString("report_id", currentReportId);
    }

    @Override
    public void onBackPressed() {
        if (currentTab >= TAB_PC) {
            selectTab(lastPrimaryTab, true);
            return;
        }
        if (currentTab != TAB_RECORDING) {
            selectTab(TAB_RECORDING, true);
            return;
        }
        super.onBackPressed();
    }

    // ---------------- 视图绑定 / 分页 ----------------

    private void bindViews() {
        root = findViewById(R.id.root);

        tvTopStatus = findViewById(R.id.tvTopStatus);

        heroDot = findViewById(R.id.heroDot);
        tvHeroStatus = findViewById(R.id.tvHeroStatus);
        tvHeroStatus.setAccessibilityLiveRegion(View.ACCESSIBILITY_LIVE_REGION_POLITE);
        tvHeroBadge = findViewById(R.id.tvHeroBadge);
        tvHeroTime = findViewById(R.id.tvHeroTime);
        tvHeroHint = findViewById(R.id.tvHeroHint);
        tvSegmentInfo = findViewById(R.id.tvSegmentInfo);
        tvHeroSub = findViewById(R.id.tvHeroSub);
        segmentProgress = findViewById(R.id.segmentProgress);
        recordButton = findViewById(R.id.recordButton);
        recordIcon = findViewById(R.id.recordIcon);
        tvRecordAction = findViewById(R.id.tvRecordAction);
        recordingWaveform = findViewById(R.id.recordingWaveform);
        pulseRing = findViewById(R.id.pulseRing);

        tvStatSegments = findViewById(R.id.tvStatSegments);
        tvStatBytes = findViewById(R.id.tvStatBytes);
        tvStatGaps = findViewById(R.id.tvStatGaps);
        attentionCard = findViewById(R.id.attentionCard);
        tvAttentionTitle = findViewById(R.id.tvAttentionTitle);
        tvAttentionDesc = findViewById(R.id.tvAttentionDesc);
        recentContainer = findViewById(R.id.recentContainer);

        tvFilesSummary = findViewById(R.id.tvFilesSummary);
        tvFilesRecordingHint = findViewById(R.id.tvFilesRecordingHint);
        filesContainer = findViewById(R.id.filesContainer);
        btnLoadMore = findViewById(R.id.btnLoadMore);
        btnRefreshFiles = findViewById(R.id.btnRefreshFiles);
        btnCleanupFiles = findViewById(R.id.btnCleanupFiles);
        filterChips = new TextView[]{
                findViewById(R.id.filterAll),
                findViewById(R.id.filterToday),
                findViewById(R.id.filterPending)
        };

        tvPcTitle = findViewById(R.id.tvPcTitle);
        tvPcSub = findViewById(R.id.tvPcSub);
        tvPcBadge = findViewById(R.id.tvPcBadge);
        tvPcStatus = findViewById(R.id.tvPcStatus);
        tvPcStats = findViewById(R.id.tvPcStats);
        tvPcSummary = findViewById(R.id.tvPcSummary);
        btnPcAction = findViewById(R.id.btnPcAction);
        btnManualPair = findViewById(R.id.btnManualPair);
        swUpload = findViewById(R.id.swUpload);
        swWifiOnly = findViewById(R.id.swWifiOnly);

        tvReportTitle = findViewById(R.id.tvReportTitle);
        tvReportMeta = findViewById(R.id.tvReportMeta);
        tvReportContent = findViewById(R.id.tvReportContent);
        tvReportEmpty = findViewById(R.id.tvReportEmpty);
        btnReportRefresh = findViewById(R.id.btnReportRefresh);
        btnReportDetail = findViewById(R.id.btnReportDetail);
        reportChips = findViewById(R.id.reportChips);

        swAutoStart = findViewById(R.id.swAutoStart);
        swBright = findViewById(R.id.swBright);
        swWatchdog = findViewById(R.id.swWatchdog);
        swDailyReport = findViewById(R.id.swDailyReport);
        tvReportTimeValue = findViewById(R.id.tvReportTimeValue);
        tvReportSettingSub = findViewById(R.id.tvReportSettingSub);
        sbBright = findViewById(R.id.sbBright);
        tvBrightValue = findViewById(R.id.tvBrightValue);
        tvSegmentValue = findViewById(R.id.tvSegmentValue);
        tvSegmentSub = findViewById(R.id.tvSegmentSub);
        tvQualityValue = findViewById(R.id.tvQualityValue);
        tvQualitySub = findViewById(R.id.tvQualitySub);
        tvKeepValue = findViewById(R.id.tvKeepValue);
        tvKeepSub = findViewById(R.id.tvKeepSub);
        tvTimeoutValue = findViewById(R.id.tvTimeoutValue);
        tvBatteryValue = findViewById(R.id.tvBatteryValue);
        tvVersion = findViewById(R.id.tvVersion);
        brightnessPanel = findViewById(R.id.brightnessPanel);
    }

    private void setupTabs() {
        pages = new View[]{
                findViewById(R.id.pageRecording),
                findViewById(R.id.pageFiles),
                findViewById(R.id.pageReport),
                findViewById(R.id.pageComputer),
                findViewById(R.id.pageSettings)
        };
        navItems = new LinearLayout[]{
                findViewById(R.id.navRec),
                findViewById(R.id.navFiles),
                findViewById(R.id.navReport),
                findViewById(R.id.navPc),
                findViewById(R.id.navSettings)
        };
        navBoxes = new FrameLayout[]{
                findViewById(R.id.navRecBox),
                findViewById(R.id.navFilesBox),
                findViewById(R.id.navReportBox),
                findViewById(R.id.navPcBox),
                findViewById(R.id.navSettingsBox)
        };
        navIcons = new ImageView[]{
                findViewById(R.id.navRecIcon),
                findViewById(R.id.navFilesIcon),
                findViewById(R.id.navReportIcon),
                findViewById(R.id.navPcIcon),
                findViewById(R.id.navSettingsIcon)
        };
        navLabels = new TextView[]{
                findViewById(R.id.navRecLabel),
                findViewById(R.id.navFilesLabel),
                findViewById(R.id.navReportLabel),
                findViewById(R.id.navPcLabel),
                findViewById(R.id.navSettingsLabel)
        };

        for (int i = 0; i < navItems.length; i++) {
            final int tab = i;
            navItems[i].setOnClickListener(v -> selectTab(tab, true));
            attachSwipe(pages[i]);
        }
        findViewById(R.id.btnHeaderComputer).setOnClickListener(v ->
                selectTab(currentTab == TAB_PC ? lastPrimaryTab : TAB_PC, true));
        findViewById(R.id.btnHeaderSettings).setOnClickListener(v ->
                selectTab(currentTab == TAB_SETTINGS ? lastPrimaryTab : TAB_SETTINGS, true));
        selectTab(initialTab, false);
    }

    private void selectTab(int tab, boolean animate) {
        if (pages == null || tab < 0 || tab >= pages.length) return;

        if (tab == currentTab) {
            if (pages[tab] instanceof ScrollView) {
                ((ScrollView) pages[tab]).smoothScrollTo(0, 0);
            }
            if (tab == TAB_FILES) {
                lastFilesMs = System.currentTimeMillis();
                refreshFiles(false);
            }
            if (tab == TAB_REPORT) {
                refreshReportPage(false);
            }
            return;
        }

        int old = currentTab;
        currentTab = tab;
        if (tab <= TAB_REPORT) lastPrimaryTab = tab;
        findViewById(R.id.btnHeaderComputer).setSelected(tab == TAB_PC);
        findViewById(R.id.btnHeaderSettings).setSelected(tab == TAB_SETTINGS);
        for (int i = 0; i < pages.length; i++) {
            boolean on = i == tab;
            View page = pages[i];
            page.animate().cancel();
            if (on) {
                page.setVisibility(View.VISIBLE);
                page.setAlpha(0f);
                page.setTranslationX(animate && old >= 0
                        ? (tab > old ? dp(14) : -dp(14))
                        : 0f);
                page.animate()
                        .alpha(1f)
                        .translationX(0f)
                        .setDuration(180)
                        .setInterpolator(new AccelerateDecelerateInterpolator())
                        .start();
            } else {
                page.setVisibility(View.GONE);
                page.setAlpha(1f);
                page.setTranslationX(0f);
            }

            navItems[i].setSelected(on);
            navBoxes[i].setSelected(on);
            navLabels[i].setSelected(on);
            navIcons[i].setColorFilter(getColor(on ? R.color.brand : R.color.text_tertiary));
            navBoxes[i].animate()
                    .scaleX(on ? 1.04f : 1f)
                    .scaleY(on ? 1.04f : 1f)
                    .setDuration(130)
                    .start();
        }

        if (tab == TAB_FILES && (!filesLoaded || System.currentTimeMillis() - lastFilesMs > 5000)) {
            lastFilesMs = System.currentTimeMillis();
            refreshFiles(false);
        }
        if (tab == TAB_RECORDING && filesLoaded) {
            renderRecent();
        }
        if (tab == TAB_REPORT) {
            refreshReportPage(false);
        }
    }

    private void attachSwipe(View page) {
        GestureDetector detector = new GestureDetector(this,
                new GestureDetector.SimpleOnGestureListener() {
                    @Override
                    public boolean onDown(MotionEvent e) {
                        return true;
                    }

                    @Override
                    public boolean onFling(MotionEvent e1, MotionEvent e2, float vx, float vy) {
                        if (e1 == null || e2 == null) return false;
                        float dx = e2.getX() - e1.getX();
                        float dy = e2.getY() - e1.getY();
                        if (Math.abs(dx) < dp(64)
                                || Math.abs(dx) < Math.abs(dy) * 1.6f
                                || Math.abs(vx) < 650) {
                            return false;
                        }
                        if (dx < 0 && currentTab < TAB_REPORT) {
                            selectTab(currentTab + 1, true);
                            return true;
                        }
                        if (dx > 0 && currentTab > TAB_RECORDING && currentTab <= TAB_REPORT) {
                            selectTab(currentTab - 1, true);
                            return true;
                        }
                        return false;
                    }
                });
        page.setOnTouchListener((v, event) -> {
            detector.onTouchEvent(event);
            return false;
        });
    }

    private void setupWindowInsets() {
        if (Build.VERSION.SDK_INT >= 30) {
            getWindow().setDecorFitsSystemWindows(false);
            root.setOnApplyWindowInsetsListener((v, insets) -> {
                Insets bars = insets.getInsets(WindowInsets.Type.systemBars());
                v.setPadding(bars.left, bars.top, bars.right, bars.bottom);
                return insets;
            });
            root.requestApplyInsets();
        }
    }

    // ---------------- 操作绑定 ----------------

    private void setupActions() {
        tvTopStatus.setOnClickListener(v -> selectTab(TAB_RECORDING, true));
        tvHeroBadge.setOnClickListener(v -> chooseSegment());
        recordButton.setOnClickListener(v -> {
            recordButton.performHapticFeedback(HapticFeedbackConstants.VIRTUAL_KEY);
            onRecordButton();
        });
        recordButton.setOnTouchListener((v, event) -> {
            switch (event.getActionMasked()) {
                case MotionEvent.ACTION_DOWN:
                    v.animate().scaleX(0.94f).scaleY(0.94f).setDuration(80).start();
                    break;
                case MotionEvent.ACTION_UP:
                case MotionEvent.ACTION_CANCEL:
                    v.animate().scaleX(1f).scaleY(1f).setDuration(130)
                            .setInterpolator(new OvershootInterpolator(1.4f)).start();
                    break;
                default:
                    break;
            }
            return false;
        });

        attentionCard.setOnClickListener(v -> onAttentionClick());
        findViewById(R.id.btnSeeAll).setOnClickListener(v -> selectTab(TAB_FILES, true));

        btnRefreshFiles.setOnClickListener(v -> {
            lastFilesMs = System.currentTimeMillis();
            refreshFiles(true);
        });
        for (int i = 0; i < filterChips.length; i++) {
            final int filter = i;
            filterChips[i].setOnClickListener(v -> selectFilter(filter));
        }
        btnLoadMore.setOnClickListener(v -> {
            filesLimit += 30;
            renderFiles();
            if (filesContainer != null) filesContainer.performHapticFeedback(
                    HapticFeedbackConstants.KEYBOARD_TAP);
        });

        btnReportRefresh.setOnClickListener(v -> {
            btnReportRefresh.animate().rotationBy(360f).setDuration(500).start();
            refreshReportPage(true);
        });

        btnPcAction.setOnClickListener(v -> onPcPrimary());
        btnManualPair.setOnClickListener(v -> manualPair());
        findViewById(R.id.rowRediscover).setOnClickListener(v -> discoverPc());
        findViewById(R.id.rowTestPc).setOnClickListener(v -> testPc());
        findViewById(R.id.rowUnpair).setOnClickListener(v -> confirmUnpair());

        findViewById(R.id.rowSegment).setOnClickListener(v -> chooseSegment());
        findViewById(R.id.rowQuality).setOnClickListener(v -> chooseQuality());
        findViewById(R.id.rowKeep).setOnClickListener(v -> chooseKeep());
        findViewById(R.id.rowReportTime).setOnClickListener(v -> chooseReportTime());
        findViewById(R.id.rowBattery).setOnClickListener(v -> requestBatteryWhitelist());
        findViewById(R.id.rowAutostart).setOnClickListener(v -> openAutostart());
        findViewById(R.id.rowTimeout).setOnClickListener(v -> openDisplaySettings());
        findViewById(R.id.rowPath).setOnClickListener(v -> showPath());
        findViewById(R.id.rowDiag).setOnClickListener(v -> exportDiag());
    }

    private void bindSettings() {
        swAutoStart.setOnCheckedChangeListener((b, v) -> {
            Settings.setBool(this, Settings.KEY_AUTO_START, v);
            toast(v ? "下次打开 App 会自动开始录音" : "已关闭打开软件自动录音");
        });
        swWatchdog.setOnCheckedChangeListener((b, v) -> {
            Settings.setBool(this, Settings.KEY_WATCHDOG, v);
            if (!v) toast("后台被杀后不会自动拉起");
        });
        swUpload.setOnCheckedChangeListener((b, v) -> {
            Settings.setBool(this, Settings.KEY_UPLOAD, v);
            if (v) {
                if (Settings.connected(this)) {
                    Uploader.scanPendingAsync(this, true);
                    toast("开始同步录音");
                } else {
                    toast("连接电脑后会自动上传");
                }
            } else {
                RecState.uploadStatus = "自动上传已关闭";
            }
        });
        swWifiOnly.setOnCheckedChangeListener((b, v) ->
                Settings.setBool(this, Settings.KEY_WIFI_ONLY, v));

        swDailyReport.setOnCheckedChangeListener((b, v) -> {
            if (reportSettingLoading) return;
            String time = Settings.getString(this, Settings.KEY_REPORT_TIME, "18:00");
            updateReportSchedule(v, time);
        });

        swBright.setOnCheckedChangeListener((b, v) -> {
            Settings.setBool(this, Settings.KEY_BRIGHT_CUSTOM, v);
            setBrightnessEnabled(v);
            applyBrightness();
        });
        sbBright.setOnSeekBarChangeListener(new SeekBar.OnSeekBarChangeListener() {
            @Override
            public void onProgressChanged(SeekBar seekBar, int progress, boolean fromUser) {
                int pct = clampPct(progress + 1);
                Settings.setInt(MainActivity.this, Settings.KEY_BRIGHT_PCT, pct);
                updateBrightValue(pct);
                if (fromUser) applyBrightness();
            }

            @Override public void onStartTrackingTouch(SeekBar seekBar) { }

            @Override public void onStopTrackingTouch(SeekBar seekBar) { }
        });
    }

    // ---------------- 设置 ----------------

    private void loadSettings() {
        swAutoStart.setChecked(Settings.getBool(this, Settings.KEY_AUTO_START, false));
        swWatchdog.setChecked(Settings.getBool(this, Settings.KEY_WATCHDOG, true));
        swUpload.setChecked(Settings.getBool(this, Settings.KEY_UPLOAD, false));
        swWifiOnly.setChecked(Settings.getBool(this, Settings.KEY_WIFI_ONLY, true));
        reportSettingLoading = true;
        swDailyReport.setChecked(Settings.getBool(this, Settings.KEY_REPORT_ENABLED, true));
        tvReportTimeValue.setText(Settings.getString(this, Settings.KEY_REPORT_TIME, "18:00"));
        reportSettingLoading = false;
        updateReportSettingSub();

        boolean custom = Settings.getBool(this, Settings.KEY_BRIGHT_CUSTOM, true);
        int pct = clampPct(Settings.getInt(this, Settings.KEY_BRIGHT_PCT, 1));
        swBright.setChecked(custom);
        sbBright.setProgress(pct - 1);
        setBrightnessEnabled(custom);
        updateBrightValue(pct);

        updateSegmentText();
        updateQualityText();
        updateKeepText();
        updateTimeoutText();
        updateBatteryValue();
    }

    private void setBrightnessEnabled(boolean on) {
        sbBright.setEnabled(on);
        brightnessPanel.setAlpha(on ? 1f : 0.45f);
    }

    private void updateBrightValue(int pct) {
        tvBrightValue.setText(pct + "%");
    }

    private void updateSegmentText() {
        int min = Settings.getInt(this, Settings.KEY_SEGMENT_MIN, 3);
        tvSegmentValue.setText(min + " 分钟");
        tvSegmentSub.setText("崩溃最多丢 " + min + " 分钟");
        tvHeroBadge.setText(min + " 分钟/段 ›");
    }

    private void updateQualityText() {
        int q = Settings.quality(this);
        tvQualityValue.setText(shortQuality(q));
        tvQualitySub.setText(qualityDetail(q));
    }

    private void updateKeepText() {
        int d = Settings.getInt(this, Settings.KEY_KEEP_DAYS, 7);
        tvKeepValue.setText(d <= 0 ? "不清理" : d + " 天");
        tvKeepSub.setText(d <= 0 ? "不会自动删除录音" : "自动清理 " + d + " 天前的录音");
        updateFilesSummary();
    }

    private void updateTimeoutText() {
        int ms;
        try {
            ms = android.provider.Settings.System.getInt(getContentResolver(),
                    android.provider.Settings.System.SCREEN_OFF_TIMEOUT, 60000);
        } catch (Exception e) {
            ms = 60000;
        }
        String text;
        if (ms >= 2147483647) {
            text = "永不";
        } else if (ms >= 60000) {
            text = (ms / 60000) + " 分钟";
        } else {
            text = Math.max(1, ms / 1000) + " 秒";
        }
        tvTimeoutValue.setText(text);
    }

    private void updateBatteryValue() {
        boolean ignored = batteryIgnored();
        tvBatteryValue.setText(ignored ? "已加入" : "未加入");
        tvBatteryValue.setTextColor(getColor(ignored ? R.color.success : R.color.warning));
    }

    private void chooseSegment() {
        final int[] vals = {1, 3, 5, 10};
        final String[] labels = {"1 分钟", "3 分钟", "5 分钟", "10 分钟"};
        final String[] details = {
                "崩溃最多丢 1 分钟",
                "崩溃最多丢 3 分钟（推荐）",
                "崩溃最多丢 5 分钟",
                "崩溃最多丢 10 分钟"
        };
        int cur = Settings.getInt(this, Settings.KEY_SEGMENT_MIN, 3);
        int idx = 1;
        for (int i = 0; i < vals.length; i++) if (vals[i] == cur) idx = i;

        Sheet.choices(this, "分段时长", "从下一段开始生效", labels, details, idx, which -> {
            Settings.setInt(this, Settings.KEY_SEGMENT_MIN, vals[which]);
            updateSegmentText();
            toast("分段时长已设为 " + vals[which] + " 分钟");
        });
    }

    private void chooseQuality() {
        final String[] labels = {"最高", "高", "标准", "省空间"};
        final String[] details = {
                "48kHz / 128kbps · 约 58 MB/小时 · 7 天约 9.7GB",
                "44.1kHz / 96kbps · 约 43 MB/小时 · 7 天约 7.2GB",
                "32kHz / 64kbps · 约 29 MB/小时 · 7 天约 4.8GB",
                "16kHz / 32kbps · 约 14 MB/小时 · 7 天约 2.4GB · 推荐"
        };
        int cur = Settings.quality(this);
        Sheet.choices(this, "录音质量", "从下一段开始生效", labels, details, cur, which -> {
            Settings.setInt(this, Settings.KEY_QUALITY, which);
            updateQualityText();
            toast("录音质量已设为「" + labels[which] + "」");
        });
    }

    /** 记录页的本地批量清理：1 / 3 / 7 天前。只删手机本地文件。 */
    private void showCleanupSheet() {
        final int[] days = {1, 3, 7};
        String[] labels = {"删除 1 天前", "删除 3 天前", "删除 7 天前"};
        String[] details = new String[days.length];
        for (int i = 0; i < days.length; i++) {
            long cutoff = System.currentTimeMillis() - days[i] * 86_400_000L;
            int n = 0;
            long bytes = 0;
            for (FileItem item : filesCache) {
                if (item.file.isFile() && item.modified < cutoff
                        && !item.name.equals(RecState.currentFile)) {
                    n++;
                    bytes += item.size;
                }
            }
            details[i] = n == 0 ? "没有符合条件的本地录音" : n + " 段 · " + size(bytes);
        }
        Sheet.choices(this, "批量清理本地录音", "只删除手机本地文件，电脑上的报告/记录不受影响", labels, details, 3, which -> {
            long cutoff = System.currentTimeMillis() - days[which] * 86_400_000L;
            int n = 0;
            long bytes = 0;
            for (FileItem item : filesCache) {
                if (item.file.isFile() && item.modified < cutoff
                        && !item.name.equals(RecState.currentFile)) {
                    n++;
                    bytes += item.size;
                }
            }
            if (n == 0) {
                toast("没有符合条件的本地录音");
                return;
            }
            final int count = n;
            final long total = bytes;
            final int keepDays = days[which];
            List<Sheet.Action> actions = new ArrayList<>();
            actions.add(act(R.drawable.ic_delete, "确认删除 " + count + " 段",
                    size(total) + " · 包括未上传的本地文件；删除后无法恢复",
                    getColor(R.color.danger), () -> doBulkDelete(cutoff, keepDays)));
            Sheet.show(this, "删除 " + keepDays + " 天前的录音？",
                    "正在录制的分段会跳过；电脑端不受影响", actions);
        });
    }

    private void doBulkDelete(long cutoff, int days) {
        int n = 0;
        long bytes = 0;
        for (FileItem item : new ArrayList<>(filesCache)) {
            if (!item.file.isFile() || item.modified >= cutoff
                    || item.name.equals(RecState.currentFile)) continue;
            long len = item.file.length();
            if (item.file.delete()) {
                // 防止已删除的本地文件被 Uploader 再次放回待上传队列。
                UploadStore.get(this).markUploaded(item.name, "");
                n++;
                bytes += len;
            }
        }
        lastFilesMs = 0L;
        refreshFiles(false);
        toast(n == 0 ? "没有文件被删除" : "已删除 " + n + " 段 · " + size(bytes));
        Diag.log(this, "Files", "bulk delete: " + n + " files older than " + days + "d");
    }

    private void chooseKeep() {
        final int[] vals = {0, 1, 3, 7, 30};
        final String[] labels = {"不清理", "1 天", "3 天", "7 天", "30 天"};
        final String[] details = {
                "不会自动删除录音",
                "自动删除 1 天前的录音",
                "自动删除 3 天前的录音",
                "自动删除 7 天前的录音",
                "自动删除 30 天前的录音"
        };
        int cur = Settings.getInt(this, Settings.KEY_KEEP_DAYS, 7);
        int idx = 3;
        for (int i = 0; i < vals.length; i++) if (vals[i] == cur) idx = i;

        Sheet.choices(this, "录音保留", "未上传的录音会被保护", labels, details, idx, which -> {
            Settings.setInt(this, Settings.KEY_KEEP_DAYS, vals[which]);
            updateKeepText();
            toast(vals[which] <= 0 ? "不会自动清理录音" : "录音保留 " + vals[which] + " 天");
        });
    }

    private void applyBrightness() {
        boolean custom = Settings.getBool(this, Settings.KEY_BRIGHT_CUSTOM, true);
        int pct = clampPct(Settings.getInt(this, Settings.KEY_BRIGHT_PCT, 1));
        WindowManager.LayoutParams lp = getWindow().getAttributes();
        lp.screenBrightness = custom
                ? pct / 100f
                : WindowManager.LayoutParams.BRIGHTNESS_OVERRIDE_NONE;
        getWindow().setAttributes(lp);
    }

    private static int clampPct(int p) {
        return p < 1 ? 1 : (p > 100 ? 100 : p);
    }

    private static String shortQuality(int q) {
        switch (q) {
            case 1: return "高";
            case 2: return "标准";
            case 3: return "省空间";
            default: return "最高";
        }
    }

    private static String qualityDetail(int q) {
        switch (q) {
            case 1: return "44.1kHz / 96kbps · 约 43 MB/小时";
            case 2: return "32kHz / 64kbps · 约 29 MB/小时";
            case 3: return "16kHz / 32kbps · 约 14 MB/小时";
            default: return "48kHz / 128kbps · 约 58 MB/小时";
        }
    }

    // ---------------- 自动启动 / 权限 ----------------

    private void maybeAutoStart() {
        if (skipAutoStart) return;
        if (!Settings.getBool(this, Settings.KEY_AUTO_START, false)) return;
        if (RecState.running) return;

        handler.postDelayed(() -> {
            if (!RecState.running && !isFinishing()) {
                startRec();
            }
        }, 600);
    }

    private void ensurePermissions() {
        List<String> need = new ArrayList<>();
        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO)
                != PackageManager.PERMISSION_GRANTED) {
            need.add(Manifest.permission.RECORD_AUDIO);
        }
        if (Build.VERSION.SDK_INT >= 33
                && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS)
                != PackageManager.PERMISSION_GRANTED) {
            need.add(Manifest.permission.POST_NOTIFICATIONS);
        }
        if (!need.isEmpty()) {
            requestPermissions(need.toArray(new String[0]), REQ_PERM);
        }
    }

    private boolean hasAudioPerm() {
        return checkSelfPermission(Manifest.permission.RECORD_AUDIO)
                == PackageManager.PERMISSION_GRANTED;
    }

    @Override
    public void onRequestPermissionsResult(int req, String[] perms, int[] res) {
        super.onRequestPermissionsResult(req, perms, res);
        if (req != REQ_PERM) return;
        if (hasAudioPerm()) {
            if (startAfterPermission) {
                startAfterPermission = false;
                handler.postDelayed(this::startRec, 200);
            }
        } else {
            startAfterPermission = false;
            List<Sheet.Action> actions = new ArrayList<>();
            actions.add(act(R.drawable.ic_settings, "打开应用设置", "授予麦克风权限后再开始录音",
                    getColor(R.color.brand), this::openAppSettings));
            Sheet.show(this, "缺少录音权限", "必须授予麦克风权限才能录音。", actions);
        }
        refreshUi();
    }

    // ---------------- 启停 ----------------

    private void onRecordButton() {
        if (awaitingStop) {
            toast("正在停止…");
            return;
        }
        if (RecState.running) {
            confirmStop();
            return;
        }
        if (awaitingStart) {
            toast("正在启动，请稍候");
            return;
        }
        startRec();
    }

    private void startRec() {
        if (!hasAudioPerm()) {
            startAfterPermission = true;
            toast("请先允许麦克风权限");
            ensurePermissions();
            return;
        }
        startAfterPermission = false;
        awaitingStart = true;
        awaitingStop = false;
        startRequestMs = System.currentTimeMillis();
        try {
            Intent i = new Intent(this, RecorderService.class)
                    .setAction(RecorderService.ACTION_START);
            startForegroundService(i);
            refreshHero();
            refreshTopChip();
        } catch (Exception e) {
            awaitingStart = false;
            toast("启动失败：" + safeMsg(e));
        }
    }

    private void stopRec() {
        awaitingStart = false;
        awaitingStop = true;
        stopRequestMs = System.currentTimeMillis();
        if (recordButton != null) {
            recordButton.performHapticFeedback(HapticFeedbackConstants.VIRTUAL_KEY);
        }
        try {
            Intent i = new Intent(this, RecorderService.class)
                    .setAction(RecorderService.ACTION_STOP);
            startService(i);
            refreshHero();
            refreshTopChip();
        } catch (Exception e) {
            awaitingStop = false;
            toast("停止失败：" + safeMsg(e));
        }
    }

    private void confirmStop() {
        List<Sheet.Action> actions = new ArrayList<>();
        actions.add(act(R.drawable.ic_stop, "停止录音", "当前分段会正常保存，之后可以随时继续",
                getColor(R.color.danger), this::stopRec));
        Sheet.show(this, "停止录音？", "已录内容不会丢失。", actions);
    }

    /** adb 调试入口：am start -n com.recnonstop.app/.MainActivity --es cmd start|stop */
    private void handleIntent(Intent intent) {
        if (intent == null) return;

        String reportId = intent.getStringExtra("report_id");
        if (reportId != null && !reportId.isEmpty()) {
            // A notification selects its report once; recreation must retain the
            // version the reader subsequently chose.
            intent.removeExtra("report_id");
            currentReportId = reportId;
            skipAutoStart = true;
            selectTab(TAB_REPORT, true);
            handler.postDelayed(() -> showReport(reportId), 200);
        }

        String cmd = intent.getStringExtra("cmd");
        if (cmd == null) return;

        skipAutoStart = true;
        int q = intent.getIntExtra("quality", -1);
        if (q >= 0 && q <= 3) {
            Settings.setInt(this, Settings.KEY_QUALITY, q);
            updateQualityText();
        }
        if ("start".equals(cmd)) {
            selectTab(TAB_RECORDING, true);
            handler.postDelayed(this::startRec, 300);
        } else if ("stop".equals(cmd)) {
            selectTab(TAB_RECORDING, true);
            handler.postDelayed(this::stopRec, 300);
        } else if ("upload".equals(cmd)) {
            selectTab(TAB_PC, true);
            handler.postDelayed(() -> Uploader.scanPendingAsync(this, true), 300);
        }
    }

    // ---------------- 权限 / 系统设置跳转 ----------------

    private void requestBatteryWhitelist() {
        if (batteryIgnored()) {
            toast("已经在电池优化白名单里");
            return;
        }
        try {
            Intent i = new Intent(
                    android.provider.Settings.ACTION_REQUEST_IGNORE_BATTERY_OPTIMIZATIONS);
            i.setData(Uri.parse("package:" + getPackageName()));
            toast("请在系统弹窗中选择「允许」");
            startActivity(i);
        } catch (Exception e) {
            openAppSettings();
        }
    }

    private void openAutostart() {
        String[][] targets = new String[][]{
                {"com.miui.securitycenter", "com.miui.permcenter.autostart.AutoStartManagementActivity"},
                {"com.huawei.systemmanager", "com.huawei.systemmanager.startupmgr.ui.StartupNormalAppListActivity"},
                {"com.coloros.safecenter", "com.coloros.safecenter.permission.startup.StartupAppListActivity"},
                {"com.oplus.safecenter", "com.oplus.safecenter.permission.startup.StartupAppListActivity"},
                {"com.vivo.permissionmanager", "com.vivo.permissionmanager.activity.BgStartUpManagerActivity"},
                {"com.iqoo.secure", "com.iqoo.secure.ui.phoneoptimize.BgStartUpManager"},
                {"com.samsung.android.lool", "com.samsung.android.sm.ui.battery.BatteryActivity"}
        };
        for (String[] t : targets) {
            try {
                Intent i = new Intent();
                i.setComponent(new ComponentName(t[0], t[1]));
                startActivity(i);
                return;
            } catch (Exception ignored) {
            }
        }

        List<Sheet.Action> actions = new ArrayList<>();
        actions.add(act(R.drawable.ic_settings, "打开应用设置",
                "把自启动、后台运行、省电策略都打开", getColor(R.color.brand),
                this::openAppSettings));
        Sheet.show(this, "手动设置自启动",
                "没找到自动跳转入口。请到「设置 → 应用管理 → 本应用」里，"
                        + "打开自启动和后台运行，并加入后台清理白名单。", actions);
    }

    private void openDisplaySettings() {
        try {
            startActivity(new Intent(android.provider.Settings.ACTION_DISPLAY_SETTINGS));
        } catch (Exception e) {
            try {
                startActivity(new Intent(android.provider.Settings.ACTION_SETTINGS));
            } catch (Exception ignored) {
            }
        }
    }

    private void openAppSettings() {
        Intent i = new Intent(android.provider.Settings.ACTION_APPLICATION_DETAILS_SETTINGS);
        i.setData(Uri.parse("package:" + getPackageName()));
        startActivity(i);
    }

    private boolean batteryIgnored() {
        PowerManager pm = (PowerManager) getSystemService(POWER_SERVICE);
        return pm != null && pm.isIgnoringBatteryOptimizations(getPackageName());
    }

    // ---------------- 电脑连接 ----------------

    // ---------------- 日报 ----------------

    private void refreshReportPage(boolean fetch) {
        if (fetch) {
            if (reportSyncing) return;
            if (!Settings.connected(this)) {
                toast("先在「电脑」页连接电脑");
                return;
            }
            lastReportSyncMs = System.currentTimeMillis();
            reportSyncing = true;
            updateReportSyncState();
            DailyReports.syncHistory(this, (ok, msg) -> {
                reportSyncing = false;
                if (isFinishing() || isDestroyed()) return;
                updateReportSyncState();
                if (msg != null && !msg.isEmpty()) toast(msg);
                loadReportList();
            });
            return;
        }
        loadReportList();
        maybeSyncReports();
    }

    /** 进入日报页时静默补齐历史，最多每分钟一次，避免来回切页重复请求。 */
    private void maybeSyncReports() {
        if (reportSyncing || !Settings.connected(this)) return;
        if (System.currentTimeMillis() - lastReportSyncMs < 60000) return;
        lastReportSyncMs = System.currentTimeMillis();
        reportSyncing = true;
        updateReportSyncState();
        DailyReports.syncHistory(this, (ok, msg) -> {
            reportSyncing = false;
            if (isFinishing() || isDestroyed()) return;
            updateReportSyncState();
            if (ok) loadReportList();
        });
    }

    private void updateReportSyncState() {
        TextView refresh = findViewById(R.id.btnReportRefresh);
        if (refresh == null) return;
        refresh.setEnabled(!reportSyncing);
        refresh.setText(reportSyncing ? "同步中…" : "同步");
        refresh.setContentDescription(reportSyncing ? "正在同步日报" : "从电脑同步日报");
    }

    private void loadReportList() {
        FILES_EXEC.execute(() -> {
            List<JSONObject> list = DailyReports.list(this);
            runOnUiThread(() -> {
                if (isFinishing() || isDestroyed()) return;
                renderReportList(list);
            });
        });
    }

    private void renderReportList(List<JSONObject> list) {
        if (reportChips == null) return;
        reportChips.removeAllViews();
        if (list == null || list.isEmpty()) {
            currentReportId = "";
            tvReportTitle.setText("还没有日报");
            tvReportMeta.setText("连接电脑后自动接收");
            tvReportContent.setVisibility(View.GONE);
            tvReportEmpty.setVisibility(View.VISIBLE);
            if (btnReportDetail != null) btnReportDetail.setVisibility(View.GONE);
            return;
        }

        // 同一天的不同版本相邻展示，补生成旧日报不会打乱日期顺序。
        list = new ArrayList<>(list);
        list.sort((a, b) -> {
            int byDate = reportDate(b).compareTo(reportDate(a));
            if (byDate != 0) return byDate;
            return b.optString("generated_at", "").compareTo(a.optString("generated_at", ""));
        });
        Map<String, Integer> versionsByDate = new HashMap<>();
        for (JSONObject r : list) {
            if (r.optString("id", "").isEmpty()) continue;
            String date = reportDate(r);
            versionsByDate.put(date, versionsByDate.getOrDefault(date, 0) + 1);
        }

        JSONObject selected = null;
        for (JSONObject r : list) {
            String id = r.optString("id", "");
            if (!id.isEmpty() && id.equals(currentReportId)) {
                selected = r;
                break;
            }
        }
        if (selected == null) {
            selected = list.get(0);
            currentReportId = selected.optString("id", "");
        }

        for (JSONObject r : list) {
            String id = r.optString("id", "");
            if (id.isEmpty()) continue;
            TextView chip = new TextView(this);
            chip.setTag(id);
            boolean multipleVersions = versionsByDate.getOrDefault(reportDate(r), 0) > 1;
            String generated = r.optString("generated_at", "");
            String time = generated.length() >= 16 ? generated.substring(11, 16) : "时间未知";
            chip.setText(reportChipText(r) + (multipleVersions ? "\n" + time : ""));
            chip.setBackgroundResource(R.drawable.bg_filter_chip);
            chip.setTextColor(getColorStateList(R.color.filter_text));
            chip.setTextSize(12);
            chip.setGravity(Gravity.CENTER);
            chip.setMinHeight(dp(48));
            chip.setContentDescription(reportDate(r) + "日报"
                    + (multipleVersions ? "，" + formatReportTime(generated) + "生成" : "")
                    + (DailyReports.isRead(this, id) ? "，已读" : "，未读"));
            chip.setPadding(dp(13), dp(6), dp(13), dp(6));
            chip.setSelected(id.equals(currentReportId));
            chip.setOnClickListener(v -> showReport(id));
            LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(
                    ViewGroup.LayoutParams.WRAP_CONTENT, ViewGroup.LayoutParams.WRAP_CONTENT);
            lp.rightMargin = dp(8);
            reportChips.addView(chip, lp);
        }
        renderReport(selected);
    }

    private String reportChipText(JSONObject r) {
        String date = reportDate(r);
        if (date.length() >= 10) return date.substring(5);
        String title = r.optString("title", "日报");
        return title.length() > 6 ? title.substring(0, 6) : title;
    }

    private String reportDate(JSONObject r) {
        String date = r.optString("date", "");
        if (!date.isEmpty()) return date;
        String generated = r.optString("generated_at", "");
        return generated.length() >= 10 ? generated.substring(0, 10) : "";
    }

    private void showReport(String id) {
        if (id == null || id.isEmpty()) return;
        FILES_EXEC.execute(() -> {
            JSONObject r = DailyReports.get(this, id);
            runOnUiThread(() -> {
                if (isFinishing() || isDestroyed()) return;
                if (r == null) {
                    toast("日报尚未保存在手机，点「同步」重试下载");
                    loadReportList();
                    return;
                }
                currentReportId = id;
                selectReportChip(id);
                renderReport(r);
            });
        });
    }

    private void selectReportChip(String id) {
        if (reportChips == null) return;
        for (int i = 0; i < reportChips.getChildCount(); i++) {
            View v = reportChips.getChildAt(i);
            v.setSelected(id != null && id.equals(v.getTag()));
        }
    }

    private void renderReport(JSONObject r) {
        if (r == null) return;
        String id = r.optString("id", "");
        String title = r.optString("title", "录音日报");
        String generated = formatReportTime(r.optString("generated_at", ""));
        int count = r.optInt("job_count", 0);
        int minutes = (int) Math.round(r.optDouble("duration_seconds", 0) / 60.0);
        String meta = (generated.isEmpty() ? r.optString("date", "") : generated + " 生成")
                + " · " + count + " 段 · " + minutes + " 分钟\n已保存在手机，可离线阅读";
        boolean incomplete = DailyReports.summaryIncomplete(r);
        if (incomplete) meta += "\n汇总未完成 · 已保留分段材料";
        boolean hasDetail = r.optJSONObject("detail") != null;
        String rawContent = hasDetail ? r.optString("summary", "") : r.optString("content", r.optString("summary", ""));
        CharSequence content = ReportMarkdown.render(this, rawContent);
        if (content.length() == 0) {
            content = ReportMarkdown.render(this, r.optString("summary", "暂无内容"));
        }

        tvReportTitle.setText(title);
        tvReportMeta.setText(meta);
        tvReportContent.setText(content);
        tvReportContent.setVisibility(View.VISIBLE);
        tvReportEmpty.setVisibility(View.GONE);
        if (btnReportDetail != null) {
            btnReportDetail.setVisibility(id.isEmpty() ? View.GONE : View.VISIBLE);
            if (!id.isEmpty()) {
                btnReportDetail.setText(incomplete ? "查看已保留的分段材料"
                        : hasDetail ? "阅读日报与对话时间线" : "阅读完整日报");
                btnReportDetail.setOnClickListener(v -> {
                    try {
                        startActivity(ReportDetailActivity.intent(this, id));
                    } catch (Exception e) {
                        toast("打不开日报：" + e.getMessage());
                    }
                });
            }
        }
        DailyReports.markRead(this, id);
    }

    private String formatReportTime(String iso) {
        if (iso == null || iso.length() < 16) return iso == null ? "" : iso;
        return iso.substring(0, 10) + " " + iso.substring(11, 16);
    }

    private void chooseReportTime() {
        if (!Settings.connected(this)) {
            toast("先在「电脑」页连接电脑");
            return;
        }
        String at = Settings.getString(this, Settings.KEY_REPORT_TIME, "18:00");
        int hour = 18;
        int minute = 0;
        try {
            hour = Integer.parseInt(at.substring(0, 2));
            minute = Integer.parseInt(at.substring(3, 5));
        } catch (Exception ignored) {
        }
        new TimePickerDialog(this, (view, h, m) -> {
            String time = String.format(Locale.ROOT, "%02d:%02d", h, m);
            updateReportSchedule(swDailyReport.isChecked(), time);
        }, hour, minute, true).show();
    }

    private void updateReportSchedule(boolean enabled, String time) {
        if (!Settings.connected(this)) {
            toast("先在「电脑」页连接电脑");
            revertReportSettingUi();
            return;
        }
        tvReportSettingSub.setText("正在更新…");
        DailyReports.updateSchedule(this, enabled, time, (ok, outEnabled, outTime, msg) -> {
            if (isFinishing() || isDestroyed()) return;
            if (!ok) {
                toast(msg == null || msg.isEmpty() ? "日报设置失败" : "日报设置失败：" + msg);
                revertReportSettingUi();
                return;
            }
            reportSettingLoading = true;
            swDailyReport.setChecked(outEnabled);
            tvReportTimeValue.setText(outTime);
            reportSettingLoading = false;
            Settings.setBool(this, Settings.KEY_REPORT_ENABLED, outEnabled);
            Settings.setString(this, Settings.KEY_REPORT_TIME, outTime);
            updateReportSettingSub();
            toast(outEnabled ? "已设置为每天 " + outTime + " 生成" : "已关闭每日日报");
        });
    }

    private void revertReportSettingUi() {
        reportSettingLoading = true;
        swDailyReport.setChecked(Settings.getBool(this, Settings.KEY_REPORT_ENABLED, true));
        tvReportTimeValue.setText(Settings.getString(this, Settings.KEY_REPORT_TIME, "18:00"));
        reportSettingLoading = false;
        updateReportSettingSub();
    }

    private void fetchReportSchedule() {
        if (!Settings.connected(this)) return;
        lastReportSettingsMs = System.currentTimeMillis();
        DailyReports.fetchSchedule(this, (ok, enabled, time, msg) -> {
            if (isFinishing() || isDestroyed() || !ok) return;
            reportSettingLoading = true;
            swDailyReport.setChecked(enabled);
            tvReportTimeValue.setText(time);
            reportSettingLoading = false;
            updateReportSettingSub();
        });
    }

    private void updateReportSettingSub() {
        boolean enabled = Settings.getBool(this, Settings.KEY_REPORT_ENABLED, true);
        String time = Settings.getString(this, Settings.KEY_REPORT_TIME, "18:00");
        tvReportSettingSub.setText(enabled
                ? "每天 " + time + " 生成，完成后自动推送"
                : "已关闭每日日报");
    }

    private void onPcPrimary() {
        if (Settings.connected(this)) {
            RecState.uploadStatus = "正在检查未上传的录音…";
            if (!swUpload.isChecked()) {
                swUpload.setChecked(true);
            } else {
                Uploader.scanPendingAsync(this, true);
                toast("正在检查未上传的录音");
            }
            refreshPcViews();
        } else {
            discoverPc();
        }
    }

    private void testPc() {
        toast("正在测试电脑连接…");
        RecState.uploadStatus = "测试连接…";
        Uploader.test(this);
    }

    private void discoverPc() {
        RecState.uploadStatus = "搜索电脑中...";
        refreshPcViews();
        Dialog wait = Sheet.progress(this, "正在搜索电脑",
                "确保电脑端服务已启动，手机和电脑连接同一个 Wi-Fi", false, null);

        Discovery.discover((devices, error) -> runOnUiThread(() -> {
            dismissQuietly(wait);
            if (isFinishing() || isDestroyed()) return;

            List<Discovery.Device> pcs = new ArrayList<>();
            for (Discovery.Device d : devices) {
                if (d.isPc()) pcs.add(d);
            }
            if (pcs.isEmpty()) {
                showNoPc(error);
                return;
            }

            String curId = Settings.getString(this, Settings.KEY_PC_ID, "");
            boolean connected = Settings.connected(this);
            List<Sheet.Action> actions = new ArrayList<>();
            for (Discovery.Device d : pcs) {
                boolean current = connected && curId != null && curId.equals(d.id);
                String sub = d.host + ":" + d.port
                        + (d.fp == null || d.fp.isEmpty() ? "" : " · " + d.fp)
                        + (current ? " · 当前连接" : "");
                actions.add(new Sheet.Action(
                        current ? R.drawable.ic_check_circle : R.drawable.ic_pc,
                        d.name,
                        sub,
                        getColor(current ? R.color.success : R.color.brand),
                        () -> connectTo(d)));
            }
            actions.add(act(R.drawable.ic_link, "手动输入地址", "知道电脑 IP 时可以直接填",
                    getColor(R.color.text_secondary), this::manualPair));
            Sheet.show(this, "选择电脑", "发现 " + pcs.size() + " 台设备，点一台连接", actions);
        }));
    }

    private void showNoPc(String error) {
        List<Sheet.Action> actions = new ArrayList<>();
        actions.add(act(R.drawable.ic_refresh, "重新搜索", "再扫一次局域网",
                getColor(R.color.brand), this::discoverPc));
        actions.add(act(R.drawable.ic_link, "手动输入地址", "知道电脑 IP 时可以直接填",
                getColor(R.color.text_secondary), this::manualPair));
        String msg = error == null || error.isEmpty()
                ? "确认电脑端服务已启动，手机和电脑在同一 Wi-Fi。"
                : error + "\n\n确认电脑端服务已启动，手机和电脑在同一 Wi-Fi。";
        Sheet.show(this, "没发现电脑", msg, actions);
    }

    /** 选中一台电脑：连过就直接用，没连过才弹一次确认。 */
    private void connectTo(Discovery.Device dev) {
        if (dev.id == null || dev.id.isEmpty()) {
            toast("电脑端版本太旧，请先更新 pc-server");
            return;
        }
        String pcId = Settings.getString(this, Settings.KEY_PC_ID, "");
        if (Settings.connected(this) && pcId != null && !pcId.isEmpty() && pcId.equals(dev.id)) {
            Settings.setString(this, Settings.KEY_PC_URL, "http://" + dev.host + ":" + dev.port);
            Settings.setString(this, Settings.KEY_PC_NAME, dev.name);
            Settings.setBool(this, Settings.KEY_UPLOAD, true);
            swUpload.setChecked(true);
            RecState.uploadStatus = "已连接：" + dev.name;
            toast("已连接「" + dev.name + "」，开始同步");
            refreshPcViews();
            Uploader.scanPendingAsync(this, true);
            return;
        }

        String fp = dev.fp == null || dev.fp.isEmpty() ? "" : "指纹：" + dev.fp + "\n";
        List<Sheet.Action> actions = new ArrayList<>();
        actions.add(act(R.drawable.ic_link, "连接这台电脑",
                "首次连接需要在电脑页面点一次「允许」", getColor(R.color.brand),
                () -> startJoin(dev.name, dev.host, dev.port)));
        Sheet.show(this, "连接到「" + dev.name + "」",
                fp + "确认后，这台电脑可以接收你的录音和转写。\n"
                        + "打开电脑上的 http://127.0.0.1:18082/ ，在弹窗里确认。\n"
                        + "之后自动同步，不再询问。", actions);
    }

    private void startJoin(String name, String host, int port) {
        RecState.uploadStatus = "等待电脑确认...";
        refreshPcViews();
        Dialog wait = Sheet.progress(this, "等待电脑确认",
                "请在电脑上打开 http://127.0.0.1:18082/ ，点击「允许」。\n\n电脑：" + name,
                true, JoinClient::cancel);

        JoinClient.joinHost(this, host, port, (ok, msg) -> runOnUiThread(() -> {
            dismissQuietly(wait);
            if (isFinishing() || isDestroyed()) return;
            if (ok) {
                Settings.setBool(this, Settings.KEY_UPLOAD, true);
                swUpload.setChecked(true);
                    RecState.uploadStatus = "已连接：" + msg;
                toast("已连接「" + msg + "」，开始同步");
                refreshPcViews();
                Uploader.scanPendingAsync(this, true);
            } else if (!"已取消".equals(msg)) {
                List<Sheet.Action> actions = new ArrayList<>();
                actions.add(act(R.drawable.ic_refresh, "重试", "再发一次连接请求",
                        getColor(R.color.brand), () -> startJoin(name, host, port)));
                actions.add(act(R.drawable.ic_search, "重新发现电脑", null,
                        getColor(R.color.text_secondary), this::discoverPc));
                Sheet.show(this, "连接失败", msg == null ? "请重试" : msg, actions);
            }
        }));
    }

    private void manualPair() {
        String msg = Settings.connected(this)
                ? "修改地址后会重新连接，需要在新电脑上确认一次。"
                : "一般用「发现电脑」就够了；自动发现失败时才需要手输。";
        Sheet.input(this, Settings.connected(this) ? "修改电脑地址" : "手动输入电脑地址",
                msg + "\n手机和电脑要连同一个 Wi-Fi。",
                Settings.getPcUrl(this), "http://192.168.1.100:18082", "下一步", raw -> {
                    try {
                        String normalized = normalizeUrl(raw);
                        java.net.URL u = new java.net.URL(normalized);
                        String host = u.getHost();
                        if (host == null || host.isEmpty()) {
                            throw new IllegalArgumentException("地址里没有主机名");
                        }
                        int port = u.getPort() > 0 ? u.getPort() : 18082;
                        startJoin(host, host, port);
                    } catch (Exception e) {
                        toast("地址无效：" + safeMsg(e));
                    }
                });
    }

    private void confirmUnpair() {
        if (!Settings.connected(this)) {
            toast("当前没有连接电脑");
            return;
        }
        List<Sheet.Action> actions = new ArrayList<>();
        actions.add(act(R.drawable.ic_logout, "解除连接", "清除电脑地址和 token，录音不受影响",
                getColor(R.color.danger), () -> {
                    PairingClient.unpair(this);
                    Settings.setString(this, Settings.KEY_PC_ID, "");
                    Settings.setString(this, Settings.KEY_PC_FP, "");
                    Settings.setBool(this, Settings.KEY_UPLOAD, false);
                    swUpload.setChecked(false);
                            RecState.uploadStatus = "已解除连接";
                    refreshPcViews();
                    toast("已解除连接");
                }));
        Sheet.show(this, "解除连接？", "录音不受影响，之后可以重新连接。", actions);
    }

    private void maybeShowInvite() {
        long now = System.currentTimeMillis();
        if (now - lastInviteCheckMs < 1000) return;
        lastInviteCheckMs = now;

        if (Sheet.isShowing() || AudioPlayerSheet.isShowing()) return;
        String raw = Settings.getString(this, Settings.KEY_PENDING_INVITE, "");
        if (raw == null || raw.isEmpty()) return;
        Settings.setString(this, Settings.KEY_PENDING_INVITE, "");

        try {
            JSONObject inv = new JSONObject(raw);
            String host = inv.optString("host", "");
            String token = inv.optString("token", "");
            JSONObject pc = inv.optJSONObject("pc");
            if (host.isEmpty() || token.isEmpty() || pc == null) return;

            String name = pc.optString("name", host);
            String fp = pc.optString("fp", "");
            int port = pc.optInt("port", 18082);
            Notifications.cancelInvite(this);

            List<Sheet.Action> actions = new ArrayList<>();
            actions.add(act(R.drawable.ic_link, "允许连接", "之后自动同步，不再询问",
                    getColor(R.color.success),
                    () -> acceptInvite(name, host, port, token)));
            Sheet.show(this, "电脑「" + name + "」想连接",
                    (fp.isEmpty() ? "" : "指纹：" + fp + "\n")
                            + "允许后，这台电脑可以接收你的录音和转写。",
                    actions);
        } catch (Exception ignored) {
        }
    }

    private void acceptInvite(String name, String host, int port, String token) {
        RecState.uploadStatus = "连接中...";
        refreshPcViews();
        Dialog wait = Sheet.progress(this, "正在连接电脑",
                "正在确认「" + name + "」的邀请", true, JoinClient::cancel);
        JoinClient.acceptInvite(this, host, port, token, (ok, msg) -> runOnUiThread(() -> {
            dismissQuietly(wait);
            if (isFinishing() || isDestroyed()) return;
            if (ok) {
                Settings.setBool(this, Settings.KEY_UPLOAD, true);
                swUpload.setChecked(true);
                    RecState.uploadStatus = "已连接：" + msg;
                toast("已连接「" + msg + "」，开始同步");
                refreshPcViews();
                Uploader.scanPendingAsync(this, true);
            } else if (!"已取消".equals(msg)) {
                toast("连接失败：" + (msg == null || msg.isEmpty() ? "未知错误" : msg));
            }
        }));
    }

    // ---------------- 文件列表 ----------------

    private void selectFilter(int filter) {
        if (filter < 0 || filter > 2) filter = 0;
        fileFilter = filter;
        filesLimit = 30;
        for (int i = 0; i < filterChips.length; i++) {
            filterChips[i].setSelected(i == filter);
        }
        renderFiles();
    }

    private void refreshFiles(boolean userInitiated) {
        filesRefreshFeedback |= userInitiated;
        if (filesLoading) {
            filesRefreshAgain = true;
            return;
        }
        filesLoading = true;
        filesError = "";
        final int gen = ++filesGen;
        btnRefreshFiles.setEnabled(false);
        btnRefreshFiles.setAlpha(0.4f);
        btnRefreshFiles.setContentDescription("正在刷新文件列表");
        updateFilesSummary();

        FILES_EXEC.execute(() -> {
            try {
                File dir = RecorderService.recDirOf(MainActivity.this);
                File[] list = dir.listFiles((d, n) -> n.endsWith(".aac") || n.endsWith(".m4a"));
                if (list == null) {
                    if (!dir.exists()) list = new File[0];
                    else throw new java.io.IOException("无法读取录音目录");
                }
                Arrays.sort(list, (a, b) -> {
                    int byTime = Long.compare(b.lastModified(), a.lastModified());
                    return byTime != 0 ? byTime : b.getName().compareTo(a.getName());
                });

                long total = 0L;
                for (File f : list) total += f.length();

                UploadStore store = UploadStore.get(MainActivity.this);
                Set<String> uploaded = store.uploadedNames();
                Set<String> pending = store.pendingNames();
                Map<String, String> sums = new HashMap<>();
                for (UploadStore.SummaryRow row : store.recentSummaries(300)) {
                    if (row.summary != null && !row.summary.isEmpty()) {
                        sums.put(row.name, row.summary);
                    }
                }

                List<FileItem> items = new ArrayList<>();
                int pendingCount = 0;
                long sig = 17L;
                for (File f : list) {
                    String name = f.getName();
                    String state = uploaded.contains(name)
                            ? "uploaded"
                            : pending.contains(name) ? "pending" : "";
                    String summary = sums.get(name);
                    if (summary == null) summary = "";
                    FileItem item = new FileItem(f, name, f.length(), f.lastModified(), state, summary);
                    items.add(item);
                    if (!item.uploaded()) pendingCount++;

                    sig = sig * 31 + name.hashCode();
                    sig = sig * 31 + item.size;
                    sig = sig * 31 + item.modified;
                    sig = sig * 31 + state.hashCode();
                    sig = sig * 31 + summary.hashCode();
                }

                final long finalSig = sig;
                final List<FileItem> snapshot = items;
                final int finalPending = pendingCount;
                final long finalTotal = total;
                final int finalCount = list.length;

                runOnUiThread(() -> {
                    if (gen != filesGen || isFinishing() || isDestroyed()) return;
                    filesTotalCount = finalCount;
                    filesTotalBytes = finalTotal;
                    filesPendingCount = finalPending;
                    boolean firstLoad = !filesLoaded;
                    filesLoaded = true;
                    boolean changed = firstLoad || finalSig != filesSig;
                    filesSig = finalSig;
                    filesCache.clear();
                    filesCache.addAll(snapshot);
                    finishFilesRefresh();
                    if (changed) {
                        renderFiles();
                        renderRecent();
                    }
                    if (filesRefreshFeedback && !filesLoading) {
                        filesRefreshFeedback = false;
                        toast("已更新 · 共 " + finalCount + " 段录音");
                    }
                });
            } catch (Exception e) {
                runOnUiThread(() -> {
                    if (gen != filesGen || isFinishing() || isDestroyed()) return;
                    String message = "暂时无法读取录音，点刷新重试";
                    filesError = message;
                    finishFilesRefresh();
                    if (!filesLoaded) renderFiles();
                    if (filesRefreshFeedback) {
                        filesRefreshFeedback = false;
                        toast(message);
                    }
                });
            }
        });
    }

    private void finishFilesRefresh() {
        filesLoading = false;
        btnRefreshFiles.setEnabled(true);
        btnRefreshFiles.setAlpha(1f);
        btnRefreshFiles.setContentDescription("刷新文件列表");
        updateFilesSummary();
        if (filesRefreshAgain) {
            filesRefreshAgain = false;
            refreshFiles(false);
        }
    }

    private void updateFilesSummary() {
        if (tvFilesSummary == null) return;
        if (!filesError.isEmpty()) {
            tvFilesSummary.setText(filesError);
            return;
        }
        if (filesLoading) {
            tvFilesSummary.setText(filesLoaded ? "正在更新 · 已有 " + filesTotalCount + " 段录音"
                    : "正在读取录音文件…");
            return;
        }
        if (!filesLoaded) {
            tvFilesSummary.setText("正在读取录音文件…");
            return;
        }
        if (filesTotalCount == 0) {
            tvFilesSummary.setText(RecState.running ? "当前分段保存后会出现在这里" : "还没有录音文件");
            return;
        }
        int keep = Settings.getInt(this, Settings.KEY_KEEP_DAYS, 7);
        String keepText = keep <= 0 ? "不自动清理" : "保留 " + keep + " 天";
        String text = "共 " + filesTotalCount + " 段 · " + size(filesTotalBytes)
                + " · " + keepText;
        if (filesPendingCount > 0) {
            text += " · 未上传 " + filesPendingCount + " 段";
        }
        tvFilesSummary.setText(text);
    }

    private void renderFiles() {
        if (filesContainer == null) return;
        filesContainer.removeAllViews();

        if (!filesLoaded) {
            addEmpty(filesContainer, R.drawable.ic_refresh,
                    filesError.isEmpty() ? "正在读取录音…" : "录音列表暂不可用",
                    filesError.isEmpty() ? "稍等一下" : "已有录音不会受影响，点右上角刷新重试");
            btnLoadMore.setVisibility(View.GONE);
            return;
        }

        List<FileItem> filtered = filteredFiles();
        int today = 0;
        for (FileItem item : filesCache) if (isToday(item.modified)) today++;
        filterChips[0].setText("全部 " + filesTotalCount);
        filterChips[1].setText("今天 " + today);
        filterChips[2].setText("未上传 " + filesPendingCount);
        if (filtered.isEmpty()) {
            if (fileFilter == 1) {
                addEmpty(filesContainer, R.drawable.ic_clock, "今天还没有录音",
                        RecState.running ? "当前分段保存后会出现在这里" : "开始录音后，这里会显示今天的文件");
            } else if (fileFilter == 2) {
                addEmpty(filesContainer, R.drawable.ic_check_circle, "没有未上传的录音",
                        filesTotalCount > 0 ? "已保存的录音都已上传到电脑" : "当前还没有已保存的录音");
            } else {
                addEmpty(filesContainer, R.drawable.ic_mic, "还没有录音文件",
                        RecState.running ? "当前分段保存后即可播放" : "点「录音」开始第一段");
            }
            btnLoadMore.setVisibility(View.GONE);
            return;
        }

        int limit = Math.min(filesLimit, filtered.size());
        String previousDay = "";
        SimpleDateFormat dayFormat = new SimpleDateFormat("yyyy年M月d日", Locale.CHINA);
        for (int i = 0; i < limit; i++) {
            FileItem item = filtered.get(i);
            String day = dayFormat.format(new Date(item.modified));
            if (!day.equals(previousDay)) {
                TextView heading = new TextView(this);
                heading.setText(isToday(item.modified) ? "今天 · " + day : day);
                heading.setTextSize(12);
                heading.setTextColor(getColor(R.color.text_secondary));
                heading.setTypeface(android.graphics.Typeface.DEFAULT, android.graphics.Typeface.BOLD);
                heading.setPadding(dp(16), dp(i == 0 ? 16 : 24), dp(16), dp(8));
                heading.setAccessibilityHeading(true);
                filesContainer.addView(heading);
                previousDay = day;
            } else {
                addDivider(filesContainer, 36);
            }
            bindFileRow(filesContainer, item);
        }

        if (filtered.size() > limit) {
            btnLoadMore.setText("再显示 30 段（还有 " + (filtered.size() - limit) + " 段）");
            btnLoadMore.setVisibility(View.VISIBLE);
        } else {
            btnLoadMore.setVisibility(View.GONE);
        }
    }

    private List<FileItem> filteredFiles() {
        List<FileItem> out = new ArrayList<>();
        for (FileItem item : filesCache) {
            if (fileFilter == 1 && !isToday(item.modified)) continue;
            if (fileFilter == 2 && item.uploaded()) continue;
            out.add(item);
        }
        return out;
    }

    private void bindFileRow(LinearLayout parent, FileItem item) {
        View row = LayoutInflater.from(this).inflate(R.layout.item_file_row, parent, false);
        TextView name = row.findViewById(R.id.tvFileName);
        TextView meta = row.findViewById(R.id.tvFileMeta);
        TextView summary = row.findViewById(R.id.tvFileSummary);
        TextView state = row.findViewById(R.id.tvFileState);
        View play = row.findViewById(R.id.btnQuickPlay);

        name.setText(new SimpleDateFormat("HH:mm:ss", Locale.CHINA).format(new Date(item.modified)));
        meta.setText(size(item.size) + (item.name.endsWith(".m4a") ? " · M4A" : " · AAC"));
        play.setContentDescription("播放 " + friendlyDate(item.modified));
        if (item.summary == null || item.summary.isEmpty()) {
            summary.setVisibility(View.GONE);
        } else {
            summary.setVisibility(View.VISIBLE);
            summary.setText(item.summary);
        }
        bindStateChip(state, item);

        row.setOnClickListener(v -> showFileActions(item));
        play.setOnClickListener(v -> playFile(item.file));
        parent.addView(row);
    }

    private void renderRecent() {
        if (recentContainer == null) return;
        recentContainer.removeAllViews();

        if (!filesLoaded) {
            addEmpty(recentContainer, R.drawable.ic_refresh, "正在读取录音…", "稍等一下");
            return;
        }
        if (filesCache.isEmpty()) {
            addEmpty(recentContainer, R.drawable.ic_mic, "还没有录音", "点上方按钮开始第一段");
            return;
        }

        int show = Math.min(filesCache.size(), 3);
        for (int i = 0; i < show; i++) {
            if (i > 0) addDivider(recentContainer, 28);
            FileItem item = filesCache.get(i);
            View row = LayoutInflater.from(this).inflate(R.layout.item_recent_row, recentContainer, false);
            TextView name = row.findViewById(R.id.tvRecentName);
            TextView meta = row.findViewById(R.id.tvRecentMeta);
            View play = row.findViewById(R.id.btnRecentPlay);

            name.setText(friendlyDate(item.modified));
            meta.setText(size(item.size) + " · " + stateText(item));
            row.setOnClickListener(v -> showFileActions(item));
            play.setOnClickListener(v -> playFile(item.file));
            recentContainer.addView(row);
        }
    }

    private void bindStateChip(TextView view, FileItem item) {
        if (item.uploaded()) {
            view.setText("已上传");
            view.setBackgroundResource(R.drawable.bg_chip_green);
            view.setTextColor(getColor(R.color.success));
        } else if (item.pending()) {
            view.setText("待上传");
            view.setBackgroundResource(R.drawable.bg_chip_amber);
            view.setTextColor(getColor(R.color.warning));
        } else {
            view.setText("本地");
            view.setBackgroundResource(R.drawable.bg_chip_gray);
            view.setTextColor(getColor(R.color.text_secondary));
        }
    }

    private static String stateText(FileItem item) {
        if (item.uploaded()) return "已上传";
        if (item.pending()) return "待上传";
        return "本地";
    }

    private void addDivider(LinearLayout parent, int startDp) {
        View divider = new View(this);
        divider.setBackgroundColor(getColor(R.color.divider));
        LinearLayout.LayoutParams lp = new LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT, dp(1));
        lp.leftMargin = dp(startDp);
        parent.addView(divider, lp);
    }

    private void addEmpty(LinearLayout parent, int icon, String title, String subtitle) {
        View empty = LayoutInflater.from(this).inflate(R.layout.item_empty, parent, false);
        ImageView iv = empty.findViewById(R.id.emptyIcon);
        TextView tvTitle = empty.findViewById(R.id.emptyTitle);
        TextView tvSub = empty.findViewById(R.id.emptySubtitle);
        iv.setImageResource(icon);
        tvTitle.setText(title);
        tvSub.setText(subtitle);
        parent.addView(empty);
    }

    // ---------------- 文件操作 ----------------

    private void showFileActions(FileItem item) {
        List<Sheet.Action> actions = new ArrayList<>();
        String jobId = UploadStore.get(this).jobId(item.name);
        if (jobId != null && !jobId.isEmpty()) {
            actions.add(act(R.drawable.ic_audio, "转写与说话人", "查看谁说了什么，点标签可以标记姓名",
                    getColor(R.color.brand),
                    () -> TranscriptDialog.show(this, jobId, friendlyDate(item.modified), () -> refreshFiles(false))));
        }
        actions.add(act(R.drawable.ic_play, "播放", "在应用内试听这一段",
                getColor(R.color.brand), () -> playFile(item.file)));
        actions.add(act(R.drawable.ic_share, "分享", "发送给其他应用",
                getColor(R.color.text_secondary), () -> shareFile(item.file)));
        actions.add(act(R.drawable.ic_delete, "删除", "删除后无法恢复",
                getColor(R.color.danger), () -> confirmDelete(item)));

        Sheet.show(this, friendlyDate(item.modified),
                item.name + "\n" + size(item.size) + " · " + stateText(item), actions);
    }

    private void confirmDelete(FileItem item) {
        if (item.name.equals(RecState.currentFile)) {
            toast("正在录制的分段不能删除");
            return;
        }
        List<Sheet.Action> actions = new ArrayList<>();
        actions.add(act(R.drawable.ic_delete, "确认删除",
                "删除后无法恢复", getColor(R.color.danger), () -> doDelete(item)));
        Sheet.show(this, "删除这一段？", item.name, actions);
    }

    private void doDelete(FileItem item) {
        boolean ok = item.file.delete();
        if (ok) {
            UploadStore.get(this).markUploaded(item.name, "");
            toast("已删除");
        } else {
            toast("删除失败，文件可能已被占用");
        }
        lastFilesMs = System.currentTimeMillis();
        refreshFiles(false);
    }

    private void playFile(File f) {
        AudioPlayerSheet.show(this, f);
    }

    private void shareFile(File f) {
        try {
            Uri uri = RecFileProvider.uriForRec(f);
            Intent share = new Intent(Intent.ACTION_SEND);
            share.setType("audio/*");
            share.putExtra(Intent.EXTRA_STREAM, uri);
            share.setClipData(ClipData.newRawUri(f.getName(), uri));
            share.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);
            startActivity(Intent.createChooser(share, "分享录音"));
        } catch (Exception e) {
            toast("无法分享：" + safeMsg(e));
        }
    }

    private void showPath() {
        File dir = RecorderService.recDirOf(this);
        List<Sheet.Action> actions = new ArrayList<>();
        actions.add(act(R.drawable.ic_storage, "复制路径", "复制到剪贴板",
                getColor(R.color.brand), () -> {
                    copyText(dir.getAbsolutePath());
                    toast("路径已复制");
                }));
        Sheet.show(this, "录音保存位置",
                dir.getAbsolutePath()
                        + "\n\n用数据线连电脑后，可在 Android/data/com.recnonstop.app/files/rec 里找到。"
                        + "\n也可以直接在「记录」页点一段播放或分享。",
                actions);
    }

    private void exportDiag() {
        try {
            File log = Diag.export(this);
            Uri uri = RecFileProvider.uriForLog(log);
            Intent share = new Intent(Intent.ACTION_SEND);
            share.setType("text/plain");
            share.putExtra(Intent.EXTRA_STREAM, uri);
            share.putExtra(Intent.EXTRA_SUBJECT, "录不停诊断日志");
            share.setClipData(ClipData.newRawUri("log", uri));
            share.addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION);

            JSONObject h = Health.read(this);
            String extra = "\n\n最近错误：" + h.optString("last_error", "-")
                    + "\n最后一段：" + h.optString("last_segment_name", "-");
            share.putExtra(Intent.EXTRA_TEXT, extra);
            startActivity(Intent.createChooser(share, "导出诊断日志"));
        } catch (Exception e) {
            toast("导出失败：" + safeMsg(e));
        }
    }

    private void copyText(String text) {
        ClipboardManager cm = (ClipboardManager) getSystemService(CLIPBOARD_SERVICE);
        if (cm != null) {
            cm.setPrimaryClip(ClipData.newPlainText("recnonstop", text));
        }
    }

    private static void dismissQuietly(Dialog dialog) {
        if (dialog == null) return;
        try {
            dialog.dismiss();
        } catch (Exception ignored) {
        }
    }

    // ---------------- 状态刷新 ----------------

    private void refreshUi() {
        maybeShowInvite();
        refreshHero();
        refreshTopChip();
        refreshStats();
        refreshAttention();
        refreshPcViews();

        long now = System.currentTimeMillis();
        if (now - lastFilesMs >= 15000) {
            lastFilesMs = now;
            refreshFiles(false);
        }
    }

    private void refreshHero() {
        long now = System.currentTimeMillis();

        if (awaitingStart && RecState.running && !recordingWaiting()) {
            awaitingStart = false;
            toast(RecState.paused ? "通话结束后会自动开始录音" : "已开始录音");
        } else if (awaitingStart && now - startRequestMs > 8000) {
            awaitingStart = false;
            toast(RecState.running ? "暂未开始录音，正在自动重试" : "启动超时，请检查麦克风或通知权限");
        }
        if (awaitingStop && !RecState.running) {
            awaitingStop = false;
            toast("录音已停止，可在记录页查看已保存分段");
            lastFilesMs = now;
            refreshFiles(false);
        } else if (awaitingStop && now - stopRequestMs > 8000) {
            awaitingStop = false;
            toast("停止请求已发送");
        }

        boolean running = RecState.running;
        boolean paused = running && RecState.paused;
        boolean low = running && RecState.lowLevel;
        boolean waiting = recordingWaiting();
        tvFilesRecordingHint.setVisibility(running ? View.VISIBLE : View.GONE);
        tvFilesRecordingHint.setText(waiting || paused
                ? "录音暂时暂停，已保存的分段仍可播放。"
                : "当前分段正在录制，保存后会出现在列表中。");

        if (running) {
            long pausedMs = RecState.pausedTotalMs;
            if (RecState.paused && RecState.pausedAt > 0L) {
                pausedMs += Math.max(0L, now - RecState.pausedAt);
            }
            long total = RecState.recordStartMs > 0
                    ? Math.max(0L, now - RecState.recordStartMs - pausedMs) : 0L;
            tvHeroTime.setText(fmt(total));

            long segMax = Settings.segmentMs(this);
            long segMs = RecState.segmentStartMs > 0
                    ? Math.max(0L, now - RecState.segmentStartMs) : 0L;
            int progress = segMax > 0
                    ? (int) Math.min(100L, segMs * 100L / segMax) : 0;

            if (awaitingStop) {
                tvHeroStatus.setText("正在停止…");
                tvHeroHint.setText("正在保存当前分段");
                tvSegmentInfo.setText("请稍候");
                segmentProgress.setProgress(0);
                heroDot.setBackgroundResource(R.drawable.bg_dot_amber);
                tvHeroSub.setText("保存后可在记录页查看");
            } else if (waiting) {
                boolean storage = recordingStorageBlocked();
                boolean hasError = RecState.lastError != null && !RecState.lastError.isEmpty();
                tvHeroStatus.setText(storage ? "空间不足 · 已暂停"
                        : hasError ? "等待恢复录音" : "准备录音");
                tvHeroTime.setText("--:--:--");
                tvHeroHint.setText("当前未录音");
                tvSegmentInfo.setText(storage ? "腾出空间后自动继续" : "正在打开麦克风");
                segmentProgress.setProgress(0);
                heroDot.setBackgroundResource(R.drawable.bg_dot_amber);
                tvHeroSub.setText(storage ? "去记录页清理已备份的旧录音"
                        : hasError ? "请检查麦克风权限，或结束其他应用的录音"
                        : "准备完成后自动继续，请稍候");
            } else if (paused) {
                tvHeroStatus.setText("通话中，已暂停");
                tvHeroHint.setText("挂断后自动继续录音");
                tvSegmentInfo.setText("通话暂停中 · 挂断后开始新段");
                segmentProgress.setProgress(0);
                heroDot.setBackgroundResource(R.drawable.bg_dot_amber);
                tvHeroSub.setText("通话内容不会被录入");
            } else if (low) {
                tvHeroStatus.setText("录音中 · 音量偏低");
                tvHeroHint.setText("靠近一点说，或检查麦克风是否被占用");
                tvSegmentInfo.setText("本段 " + fmt(segMs) + " / " + fmt(segMax));
                segmentProgress.setProgress(progress);
                heroDot.setBackgroundResource(R.drawable.bg_dot_amber);
                tvHeroSub.setText("本次第 " + RecState.segmentCount + " 段 · 来电自动暂停");
            } else {
                tvHeroStatus.setText("录音中");
                tvHeroHint.setText("本次会话时长");
                tvSegmentInfo.setText("本段 " + fmt(segMs) + " / " + fmt(segMax));
                segmentProgress.setProgress(progress);
                heroDot.setBackgroundResource(R.drawable.bg_dot_green);
                tvHeroSub.setText("本次第 " + RecState.segmentCount + " 段 · 来电自动暂停");
            }
            recordIcon.setImageResource(R.drawable.ic_stop);
            recordIcon.setColorFilter(getColor(R.color.danger));
            recordButton.setContentDescription("停止录音");
            setRecordPulse(false);
            setDotPulse(!paused && !waiting && !awaitingStop);
        } else if (awaitingStart) {
            tvHeroStatus.setText("正在启动…");
            tvHeroHint.setText("正在打开麦克风");
            tvHeroTime.setText("00:00:00");
            tvSegmentInfo.setText("准备开始第一段");
            segmentProgress.setProgress(0);
            heroDot.setBackgroundResource(R.drawable.bg_dot_blue);
            tvHeroSub.setText("录音会在锁屏后继续");
            recordIcon.setImageResource(R.drawable.ic_mic);
            recordIcon.setColorFilter(getColor(R.color.brand));
            recordButton.setContentDescription("正在启动录音");
            setRecordPulse(false);
            setDotPulse(false);
        } else if (awaitingStop) {
            tvHeroStatus.setText("正在停止…");
            tvHeroHint.setText("正在保存当前分段");
            tvSegmentInfo.setText("分段会正常关闭");
            segmentProgress.setProgress(0);
            heroDot.setBackgroundResource(R.drawable.bg_dot_amber);
            tvHeroSub.setText("已录内容不会丢失");
            recordIcon.setImageResource(R.drawable.ic_mic);
            recordIcon.setColorFilter(getColor(R.color.brand));
            recordButton.setContentDescription("正在停止录音");
            setRecordPulse(false);
            setDotPulse(false);
        } else {
            tvHeroStatus.setText("待机中");
            tvHeroHint.setText("按下开始，记录此刻");
            tvHeroTime.setText("00:00:00");
            tvSegmentInfo.setText("分段保存 · 每 " + Settings.getInt(this, Settings.KEY_SEGMENT_MIN, 3) + " 分钟一段");
            segmentProgress.setProgress(0);
            heroDot.setBackgroundResource(R.drawable.bg_dot_gray);
            JSONObject h = health();
            long last = h.optLong("last_segment_at", 0L);
            tvHeroSub.setText(last > 0 ? "上次成功录音：" + ago(last) : "来电自动暂停，挂断后自动续录");
            recordIcon.setImageResource(R.drawable.ic_mic);
            recordIcon.setColorFilter(getColor(R.color.brand));
            recordButton.setContentDescription("开始录音");
            setRecordPulse(false);
            setDotPulse(false);
        }
        String action = awaitingStop ? "正在保存…" : awaitingStart ? "正在启动…"
                : running ? "停止录音" : "开始录音";
        tvRecordAction.setText(action);
        recordButton.setContentDescription(action);
        recordButton.setEnabled(!awaitingStart && !awaitingStop);
        recordButton.setAlpha(awaitingStart || awaitingStop ? .65f : 1f);
        recordingWaveform.update(running && !paused && !waiting && !awaitingStop);
    }

    private void refreshTopChip() {
        if (awaitingStop) {
            setTopChip("停止中", R.drawable.bg_chip_amber, R.color.warning);
        } else if (RecState.running) {
            if (RecState.paused) {
                setTopChip("通话暂停", R.drawable.bg_chip_amber, R.color.warning);
            } else if (recordingWaiting()) {
                setTopChip(recordingStorageBlocked() ? "空间不足" : "等待录音",
                        R.drawable.bg_chip_amber, R.color.warning);
            } else if (RecState.lowLevel) {
                setTopChip("音量偏低", R.drawable.bg_chip_amber, R.color.warning);
            } else {
                setTopChip("录音中", R.drawable.bg_chip_green, R.color.success);
            }
        } else if (awaitingStart) {
            setTopChip("启动中", R.drawable.bg_chip_blue, R.color.brand);
        } else {
            setTopChip("待机", R.drawable.bg_chip_gray, R.color.text_secondary);
        }
    }

    private static boolean recordingWaiting() {
        return RecState.running && !RecState.paused
                && (RecState.currentFile == null || RecState.currentFile.isEmpty());
    }

    private static boolean recordingStorageBlocked() {
        return recordingWaiting() && RecState.lastError != null
                && RecState.lastError.contains("存储空间不足");
    }

    private void setTopChip(String text, int background, int color) {
        tvTopStatus.setText(text);
        tvTopStatus.setBackgroundResource(background);
        tvTopStatus.setTextColor(getColor(color));
    }

    private void refreshStats() {
        JSONObject h = health();
        tvStatSegments.setText(String.valueOf(h.optInt("segments_today", 0)));
        tvStatBytes.setText(size(h.optLong("bytes_today", 0L)));
        tvStatGaps.setText(String.valueOf(h.optInt("gaps_today", 0)));
    }

    private void refreshAttention() {
        long now = System.currentTimeMillis();
        if (now - lastAttentionMs < 1800) return;
        lastAttentionMs = now;
        updateBatteryValue();

        int action = ATTENTION_NONE;
        String title = "";
        String desc = "";

        if (!hasAudioPerm()) {
            action = ATTENTION_PERMISSION;
            title = "缺少麦克风权限";
            desc = "点此去系统设置授权，否则无法录音";
        } else if (recordingStorageBlocked()) {
            action = ATTENTION_STORAGE;
            title = "空间不足，当前没有录音";
            desc = "点此查看文件，清理已备份的旧录音后会自动继续";
        } else if (recordingWaiting() && !RecState.lastError.isEmpty()) {
            action = ATTENTION_DIAG;
            title = "录音暂时中断，正在重试";
            desc = "检查麦克风是否被占用 · 点此导出诊断日志";
        } else if (Build.VERSION.SDK_INT >= 33
                && checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS)
                != PackageManager.PERMISSION_GRANTED) {
            action = ATTENTION_PERMISSION;
            title = "通知权限未开启";
            desc = "前台录音通知会被隐藏，建议打开";
        } else if (!batteryIgnored()) {
            action = ATTENTION_BATTERY;
            title = "后台可能被系统杀掉";
            desc = "点此关闭电池优化，提高锁屏录音稳定性";
        } else {
            SharedPreferences sp = getSharedPreferences(RecorderService.PREFS, MODE_PRIVATE);
            JSONObject h = health();
            long bootBlocked = h.optLong("boot_blocked_at", 0L);
            if (bootBlocked > 0 && sp.getBoolean("enabled", false) && !RecState.running) {
                action = ATTENTION_AUTOSTART;
                title = "上次开机自启被系统拦截";
                desc = "点此设置自启动 / 后台运行";
            } else if (!RecState.lastError.isEmpty()) {
                action = ATTENTION_DIAG;
                title = "上次录音出现错误";
                desc = truncate(RecState.lastError, 90) + " · 点此导出日志";
            }
        }

        attentionAction = action;
        if (action == ATTENTION_NONE) {
            attentionCard.setVisibility(View.GONE);
        } else {
            attentionCard.setVisibility(View.VISIBLE);
            tvAttentionTitle.setText(title);
            tvAttentionDesc.setText(desc);
        }
    }

    private void onAttentionClick() {
        switch (attentionAction) {
            case ATTENTION_PERMISSION:
                openAppSettings();
                break;
            case ATTENTION_BATTERY:
                requestBatteryWhitelist();
                break;
            case ATTENTION_AUTOSTART:
                openAutostart();
                break;
            case ATTENTION_DIAG:
                exportDiag();
                break;
            case ATTENTION_STORAGE:
                selectTab(TAB_FILES, true);
                break;
            default:
                break;
        }
    }

    private void refreshPcViews() {
        if (tvPcTitle == null) return;
        boolean connected = Settings.connected(this);
        String pcName = Settings.getString(this, Settings.KEY_PC_NAME, "");
        boolean upload = Settings.getBool(this, Settings.KEY_UPLOAD, false);
        int[] counts = counts();
        int failed = counts[2] + counts[3];
        String us = RecState.uploadStatus == null ? "" : RecState.uploadStatus;
        boolean sending = us.startsWith("上传中") || us.startsWith("排队上传");
        boolean analyzing = us.startsWith("分析中") || us.startsWith("已上传，分析");

        if (connected) {
            tvPcTitle.setText(pcName == null || pcName.isEmpty() ? "我的电脑" : pcName);
            tvPcSub.setText(Settings.getPcUrl(this));
        } else {
            tvPcTitle.setText("未连接电脑");
            tvPcSub.setText("配对后可自动同步已保存的录音");
        }

        if (!connected) {
            tvPcBadge.setText("未连接");
            tvPcBadge.setBackgroundResource(R.drawable.bg_chip_gray);
            tvPcBadge.setTextColor(getColor(R.color.text_secondary));
        } else if (!upload) {
            tvPcBadge.setText("已暂停");
            tvPcBadge.setBackgroundResource(R.drawable.bg_chip_amber);
            tvPcBadge.setTextColor(getColor(R.color.warning));
        } else if (sending || analyzing) {
            tvPcBadge.setText(sending ? "同步中" : "分析中");
            tvPcBadge.setBackgroundResource(R.drawable.bg_chip_blue);
            tvPcBadge.setTextColor(getColor(R.color.brand));
        } else if (failed > 0 || us.contains("失败") || us.contains("失效")) {
            tvPcBadge.setText(counts[3] > 0 ? "需检查" : "待重试");
            tvPcBadge.setBackgroundResource(R.drawable.bg_chip_amber);
            tvPcBadge.setTextColor(getColor(R.color.warning));
        } else {
            tvPcBadge.setText("已配对");
            tvPcBadge.setBackgroundResource(R.drawable.bg_chip_gray);
            tvPcBadge.setTextColor(getColor(R.color.text_secondary));
        }

        tvPcStatus.setText(syncStatusText(connected, upload, us, failed));

        tvPcStats.setVisibility(connected ? View.VISIBLE : View.GONE);
        String stats = "已上传 " + counts[0] + " 段 · 待上传 " + (counts[1] + counts[2]) + " 段";
        if (counts[3] > 0) stats += " · 需检查 " + counts[3] + " 段";
        tvPcStats.setText(stats);

        String summary = RecState.lastSummary;
        if (connected && summary != null && !summary.isEmpty()) {
            tvPcSummary.setVisibility(View.VISIBLE);
            tvPcSummary.setText("最近摘要：" + truncate(summary, 180));
        } else {
            tvPcSummary.setVisibility(View.GONE);
        }

        btnPcAction.setText(!connected ? "发现电脑" : upload ? "立即同步" : "开启同步");
        btnManualPair.setText(connected ? "修改电脑地址" : "手动输入电脑地址");
    }

    private static String syncStatusText(boolean connected, boolean upload, String status, int failed) {
        if (!connected) return "先连接你的电脑，录音会继续保存在手机。";
        if (!upload) return "自动同步已暂停，新的录音仍会保存在手机。";
        if (status.contains("失效") || status.contains("401")) return "连接授权已失效，请重新连接电脑。";
        if (status.contains("队列已满")) return "电脑正在处理积压录音，稍后会自动重试上传。";
        if (status.startsWith("文件无法上传")) return "有录音未被电脑接收，可在记录页查看并重试同步。";
        if (status.startsWith("等待 Wi-Fi")) return "等待 Wi-Fi，连接后会自动上传已保存的录音。";
        if (status.startsWith("上传中") || status.startsWith("排队上传")) return "正在同步已保存的录音，当前录音不会中断。";
        if (status.startsWith("分析中") || status.startsWith("已上传，分析")) return "录音已上传，电脑正在生成转写和摘要。";
        if (status.startsWith("分析完成")) return "最近一段分析完成，可在记录页查看转写。";
        if (status.startsWith("分析失败")) return "电脑分析未完成，已上传的录音仍然保留。";
        if (status.startsWith("电脑连接正常")) return "电脑服务可访问，可以开始同步录音。";
        if (status.contains("失败") || status.contains("任务丢失") || failed > 0) {
            return "有录音等待重试。请确认电脑已启动且网络可达，连接恢复后会自动补传。";
        }
        if (status.startsWith("测试连接")) return "正在检查电脑是否可访问…";
        if (status.startsWith("正在检查")) return status;
        return "已开启自动同步，当前分段保存后会上传到电脑。";
    }

    private JSONObject health() {
        long now = System.currentTimeMillis();
        if (now - lastHealthMs > 3000) {
            lastHealthMs = now;
            cachedHealth = Health.read(this);
        }
        return cachedHealth;
    }

    private int[] counts() {
        long now = System.currentTimeMillis();
        if (now - lastCountsMs > 5000) {
            lastCountsMs = now;
            cachedCounts = UploadStore.get(this).counts();
        }
        return cachedCounts;
    }

    // ---------------- 动画 ----------------

    private void setRecordPulse(boolean on) {
        if (on) {
            if (pulseAnimator != null && pulseAnimator.isRunning()) return;
            pulseRing.setVisibility(View.VISIBLE);
            pulseRing.setScaleX(1f);
            pulseRing.setScaleY(1f);
            pulseRing.setAlpha(0.85f);
            pulseAnimator = ObjectAnimator.ofPropertyValuesHolder(pulseRing,
                    android.animation.PropertyValuesHolder.ofFloat(View.SCALE_X, 1f, 1.35f),
                    android.animation.PropertyValuesHolder.ofFloat(View.SCALE_Y, 1f, 1.35f),
                    android.animation.PropertyValuesHolder.ofFloat(View.ALPHA, 0.85f, 0f));
            pulseAnimator.setDuration(1500);
            pulseAnimator.setRepeatCount(ObjectAnimator.INFINITE);
            pulseAnimator.setInterpolator(new AccelerateDecelerateInterpolator());
            pulseAnimator.start();
        } else {
            if (pulseAnimator != null) {
                pulseAnimator.cancel();
                pulseAnimator = null;
            }
            pulseRing.setVisibility(View.GONE);
            pulseRing.setScaleX(1f);
            pulseRing.setScaleY(1f);
            pulseRing.setAlpha(1f);
        }
    }

    private void setDotPulse(boolean on) {
        if (on) {
            if (dotAnimator != null && dotAnimator.isRunning()) return;
            dotAnimator = ObjectAnimator.ofFloat(heroDot, View.ALPHA, 1f, 0.35f);
            dotAnimator.setDuration(850);
            dotAnimator.setRepeatCount(ObjectAnimator.INFINITE);
            dotAnimator.setRepeatMode(ObjectAnimator.REVERSE);
            dotAnimator.setInterpolator(new AccelerateDecelerateInterpolator());
            dotAnimator.start();
        } else {
            if (dotAnimator != null) {
                dotAnimator.cancel();
                dotAnimator = null;
            }
            heroDot.setAlpha(1f);
        }
    }

    // ---------------- 轻提示 ----------------

    private void toast(String message) {
        if (message == null || message.isEmpty() || root == null) return;
        if (snackbar != null) {
            root.removeView(snackbar);
            snackbar = null;
        }

        TextView tv = new TextView(this);
        tv.setText(message);
        tv.setTextSize(13);
        tv.setTextColor(getColor(R.color.white));
        tv.setGravity(Gravity.CENTER);
        tv.setMaxWidth(dp(420));
        tv.setBackgroundResource(R.drawable.bg_snackbar);
        tv.setPadding(dp(16), dp(11), dp(16), dp(11));
        tv.setElevation(dp(8));
        tv.setAlpha(0f);
        tv.setTranslationY(dp(12));

        FrameLayout.LayoutParams lp = new FrameLayout.LayoutParams(
                ViewGroup.LayoutParams.WRAP_CONTENT,
                ViewGroup.LayoutParams.WRAP_CONTENT);
        lp.gravity = Gravity.BOTTOM | Gravity.CENTER_HORIZONTAL;
        lp.bottomMargin = dp(82);
        root.addView(tv, lp);
        snackbar = tv;

        tv.animate().alpha(1f).translationY(0f).setDuration(180).start();
        final TextView current = tv;
        handler.postDelayed(() -> {
            if (snackbar != current) return;
            current.animate()
                    .alpha(0f)
                    .translationY(dp(12))
                    .setDuration(150)
                    .withEndAction(() -> {
                        root.removeView(current);
                        if (snackbar == current) snackbar = null;
                    })
                    .start();
        }, 2600);
    }

    // ---------------- 工具 ----------------

    private Sheet.Action act(int icon, String title, String subtitle, int tint, Runnable run) {
        return new Sheet.Action(icon, title, subtitle, tint, run);
    }

    private void setVersionText() {
        String version = "";
        try {
            version = getPackageManager().getPackageInfo(getPackageName(), 0).versionName;
        } catch (Exception ignored) {
        }
        tvVersion.setText(version == null || version.isEmpty()
                ? "录不停"
                : "录不停 " + version + " · 数据默认只保存在本机");
    }

    private int dp(int value) {
        return Math.round(value * getResources().getDisplayMetrics().density);
    }

    private static String normalizeUrl(String raw) {
        String s = raw == null ? "" : raw.trim();
        if (!s.startsWith("http://") && !s.startsWith("https://")) {
            s = "http://" + s;
        }
        while (s.endsWith("/")) s = s.substring(0, s.length() - 1);
        return s;
    }

    private static String size(long bytes) {
        if (bytes <= 0) return "0 MB";
        double kb = bytes / 1024.0;
        if (kb < 1024) return String.format(Locale.US, "%.0f KB", kb);
        double mb = kb / 1024.0;
        if (mb < 1024) return String.format(Locale.US, "%.1f MB", mb);
        return String.format(Locale.US, "%.2f GB", mb / 1024.0);
    }

    private static String friendlyDate(long ms) {
        Calendar now = Calendar.getInstance();
        Calendar then = Calendar.getInstance();
        then.setTimeInMillis(ms);
        String day;
        if (sameDay(now, then)) {
            day = "今天";
        } else {
            Calendar yesterday = Calendar.getInstance();
            yesterday.add(Calendar.DAY_OF_YEAR, -1);
            if (sameDay(yesterday, then)) {
                day = "昨天";
            } else if (now.get(Calendar.YEAR) == then.get(Calendar.YEAR)) {
                day = (then.get(Calendar.MONTH) + 1) + "月" + then.get(Calendar.DAY_OF_MONTH) + "日";
            } else {
                day = then.get(Calendar.YEAR) + "年"
                        + (then.get(Calendar.MONTH) + 1) + "月"
                        + then.get(Calendar.DAY_OF_MONTH) + "日";
            }
        }
        String time = new SimpleDateFormat("HH:mm:ss", Locale.US).format(new Date(ms));
        return day + " " + time;
    }

    private static boolean isToday(long ms) {
        Calendar now = Calendar.getInstance();
        Calendar then = Calendar.getInstance();
        then.setTimeInMillis(ms);
        return sameDay(now, then);
    }

    private static boolean sameDay(Calendar a, Calendar b) {
        return a.get(Calendar.YEAR) == b.get(Calendar.YEAR)
                && a.get(Calendar.DAY_OF_YEAR) == b.get(Calendar.DAY_OF_YEAR);
    }

    private static String ago(long ms) {
        long d = System.currentTimeMillis() - ms;
        if (d < 0) d = 0;
        if (d < 60_000) return (d / 1000) + " 秒前";
        if (d < 3600_000) return (d / 60_000) + " 分钟前";
        if (d < 86_400_000) return (d / 3_600_000) + " 小时前";
        return (d / 86_400_000) + " 天前";
    }

    private static String fmt(long ms) {
        long s = ms / 1000;
        return String.format(Locale.US, "%02d:%02d:%02d",
                s / 3600, (s % 3600) / 60, s % 60);
    }

    private static String truncate(String s, int max) {
        if (s == null) return "";
        s = s.replace('\n', ' ');
        return s.length() <= max ? s : s.substring(0, max) + "…";
    }

    private static String safeMsg(Exception e) {
        String msg = e == null ? "" : e.getMessage();
        return msg == null || msg.isEmpty() ? "未知错误" : msg;
    }

    private static final class FileItem {
        final File file;
        final String name;
        final long size;
        final long modified;
        final String state;
        final String summary;

        FileItem(File file, String name, long size, long modified, String state, String summary) {
            this.file = file;
            this.name = name;
            this.size = size;
            this.modified = modified;
            this.state = state == null ? "" : state;
            this.summary = summary == null ? "" : summary;
        }

        boolean uploaded() {
            return "uploaded".equals(state);
        }

        boolean pending() {
            return "pending".equals(state);
        }
    }
}
