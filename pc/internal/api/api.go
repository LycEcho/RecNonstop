package api

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"warehouse-audio/internal/discovery"
	"warehouse-audio/internal/identity"
	"warehouse-audio/internal/infer"
	"warehouse-audio/internal/job"
	"warehouse-audio/internal/merge"
	"warehouse-audio/internal/pairing"
	"warehouse-audio/internal/pipeline"
	"warehouse-audio/internal/report"
)

type Server struct {
	Store          *job.Store
	Pipeline       *pipeline.Pipeline
	Worker         *infer.WorkerASR
	Pair           *pairing.Manager
	Identity       *identity.Identity
	DiscoveryPort  int
	ServicePort    int
	UploadDir      string
	MaxUploadBytes int64

	// 合并任务：输出目录、总时长上限、单次最多合并段数。
	MergeDir        string
	MergeMaxSeconds float64
	MergeMaxJobs    int
	MergeOptions    merge.Options

	// 每日日报：存储、定时器、推送状态。
	Reports         *report.Store
	ReportScheduler *report.Scheduler
}

func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", s.health)
	mux.HandleFunc("/api/v1/pair", s.pairDevice)
	mux.HandleFunc("/api/v1/join", s.joinDevice)
	mux.HandleFunc("/api/v1/join/", s.joinStatus)
	mux.Handle("/api/v1/commands", s.auth(http.HandlerFunc(s.commands)))
	mux.Handle("/api/v1/report-settings", s.auth(http.HandlerFunc(s.reportSettings)))
	mux.Handle("/api/v1/reports", s.auth(http.HandlerFunc(s.reportsIndex)))
	mux.Handle("/api/v1/reports/latest", s.auth(http.HandlerFunc(s.latestReport)))
	mux.Handle("/api/v1/reports/", s.auth(http.HandlerFunc(s.reportByID)))
	mux.HandleFunc("/api/v1/admin/", s.admin)
	mux.Handle("/api/v1/jobs/merge", s.auth(http.HandlerFunc(s.mergeJobs)))
	mux.Handle("/api/v1/jobs", s.auth(http.HandlerFunc(s.jobsRoot)))
	mux.Handle("/api/v1/jobs/", s.auth(http.HandlerFunc(s.jobByID)))
	mux.HandleFunc("/", s.ui)
	return withCORS(mux)
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	_, _, open := s.Pair.Status()
	resp := map[string]any{
		"status":        "ok",
		"pairing_open":  open,
		"pending_joins": len(s.Pair.Pending()),
		"time":          time.Now().Format(time.RFC3339),
		"jobs":          s.Store.Stats(),
		"queue":         s.Pipeline.QueueLen(),
		"active":        s.Pipeline.Active(),
	}
	if s.Reports != nil {
		resp["reports"] = s.Reports.Stats()
	}
	if s.ReportScheduler != nil {
		resp["daily_report"] = s.ReportScheduler.State()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) auth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := r.Header.Get("Authorization")
		token := strings.TrimSpace(strings.TrimPrefix(h, "Bearer "))
		if token == "" {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "missing token"})
			return
		}
		d, ok := s.Pair.Authenticate(token)
		if !ok {
			writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
			return
		}
		ctx := context.WithValue(r.Context(), deviceContextKey{}, d)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

type deviceContextKey struct{}

func deviceFrom(r *http.Request) *pairing.Device {
	if d, ok := r.Context().Value(deviceContextKey{}).(*pairing.Device); ok {
		return d
	}
	return nil
}

func (s *Server) pairDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req struct {
		DeviceName string `json:"device_name"`
		Code       string `json:"code"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if req.DeviceName == "" {
		req.DeviceName = "android"
	}
	d, token, err := s.Pair.Pair(req.DeviceName, strings.TrimSpace(req.Code))
	if err != nil {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("paired device %s (%s)", d.Name, d.ID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"device_id": d.ID,
		"name":      d.Name,
		"token":     token,
	})
}

// joinDevice 是“扫到就点”的第一步：手机申请连接，电脑端页面弹确认。
func (s *Server) joinDevice(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req struct {
		DeviceID   string `json:"device_id"`
		DeviceName string `json:"device_name"`
		FP         string `json:"fp"`
		Invite     string `json:"invite"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}
	if !validDeviceID(req.DeviceID) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid device_id"})
		return
	}
	name := cleanName(req.DeviceName)
	if name == "" {
		name = "手机"
	}
	fp := strings.ToUpper(strings.TrimSpace(req.FP))
	if len(fp) > 8 {
		fp = fp[:8]
	}

	jr, err := s.Pair.RequestJoin(req.DeviceID, name, fp, remoteHost(r), strings.TrimSpace(req.Invite))
	if err != nil {
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": err.Error()})
		return
	}
	log.Printf("join request %s from %s (%s)", jr.ID, name, jr.DeviceID)
	writeJSON(w, http.StatusCreated, map[string]any{
		"request_id": jr.ID,
		"status":     jr.Status,
		"expires":    jr.Expires().Format(time.RFC3339),
		"pc":         s.pcInfo(),
	})
}

