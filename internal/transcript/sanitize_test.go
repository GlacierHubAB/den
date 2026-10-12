package transcript

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// evil is JSON-escaped text that would write the clipboard, clear the
// screen, retitle the window and flip the reading order if printed raw.
const evil = `\u001b]52;c;SGVsbG8=\u0007\u001b[2J\u001b[H\u001b]0;pwned\u0007\u202e\r`

func injected(s string) bool {
	return strings.ContainsAny(s, "\x1b\x07\r\u202e") || strings.Contains(s, "]52;") || strings.Contains(s, "pwned")
}

func TestTranscriptTextIsSanitised(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	lines := []string{
		`{"type":"ai-title","aiTitle":"title` + evil + `"}`,
		`{"type":"user","message":{"content":"prompt` + evil + `"}}`,
		`{"type":"assistant","message":{"model":"claude-fable-5-1","content":[{"type":"text","text":"reply` + evil + `"}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t1","name":"Bash","input":{"command":"echo` + evil + `","description":"desc` + evil + `"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},"toolUseResult":{"stdout":"\u001b[31mout` + evil + `","bashEditDiff":{"files":[{"filePath":"/x/b` + evil + `.go","hunks":[{"oldStart":1,"newStart":1,"lines":["-x` + evil + `","+y"]}]}]}}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t3","name":"Gr` + evil + `ep","input":{"pattern":"x"}}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","id":"t2","name":"Write","input":{"file_path":"/x/n.txt"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"ok"}]},"toolUseResult":{"type":"create","filePath":"/x/n.txt","content":"one` + evil + `two\n"}}`,
	}
	os.WriteFile(p, []byte(strings.Join(lines, "\n")+"\n"), 0o600)

	if f := Read(p); injected(f.Title) || f.Title != "title" {
		t.Errorf("title = %q", f.Title)
	}
	tail := Tail(p, 10)
	if len(tail) != 5 {
		t.Fatalf("tail = %+v", tail)
	}
	var texts []string
	for _, e := range tail {
		texts = append(texts, e.Text, e.Tool, e.Code, e.Output)
		for _, c := range e.Changes {
			texts = append(texts, c.Path)
			for _, h := range c.Hunks {
				texts = append(texts, h.Lines...)
			}
		}
	}
	changes := Changes(p, time.Time{})
	if len(changes) != 2 {
		t.Fatalf("changes = %+v", changes)
	}
	for _, c := range changes {
		texts = append(texts, c.Tool, c.Path)
		for _, h := range c.Hunks {
			for _, l := range h.Lines {
				if strings.Contains(l, "\n") {
					t.Errorf("hunk line %q spans lines", l)
				}
			}
			texts = append(texts, h.Lines...)
		}
	}
	for _, s := range texts {
		if injected(s) {
			t.Errorf("injected sequence survived in %q", s)
		}
	}
	if tail[0].Text != "prompt" || tail[2].Code != "echo" || tail[2].Output != "out" || tail[3].Tool != "Gr ep" || len(tail[2].Changes) != 1 {
		t.Errorf("tail = %+v", tail)
	}
}
