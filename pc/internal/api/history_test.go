package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"warehouse-audio/internal/report"
)

func TestReportsIndexCursorAndSingleAckCapability(t *testing.T) {
	store, err := report.NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 93; i++ {
		r := &report.Report{ID: fmt.Sprintf("synthetic-%03d", i), Status: report.StatusDone, JobCount: 1, GeneratedAt: time.Date(2027, 3, 2, 12, i, 0, 0, time.UTC), Error: "summary failed", Content: "not metadata"}
		if err := store.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	srv := &Server{Reports: store}
	var cursor string
	seen := map[string]bool{}
	for page := 0; page < 2; page++ {
		rec := httptest.NewRecorder()
		srv.reportsIndex(rec, httptest.NewRequest(http.MethodGet, "/api/v1/reports?limit=90&cursor="+cursor, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("status: %d", rec.Code)
		}
		var body struct {
			Reports []map[string]any `json:"reports"`
			Next    string           `json:"next_cursor"`
			Ack     string           `json:"ack_mode"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatal(err)
		}
		if body.Ack != "single" {
			t.Fatal("client cannot identify safe ack semantics")
		}
		for _, r := range body.Reports {
			id := r["id"].(string)
			if seen[id] {
				t.Fatal("duplicate metadata")
			}
			seen[id] = true
			if _, ok := r["content"]; ok || r["error"] != "summary failed" {
				t.Fatal("wrong metadata visibility")
			}
		}
		cursor = body.Next
		if (page == 0) != (cursor != "") {
			t.Fatal("wrong continuation")
		}
	}
	if len(seen) != 93 {
		t.Fatal("history truncated")
	}
	rec := httptest.NewRecorder()
	srv.reportsIndex(rec, httptest.NewRequest(http.MethodGet, "/api/v1/reports?cursor=invalid", nil))
	if rec.Code != http.StatusBadRequest {
		t.Fatal("invalid cursor silently reset history")
	}
}
