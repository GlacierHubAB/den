package transcript

import (
	"os"
	"path/filepath"
	"testing"
)

func TestUsageAndTail(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{
		`{"type":"user","message":{"role":"user","content":"<local-command-caveat>ignored</local-command-caveat>"}}`,
		`{"type":"user","message":{"role":"user","content":"fix the login redirect"}}`,
		`{"type":"assistant","message":{"model":"claude-fable-5-1","content":[{"type":"text","text":"Looking at it.\nMore."},{"type":"type_use"}],"usage":{"input_tokens":10,"cache_read_input_tokens":100,"cache_creation_input_tokens":5}}}`,
		`{"type":"assistant","message":{"model":"claude-fable-5-1","content":[{"type":"tool_use","name":"Bash","input":{"command":"go test ./...","description":"Run tests"}}],"usage":{"input_tokens":20,"cache_read_input_tokens":250000,"cache_creation_input_tokens":0}}}`,
		`{"type":"user","message":{"role":"user","content":[{"type":"tool_result","tool_use_id":"x","content":"ok"}]}}`,
	}
	var b []byte
	for _, l := range lines {
		b = append(b, l...)
		b = append(b, '\n')
	}
	os.WriteFile(p, b, 0o600)

	u, ok := ReadUsage(p)
	if !ok || u.Tokens != 250020 || u.Window != 1_000_000 || u.Percent() != 25 {
		t.Errorf("usage = %+v %v", u, ok)
	}
	tail := Tail(p, 10)
	want := []Entry{{Kind: "you", Text: "fix the login redirect"}, {Kind: "text", Text: "Looking at it.\nMore."}, {Kind: "tool", Tool: "Bash", Text: "Run tests", Code: "go test ./..."}}
	if len(tail) != len(want) {
		t.Fatalf("tail = %+v", tail)
	}
	for i := range want {
		if tail[i] != want[i] {
			t.Errorf("tail[%d] = %+v, want %+v", i, tail[i], want[i])
		}
	}
	if got := Tail(p, 2); len(got) != 2 || got[0].Kind != "text" {
		t.Errorf("tail(2) = %+v", got)
	}
}

func TestExcerptKeepsLinesAndMarksCut(t *testing.T) {
	in := "one\n\n  two  \nthree\nfour"
	if got := excerpt(in, 3); got != "one\n  two\nthree\n…" {
		t.Errorf("excerpt = %q", got)
	}
	if got := excerpt("   ", 3); got != "" {
		t.Errorf("blank excerpt = %q", got)
	}
}
