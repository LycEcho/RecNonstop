package infer

import (
	"context"
	"strings"
	"testing"
)

func TestDailyPersonalAttributionRequiresExplicitSource(t *testing.T) {
	const unknown = "录音可能含背景播放或引用，第一人称身份未确认。声音说：我把旧时光留在风里。"
	const mixed = "### 录音 1\n原始转写：用户本人说：明天核对清单。\n### 录音 2\n原始转写：来源未确认，背景播放一段关于离别的抒情引用。"
	for _, tc := range []struct {
		name, source, output string
		want                 bool
	}{
		{"unknown lyrical attribution", unknown, "录音记录了用户表达的一段关于旧时光与离别的抒情内容。", true},
		{"unknown recorder attribution", unknown, "录音者表示自己不再回头。", true},
		{"first person alone does not identify user", "我今天很开心。", "用户表示今天很开心。", true},
		{"explicit user statement", "用户本人说：我今天很开心。", "用户表示今天很开心。", false},
		{"explicit recorder statement", "录音者本人明确自述：今天检查已经完成。", "录音者自述检查已经完成。", false},
		{"confirmed personal identity", "已确认说话人是用户本人。他说检查已经完成。", "用户说检查已经完成。", false},
		{"arbitrary user mention is not evidence", unknown + "产品介绍提到用户操作按钮。", "用户表达关于离别的想法。", true},
		{"quote does not confirm identity", "剧情台词写着“用户本人说：今天很开心”。", "用户表示今天很开心。", true},
		{"negated source identity", "无法确认用户本人说过这些话。", "用户表达了这些想法。", true},
		{"source identity unconfirmed after claim", "用户本人说过这些话的说法尚未证实。", "用户表达了这些想法。", true},
		{"negated output", unknown, "不能认定用户表达了这些想法。", false},
		{"no evidence of attribution", unknown, "没有证据说明用户经历了这些事情。", false},
		{"explicit denial", unknown, "这并非用户自述。", false},
		{"reported quoted attribution", unknown, "错误示例是“用户表达了离别感受”。", false},
		{"rejected attribution", unknown, "用户表达的说法不成立。", false},
		{"neutral recorded expression", unknown, "录音含抒情表达，来源未确认。", false},
		{"product user group", "产品介绍讨论不同群体的评价。", "部分用户表示界面简单，产品用户表达了不同使用体验。", false},
		{"plural users", "调查描述用户群体。", "用户群体表达了不同意见，用户们表示认可。", false},
		{"survey context", "问卷仅讨论产品反馈。", "调查中用户表示界面方便。", false},
		{"original ordinary product user statement", "产品体验调查记录：用户表示界面比较清晰。", "用户表示界面比较清晰。", false},
		{"product user claim does not authorize another claim", "产品体验调查记录：用户表示界面比较清晰。\n背景播放来源未确认的抒情内容。", "用户表达了关于旧时光的抒情内容。", true},
		{"same scoped explicit user", mixed, "录音1中用户表示明天核对清单。", false},
		{"other recording cannot borrow identity", mixed, "录音2中用户表达了离别感受。", true},
		{"unscoped cannot borrow mixed identity", mixed, "用户表达了离别感受。", true},
		{"scope preserved through heading", mixed, "### 录音2\n用户表达了离别感受。", true},
		{"same spk across recordings not identity", "[01-02 09:00:00 · 录音1/spk_0] 用户本人说：我检查完了。\n[01-02 10:00:00 · 录音2/spk_0] 来源未确认的剧情引用。", "录音2/spk_0中用户表示自己要远行。", true},
		{"spoken recording number cannot redirect identity", "### 录音 1\n用户本人说：我正在整理录音2的文件。\n### 录音 2\n背景播放一段抒情引用。", "录音2中用户表达了离别感受。", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := unsupportedPersonalAttribution(tc.source, tc.output); got != tc.want {
				t.Fatalf("unsupported attribution=%v, want %v", got, tc.want)
			}
		})
	}
}

func TestDailyPersonalAttributionRepairKeepsOriginalSourceAndBudget(t *testing.T) {
	const source = "### 录音 1\n原始转写：背景播放来源未确认的抒情引用，风替我收藏旧日时光。"
	const wrong = "## 今日概览\n录音记录用户表达的关于旧时光的抒情内容。"
	const corrected = "## 今日概览\n录音含关于旧时光的抒情引用，来源未确认。"
	for _, repeats := range []bool{false, true} {
		t.Run(map[bool]string{false: "one repair", true: "no third attempt"}[repeats], func(t *testing.T) {
			calls := 0
			c := mockChatClient(t, func(req chatRequest) (string, string) {
				calls++
				if calls == 1 {
					return wrong, "stop"
				}
				if !strings.Contains(req.Messages[1].Content, dailyUserAttributionIssue) || !strings.Contains(req.Messages[1].Content, source) || req.MaxTokens != 2048 {
					t.Fatal("repair lost source, issue or output budget")
				}
				if repeats {
					return wrong, "stop"
				}
				return corrected, "stop"
			})
			// Instructions mentioning an explicit user must not authorize identity.
			user := dailyReportPrompt(source) + "\n禁止示例：用户本人说自己很开心。"
			out, err := c.dailyChat(context.Background(), user, source, 2048)
			if calls != 2 || (err != nil) != repeats || repeats && out != "" || !repeats && out != corrected {
				t.Fatalf("attribution repair failed: calls=%d out=%q err=%v", calls, out, err)
			}
		})
	}
}
