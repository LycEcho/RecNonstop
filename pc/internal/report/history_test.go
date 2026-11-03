package report

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestReadyPageKeepsAllHistoryAcrossInsertAndRetention(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2027, 3, 2, 12, 0, 0, 0, time.UTC)
	// More than 500 newer empty reports must not hide the useful history.
	for i := 0; i < 510; i++ {
		r := &Report{ID: fmt.Sprintf("empty-%03d", i), Status: StatusDone, GeneratedAt: stamp.Add(time.Hour)}
		if err := s.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	for i := 0; i < 215; i++ {
		r := &Report{ID: fmt.Sprintf("ready-%03d", i), Date: "2027-03-02", Status: StatusDone, JobCount: 1, GeneratedAt: stamp, Content: "合成正文"}
		if err := s.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	first, cursor, err := s.ReadyPage(90, "")
	if err != nil || len(first) != 90 || cursor == "" || first[0].ID != "ready-214" {
		t.Fatalf("first page: %d, %q, %v", len(first), cursor, err)
	}
	seen := map[string]bool{}
	for _, r := range first {
		seen[r.ID] = true
	}
	// A newer insertion and deletion of the cursor's report cannot shift pages.
	if err := s.Save(&Report{ID: "inserted", Status: StatusDone, JobCount: 1, GeneratedAt: stamp.Add(2 * time.Hour)}); err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	delete(s.reports, first[len(first)-1].ID)
	s.mu.Unlock()
	for cursor != "" {
		page, next, err := s.ReadyPage(90, cursor)
		if err != nil || len(page) == 0 || next == cursor {
			t.Fatalf("page: %v, %q", err, next)
		}
		for _, r := range page {
			if seen[r.ID] || r.ID == "inserted" {
				t.Fatalf("shifted or duplicate entry: %s", r.ID)
			}
			seen[r.ID] = true
		}
		cursor = next
	}
	if len(seen) != 215 {
		t.Fatalf("only %d original versions fetched", len(seen))
	}
	first[0].Content = "changed copy"
	stored, _ := s.Get(first[0].ID)
	if stored.Content != "合成正文" {
		t.Fatal("page exposed stored report")
	}
	if r, ok := s.LatestForDate("2027-03-02"); !ok || r.ID != "ready-214" {
		t.Fatal("date search capped before older reports")
	}
	for _, bad := range []string{"!", "e30", "bnVsbA"} {
		if _, _, err := s.ReadyPage(90, bad); err != ErrInvalidCursor {
			t.Fatalf("accepted invalid cursor %q", bad)
		}
	}
}

func TestAckOnlyConfirmsDurablySavedReport(t *testing.T) {
	dir := t.TempDir()
	s, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	stamp := time.Date(2027, 3, 2, 12, 0, 0, 0, time.UTC)
	for i := 0; i < 210; i++ {
		r := &Report{ID: fmt.Sprintf("report-%03d", i), Status: StatusDone, JobCount: 1, GeneratedAt: stamp.Add(time.Duration(i) * time.Minute)}
		if err := s.Save(r); err != nil {
			t.Fatal(err)
		}
	}
	if !s.Ack("report-209", "test-device") || !s.Ack("report-209", "test-device") {
		t.Fatal("ack must be idempotent")
	}
	old, _ := s.Get("report-000")
	if len(old.DeliveredTo) != 0 {
		t.Fatal("ack confirmed an undownloaded report")
	}
	for i := 1; i < 209; i++ {
		if !s.Ack(fmt.Sprintf("report-%03d", i), "test-device") {
			t.Fatal("ack failed")
		}
	}
	if pending := s.PendingFor("test-device"); len(pending) != 1 || pending[0].ID != "report-000" {
		t.Fatal("old pending report hidden by list cap")
	}
	reloaded, err := NewStore(dir)
	if err != nil {
		t.Fatal(err)
	}
	if pending := reloaded.PendingFor("test-device"); len(pending) != 1 || pending[0].ID != "report-000" {
		t.Fatal("ack did not survive reload")
	}
	// A failed disk write must leave the in-memory report pending for retry.
	block := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(block, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	s.dir = block
	if s.Ack("report-000", "test-device") {
		t.Fatal("ack succeeded without persistence")
	}
	if pending := s.PendingFor("test-device"); len(pending) != 1 {
		t.Fatal("failed ack lost pending delivery")
	}
}

func TestHistoryMissingTimestampsAndFailedWrites(t *testing.T) {
	s, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	for _, id := range []string{"a", "b", "c"} {
		if err := s.Save(&Report{ID: id, Status: StatusDone, JobCount: 1, Content: "original"}); err != nil {
			t.Fatal(err)
		}
	}
	page, cursor, err := s.ReadyPage(2, "")
	if err != nil || len(page) != 2 || cursor == "" {
		t.Fatal("missing-timestamp first page failed")
	}
	page, next, err := s.ReadyPage(2, cursor)
	if err != nil || len(page) != 1 || page[0].ID != "a" || next != "" {
		t.Fatal("generated zero-time cursor could not be resumed")
	}
	block := filepath.Join(t.TempDir(), "not-a-directory")
	if err := os.WriteFile(block, []byte("test"), 0600); err != nil {
		t.Fatal(err)
	}
	s.dir = block
	if err := s.Save(&Report{ID: "new", Status: StatusDone, JobCount: 1}); err == nil {
		t.Fatal("save should fail")
	}
	if _, exists := s.Get("new"); exists {
		t.Fatal("failed save published a nonexistent disk report")
	}
	if err := s.Update("a", func(r *Report) { r.Content = "unsaved"; r.JobCount = 0 }); err == nil {
		t.Fatal("update should fail")
	}
	original, _ := s.Get("a")
	if original.Content != "original" || original.JobCount != 1 {
		t.Fatal("failed update changed visible history")
	}
}
