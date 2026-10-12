package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReadFindsLatestTitleAndModel(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "s.jsonl")
	var b strings.Builder
	b.WriteString(`{"type":"ai-title","aiTitle":"Old title","sessionId":"s"}` + "\n")
	b.WriteString(`{"type":"assistant","message":{"model":"claude-opus-5-5","role":"assistant"}}` + "\n")
	// padding so the file spans more than one chunk
	for i := 0; i < 3000; i++ {
		b.WriteString(`{"type":"user","message":{"role":"user","content":"` + strings.Repeat("x", 40) + `"}}` + "\n")
	}
	b.WriteString(`{"type":"ai-title","aiTitle":"New title","sessionId":"s"}` + "\n")
	b.WriteString(`{"type":"assistant","message":{"model":"claude-fable-5-1","role":"assistant"}}` + "\n")
	if err := os.WriteFile(p, []byte(b.String()), 0o600); err != nil {
		t.Fatal(err)
	}
	f := Read(p)
	if f.Title != "New title" {
		t.Errorf("title = %q", f.Title)
	}
	if f.Model != "claude-fable-5-1" {
		t.Errorf("model = %q", f.Model)
	}
	if got := PrettyModel(f.Model); got != "Fable 5.1" {
		t.Errorf("pretty = %q", got)
	}
	if got := PrettyModel("claude-haiku-4-5-20251001"); got != "Haiku 4.5" {
		t.Errorf("pretty haiku = %q", got)
	}
}

// The model in use is the newest model attachment or real reply, in the
// shapes Claude Code 2.1.289 writes: it switches models in and out of plan
// mode with an attachment before the next reply, and makes up an assistant
// record ("<synthetic>", isApiErrorMessage) for an API error or when
// logged out, which names no model.
func TestReadModelInUse(t *testing.T) {
	const (
		haikuStart = `{"type":"attachment","attachment":{"type":"model","identity":{"modelId":"claude-haiku-4-5-20251001","marketingName":"Haiku 4.5"},"text":"x"}}`
		planModel  = `{"type":"attachment","attachment":{"type":"model","identity":{"modelId":"claude-sonnet-5-5"}}}`
		planReply  = `{"parentUuid":"p","isSidechain":false,"type":"assistant","message":{"model":"claude-sonnet-5-5","id":"msg_1","role":"assistant","content":[{"type":"text","text":"plan"}]},"requestId":"req_1"}`
		exitModel  = `{"type":"attachment","attachment":{"type":"model","identity":{"modelId":"claude-haiku-4-5-20251001"}}}`
		haikuReply = `{"type":"assistant","message":{"model":"claude-haiku-4-5-20251001","id":"msg_2","role":"assistant","content":[]}}`
		synthetic  = `{"type":"assistant","error":"authentication_failed","isApiErrorMessage":true,"message":{"model":"<synthetic>","id":"942f1410-8e27-43d1-ba40-b568d31284f6","role":"assistant","content":[{"type":"text","text":"Not logged in"}]}}`
		toolText   = `{"type":"user","message":{"role":"user","content":[{"type":"tool_result","content":"{\"type\":\"attachment\",\"attachment\":{\"type\":\"model\",\"identity\":{\"modelId\":\"claude-opus-5-5\"}}}"}]}}`
	)
	for name, c := range map[string]struct {
		lines []string
		want  string
	}{
		"started, before any reply": {[]string{haikuStart}, "claude-haiku-4-5-20251001"},
		"a plan-mode reply":         {[]string{haikuStart, planModel, planReply}, "claude-sonnet-5-5"},
		"plan approved, no reply":   {[]string{haikuStart, planModel, planReply, exitModel}, "claude-haiku-4-5-20251001"},
		"after a haiku reply":       {[]string{haikuStart, planModel, planReply, exitModel, haikuReply}, "claude-haiku-4-5-20251001"},
		"an API error after":        {[]string{haikuStart, haikuReply, synthetic}, "claude-haiku-4-5-20251001"},
		"logged out":                {[]string{synthetic}, ""},
		"an attachment as text":     {[]string{haikuReply, toolText}, "claude-haiku-4-5-20251001"},
	} {
		p := filepath.Join(t.TempDir(), "s.jsonl")
		os.WriteFile(p, []byte(strings.Join(c.lines, "\n")+"\n"), 0o600)
		if got := Read(p).Model; got != c.want {
			t.Errorf("%s: model %q, want %q", name, got, c.want)
		}
	}
}
