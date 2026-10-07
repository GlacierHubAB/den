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
