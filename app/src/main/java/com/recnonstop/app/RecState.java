package com.recnonstop.app;

/** 进程内共享状态，Activity 轮询显示 */
public class RecState {
    public static volatile boolean running = false;
    public static volatile boolean paused = false;
    public static volatile boolean lowLevel = false;
    public static volatile float levelDb = -90f;
    public static volatile long levelSampleMs = 0L;
    public static volatile String currentFile = "";
    public static volatile long segmentStartMs = 0L;
    public static volatile long recordStartMs = 0L;
    public static volatile long pausedAt = 0L;
    public static volatile long pausedTotalMs = 0L;
    public static volatile int segmentCount = 0;
    public static volatile String lastError = "";

    public static volatile String uploadStatus = "";
    public static volatile String lastJobId = "";
    public static volatile String lastSummary = "";
    public static volatile int uploadedCount = 0;
    public static volatile int pendingCount = 0;
}