func (s *Server) joinStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/api/v1/join/")
	if !validDeviceID(id) {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid request id"})
		return
	}
	status, token, d := s.Pair.JoinStatus(id)
	resp := map[string]any{"status": status}
	if status == "approved" && token != "" && d != nil {
		resp["token"] = token
		resp["device_id"] = d.ID
		resp["name"] = d.Name
		resp["pc"] = s.pcInfo()
	}
	writeJSON(w, http.StatusOK, resp)
}

// commands 是手机定时拉取的指令通道：立即同步 + 待推送的日报。
func (s *Server) commands(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	d := deviceFrom(r)
	if d == nil {
		writeJSON(w, http.StatusUnauthorized, map[string]string{"error": "invalid token"})
		return
	}
	sync := false
	for _, c := range s.Pair.TakeCommands(d.ID) {
		if c == "sync" {
			sync = true
		}
	}
	reports := make([]map[string]any, 0, 1)
	if s.Reports != nil {
		for _, rep := range s.Reports.PendingFor(d.ID) {
			reports = append(reports, map[string]any{
				"id":           rep.ID,
				"date":         rep.Date,
				"title":        rep.Title,
				"summary":      rep.Summary,
				"generated_at": rep.GeneratedAt,
			})
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"sync": sync, "reports": reports})
}

// reportsIndex 给已连接手机同步日报历史。只返回元数据，全文按 id 单独下载，
// 避免一次把几十份 Markdown 全塞进列表响应。
func (s *Server) reportsIndex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.Reports == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reports disabled"})
		return
	}
	limit := intQuery(r, "limit", 90)
	if limit <= 0 || limit > 500 {
		limit = 90
	}
	list, next, err := s.Reports.ReadyPage(limit, r.URL.Query().Get("cursor"))
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid report history cursor"})
		return
	}
	out := make([]map[string]any, 0, limit)
	for _, rep := range list {
		out = append(out, map[string]any{
			"id":               rep.ID,
			"date":             rep.Date,
			"title":            rep.Title,
			"summary":          rep.Summary,
			"job_count":        rep.JobCount,
			"duration_seconds": rep.DurationSeconds,
			"generated_at":     rep.GeneratedAt,
			"error":            rep.Error,
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"reports": out, "next_cursor": next, "ack_mode": "single"})
}

func (s *Server) latestReport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if s.Reports == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reports disabled"})
		return
	}
	rep, ok := s.Reports.LatestDone()
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "no report yet"})
		return
	}
	rep.DeliveredTo = nil
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) reportByID(w http.ResponseWriter, r *http.Request) {
	if s.Reports == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reports disabled"})
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/api/v1/reports/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	id := parts[0]
	if len(parts) == 2 && parts[1] == "ack" {
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		d := deviceFrom(r)
		if d == nil || !s.Reports.Ack(id, d.ID) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "report not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true})
		return
	}
	if len(parts) != 1 {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	rep, ok := s.Reports.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "report not found"})
		return
	}
	rep.DeliveredTo = nil
	writeJSON(w, http.StatusOK, rep)
}

