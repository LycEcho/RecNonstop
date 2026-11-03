package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"warehouse-audio/internal/identity"
	"warehouse-audio/internal/job"
	"warehouse-audio/internal/pairing"
	"warehouse-audio/internal/report"
)

type reportFunc func(context.Context, string) (string, error)

func (f reportFunc) GenerateDailyReport(ctx context.Context, material string) (string, error) {
	return f(ctx, material)
}

func TestManualReportGenerationDeadline(t *testing.T) {
	for _, parentTimeout := range []time.Duration{0, time.Minute} {
		t.Run(parentTimeout.String(), func(t *testing.T) {
			dir := t.TempDir()
			js, err := job.NewStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			_, start, _, err := report.PeriodFor(time.Now(), "18:00")
			if err != nil {
				t.Fatal(err)
			}
			js.CreateFull(&job.Job{ID: "synthetic", Status: job.StatusDone,
				RecordedAt: start.Add(time.Minute).UnixMilli(),
				Result:     &job.Result{Duration: 60, Segments: []job.Segment{{Text: "合成日报材料。"}}}})
			rs, err := report.NewStore(filepath.Join(dir, "reports"))
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			if parentTimeout > 0 {
				cancel()
				ctx, cancel = context.WithTimeout(context.Background(), parentTimeout)
			}
			defer cancel()
			var deadline time.Time
			reporter := reportFunc(func(ctx context.Context, material string) (string, error) {
				deadline, _ = ctx.Deadline()
				return "## 今日概览\n\n合成日报。", nil
			})
			sched := report.NewScheduler(rs, js, reporter, filepath.Join(dir, "report_settings.json"),
				"录音日报", report.Settings{Enabled: true, GenerateAt: "18:00"})
			srv := &Server{Reports: rs, ReportScheduler: sched}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/reports/generate", nil).WithContext(ctx)
			req.RemoteAddr = "127.0.0.1:5555"
			out := httptest.NewRecorder()
			started := time.Now()
			srv.Handler().ServeHTTP(out, req)
			if out.Code != http.StatusOK || deadline.IsZero() {
				t.Fatalf("manual report did not reach reporter with deadline: status=%d body=%s", out.Code, out.Body.String())
			}
			if parentTimeout > 0 {
				want, _ := ctx.Deadline()
				if !deadline.Equal(want) {
					t.Errorf("parent deadline extended: got %v want %v", deadline, want)
				}
			} else if budget := deadline.Sub(started); budget < time.Hour-5*time.Second || budget > time.Hour+5*time.Second {
				t.Errorf("manual full-day report budget = %v, want one hour", budget)
			}
		})
	}
}

type uploadSink struct {
	buffer   bytes.Buffer
	writeErr error
	closeErr error
	closed   bool
}

func (s *uploadSink) Write(p []byte) (int, error) {
	if s.writeErr != nil {
		return 0, s.writeErr
	}
	return s.buffer.Write(p)
}

func (s *uploadSink) Close() error {
	s.closed = true
	return s.closeErr
}

func TestSaveUploadDistinguishesStorageFailureFromOversize(t *testing.T) {
	storageErr := errors.New("synthetic storage failure")
	for _, tc := range []struct {
		name     string
		input    string
		sink     uploadSink
		wantCode int
	}{
		{"saved", "1234", uploadSink{}, http.StatusOK},
		{"oversize", "12345", uploadSink{}, http.StatusRequestEntityTooLarge},
		{"disk full", "1234", uploadSink{writeErr: storageErr}, http.StatusInternalServerError},
		{"close failed", "1234", uploadSink{closeErr: storageErr}, http.StatusInternalServerError},
	} {
		t.Run(tc.name, func(t *testing.T) {
			status, err := saveUpload(&tc.sink, strings.NewReader(tc.input), 4)
			if status != tc.wantCode || (err == nil) != (status == http.StatusOK) {
				t.Fatalf("status=%d err=%v, want %d", status, err, tc.wantCode)
			}
			if !tc.sink.closed {
				t.Fatal("upload file was not closed")
			}
			if tc.wantCode == http.StatusInternalServerError && !errors.Is(err, storageErr) {
				t.Fatalf("storage cause lost: %v", err)
			}
		})
	}
}

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

