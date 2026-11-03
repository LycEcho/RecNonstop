package report

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"sort"
	"time"
)

var ErrInvalidCursor = errors.New("invalid report history cursor")

type historyCursor struct {
	Time time.Time `json:"time"`
	ID   string    `json:"id"`
}

// Timestamps can tie when reports are regenerated. IDs provide a stable order.
func reportNewer(a, b *Report) bool {
	ta, tb := reportTime(a), reportTime(b)
	if ta.Equal(tb) {
		return a.ID > b.ID
	}
	return ta.After(tb)
}

func (s *Store) latestMatching(match func(*Report) bool) (*Report, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var best *Report
	for _, r := range s.reports {
		if match(r) && (best == nil || reportNewer(r, best)) {
			best = r
		}
	}
	if best == nil {
		return nil, false
	}
	return clone(best), true
}

// ReadyPage filters before limiting and does not cap the complete history.
// The cursor carries its ordering key, so retention of the previous page or a
// concurrent newer report cannot shift offsets and silently skip old reports.
func (s *Store) ReadyPage(limit int, cursor string) ([]*Report, string, error) {
	if limit <= 0 || limit > 500 {
		limit = 90
	}
	var before *Report
	if cursor != "" {
		if len(cursor) > 1024 {
			return nil, "", ErrInvalidCursor
		}
		b, err := base64.RawURLEncoding.DecodeString(cursor)
		var key struct {
			Time *time.Time `json:"time"`
			ID   string     `json:"id"`
		}
		if err != nil || json.Unmarshal(b, &key) != nil || key.Time == nil || key.ID == "" || len(key.ID) > 96 {
			return nil, "", ErrInvalidCursor
		}
		before = &Report{ID: key.ID, GeneratedAt: *key.Time}
	}
	s.mu.RLock()
	defer s.mu.RUnlock()
	var list []*Report
	for _, r := range s.reports {
		if r.Status == StatusDone && r.JobCount > 0 && (before == nil || reportNewer(before, r)) {
			list = append(list, r)
		}
	}
	sort.Slice(list, func(i, j int) bool { return reportNewer(list[i], list[j]) })
	next := ""
	if len(list) > limit {
		list = list[:limit]
		last := list[len(list)-1]
		b, _ := json.Marshal(historyCursor{Time: reportTime(last), ID: last.ID})
		next = base64.RawURLEncoding.EncodeToString(b)
	}
	// Clone only the selected page. Deep-copying every historical report's
	// interactive detail on every page makes large archives needlessly costly.
	for i, r := range list {
		list[i] = clone(r)
	}
	return list, next, nil
}
