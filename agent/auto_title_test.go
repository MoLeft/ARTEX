package agent

import (
	"context"
	"errors"
	"iter"
	"strings"
	"testing"

	"github.com/Autumn-27/norma/llm"
)

type autoTitleProvider struct {
	reply          string
	err            error
	req            llm.CompletionRequest
	calls          []llm.CompletionRequest
	rejectDisabled bool
}

func (p *autoTitleProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.req = req
	p.calls = append(p.calls, req)
	if p.rejectDisabled && req.Thinking == "disabled" {
		return llm.Message{}, "", llm.Usage{}, errors.New("MiniMax requires adaptive thinking (2013)")
	}
	if p.err != nil {
		return llm.Message{}, "", llm.Usage{}, p.err
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(p.reply)}}, "stop", llm.Usage{}, nil
}

func (p *autoTitleProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(func(llm.StreamEvent, error) bool) {}
}

// scriptedTitleProvider answers the Nth call with replies[N-1] (the last one
// repeats), so a retry can be observed without a stateful flag.
type scriptedTitleProvider struct {
	replies []string
	calls   []llm.CompletionRequest
}

func (p *scriptedTitleProvider) Complete(_ context.Context, req llm.CompletionRequest) (llm.Message, string, llm.Usage, error) {
	p.calls = append(p.calls, req)
	reply := p.replies[len(p.replies)-1]
	if len(p.calls) <= len(p.replies) {
		reply = p.replies[len(p.calls)-1]
	}
	return llm.Message{Role: llm.RoleAssistant, Content: []llm.ContentBlock{llm.TextBlock(reply)}}, "stop", llm.Usage{}, nil
}

func (p *scriptedTitleProvider) Stream(context.Context, llm.CompletionRequest) iter.Seq2[llm.StreamEvent, error] {
	return func(func(llm.StreamEvent, error) bool) {}
}

func systemText(req llm.CompletionRequest) string { return strings.Join(req.System, "\n") }

