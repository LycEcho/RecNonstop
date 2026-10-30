package main

import (
	"context"
	"errors"
	"flag"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"warehouse-audio/internal/api"
	"warehouse-audio/internal/config"
	"warehouse-audio/internal/discovery"
	"warehouse-audio/internal/identity"
	"warehouse-audio/internal/infer"
	"warehouse-audio/internal/job"
	"warehouse-audio/internal/merge"
	"warehouse-audio/internal/pairing"
	"warehouse-audio/internal/pipeline"
	"warehouse-audio/internal/report"
)

func main() {
	configPath := flag.String("config", "./configs/config.json", "path to config file")
	openPair := flag.Bool("pair", false, "open pairing window at startup even if devices exist")
	flag.Parse()

	cfg, err := config.Load(*configPath)
	if err != nil {
		log.Fatalf("load config: %v", err)
	}

	if cfg.Server.Addr == "" {
		cfg.Server.Addr = "0.0.0.0:18082"
	}
	if cfg.Server.MaxUploadMB <= 0 {
		cfg.Server.MaxUploadMB = 512
	}
	if cfg.Storage.Dir == "" {
		cfg.Storage.Dir = "./data"
	}
	if cfg.Storage.AudioKeepDays == 0 {
		cfg.Storage.AudioKeepDays = 7
	}
	if cfg.Storage.JobKeepDays == 0 {
		cfg.Storage.JobKeepDays = 30
	}
	if cfg.Pipeline.Mode == "" {
		cfg.Pipeline.Mode = "local"
	}
	if cfg.Pipeline.Workers <= 0 {
		cfg.Pipeline.Workers = 1
	}
	if cfg.Pipeline.Queue <= 0 {
		cfg.Pipeline.Queue = 100
	}
	if cfg.Pipeline.JobTimeoutSeconds <= 0 {
		cfg.Pipeline.JobTimeoutSeconds = 1800
	}
	if cfg.Merge.MaxMinutes <= 0 {
		cfg.Merge.MaxMinutes = 120
	}
	if cfg.Merge.MaxJobs <= 0 {
		cfg.Merge.MaxJobs = 100
	}
	if cfg.LMStudio.TimeoutSeconds <= 0 {
		cfg.LMStudio.TimeoutSeconds = 300
	}
	if cfg.Pairing.DiscoveryPort == 0 {
		cfg.Pairing.DiscoveryPort = 18083
	}
	if cfg.Pairing.WindowMinutes <= 0 {
		cfg.Pairing.WindowMinutes = 10
	}
	if cfg.Pairing.DevicesFile == "" {
		cfg.Pairing.DevicesFile = filepath.Join(cfg.Storage.Dir, "devices.json")
	}
	if cfg.DailyReport.GenerateAt == "" {
		cfg.DailyReport.GenerateAt = "18:00"
	}
	if cfg.DailyReport.Title == "" {
		cfg.DailyReport.Title = "录音日报"
	}
	if cfg.DailyReport.KeepDays == 0 {
		cfg.DailyReport.KeepDays = 30
	}

	servicePort := 18082
	if _, p, err := net.SplitHostPort(cfg.Server.Addr); err == nil {
		if v, err := strconv.Atoi(p); err == nil {
			servicePort = v
		}
	}

	pairMgr, err := pairing.NewManager(
		cfg.Pairing.DevicesFile,
		time.Duration(cfg.Pairing.WindowMinutes)*time.Minute,
	)
	if err != nil {
		log.Fatalf("pairing: %v", err)
	}

	ident, err := identity.LoadOrCreate(filepath.Join(cfg.Storage.Dir, "identity.json"), cfg.Server.Name)
	if err != nil {
		log.Fatalf("identity: %v", err)
	}
	log.Printf("device identity: %s (%s)", ident.Name, ident.Fingerprint())

	if *openPair {
		code, expires := pairMgr.StartWindow()
		log.Printf("pairing code %s (expires %s)", code, expires.Format("15:04:05"))
	} else {
		log.Printf("手机连接：App 里「发现电脑」→ 选中本机 → 在 http://127.0.0.1:%d/ 点「允许」", servicePort)
	}
	if err := discovery.Start(discovery.Service{
		Port: cfg.Pairing.DiscoveryPort,
		Info: discovery.Info{
			Kind: "pc",
			ID:   ident.ID,
			Name: ident.Name,
			Port: servicePort,
			FP:   ident.Fingerprint(),
		},
		Pair: pairMgr,
	}); err != nil {
		log.Printf("discovery disabled: %v", err)
	}

	uploadDir := filepath.Join(cfg.Storage.Dir, "uploads")
	if err := os.MkdirAll(uploadDir, 0o755); err != nil {
		log.Fatalf("create upload dir: %v", err)
	}
	mergeDir := filepath.Join(cfg.Storage.Dir, "merged")
	if err := os.MkdirAll(mergeDir, 0o755); err != nil {
		log.Fatalf("create merge dir: %v", err)
	}

	store, err := job.NewStore(cfg.Storage.Dir)
	if err != nil {
		log.Fatalf("create job store: %v", err)
	}

	var asr infer.ASR
	var workerASR *infer.WorkerASR
	asrLabel := "local:worker"
	if cfg.Pipeline.Mode == "remote" && cfg.RemoteASR.BaseURL != "" {
		asr = infer.NewRemoteASR(cfg.RemoteASR.BaseURL, cfg.RemoteASR.APIKey, cfg.RemoteASR.Model)
		asrLabel = "remote:" + cfg.RemoteASR.Model
	} else {
		wa := infer.NewWorkerASR(cfg.Worker.URL)
		if cfg.Worker.TimeoutSeconds > 0 {
			wa.HC.Timeout = time.Duration(cfg.Worker.TimeoutSeconds) * time.Second
		}
		asr = wa
		workerASR = wa
	}

	analyzer := infer.NewLMStudio(
		cfg.LMStudio.BaseURL,
		cfg.LMStudio.APIKey,
		cfg.LMStudio.Model,
		cfg.LMStudio.ReasoningEffort,
		cfg.LMStudio.MaxTokens,
		cfg.LMStudio.Temperature,
		time.Duration(cfg.LMStudio.TimeoutSeconds)*time.Second,
		cfg.LMStudio.JSONMode,
	)

	hostBase := strings.TrimRight(cfg.HostManager.BaseURL, "/")
	if hostBase != "" {
		touch := hostBase + "/api/touch"
		if workerASR != nil {
			workerASR.WakeURL = hostBase + "/api/wake"
			workerASR.WakeToken = cfg.HostManager.Token
			workerASR.TouchURL = touch
		}
		analyzer.WakeURL = hostBase + "/api/wake"
		analyzer.WakeToken = cfg.HostManager.Token
		analyzer.TouchURL = touch
	}

	p := pipeline.New(
		asr, analyzer, store,
		cfg.Pipeline.Mode,
		map[string]string{
			"asr":      asrLabel,
			"analysis": "openai-compatible:" + cfg.LMStudio.Model,
		},
		cfg.Pipeline.Workers,
		cfg.Pipeline.Queue,
		time.Duration(cfg.Pipeline.JobTimeoutSeconds)*time.Second,
	)
	// 服务重启后把 queued 的任务重新跑起来。
	p.Recover()

	reportStore, err := report.NewStore(filepath.Join(cfg.Storage.Dir, "reports"))
	if err != nil {
		log.Fatalf("create report store: %v", err)
	}
	reportScheduler := report.NewScheduler(
		reportStore, store, analyzer,
		filepath.Join(cfg.Storage.Dir, "report_settings.json"),
		cfg.DailyReport.Title,
		report.Settings{Enabled: cfg.DailyReport.Enabled, GenerateAt: cfg.DailyReport.GenerateAt},
	)

	srv := &api.Server{
		Store:           store,
		Pipeline:        p,
		Worker:          workerASR,
		Pair:            pairMgr,
		Identity:        ident,
		DiscoveryPort:   cfg.Pairing.DiscoveryPort,
		ServicePort:     servicePort,
		UploadDir:       uploadDir,
		MaxUploadBytes:  int64(cfg.Server.MaxUploadMB) << 20,
		MergeDir:        mergeDir,
		MergeMaxSeconds: float64(cfg.Merge.MaxMinutes) * 60,
		MergeMaxJobs:    cfg.Merge.MaxJobs,
		MergeOptions:    merge.Options{FFmpeg: cfg.Merge.FFmpeg, FFprobe: cfg.Merge.FFprobe},
		Reports:         reportStore,
		ReportScheduler: reportScheduler,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go retentionLoop(ctx, store, reportStore, cfg.Storage.AudioKeepDays, cfg.Storage.JobKeepDays, cfg.DailyReport.KeepDays)
	go recoverLoop(ctx, p)
	go reportScheduler.Run(ctx)

	httpServer := &http.Server{
		Addr:              cfg.Server.Addr,
		Handler:           srv.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       120 * time.Second,
	}

	go func() {
		<-ctx.Done()
		log.Printf("shutting down...")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
		p.Close()
	}()

	log.Printf("warehouse-audio listening on %s (mode=%s, asr=%s, workers=%d, queue=%d)",
		cfg.Server.Addr, cfg.Pipeline.Mode, asrLabel, cfg.Pipeline.Workers, cfg.Pipeline.Queue)
	log.Printf("local admin: http://127.0.0.1:%d/api/v1/admin/jobs", servicePort)

	if err := httpServer.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Fatalf("server: %v", err)
	}
}

func recoverLoop(ctx context.Context, p *pipeline.Pipeline) {
	ticker := time.NewTicker(2 * time.Minute)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// 队列满时 Recover 会漏掉一些 queued 任务；定时补一次。
			p.Recover()
		}
	}
}

func retentionLoop(ctx context.Context, store *job.Store, reports *report.Store, audioDays, jobDays, reportDays int) {
	run := func() {
		a, j := store.Retention(audioDays, jobDays)
		r := reports.Retention(reportDays)
		if a > 0 || j > 0 || r > 0 {
			log.Printf("retention: removed %d audio files, %d jobs, %d reports", a, j, r)
		}
	}
	run()
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			run()
		}
	}
}