func (s *Server) reportSettings(w http.ResponseWriter, r *http.Request) {
	if s.ReportScheduler == nil {
		writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reports disabled"})
		return
	}
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, s.ReportScheduler.State())
	case http.MethodPost, http.MethodPut:
		var req struct {
			Enabled    bool   `json:"enabled"`
			GenerateAt string `json:"generate_at"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		if err := s.ReportScheduler.Update(req.Enabled, req.GenerateAt); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, s.ReportScheduler.State())
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) pcInfo() map[string]any {
	if s.Identity == nil {
		return nil
	}
	return map[string]any{
		"id":   s.Identity.ID,
		"name": s.Identity.Name,
		"fp":   s.Identity.Fingerprint(),
		"port": s.ServicePort,
	}
}

func remoteHost(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}

// privateHost 只允许给局域网/本机地址发 UDP，避免被当成任意地址的探测器。
func privateHost(h string) bool {
	ip := net.ParseIP(h)
	if ip == nil {
		return false
	}
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast()
}

func validDeviceID(v string) bool {
	if len(v) < 8 || len(v) > 32 {
		return false
	}
	_, err := hex.DecodeString(v)
	return err == nil
}

func cleanName(v string) string {
	v = strings.TrimSpace(v)
	v = strings.Map(func(r rune) rune {
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, v)
	if runes := []rune(v); len(runes) > 64 {
		v = string(runes[:64])
	}
	return v
}

func (s *Server) admin(w http.ResponseWriter, r *http.Request) {
	if !isLocal(r) {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "local only"})
		return
	}
	// 防 CSRF：本机页面发起的请求是 same-origin；跨站页面发起的请求带 cross-site。
	if site := r.Header.Get("Sec-Fetch-Site"); site != "" && site != "same-origin" && site != "none" {
		writeJSON(w, http.StatusForbidden, map[string]string{"error": "cross-site request blocked"})
		return
	}

	path := strings.TrimPrefix(r.URL.Path, "/api/v1/admin/")
	switch {
	case path == "pairing":
		if r.Method == http.MethodPost {
			code, expires := s.Pair.StartWindow()
			log.Printf("pairing window opened, code=%s expires=%s", code, expires.Format("15:04:05"))
			writeJSON(w, http.StatusOK, map[string]any{
				"code":    code,
				"expires": expires.Format(time.RFC3339),
			})
			return
		}
		code, expires, open := s.Pair.Status()
		writeJSON(w, http.StatusOK, map[string]any{
			"open":    open,
			"code":    code,
			"expires": expires.Format(time.RFC3339),
		})

	case path == "devices":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"devices": s.Pair.Devices()})

	case strings.HasPrefix(path, "devices/"):
		if r.Method != http.MethodDelete {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		id := strings.TrimPrefix(path, "devices/")
		if !s.Pair.Revoke(id) {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "device not found"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "revoked"})

	case path == "join":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"requests": s.Pair.Pending()})

	case strings.HasPrefix(path, "join/"):
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		parts := strings.Split(strings.TrimPrefix(path, "join/"), "/")
		if len(parts) != 2 || parts[0] == "" {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
			return
		}
		id, action := parts[0], parts[1]
		switch action {
		case "approve":
			d, err := s.Pair.Approve(id)
			if err != nil {
				writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
				return
			}
			log.Printf("join approved: %s (%s)", d.Name, d.ID)
			writeJSON(w, http.StatusOK, map[string]any{"status": "approved", "device_id": d.ID})
		case "reject":
			if err := s.Pair.Reject(id); err != nil {
				writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]string{"status": "rejected"})
		default:
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		}

	case path == "nearby":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		devices, err := discovery.Scan(s.DiscoveryPort, 2500*time.Millisecond)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		out := make([]nearbyDevice, 0, len(devices))
		for _, d := range devices {
			if d.Kind != "phone" {
				continue
			}
			out = append(out, nearbyDevice{Device: d, Paired: s.Pair.HasDevice(d.ID)})
		}
		writeJSON(w, http.StatusOK, map[string]any{"devices": out})

	case path == "sync":
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			DeviceID string `json:"device_id"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		if err := s.Pair.RequestSync(strings.TrimSpace(req.DeviceID)); err != nil {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"status": "queued"})

	case path == "invite":
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			DeviceID string `json:"device_id"`
			Host     string `json:"host"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 2048)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		host := strings.TrimSpace(req.Host)
		if !validDeviceID(req.DeviceID) || !privateHost(host) {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid device_id or host"})
			return
		}
		token, err := s.Pair.CreateInvite(req.DeviceID)
		if err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": err.Error()})
			return
		}
		err = discovery.Send(host, s.DiscoveryPort, map[string]any{
			"service": "recnonstop",
			"cmd":     "join",
			"token":   token,
			"pc":      s.pcInfo(),
		})
		if err != nil {
			writeJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
			return
		}
		log.Printf("invite sent to %s (%s)", host, req.DeviceID)
		writeJSON(w, http.StatusOK, map[string]string{"status": "sent"})

	case path == "reports":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if s.Reports == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reports disabled"})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"reports": s.Reports.List(intQuery(r, "limit", 30))})

	case path == "reports/generate":
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if s.ReportScheduler == nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "reports disabled"})
			return
		}
		date, err := reportGenerationDate(r.Body)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		ctx, cancel := context.WithTimeout(r.Context(), report.GenerationTimeout)
		defer cancel()
		var rep *report.Report
		if date == "" {
			rep, err = s.ReportScheduler.GenerateNow(ctx)
		} else {
			rep, err = s.ReportScheduler.GenerateDate(ctx, date)
		}
		if err != nil {
			status := http.StatusInternalServerError
			if errors.Is(err, report.ErrInvalidDate) {
				status = http.StatusBadRequest
			}
			writeJSON(w, status, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rep)

	case path == "jobs":
		s.adminList(w, r)

	case strings.HasPrefix(path, "jobs/"):
		s.adminJob(w, r)

	case path == "merge":
		s.adminMerge(w, r)

	default:
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	}
}

// reportGenerationDate accepts an empty body or one small JSON object. Reject
// duplicate/unknown fields and trailing data instead of silently choosing a day.
func reportGenerationDate(body io.Reader) (string, error) {
	data, err := io.ReadAll(io.LimitReader(body, 1025))
	if err != nil || len(data) > 1024 {
		return "", fmt.Errorf("report request must be at most 1024 bytes")
	}
	if strings.TrimSpace(string(data)) == "" {
		return "", nil
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	token, err := dec.Token()
	if err != nil || token != json.Delim('{') {
		return "", fmt.Errorf("expected a JSON object with an optional date")
	}
	var date string
	seen := false
	for dec.More() {
		key, err := dec.Token()
		if err != nil || key != "date" || seen {
			return "", fmt.Errorf("only one date field is allowed")
		}
		var value *string
		if err := dec.Decode(&value); err != nil || value == nil || *value == "" {
			return "", fmt.Errorf("date must be a YYYY-MM-DD string")
		}
		date, seen = *value, true
	}
	if token, err := dec.Token(); err != nil || token != json.Delim('}') {
		return "", fmt.Errorf("invalid report request JSON")
	}
	if err := dec.Decode(new(any)); err != io.EOF {
		return "", fmt.Errorf("unexpected data after report request")
	}
	return date, nil
}

type nearbyDevice struct {
	discovery.Device
	Paired bool `json:"paired"`
}

type mergeRequest struct {
	JobIDs []string `json:"job_ids"`
	Title  string   `json:"title"`
	Mode   string   `json:"mode"` // transcript（默认，复用已有转写）| audio（重新识别，统一说话人）
}

// adminMerge 给本机时间线页面用；真正的公开入口是 mergeJobs。
func (s *Server) adminMerge(w http.ResponseWriter, r *http.Request) {
	s.createMerge(w, r, nil)
}

// mergeJobs 是配了 token 的正式接口：手机/其他已配对客户端都能调。
// owner 不为空时，只允许合并该设备自己的分段，避免拿到 token 就能合并别人录音。
func (s *Server) mergeJobs(w http.ResponseWriter, r *http.Request) {
	s.createMerge(w, r, deviceFrom(r))
}

func (s *Server) createMerge(w http.ResponseWriter, r *http.Request, owner *pairing.Device) {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	var req mergeRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
		return
	}

	maxJobs := s.MergeMaxJobs
	if maxJobs <= 0 {
		maxJobs = 100
	}
	mode := strings.ToLower(strings.TrimSpace(req.Mode))
	if mode == "" {
		mode = "transcript"
	}
	if mode != "transcript" && mode != "audio" {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "mode must be transcript or audio"})
		return
	}
	seen := make(map[string]bool, len(req.JobIDs))
	ids := make([]string, 0, len(req.JobIDs))
	for _, raw := range req.JobIDs {
		id := strings.TrimSpace(raw)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		ids = append(ids, id)
	}
	if len(ids) < 2 {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "need at least 2 distinct jobs"})
		return
	}
	if len(ids) > maxJobs {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": fmt.Sprintf("too many jobs (max %d)", maxJobs)})
		return
	}

	jobs := make([]*job.Job, 0, len(ids))
	for _, id := range ids {
		j, ok := s.Store.Get(id)
		if !ok {
			writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found: " + id})
			return
		}
		if owner != nil && j.DeviceID != owner.ID {
			writeJSON(w, http.StatusForbidden, map[string]string{"error": "job belongs to another device: " + id})
			return
		}
		if j.Kind == "merge" {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "cannot merge merged job: " + id})
			return
		}
		if j.Status != job.StatusDone || j.Result == nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "job not ready: " + id})
			return
		}
		if j.AudioDeleted || j.AudioPath == "" {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "audio deleted: " + id})
			return
		}
		if _, err := os.Stat(j.AudioPath); err != nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "audio missing: " + id})
			return
		}
		jobs = append(jobs, j)
	}

	// 按录音时间排序，避免前端勾选顺序影响合并结果。
	sort.SliceStable(jobs, func(i, k int) bool {
		return jobStart(jobs[i]).Before(jobStart(jobs[k]))
	})

	sources := make([]merge.Source, 0, len(jobs))
	for _, j := range jobs {
		sources = append(sources, merge.Source{
			JobID:       j.ID,
			SegmentName: sourceName(j),
			Path:        j.AudioPath,
		})
	}

	id := fmt.Sprintf("j_%d", time.Now().UnixNano())
	mergeDir := s.MergeDir
	if mergeDir == "" {
		mergeDir = filepath.Join(filepath.Dir(s.UploadDir), "merged")
	}
	outPath := filepath.Join(mergeDir, id+".wav")

	ctx, cancel := context.WithTimeout(r.Context(), 10*time.Minute)
	defer cancel()
	spans, duration, err := merge.Build(ctx, s.MergeOptions, sources, outPath, s.MergeMaxSeconds)
	if err != nil {
		status := http.StatusInternalServerError
		if errors.Is(err, merge.ErrTooLong) {
			status = http.StatusBadRequest
		}
		log.Printf("merge: build failed: %v", err)
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}
	abs, _ := filepath.Abs(outPath)

	// 老任务没有 segment_name / recorded_at：用“上传时间 - 实际音频时长”
	// 反推分段开始时间，比用 ASR 返回的 duration（会截掉尾部静音）准确。
	for i := range spans {
		if jobs[i].SegmentName != "" {
			continue
		}
		if jobs[i].RecordedAt > 0 {
			spans[i].SegmentName = time.UnixMilli(jobs[i].RecordedAt).Format("15:04:05")
		} else {
			spans[i].SegmentName = jobs[i].CreatedAt.Add(
				-time.Duration(spans[i].Duration * float64(time.Second))).Format("15:04:05")
		}
	}

	jobIDs := make([]string, len(jobs))
	for i, j := range jobs {
		jobIDs[i] = j.ID
	}
	title := strings.TrimSpace(req.Title)
	if title == "" {
		title = mergeTitle(jobs, spans, duration)
	}
	recordedAt := jobStart(jobs[0]).UnixMilli()
	if jobs[0].RecordedAt == 0 {
		recordedAt = jobs[0].CreatedAt.Add(
			-time.Duration(spans[0].Duration * float64(time.Second))).UnixMilli()
	}

	base := func() *job.Job {
		return &job.Job{
			ID:           id,
			Kind:         "merge",
			AudioPath:    abs,
			DeviceID:     jobs[0].DeviceID,
			DeviceName:   jobs[0].DeviceName,
			RecordedAt:   recordedAt,
			SegmentName:  title,
			SourceJobIDs: jobIDs,
			SourceSpans:  spans,
		}
	}

	// transcript 模式：不重新跑 ASR，直接复用各分段已有转写。
	// 这样保留每段更好的上下文，只做时间偏移和整段摘要。
	if mode == "transcript" {
		analysisTr, storedSegs, total := buildMergedTranscript(jobs, spans)
		if len(storedSegs) == 0 {
			_ = os.Remove(abs)
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "selected jobs have no transcribed content"})
			return
		}
		res := &job.Result{
			JobID:    id,
			Kind:     "merge",
			Mode:     s.Pipeline.Mode,
			Duration: total,
			Segments: storedSegs,
			Sources:  spans,
			Backend:  s.Pipeline.Backend,
		}
		an, anErr := s.Pipeline.Analyzer.Analyze(ctx, analysisTr)
		if anErr != nil {
			log.Printf("merge: %s analyze failed, keeping transcript: %v", id, anErr)
		} else {
			res.Summary = an.Summary
			if len(an.Raw) > 0 && json.Valid(an.Raw) {
				res.Analysis = an.Raw
			}
		}
		mj := base()
		mj.MergeMode = "transcript"
		mj.Result = res
		mj.Status = job.StatusDone
		mj.Stage = "done"
		if _, created := s.Store.CreateFull(mj); !created {
			_ = os.Remove(abs)
			writeJSON(w, http.StatusConflict, map[string]string{"error": "duplicate merge job"})
			return
		}
		log.Printf("merge: %s <- %d segments, %.1fs, mode=transcript, %d sentences",
			id, len(jobs), total, len(storedSegs))
		writeJSON(w, http.StatusCreated, map[string]any{
			"job_id":   id,
			"status":   mj.Status,
			"mode":     mode,
			"duration": total,
			"sources":  len(jobs),
		})
		return
	}

	mj := base()
	mj.MergeMode = "audio"
	mj.Status = job.StatusQueued
	mj.Stage = "merging"
	if _, created := s.Store.CreateFull(mj); !created {
		_ = os.Remove(abs)
		writeJSON(w, http.StatusConflict, map[string]string{"error": "duplicate merge job"})
		return
	}
	if err := s.Pipeline.Enqueue(id, abs); err != nil {
		s.Store.Delete(id)
		_ = os.Remove(abs)
		writeJSON(w, http.StatusTooManyRequests, map[string]string{"error": "queue full, retry later"})
		return
	}
	log.Printf("merge: %s <- %d segments, %.1fs, mode=audio", id, len(jobs), duration)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id":   id,
		"status":   job.StatusQueued,
		"mode":     mode,
		"duration": duration,
		"sources":  len(jobs),
	})
}

func buildMergedTranscript(jobs []*job.Job, spans []job.SourceSpan) (*infer.Transcript, []job.Segment, float64) {
	var analysis infer.Transcript
	stored := make([]job.Segment, 0)
	var full strings.Builder
	for i, j := range jobs {
		if j.Result == nil {
			continue
		}
		offset := spans[i].Offset
		label := jobStart(j).Format("15:04:05")
		for _, s := range j.Result.Segments {
			text := strings.TrimSpace(s.Text)
			if !hasSpeechText(text) {
				continue
			}
			start := s.Start + offset
			end := s.End + offset
			analysis.Segments = append(analysis.Segments, infer.Segment{
				Start:   start,
				End:     end,
				Speaker: fmt.Sprintf("%s@%s", s.Speaker, label),
				Text:    text,
			})
			stored = append(stored, job.Segment{
				Start:       start,
				End:         end,
				Speaker:     s.Speaker,
				Text:        text,
				SourceJobID: j.ID,
			})
			full.WriteString(text)
			full.WriteByte('\n')
		}
	}
	total := 0.0
	if len(spans) > 0 {
		last := spans[len(spans)-1]
		total = last.Offset + last.Duration
	}
	analysis.Duration = total
	analysis.Text = full.String()
	return &analysis, stored, total
}

func hasSpeechText(s string) bool {
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			return true
		}
	}
	return false
}

func sourceName(j *job.Job) string {
	if j.SegmentName != "" {
		return j.SegmentName
	}
	start := jobStart(j)
	return start.Format("15:04:05")
}

func mergeTitle(jobs []*job.Job, spans []job.SourceSpan, duration float64) string {
	start := jobs[0].CreatedAt.Add(-time.Duration(spans[0].Duration * float64(time.Second)))
	if jobs[0].RecordedAt > 0 {
		start = jobStart(jobs[0])
	}
	// 上传发生在分段关闭时，用最后一段的上传时间当结束时间，
	// 比 start+音频总时长更接近真实结束（合并会去掉段间空档）。
	end := jobs[len(jobs)-1].CreatedAt
	if end.Before(start) {
		end = start.Add(time.Duration(duration * float64(time.Second)))
	}
	endFormat := "15:04"
	if start.Format("2006-01-02") != end.Format("2006-01-02") {
		endFormat = "01-02 15:04"
	}
	return fmt.Sprintf("合并 %s–%s · %d 段", start.Format("01-02 15:04"), end.Format(endFormat), len(jobs))
}

func jobStart(j *job.Job) time.Time {
	return job.RecordingStart(j)
}

func (s *Server) jobsRoot(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.listJobs(w, r)
	case http.MethodPost:
		s.createJob(w, r)
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
	}
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	limit := intQuery(r, "limit", 50)
	q := r.URL.Query().Get("q")
	writeJSON(w, http.StatusOK, map[string]any{
		"jobs":  s.Store.List(limit, q),
		"stats": s.Store.Stats(),
	})
}

func (s *Server) createJob(w http.ResponseWriter, r *http.Request) {
	if s.MaxUploadBytes > 0 {
		r.Body = http.MaxBytesReader(w, r.Body, s.MaxUploadBytes)
	}
	if err := r.ParseMultipartForm(32 << 20); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid multipart: " + err.Error()})
		return
	}
	if r.MultipartForm != nil {
		defer r.MultipartForm.RemoveAll()
	}
	file, hdr, err := r.FormFile("file")
	if err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "missing file field"})
		return
	}
	defer file.Close()

	ext, ok := allowedExt(hdr.Filename)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": "unsupported audio extension"})
		return
	}

	deviceName := strings.TrimSpace(r.FormValue("device"))
	deviceID := strings.TrimSpace(r.FormValue("device_id"))
	segmentName := strings.TrimSpace(r.FormValue("segment_name"))
	gaps := r.FormValue("gaps")
	if len(gaps) > 1<<20 {
		gaps = ""
	}
	recordedAt, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("recorded_at")), 10, 64)

	idem := strings.TrimSpace(r.Header.Get("X-Idempotency-Key"))
	if idem == "" && segmentName != "" {
		idem = fmt.Sprintf("%s:%s:%s", deviceID, segmentName, ext)
	}
	if old, found := s.Store.FindIdem(idem); found {
		writeJSON(w, http.StatusOK, map[string]any{
			"job_id":    old.ID,
			"status":    old.Status,
			"duplicate": true,
		})
		return
	}

	if s.Pipeline.Full() {
		writeJSON(w, http.StatusTooManyRequests,
			map[string]string{"error": "queue full, retry later"})
		return
	}

	id := fmt.Sprintf("j_%d", time.Now().UnixNano())
	dst := filepath.Join(s.UploadDir, id+ext)
	out, err := os.Create(dst)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
		return
	}
	max := s.MaxUploadBytes
	if max <= 0 {
		max = 512 << 20
	}
	if status, err := saveUpload(out, file, max); err != nil {
		_ = os.Remove(dst)
		writeJSON(w, status, map[string]string{"error": err.Error()})
		return
	}

	if err := validateAudio(dst, ext); err != nil {
		_ = os.Remove(dst)
		writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
		return
	}

	abs, err := filepath.Abs(dst)
	if err != nil {
		abs = dst
	}
	d := deviceFrom(r)
	if d != nil {
		// token 已经绑定了设备，服务端以认证结果为准，避免表单伪造 device_id。
		deviceID = d.ID
		if deviceName == "" {
			deviceName = d.Name
		}
	}

	j, created := s.Store.CreateFull(&job.Job{
		ID:             id,
		Kind:           "segment",
		Status:         job.StatusQueued,
		Stage:          "queued",
		AudioPath:      abs,
		DeviceID:       deviceID,
		DeviceName:     deviceName,
		RecordedAt:     recordedAt,
		SegmentName:    segmentName,
		Gaps:           gaps,
		IdempotencyKey: idem,
	})
	if !created {
		_ = os.Remove(dst)
		writeJSON(w, http.StatusOK, map[string]any{
			"job_id": j.ID, "status": j.Status, "duplicate": true,
		})
		return
	}

	if err := s.Pipeline.Enqueue(j.ID, abs); err != nil {
		s.Store.Delete(j.ID)
		writeJSON(w, http.StatusTooManyRequests,
			map[string]string{"error": "queue full, retry later"})
		return
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"job_id": j.ID,
		"status": j.Status,
	})
}

// Storage failures are retryable. Only the actual size limit may tell the phone
// to quarantine the file permanently.
func saveUpload(out io.WriteCloser, in io.Reader, max int64) (int, error) {
	n, copyErr := io.Copy(out, io.LimitReader(in, max+1))
	closeErr := out.Close()
	if copyErr != nil {
		return http.StatusInternalServerError, copyErr
	}
	if closeErr != nil {
		return http.StatusInternalServerError, closeErr
	}
	if n > max {
		return http.StatusRequestEntityTooLarge, errors.New("file too large")
	}
	return http.StatusOK, nil
}

func (s *Server) jobByID(w http.ResponseWriter, r *http.Request) {
	path := strings.TrimPrefix(r.URL.Path, "/api/v1/jobs/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
		return
	}
	id := parts[0]
	j, ok := s.Store.Get(id)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]string{"error": "job not found"})
		return
	}

	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}

	switch action {
	case "result":
		if j.Result == nil {
			writeJSON(w, http.StatusConflict, map[string]string{
				"error": "result not ready", "status": string(j.Status),
			})
			return
		}
		writeJSON(w, http.StatusOK, j.Result)
		return
	case "speaker":
		if r.Method != http.MethodPost {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		var req struct {
			Speaker string `json:"speaker"`
			Name    string `json:"name"`
		}
		if err := json.NewDecoder(io.LimitReader(r.Body, 2048)).Decode(&req); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "invalid json"})
			return
		}
		req.Name = strings.TrimSpace(req.Name)
		if req.Name == "" || len([]rune(req.Name)) > 24 {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "name must be 1-24 chars"})
			return
		}
		if j.Result == nil {
			writeJSON(w, http.StatusConflict, map[string]string{"error": "result not ready"})
			return
		}
		target := req.Speaker
		if target == "" {
			target = "spk_0"
		}
		matched := make([]infer.Segment, 0)
		for i := range j.Result.Segments {
			if j.Result.Segments[i].Speaker == target {
				j.Result.Segments[i].Person = req.Name
				j.Result.Segments[i].PersonName = req.Name
				matched = append(matched, infer.Segment{
					Start:      j.Result.Segments[i].Start,
					End:        j.Result.Segments[i].End,
					Speaker:    target,
					Person:     req.Name,
					PersonName: req.Name,
					Text:       j.Result.Segments[i].Text,
				})
			}
		}
		_ = s.Store.Update(id, func(x *job.Job) {
			x.Result = j.Result
		})
		voice := false
		warning := ""
		if s.Worker != nil && j.AudioPath != "" && len(matched) > 0 {
			ok, err := s.Worker.EnrollSpeaker(r.Context(), j.AudioPath, req.Name, matched)
			if err != nil {
				warning = err.Error()
			}
			voice = ok
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":         true,
			"speaker":    target,
			"name":       req.Name,
			"voiceprint": voice,
			"warning":    warning,
		})
		return
	case "clip":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if j.AudioDeleted || j.AudioPath == "" {
			writeJSON(w, http.StatusGone, map[string]string{"error": "audio deleted"})
			return
		}
		if _, err := os.Stat(j.AudioPath); err != nil {
			writeJSON(w, http.StatusGone, map[string]string{"error": "audio missing"})
			return
		}
		start, _ := strconv.ParseFloat(r.URL.Query().Get("start"), 64)
		end, _ := strconv.ParseFloat(r.URL.Query().Get("end"), 64)
		if start < 0 {
			start = 0
		}
		if end <= start {
			end = start + 6
		}
		if end-start > 30 {
			end = start + 30
		}
		ff := "ffmpeg"
		if _, err := exec.LookPath(ff); err != nil {
			writeJSON(w, http.StatusServiceUnavailable, map[string]string{"error": "ffmpeg missing"})
			return
		}
		cmd := exec.Command(ff, "-v", "error", "-ss", fmt.Sprintf("%.3f", start),
			"-to", fmt.Sprintf("%.3f", end), "-i", j.AudioPath,
			"-ac", "1", "-ar", "16000", "-f", "wav", "pipe:1")
		out, err := cmd.Output()
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]string{"error": err.Error()})
			return
		}
		w.Header().Set("Content-Type", "audio/wav")
		w.Header().Set("Cache-Control", "no-store")
		_, _ = w.Write(out)
		return
	case "audio":
		if r.Method != http.MethodGet {
			writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
			return
		}
		if j.AudioDeleted || j.AudioPath == "" {
			writeJSON(w, http.StatusGone, map[string]string{"error": "audio deleted"})
			return
		}
		if _, err := os.Stat(j.AudioPath); err != nil {
			writeJSON(w, http.StatusGone, map[string]string{"error": "audio missing"})
			return
		}
		w.Header().Set("Content-Type", contentType(j.AudioPath))
		http.ServeFile(w, r, j.AudioPath)
		return
	}

	if r.Method == http.MethodDelete {
		s.Store.Delete(id)
		writeJSON(w, http.StatusOK, map[string]string{"status": "deleted"})
		return
	}
	writeJSON(w, http.StatusOK, j)
}

func intQuery(r *http.Request, key string, def int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil || n <= 0 {
		return def
	}
	if n > 500 {
		n = 500
	}
	return n
}

func allowedExt(name string) (string, bool) {
	ext := strings.ToLower(filepath.Ext(name))
	switch ext {
	case ".aac", ".m4a", ".mp4", ".wav", ".mp3", ".ogg", ".opus", ".flac", ".webm":
		return ext, true
	case "":
		return ".aac", true
	default:
		return "", false
	}
}

func validateAudio(path, ext string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	head := make([]byte, 12)
	n, _ := io.ReadFull(f, head)
	head = head[:n]
	if n < 4 {
		return fmt.Errorf("audio file too small")
	}
	switch ext {
	case ".aac":
		if n < 2 || head[0] != 0xFF || (head[1]&0xF0) != 0xF0 {
			return fmt.Errorf("invalid ADTS header")
		}
	case ".m4a", ".mp4":
		if n < 8 || string(head[4:8]) != "ftyp" {
			return fmt.Errorf("invalid MP4 header")
		}
	case ".wav":
		if string(head[0:4]) != "RIFF" {
			return fmt.Errorf("invalid WAV header")
		}
	case ".mp3":
		if string(head[0:3]) != "ID3" && !(head[0] == 0xFF && (head[1]&0xE0) == 0xE0) {
			return fmt.Errorf("invalid MP3 header")
		}
	case ".ogg", ".opus":
		if string(head[0:4]) != "OggS" {
			return fmt.Errorf("invalid Ogg header")
		}
	case ".flac":
		if string(head[0:4]) != "fLaC" {
			return fmt.Errorf("invalid FLAC header")
		}
	case ".webm":
		if n < 4 || head[0] != 0x1A || head[1] != 0x45 || head[2] != 0xDF || head[3] != 0xA3 {
			return fmt.Errorf("invalid WebM header")
		}
	}
	return nil
}

func contentType(path string) string {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".aac":
		return "audio/aac"
	case ".m4a", ".mp4":
		return "audio/mp4"
	case ".wav":
		return "audio/wav"
	case ".mp3":
		return "audio/mpeg"
	case ".ogg", ".opus":
		return "audio/ogg"
	case ".flac":
		return "audio/flac"
	case ".webm":
		return "audio/webm"
	default:
		return "application/octet-stream"
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	b, err := json.Marshal(v)
	if err != nil {
		log.Printf("api: marshal response: %v", err)
		http.Error(w, `{"error":"internal marshal error"}`, http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(status)
	if _, err := w.Write(b); err != nil {
		log.Printf("api: write response: %v", err)
	}
}

func isLocal(r *http.Request) bool {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

func withCORS(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 时间线页面和 API 同源，不需要跨域。只对“本机同源”的页面放行，
		// 避免任意网页读取本机转写。
		origin := r.Header.Get("Origin")
		if origin != "" && (origin == "http://"+r.Host || origin == "https://"+r.Host) {
			w.Header().Set("Access-Control-Allow-Origin", origin)
			w.Header().Set("Vary", "Origin")
			w.Header().Set("Access-Control-Allow-Headers", "Content-Type, Authorization, X-Idempotency-Key")
			w.Header().Set("Access-Control-Allow-Methods", "GET, POST, DELETE, OPTIONS")
		}
		if r.Method == http.MethodOptions {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		next.ServeHTTP(w, r)
	})
}