func TestManualReportGenerationDateRequests(t *testing.T) {
	for _, tc := range []struct {
		name   string
		body   string
		remote string
		site   string
		method string
		status int
	}{
		{name: "empty body", status: http.StatusOK},
		{name: "empty object", body: `{}`, status: http.StatusOK},
		{name: "historical", body: `{"date":"2001-02-03"}`, status: http.StatusOK},
		{name: "today", body: `{"date":"` + time.Now().Format("2006-01-02") + `"}`, status: http.StatusOK},
		{name: "explicit empty date", body: `{"date":""}`, status: http.StatusBadRequest},
		{name: "blank date", body: `{"date":" "}`, status: http.StatusBadRequest},
		{name: "invalid calendar date", body: `{"date":"2001-02-30"}`, status: http.StatusBadRequest},
		{name: "not strict date", body: `{"date":"2001-2-3"}`, status: http.StatusBadRequest},
		{name: "future date", body: `{"date":"2999-01-01"}`, status: http.StatusBadRequest},
		{name: "null date", body: `{"date":null}`, status: http.StatusBadRequest},
		{name: "numeric date", body: `{"date":20010203}`, status: http.StatusBadRequest},
		{name: "null body", body: `null`, status: http.StatusBadRequest},
		{name: "array body", body: `[]`, status: http.StatusBadRequest},
		{name: "unknown field", body: `{"day":"2001-02-03"}`, status: http.StatusBadRequest},
		{name: "duplicate date", body: `{"date":"2001-02-03","date":"2001-02-04"}`, status: http.StatusBadRequest},
		{name: "second JSON", body: `{"date":"2001-02-03"}{}`, status: http.StatusBadRequest},
		{name: "trailing text", body: `{"date":"2001-02-03"} trailing`, status: http.StatusBadRequest},
		{name: "truncated JSON", body: `{"date":"2001-02-03"`, status: http.StatusBadRequest},
		{name: "oversize", body: `{}` + strings.Repeat(" ", 1023), status: http.StatusBadRequest},
		{name: "nonlocal", body: `{"date":"2001-02-03"}`, remote: "192.0.2.10:1234", status: http.StatusForbidden},
		{name: "cross site", body: `{"date":"2001-02-03"}`, site: "cross-site", status: http.StatusForbidden},
		{name: "wrong method", body: `{"date":"2001-02-03"}`, method: http.MethodGet, status: http.StatusMethodNotAllowed},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			js, err := job.NewStore(dir)
			if err != nil {
				t.Fatal(err)
			}
			rs, err := report.NewStore(filepath.Join(dir, "reports"))
			if err != nil {
				t.Fatal(err)
			}
			sched := report.NewScheduler(rs, js, reportFunc(func(context.Context, string) (string, error) {
				t.Fatal("empty store or rejected request should not reach reporter")
				return "", nil
			}), filepath.Join(dir, "settings.json"), "合成日报", report.Settings{GenerateAt: "18:00"})
			srv := &Server{Reports: rs, ReportScheduler: sched}
			method := tc.method
			if method == "" {
				method = http.MethodPost
			}
			req := httptest.NewRequest(method, "/api/v1/admin/reports/generate", strings.NewReader(tc.body))
			req.RemoteAddr = tc.remote
			if req.RemoteAddr == "" {
				req.RemoteAddr = "127.0.0.1:5555"
			}
			if tc.site != "" {
				req.Header.Set("Sec-Fetch-Site", tc.site)
			}
			out := httptest.NewRecorder()
			srv.Handler().ServeHTTP(out, req)
			if out.Code != tc.status {
				t.Fatalf("status = %d want %d; body %s", out.Code, tc.status, out.Body.String())
			}
			if tc.status != http.StatusOK {
				if len(rs.List(100)) != 0 {
					t.Fatal("rejected request created a report")
				}
				return
			}
			var got report.Report
			if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
				t.Fatal(err)
			}
			want := time.Now().Format("2006-01-02")
			if tc.name == "historical" {
				want = "2001-02-03"
			}
			if got.Date != want || len(rs.List(100)) != 1 {
				t.Fatalf("wrong default/explicit date: %+v", got)
			}
		})
	}
}

