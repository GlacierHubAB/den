package transcript

import (
	"os"
	"path/filepath"
	"strings"
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
		if tail[i].Kind != want[i].Kind || tail[i].Text != want[i].Text || tail[i].Tool != want[i].Tool || tail[i].Code != want[i].Code {
			t.Errorf("tail[%d] = %+v, want %+v", i, tail[i], want[i])
		}
	}
	if got := Tail(p, 2); len(got) != 2 || got[0].Kind != "text" {
		t.Errorf("tail(2) = %+v", got)
	}
}

func TestExcerptKeepsLinesAndMarksCut(t *testing.T) {
	in := "one\n\n\n  two  \nthree\nfour"
	if got := excerpt(in, 3); got != "one\n\n  two\nthree\n…" {
		t.Errorf("excerpt = %q", got)
	}
	if got := excerpt("   ", 3); got != "" {
		t.Errorf("blank excerpt = %q", got)
	}
}

func TestTailAttachesResults(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/x/a.go","old_string":"a","new_string":"b"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},"toolUseResult":{"filePath":"/x/a.go","structuredPatch":[{"oldStart":3,"newStart":3,"lines":[" ctx","-a","+b","+c"]}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"go test ./...","description":"Run tests"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"ok"}]},"toolUseResult":{"stdout":"ok  pkg\n\nFAIL other","stderr":"","interrupted":false}}`,
	}
	os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
	tail := Tail(p, 10)
	if len(tail) != 2 {
		t.Fatalf("tail = %+v", tail)
	}
	edit, bash := tail[0], tail[1]
	if edit.Tool != "Edit" || edit.File != "/x/a.go" || len(edit.Patch) != 1 || edit.Patch[0].NewStart != 3 || len(edit.Patch[0].Lines) != 4 {
		t.Errorf("edit = %+v", edit)
	}
	if a, r := edit.Added(); a != 2 || r != 1 {
		t.Errorf("added/removed = %d/%d", a, r)
	}
	if bash.Output != "ok  pkg\n\nFAIL other" {
		t.Errorf("output = %q", bash.Output)
	}
}