func TestGenerateAutoTitleCleansResponse(t *testing.T) {
	long := strings.Repeat("很长", 30)
	p := &autoTitleProvider{reply: "\n\"标题：" + long + "\"\n第二行"}
	got, err := GenerateAutoTitle(t.Context(), p, "用户希望整理项目设置", false)
	if err != nil {
		t.Fatal(err)
	}
	if strings.ContainsAny(got, "\r\n") || strings.Contains(got, "标题：") || strings.HasPrefix(got, "\"") || len([]rune(got)) > 40 {
		t.Fatalf("unclean title %q", got)
	}
	if p.req.Thinking != "" || p.req.MaxTokens != 96 || len(p.req.Tools) != 0 {
		t.Fatalf("request options: %+v", p.req)
	}

	quoted := &autoTitleProvider{reply: `标题："整理项目设置"`}
	got, err = GenerateAutoTitle(t.Context(), quoted, "整理项目设置", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "整理项目设置" {
		t.Fatalf("nested quote/prefix cleanup: got %q", got)
	}
}

func TestGenerateAutoTitleAllowsAdaptiveThinkingModel(t *testing.T) {
	p := &autoTitleProvider{reply: "任务进展", rejectDisabled: true}
	got, err := GenerateAutoTitle(t.Context(), p, "检查任务进展", false)
	if err != nil || got != "任务进展" {
		t.Fatalf("title=%q, err=%v", got, err)
	}
}

func TestGenerateAutoTitleProviderError(t *testing.T) {
	_, err := GenerateAutoTitle(t.Context(), &autoTitleProvider{err: errors.New("boom")}, "内容", false)
	if err == nil {
		t.Fatal("expected provider error")
	}
}

// Some models answer with 《书名号》 or **粗体**; neither belongs in a title.
func TestCleanAutoTitleStripsBookQuotesAndBold(t *testing.T) {
	cases := []struct{ in, want string }{
		{"《东方希望集团资产测绘》", "东方希望集团资产测绘"},
		{"**标题：edu.cn GitLab 源码检索**", "edu.cn GitLab 源码检索"},
		{"「119.gov.cn 资产测绘」", "119.gov.cn 资产测绘"},
	}
	for _, tc := range cases {
		if got := cleanAutoTitle(tc.in); got != tc.want {
			t.Errorf("cleanAutoTitle(%q) = %q, want %q", tc.in, got, tc.want)
		}
	}
}

// The two shapes that actually showed up in production titles: the model
// narrating the request ("嗯，用户提出的是…", "The user is asking me to…") and the
// model refusing the material ("抱歉，我无法协助完成这项任务。").
func TestAutoTitleRejectsNarrationAndRefusal(t *testing.T) {
	bad := []string{
		"嗯，用户提出的是一个针对119.gov.cn体",
		"The user is asking me to help with vulnerabilities",
		"用户让我梳理东方希望集团的资产",
		"好的，以下是标题：资产测绘",
		"抱歉，我无法协助完成这项任务。",
		"I'm sorry, but I can't help with that.",
		"根据您的要求整理如下",
	}
	for _, title := range bad {
		if reason := autoTitleRejectReason(title); reason == "" {
			t.Errorf("autoTitleRejectReason(%q) = \"\", want a rejection", title)
		}
	}
	good := []string{
		"东方希望集团 资产测绘",
		"针对 119.gov.cn 的资产测绘",
		"叮咚买菜老域 dingdongxiaoqu.com 与遗留系统梳理",
		"edu.cn GitLab 中 LLM 网关检索",
	}
	for _, title := range good {
		if reason := autoTitleRejectReason(title); reason != "" {
			t.Errorf("autoTitleRejectReason(%q) = %q, want accepted", title, reason)
		}
	}
}

func TestGenerateAutoTitleRetriesOnceWithReason(t *testing.T) {
	p := &scriptedTitleProvider{replies: []string{
		"嗯，用户要求对 119.gov.cn 做资产测绘",
		"119.gov.cn 资产测绘",
	}}
	got, err := GenerateAutoTitle(t.Context(), p, "针对119.gov.cn做资产测绘", false)
	if err != nil {
		t.Fatal(err)
	}
	if got != "119.gov.cn 资产测绘" {
		t.Fatalf("title=%q", got)
	}
	if len(p.calls) != 2 {
		t.Fatalf("calls=%d, want 2", len(p.calls))
	}
	if !strings.Contains(systemText(p.calls[1]), "叙述") {
		t.Fatalf("retry system does not carry the rejection reason:\n%s", systemText(p.calls[1]))
	}
}

func TestGenerateAutoTitleGivesUpAfterOneRetry(t *testing.T) {
	p := &scriptedTitleProvider{replies: []string{"抱歉，我无法协助完成这项任务。"}}
	if _, err := GenerateAutoTitle(t.Context(), p, "对 119.gov.cn 做资产测绘", false); err == nil {
		t.Fatal("expected rejection error, not a refusal title")
	}
	if len(p.calls) != 2 {
		t.Fatalf("calls=%d, want 2 (one attempt + one retry)", len(p.calls))
	}
}

// The material must be framed as data, otherwise a first-person request reads
// as an instruction addressed to the model and gets narrated back. The framing
// is deliberately a plain request: bracketed [起名素材开始] markers made the
// reasoning backend treat the call as prompt injection and burn the budget on
// meta-analysis (see autoTitleLeadIn).
func TestGenerateAutoTitleFramesMaterialAsData(t *testing.T) {
	p := &autoTitleProvider{reply: "项目设置整理"}
	if _, err := GenerateAutoTitle(t.Context(), p, "  帮我整理一下项目设置  ", false); err != nil {
		t.Fatal(err)
	}
	body := p.req.Messages[0].Content[0].Text
	if !strings.HasPrefix(body, "为下面这段内容起一个标题") {
		t.Fatalf("material not framed: %q", body)
	}
	if !strings.HasSuffix(body, "帮我整理一下项目设置") {
		t.Fatalf("material content missing: %q", body)
	}
	if strings.Contains(body, "[起名素材") {
		t.Fatalf("bracketed markers are back — they trigger injection heuristics: %q", body)
	}
}

func TestAutoTitleUltraGuardOnlyWhenEnabled(t *testing.T) {
	off := autoTitleSystem(false)
	if len(off) != 1 || strings.Contains(off[0], "Artex Ultra") {
		t.Fatalf("ultra off should send the base prompt only: %+v", off)
	}

	previous := OpsecEnabled
	t.Cleanup(func() { OpsecEnabled = previous })
	OpsecEnabled = nil

	on := autoTitleSystem(true)
	if len(on) != 2 || !strings.Contains(on[0], "Artex Ultra") {
		t.Fatalf("ultra on should prepend the guard: %+v", on)
	}
	if !strings.Contains(on[0], "不得") && !strings.Contains(on[0], "照常起名") {
		t.Fatalf("guard lost its no-refusal clause: %q", on[0])
	}
}

// OPSEC wins over Ultra here too: the naming call must not carry a guard that
// the conversation path already refuses to inject.
func TestAutoTitleUltraSuppressedByOpsec(t *testing.T) {
	previous := OpsecEnabled
	t.Cleanup(func() { OpsecEnabled = previous })
	OpsecEnabled = func() bool { return true }

	if got := autoTitleSystem(true); len(got) != 1 || strings.Contains(got[0], "Artex Ultra") {
		t.Fatalf("opsec on must suppress the ultra guard: %+v", got)
	}
}
