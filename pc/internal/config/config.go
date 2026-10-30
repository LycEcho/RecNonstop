package config

import (
	"encoding/json"
	"os"
)

type Config struct {
	Server struct {
		Addr        string `json:"addr"`
		Name        string `json:"name"` // 局域网里显示的名字，留空自动生成
		MaxUploadMB int    `json:"max_upload_mb"`
	} `json:"server"`

	Storage struct {
		Dir           string `json:"dir"`
		AudioKeepDays int    `json:"audio_keep_days"` // <=0 不清理
		JobKeepDays   int    `json:"job_keep_days"`   // <=0 不清理
	} `json:"storage"`

	Pipeline struct {
		Mode              string `json:"mode"` // local | remote | hybrid
		Workers           int    `json:"workers"`
		Queue             int    `json:"queue"`
		JobTimeoutSeconds int    `json:"job_timeout_seconds"`
	} `json:"pipeline"`

	Merge struct {
		MaxMinutes int    `json:"max_minutes"` // 单次合并的音频总时长上限
		MaxJobs    int    `json:"max_jobs"`    // 单次最多合并多少段
		FFmpeg     string `json:"ffmpeg"`      // 留空用 PATH 里的 ffmpeg
		FFprobe    string `json:"ffprobe"`     // 留空用 PATH 里的 ffprobe
	} `json:"merge"`

	Worker struct {
		URL            string `json:"url"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	} `json:"worker"`

	HostManager struct {
		BaseURL        string `json:"base_url"`
		Token          string `json:"token"`
		TimeoutSeconds int    `json:"timeout_seconds"`
	} `json:"host_manager"`

	LMStudio struct {
		BaseURL         string  `json:"base_url"`
		APIKey          string  `json:"api_key"`
		Model           string  `json:"model"`
		ReasoningEffort string  `json:"reasoning_effort"`
		MaxTokens       int     `json:"max_tokens"`
		Temperature     float64 `json:"temperature"`
		TimeoutSeconds  int     `json:"timeout_seconds"`
		JSONMode        bool    `json:"json_mode"`
	} `json:"lmstudio"`

	RemoteASR struct {
		BaseURL string `json:"base_url"`
		APIKey  string `json:"api_key"`
		Model   string `json:"model"`
	} `json:"remote_asr"`

	// DailyReport 默认开启：每天 generate_at 生成前一天/当天的日报，
	// 生成完成后等手机下一次拉指令时自动推送。
	DailyReport struct {
		Enabled    bool   `json:"enabled"`
		GenerateAt string `json:"generate_at"` // HH:MM，电脑本地时间
		Title      string `json:"title"`
		KeepDays   int    `json:"keep_days"`
	} `json:"daily_report"`

	Pairing struct {
		DiscoveryPort int    `json:"discovery_port"`
		WindowMinutes int    `json:"window_minutes"`
		DevicesFile   string `json:"devices_file"`
	} `json:"pairing"`
}

func Load(path string) (*Config, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	// 默认值：json_mode / 每日日报没写时保持开启；显式 false 需要写 false
	//（JSON 解码会覆盖这里的默认值）。
	c.LMStudio.JSONMode = true
	c.DailyReport.Enabled = true
	if err := json.Unmarshal(b, &c); err != nil {
		return nil, err
	}
	return &c, nil
}