func TestManualHistoricalReportPreservesVersionsAndDeadline(t *testing.T) {
	dir := t.TempDir()
	js, err := job.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Date(2001, 2, 3, 0, 0, 0, 0, time.Local)
	for i, sample := range []struct {
		at   time.Time
		text string
	}{
		{start.Add(23 * time.Hour), "晚间仍属于该日的原文"},
		{start.AddDate(0, 0, 1), "次日的原文不可纳入"},
	} {
		id := []string{"late", "next"}[i]
		js.CreateFull(&job.Job{ID: id, Status: job.StatusDone, RecordedAt: sample.at.UnixMilli(),
			Result: &job.Result{Duration: 60, Segments: []job.Segment{{Text: sample.text}}}})
	}
	rs, err := report.NewStore(filepath.Join(dir, "reports"))
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	wantDeadline, _ := ctx.Deadline()
	calls := 0
	reporter := reportFunc(func(ctx context.Context, material string) (string, error) {
		calls++
		deadline, ok := ctx.Deadline()
		if !ok || !deadline.Equal(wantDeadline) {
			t.Error("historical request extended its parent deadline")
		}
		if !strings.Contains(material, "晚间仍属于该日的原文") || strings.Contains(material, "次日的原文不可纳入") {
			t.Error("historical request did not use the full local day")
		}
		return "合成历史汇总。", nil
	})
	sched := report.NewScheduler(rs, js, reporter, filepath.Join(dir, "settings.json"), "合成日报",
		report.Settings{GenerateAt: "18:00"})
	srv := &Server{Reports: rs, ReportScheduler: sched}
	var previousID string
	for i := 0; i < 2; i++ {
		req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/reports/generate",
			strings.NewReader(`{"date":"2001-02-03"}`)).WithContext(ctx)
		req.RemoteAddr = "127.0.0.1:5555"
		out := httptest.NewRecorder()
		srv.Handler().ServeHTTP(out, req)
		if out.Code != http.StatusOK {
			t.Fatalf("historical generation failed: %d %s", out.Code, out.Body.String())
		}
		var got report.Report
		if err := json.Unmarshal(out.Body.Bytes(), &got); err != nil {
			t.Fatal(err)
		}
		if got.ID == previousID || got.JobCount != 1 || got.Date != "2001-02-03" ||
			!got.PeriodStart.Equal(start) || !got.PeriodEnd.Equal(start.AddDate(0, 0, 1)) {
			t.Fatalf("wrong historical report or overwritten ID: %+v", got)
		}
		previousID = got.ID
	}
	if calls != 2 || len(rs.List(100)) != 2 {
		t.Fatal("historical regeneration did not retain both versions")
	}
}

func TestCanceledManualHistoricalReportCreatesNoVersion(t *testing.T) {
	dir := t.TempDir()
	js, err := job.NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	rs, err := report.NewStore(filepath.Join(dir, "reports"))
	if err != nil {
		t.Fatal(err)
	}
	sched := report.NewScheduler(rs, js, nil, filepath.Join(dir, "settings.json"), "合成日报", report.Settings{GenerateAt: "18:00"})
	srv := &Server{Reports: rs, ReportScheduler: sched}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/reports/generate",
		strings.NewReader(`{"date":"2001-02-03"}`)).WithContext(ctx)
	req.RemoteAddr = "127.0.0.1:5555"
	out := httptest.NewRecorder()
	srv.Handler().ServeHTTP(out, req)
	if out.Code == http.StatusOK || len(rs.List(100)) != 0 {
		t.Fatal("already canceled request created a successful report")
	}
}
