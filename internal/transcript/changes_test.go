package transcript

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func appendLines(t *testing.T, p string, lines ...string) {
	t.Helper()
	f, err := os.OpenFile(p, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	for _, l := range lines {
		f.WriteString(l + "\n")
	}
}

func TestChangesScansWholeFileAndAppends(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	appendLines(t, p,
		`{"type":"user","timestamp":"2026-10-08T09:00:00Z","message":{"content":"add a flag"}}`,
		`{"type":"assistant","timestamp":"2026-10-08T09:00:01Z","message":{"content":[{"type":"tool_use","id":"t1","name":"Edit","input":{"file_path":"/r/a.go"}}]}}`,
		`{"type":"user","timestamp":"2026-10-08T09:00:02Z","message":{"content":[{"type":"tool_result","tool_use_id":"t1","content":"ok"}]},"toolUseResult":{"filePath":"/r/a.go","structuredPatch":[{"oldStart":3,"newStart":3,"lines":[" ctx","-a","+b"]}]}}`,
		// A result whose call we never saw (it was in another file) is skipped.
		`{"type":"user","timestamp":"2026-10-08T09:00:03Z","message":{"content":[{"type":"tool_result","tool_use_id":"zz","content":"ok"}]},"toolUseResult":{"filePath":"/r/x.go","structuredPatch":[{"oldStart":1,"newStart":1,"lines":["+x"]}]}}`,
	)

	got := Changes(p, time.Time{})
	if len(got) != 1 || got[0].Path != "/r/a.go" || got[0].Tool != "Edit" || got[0].At.Format(time.RFC3339) != "2026-10-08T09:00:02Z" {
		t.Fatalf("first read = %+v", got)
	}

	// The call arrives, then half a result line: only the finished part counts.
	appendLines(t, p, `{"type":"assistant","timestamp":"2026-10-08T10:00:00Z","message":{"content":[{"type":"tool_use","id":"t2","name":"Bash","input":{"command":"sed -i s/b/c/ a.go"}}]}}`)
	partial := `{"type":"user","timestamp":"2026-10-08T10:00:01Z","message":{"content":[{"type":"tool_result","tool_use_id":"t2","content":"ok"}]},"toolUseResult":{"stdout":"","bashEditDiff":{"files":[{"filePath":"/r/a.go","hunks":[{"oldStart":3,"newStart":3,"lines":["-b","+c"]}]}]}}}`
	f, _ := os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(partial[:40])
	f.Close()
	if got := Changes(p, time.Time{}); len(got) != 1 {
		t.Fatalf("partial line counted: %+v", got)
	}
	f, _ = os.OpenFile(p, os.O_APPEND|os.O_WRONLY, 0o600)
	f.WriteString(partial[40:] + "\n")
	f.Close()

	got = Changes(p, time.Time{})
	if len(got) != 2 || got[1].Tool != "Bash" || got[1].Hunks[0].Lines[1] != "+c" {
		t.Fatalf("after append = %+v", got)
	}
	since, _ := time.Parse(time.RFC3339, "2026-10-08T09:30:00Z")
	if got := Changes(p, since); len(got) != 1 || got[0].Tool != "Bash" {
		t.Fatalf("since filter = %+v", got)
	}

	// Rewritten shorter: read again from the start.
	os.WriteFile(p, nil, 0o600)
	if got := Changes(p, time.Time{}); len(got) != 0 {
		t.Fatalf("after truncate = %+v", got)
	}
}
