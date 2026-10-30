package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"warehouse-audio/internal/identity"
	"warehouse-audio/internal/pairing"
	"warehouse-audio/internal/report"
)

func TestAllowedExt(t *testing.T) {
	if _, ok := allowedExt("a.aac"); !ok {
		t.Fatal("aac rejected")
	}
	if _, ok := allowedExt("a.exe"); ok {
		t.Fatal("exe accepted")
	}
	if ext, ok := allowedExt("noext"); !ok || ext != ".aac" {
		t.Fatalf("noext = %q %v", ext, ok)
	}
}

func TestValidateAudio(t *testing.T) {
	dir := t.TempDir()

	adts := filepath.Join(dir, "ok.aac")
	if err := os.WriteFile(adts, []byte{0xFF, 0xF1, 0x50, 0x80, 0x00, 0x1F, 0xFC}, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateAudio(adts, ".aac"); err != nil {
		t.Fatalf("valid adts rejected: %v", err)
	}

	bad := filepath.Join(dir, "bad.aac")
	if err := os.WriteFile(bad, []byte("not-audio-at-all"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateAudio(bad, ".aac"); err == nil {
		t.Fatal("invalid adts accepted")
	}

	wav := filepath.Join(dir, "ok.wav")
	if err := os.WriteFile(wav, append([]byte("RIFF"), make([]byte, 20)...), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := validateAudio(wav, ".wav"); err != nil {
		t.Fatalf("valid wav rejected: %v", err)
	}
}

func TestJoinApproveFlow(t *testing.T) {
	dir := t.TempDir()
	m, err := pairing.NewManager(filepath.Join(dir, "devices.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	id, err := identity.LoadOrCreate(filepath.Join(dir, "identity.json"), "测试电脑")
	if err != nil {
		t.Fatal(err)
	}
	srv := &Server{Pair: m, Identity: id, ServicePort: 18082}
	h := srv.Handler()

	post := func(path, body, remote string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(body))
		req.RemoteAddr = remote
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := post("/api/v1/join", `{"device_id":"0011223344556677","device_name":"手机 A","fp":"AA11"}`, "192.0.2.10:1234")
	if rec.Code != http.StatusCreated {
		t.Fatalf("join status = %d: %s", rec.Code, rec.Body.String())
	}
	var created map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatal(err)
	}
	reqID, _ := created["request_id"].(string)
	if reqID == "" {
		t.Fatal("empty request id")
	}

	// 手机还没被允许之前拿不到 token。
	get := func(path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodGet, path, nil)
		req.RemoteAddr = "192.0.2.10:1234"
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}
	var st map[string]any
	if err := json.Unmarshal(get("/api/v1/join/"+reqID).Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	if st["status"] != "pending" || st["token"] != nil {
		t.Fatalf("pending status = %v", st)
	}

	// 非本机不能点允许。
	rec = post("/api/v1/admin/join/"+reqID+"/approve", "", "192.0.2.10:1234")
	if rec.Code != http.StatusForbidden {
		t.Fatalf("remote approve status = %d", rec.Code)
	}

	// 本机页面点允许。
	rec = post("/api/v1/admin/join/"+reqID+"/approve", "", "127.0.0.1:5555")
	if rec.Code != http.StatusOK {
		t.Fatalf("approve status = %d: %s", rec.Code, rec.Body.String())
	}

	st = nil
	if err := json.Unmarshal(get("/api/v1/join/"+reqID).Body.Bytes(), &st); err != nil {
		t.Fatal(err)
	}
	token, _ := st["token"].(string)
	if st["status"] != "approved" || token == "" {
		t.Fatalf("approved status = %v", st)
	}

	// token 能拉指令。
	req := httptest.NewRequest(http.MethodGet, "/api/v1/commands", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"sync":false`) {
		t.Fatalf("commands = %d %s", rec.Code, rec.Body.String())
	}
}

func TestReportsPushFlow(t *testing.T) {
	dir := t.TempDir()
	m, err := pairing.NewManager(filepath.Join(dir, "devices.json"), time.Minute)
	if err != nil {
		t.Fatal(err)
	}
	code, _ := m.StartWindow()
	_, token, err := m.Pair("手机 A", code)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := report.NewStore(filepath.Join(dir, "reports"))
	if err != nil {
		t.Fatal(err)
	}
	rep := &report.Report{
		ID: "r_20261028_180000", Date: "2026-10-28", Title: "10月28日 录音日报",
		Status: report.StatusDone, GeneratedAt: time.Now(),
		Summary: "今日 2 段录音", Content: "## 今日概览\n\n今天完成日报。", JobCount: 2,
	}
	if err := rs.Save(rep); err != nil {
		t.Fatal(err)
	}
	sched := report.NewScheduler(rs, nil, nil, filepath.Join(dir, "report_settings.json"), "录音日报",
		report.Settings{Enabled: true, GenerateAt: "18:00"})
	srv := &Server{Pair: m, Reports: rs, ReportScheduler: sched}
	h := srv.Handler()

	do := func(method, path string, body string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+token)
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, req)
		return rec
	}

	rec := do(http.MethodGet, "/api/v1/commands", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), rep.ID) {
		t.Fatalf("commands = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(http.MethodGet, "/api/v1/reports/"+rep.ID, "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), "今天完成日报") {
		t.Fatalf("report = %d %s", rec.Code, rec.Body.String())
	}
	// 历史同步只返回元数据和有内容的日报，不带全文、不带空日报。
	empty := &report.Report{
		ID: "r_20261029_180000", Date: "2026-10-29", Title: "10月29日 录音日报",
		Status: report.StatusDone, GeneratedAt: time.Now(), JobCount: 0,
	}
	if err := rs.Save(empty); err != nil {
		t.Fatal(err)
	}
	rec = do(http.MethodGet, "/api/v1/reports", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), rep.ID) {
		t.Fatalf("reports index = %d %s", rec.Code, rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), "今天完成日报") {
		t.Fatalf("reports index should not include content: %s", rec.Body.String())
	}
	if strings.Contains(rec.Body.String(), empty.ID) {
		t.Fatalf("reports index should skip empty report: %s", rec.Body.String())
	}
	rec = do(http.MethodPost, "/api/v1/reports/"+rep.ID+"/ack", "{}")
	if rec.Code != http.StatusOK {
		t.Fatalf("ack = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(http.MethodGet, "/api/v1/commands", "")
	if strings.Contains(rec.Body.String(), rep.ID) {
		t.Fatalf("report pushed twice: %s", rec.Body.String())
	}

	rec = do(http.MethodGet, "/api/v1/report-settings", "")
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"generate_at":"18:00"`) {
		t.Fatalf("settings = %d %s", rec.Code, rec.Body.String())
	}
	rec = do(http.MethodPost, "/api/v1/report-settings",
		`{"enabled":false,"generate_at":"22:30"}`)
	if rec.Code != http.StatusOK || !strings.Contains(rec.Body.String(), `"generate_at":"22:30"`) {
		t.Fatalf("update settings = %d %s", rec.Code, rec.Body.String())
	}
}

func TestAdminBlocksCrossSite(t *testing.T) {
	dir := t.TempDir()
	m, _ := pairing.NewManager(filepath.Join(dir, "devices.json"), time.Minute)
	srv := &Server{Pair: m}
	h := srv.Handler()

	req := httptest.NewRequest(http.MethodGet, "/api/v1/admin/join", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("Sec-Fetch-Site", "cross-site")
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("cross-site status = %d", rec.Code)
	}

	req = httptest.NewRequest(http.MethodGet, "/api/v1/admin/join", nil)
	req.RemoteAddr = "127.0.0.1:5555"
	req.Header.Set("Sec-Fetch-Site", "same-origin")
	rec = httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("same-origin status = %d", rec.Code)
	}
}
