package report

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"warehouse-audio/internal/job"
)

func TestDetailSourceCoordinatesSurviveChronologicalSortAndSessionSplit(t *testing.T) {
	start := time.Date(2027, 4, 10, 9, 0, 0, 0, time.Local)
	jobs := []*job.Job{
		{ID: "synthetic-one", RecordedAt: start.UnixMilli(), Result: &job.Result{Segments: []job.Segment{
			{Start: 0, End: 1, Text: ""},
			{Start: 400.125, End: 401, Speaker: "spk_0", Text: "较晚的原文"},
			{Start: 1.25, End: 2, Speaker: "spk_0", Text: "本录音最早的有效原文"},
		}, Analysis: json.RawMessage(`{"actions":[{"speaker":"spk_0","action":"核对清单"}]}`)}},
		{ID: "synthetic-two", RecordedAt: start.Add(10 * time.Second).UnixMilli(), Result: &job.Result{Segments: []job.Segment{
			{Start: 2.375, End: 3, Speaker: "spk_0", Text: "另一录音的原文"},
		}, Analysis: json.RawMessage(`{"actions":[{"speaker":"spk_0","action":"核对另一张清单"}]}`)}},
	}
	raw, err := buildInteractiveDetail(&Report{JobCount: 2}, jobs)
	if err != nil {
		t.Fatal(err)
	}
	var detail detailPayload
	if err = json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	if detail.Version != 2 || len(detail.Utterances) != 3 || len(detail.Actions) != 2 {
		t.Fatalf("unexpected detail shape: %+v", detail)
	}
	first, second, last := detail.Utterances[0], detail.Utterances[1], detail.Utterances[2]
	if first.RecordingIndex != 1 || first.SegmentIndex != 2 || first.StartMS != 1250 || !strings.Contains(first.SourceLabel, "09:00:01.250 · 录音1/spk_0") {
		t.Fatalf("first source coordinates lost: %+v", first)
	}
	if second.RecordingIndex != 2 || second.SegmentIndex != 0 || second.StartMS != 2375 || !strings.Contains(second.SourceLabel, "09:00:12.375 · 录音2/spk_0") {
		t.Fatalf("second source coordinates lost: %+v", second)
	}
	if last.RecordingIndex != 1 || last.SegmentIndex != 1 || last.StartMS != 400125 || last.Session == first.Session {
		t.Fatalf("late segment must remain in original recording but later session: %+v", last)
	}
	if first.Session != second.Session {
		t.Fatal("test requires different recordings within one session")
	}
	for i, a := range detail.Actions {
		target := detail.Utterances[a.FirstUtteranceID]
		if target.RecordingIndex != i+1 || target.Session != a.Session || !strings.Contains(a.Source, "回看录音") || !strings.Contains(a.Source, "起点") {
			t.Fatalf("candidate action falsely targets an utterance or wrong recording: %+v -> %+v", a, target)
		}
	}
	if detail.Actions[0].FirstUtteranceID != first.ID || detail.Actions[1].FirstUtteranceID != second.ID {
		t.Fatal("same-session candidate links must not collapse to session start")
	}
	for _, u := range detail.Utterances {
		material := materialTranscript(jobs[u.RecordingIndex-1], u.RecordingIndex-1)
		if !strings.Contains(material, "["+u.SourceLabel+"] "+u.Text) {
			t.Fatalf("detail label no longer matches source material: %+v", u)
		}
	}
}

func TestDetailSourceLabelsKeepNamedCollisionsAsSeparateCoordinates(t *testing.T) {
	start := time.Date(2027, 4, 10, 9, 0, 0, 0, time.Local)
	jobs := []*job.Job{
		{ID: "synthetic-a", RecordedAt: start.UnixMilli(), Result: &job.Result{Segments: []job.Segment{{Start: .125, End: 1, PersonName: "合成姓名", Person: "旧标签", Speaker: "spk_0", Text: "第一段原文"}}}},
		{ID: "synthetic-b", RecordedAt: start.UnixMilli(), Result: &job.Result{Segments: []job.Segment{{Start: .125, End: 1, PersonName: "合成姓名", Speaker: "spk_0", Text: "第二段原文"}}}},
	}
	raw, err := buildInteractiveDetail(&Report{}, jobs)
	if err != nil {
		t.Fatal(err)
	}
	var detail detailPayload
	if err = json.Unmarshal(raw, &detail); err != nil {
		t.Fatal(err)
	}
	a, b := detail.Utterances[0], detail.Utterances[1]
	if a.SourceLabel != b.SourceLabel || !strings.HasSuffix(a.SourceLabel, "· 合成姓名") {
		t.Fatal("named source priority or equal-label fixture changed")
	}
	if a.ID == b.ID || a.RecordingIndex == b.RecordingIndex {
		t.Fatal("equal display labels incorrectly merged original recordings")
	}
	// A display-label lookup must treat this as ambiguous, while explicit report
	// utterance IDs and original recording/segment coordinates remain distinct.
}
