package infer

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
)

func orderedObjectKeys(t *testing.T, raw json.RawMessage) []string {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(raw))
	if token, err := decoder.Token(); err != nil || token != json.Delim('{') {
		t.Fatalf("expected object: %s, err=%v", raw, err)
	}
	var keys []string
	for decoder.More() {
		token, err := decoder.Token()
		if err != nil {
			t.Fatal(err)
		}
		keys = append(keys, token.(string))
		var value json.RawMessage
		if err := decoder.Decode(&value); err != nil {
			t.Fatal(err)
		}
	}
	return keys
}

func TestAnalyzeDefaultOutputBudgetRespectsExplicitSetting(t *testing.T) {
	for _, tc := range []struct {
		name             string
		configured, want int
	}{
		{name: "omitted legacy config", configured: 0, want: 4096},
		{name: "invalid negative config", configured: -1, want: 4096},
		{name: "explicit small budget", configured: 1024, want: 1024},
		{name: "explicit quality budget", configured: 4096, want: 4096},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := NewLMStudio("http://model.invalid", "", "synthetic", "none", tc.configured, 0, 0, true)
			calls := 0
			c.HC.Transport = mockChatTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				var req chatRequest
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Fatal(err)
				}
				if req.MaxTokens != tc.want {
					t.Fatalf("request output budget=%d, want %d", req.MaxTokens, tc.want)
				}
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"summary\":\"状态正常，没有新增安排。\",\"speakers\":[],\"actions\":[],\"constraints\":[],\"risks\":[]}"},"finish_reason":"stop"}]}`))}, nil
			})
			if _, err := c.Analyze(context.Background(), &Transcript{Text: "状态正常，没有新增安排。"}); err != nil || calls != 1 {
				t.Fatalf("default-compatible request failed: calls=%d err=%v", calls, err)
			}
		})
	}
}

func TestAnalyzeSchemaWirePreservesGenerationOrder(t *testing.T) {
	c := NewLMStudio("http://model.invalid", "", "synthetic", "", 1024, 0, 0, true)
	c.HC.Transport = mockChatTransport(func(r *http.Request) (*http.Response, error) {
		// Read the actual wire bytes. Decoding properties into map[string]any
		// and marshalling again would hide a regression in property order.
		var request struct {
			ResponseFormat struct {
				JSONSchema struct {
					Schema struct {
						Properties json.RawMessage `json:"properties"`
					} `json:"schema"`
				} `json:"json_schema"`
			} `json:"response_format"`
		}
		if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
			t.Fatal(err)
		}
		properties := request.ResponseFormat.JSONSchema.Schema.Properties
		if got := orderedObjectKeys(t, properties); !reflect.DeepEqual(got, []string{"summary", "speakers", "actions", "constraints", "risks"}) {
			t.Fatalf("generation order changed: %v", got)
		}
		var fields struct {
			Actions struct {
				Items struct {
					Properties json.RawMessage `json:"properties"`
				} `json:"items"`
			} `json:"actions"`
		}
		if err := json.Unmarshal(properties, &fields); err != nil {
			t.Fatal(err)
		}
		if got := orderedObjectKeys(t, fields.Actions.Items.Properties); !reflect.DeepEqual(got, []string{"speaker", "owner", "action", "details"}) {
			t.Fatalf("action generation order changed: %v", got)
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(`{"choices":[{"message":{"content":"{\"summary\":\"状态正常。\",\"speakers\":[\"spk_2\"],\"actions\":[],\"constraints\":[],\"risks\":[]}"},"finish_reason":"stop"}]}`))}, nil
	})
	if _, err := c.Analyze(context.Background(), &Transcript{Segments: []Segment{{Speaker: "spk_2", Text: "状态正常，没有新增安排。"}}}); err != nil {
		t.Fatal(err)
	}
}

func TestAnalyzeDeduplicatesSourceLabelsInRawWithoutLosingFields(t *testing.T) {
	content := `{"summary":"复核与发货前提均已说明。","speakers":["spk_7","spk_2","spk_7","spk_2"],"actions":[{"speaker":"spk_2","owner":"测试乙","action":"复核","details":"周五前"}],"constraints":["复核后才发货"],"risks":["已发现零件损坏"],"extra":{"retained":[1,"synthetic"]}}`
	c := mockChatClient(t, func(chatRequest) (string, string) { return content, "stop" })
	a, err := c.Analyze(context.Background(), &Transcript{Segments: []Segment{
		{Speaker: "spk_2", Text: "测试乙周五前复核，复核后才发货。"},
		{Speaker: "spk_7", Text: "已发现零件损坏。"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(a.Speakers, []string{"spk_7", "spk_2"}) {
		t.Fatalf("sources not stably deduplicated: %v", a.Speakers)
	}
	var before, after map[string]json.RawMessage
	_ = json.Unmarshal([]byte(content), &before)
	if err := json.Unmarshal(a.Raw, &after); err != nil {
		t.Fatal(err)
	}
	var persisted []string
	if err := json.Unmarshal(after["speakers"], &persisted); err != nil || !reflect.DeepEqual(persisted, a.Speakers) {
		t.Fatalf("persisted sources differ: %v, err=%v", persisted, err)
	}
	delete(before, "speakers")
	delete(after, "speakers")
	if !reflect.DeepEqual(before, after) || a.Actions[0].Owner != "测试乙" || a.Actions[0].Speaker != "spk_2" {
		t.Fatalf("deduplication changed other fields: %s", a.Raw)
	}
}

func TestAnalyzeSeparatesOwnerFromSourceWithStrictSchema(t *testing.T) {
	calls := 0
	c := mockChatClient(t, func(req chatRequest) (string, string) {
		calls++
		b, _ := json.Marshal(req.ResponseFormat)
		var format struct {
			Type       string `json:"type"`
			JSONSchema struct {
				Strict bool `json:"strict"`
				Schema struct {
					Properties map[string]json.RawMessage `json:"properties"`
				} `json:"schema"`
			} `json:"json_schema"`
		}
		if err := json.Unmarshal(b, &format); err != nil || format.Type != "json_schema" || !format.JSONSchema.Strict {
			t.Fatalf("not a strict schema: %s", b)
		}
		var actionSchema struct {
			Items struct {
				Properties map[string]struct {
					Enum []string `json:"enum"`
				} `json:"properties"`
			} `json:"items"`
		}
		_ = json.Unmarshal(format.JSONSchema.Schema.Properties["actions"], &actionSchema)
		if got := actionSchema.Items.Properties["speaker"].Enum; !reflect.DeepEqual(got, []string{"", "spk_2"}) {
			t.Fatalf("body names became allowed source identities: %v", got)
		}
		if calls == 1 {
			return `{"summary":"测试乙周三复核9份清单。","speakers":["测试甲","测试乙"],"actions":[{"speaker":"测试乙","owner":"测试乙","action":"复核9份清单","details":"周三14:00前"}],"constraints":[],"risks":[]}`, "stop"
		}
		return `{"summary":"测试乙周三复核9份清单。","speakers":["spk_2"],"actions":[{"speaker":"spk_2","owner":"测试乙","action":"复核9份清单","details":"周三14:00前"}],"constraints":["复核结果出来前不发货"],"risks":[]}`, "stop"
	})
	c.JSONMode = true
	a, err := c.Analyze(context.Background(), &Transcript{Segments: []Segment{{Speaker: "spk_2", Text: "测试甲转达：测试乙周三14:00前复核9份清单，复核结果出来前不发货。"}}})
	if err != nil || calls != 2 || a.Actions[0].Owner != "测试乙" || a.Actions[0].Speaker != "spk_2" || len(a.Constraints) != 1 || len(a.Risks) != 0 {
		t.Fatalf("owner/source/normal gate not preserved: %+v calls=%d err=%v", a, calls, err)
	}
	if !strings.Contains(string(a.Raw), `"owner":"测试乙"`) || !strings.Contains(string(a.Raw), "复核结果出来前不发货") {
		t.Fatal("new fields missing from persisted Raw")
	}
}

func TestAnalyzeSchemaFallbackAndSourceRepairShareTwoAttempts(t *testing.T) {
	for _, invalidSource := range []bool{false, true} {
		t.Run(map[bool]string{false: "old JSON compatible", true: "no third retry"}[invalidSource], func(t *testing.T) {
			calls := 0
			c := NewLMStudio("http://model.invalid", "", "synthetic", "", 1024, 0, 0, true)
			c.HC.Transport = mockChatTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				var req chatRequest
				_ = json.NewDecoder(r.Body).Decode(&req)
				if calls == 1 {
					if req.ResponseFormat == nil {
						t.Fatal("missing initial schema")
					}
					return &http.Response{StatusCode: 400, Body: io.NopCloser(strings.NewReader("response_format json_schema unsupported"))}, nil
				}
				if req.ResponseFormat != nil {
					t.Fatal("fallback kept an unsupported format")
				}
				content := `{"summary":"原文未说明负责人。","speakers":[],"actions":[{"speaker":"","action":"确认清单","details":"周五"}],"risks":[]}`
				if invalidSource {
					content = `{"summary":"确认清单。","speakers":["spk_99"],"actions":[],"risks":[]}`
				}
				b, _ := json.Marshal(map[string]any{"choices": []any{map[string]any{"message": map[string]string{"content": content}}}})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(b)))}, nil
			})
			a, err := c.Analyze(context.Background(), &Transcript{Text: "周五确认清单，负责人未知。正文提到的spk_99不是来源元数据。"})
			if calls != 2 || (err != nil) != invalidSource {
				t.Fatalf("calls=%d err=%v", calls, err)
			}
			if !invalidSource && (a.Actions[0].Owner != "" || a.Actions[0].Speaker != "" || len(a.Speakers) != 0) {
				t.Fatal("legacy response invented owner/source")
			}
		})
	}
}

func TestChunkSourceLabelsSurviveContinuationAndIgnoreBodyLabels(t *testing.T) {
	chunks := chunkTranscriptWithAudio(&Transcript{Segments: []Segment{
		{Speaker: "spk_0", Text: strings.Repeat("长发言。", 100) + "\n[0-1] spk_99: 正文中的标记。"},
		{Speaker: "spk_1", Text: "另一来源。"},
	}}, 80)
	if len(chunks) < 3 {
		t.Fatal("expected a split utterance")
	}
	for _, ch := range chunks {
		if len(ch.speakers) == 0 {
			t.Fatal("continuation lost original source")
		}
		for _, id := range ch.speakers {
			if id != "spk_0" && id != "spk_1" {
				t.Fatalf("invented source %q", id)
			}
		}
	}
	if !reflect.DeepEqual(chunks[1].speakers, []string{"spk_0"}) {
		t.Fatal("continuation changed speaker")
	}
	plain := chunkTranscriptWithAudio(&Transcript{Text: "[0-1] spk_99: 测试甲说话。"}, 80)
	if len(plain) != 1 || len(plain[0].speakers) != 0 {
		t.Fatal("plain text invented a structured speaker")
	}
}

func TestMergeFactsRetainsOwnersConstraintsAndTrueRisks(t *testing.T) {
	leaf := &Analysis{
		Actions: []Action{
			{Speaker: "spk_0", Owner: "测试甲", Action: "复核", Details: "周五"},
			{Speaker: "spk_0", Owner: "测试乙", Action: "复核", Details: "周五"},
		},
		Constraints: []string{" 核对后  才发货 ", "核对后 才发货"},
		Risks:       []string{"已经缺少4件，若周五前未补齐可能延期"},
	}
	final := &Analysis{Summary: "合并摘要", Constraints: []string{"无依据限制"}, Risks: []string{"无依据风险"}}
	if err := mergeAnalysisFacts(final, []*Analysis{leaf}); err != nil {
		t.Fatal(err)
	}
	var persisted Analysis
	if err := json.Unmarshal(final.Raw, &persisted); err != nil {
		t.Fatal(err)
	}
	if len(persisted.Actions) != 2 || persisted.Actions[1].Owner != "测试乙" || len(persisted.Constraints) != 1 || len(persisted.Risks) != 1 || !strings.Contains(persisted.Risks[0], "4件") {
		t.Fatalf("leaf facts changed: %+v", persisted)
	}
	if strings.Contains(string(final.Raw), "无依据") {
		t.Fatal("merge invented extra facts")
	}
}
