package transcript

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// msg is one assistant line the way Claude Code writes it: a message with
// several content blocks is several lines with the same message id.
func msg(id, model, at string, in, out, cc, cr int) string {
	return fmt.Sprintf(`{"type":"assistant","timestamp":%q,"requestId":"req_%s","message":{"id":%q,"model":%q,"content":[{"type":"text","text":"x"}],"usage":{"input_tokens":%d,"output_tokens":%d,"cache_creation_input_tokens":%d,"cache_read_input_tokens":%d}}}`, at, id, id, model, in, out, cc, cr)
}

func write(t *testing.T, path string, lines ...string) {
	t.Helper()
	os.MkdirAll(filepath.Dir(path), 0o755)
	if err := os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
}

func appendTo(t *testing.T, path, s string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_APPEND|os.O_WRONLY, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	f.WriteString(s)
}

func TestLedgerCountsEachMessageOnce(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	write(t, p,
		msg("m1", "claude-opus-5-5", "2026-10-11T10:00:00Z", 2, 100, 5000, 0),
		msg("m1", "claude-opus-5-5", "2026-10-11T10:00:00Z", 2, 100, 5000, 0), // second block of the same message
		msg("m1", "claude-opus-5-5", "2026-10-11T10:00:00Z", 2, 100, 5000, 0),
		msg("m2", "claude-opus-5-5", "2026-10-11T10:00:05Z", 3, 40, 100, 5000),
		`{"type":"user","message":{"role":"user","content":"hello"}}`,
		msg("m3", "<synthetic>", "2026-10-11T10:00:09Z", 0, 0, 0, 0),
	)
	var l Ledger
	got := l.Account(p).Main["claude-opus-5-5"]
	want := Tokens{Input: 5, Output: 140, CacheCreation: 5100, CacheRead: 5000}
	if got != want {
		t.Errorf("tokens = %+v, want %+v", got, want)
	}
	if _, ok := l.Account(p).Main["<synthetic>"]; ok {
		t.Error("synthetic messages are not usage")
	}
}

func TestLedgerStreamedChunkKeepsTheLargestFigures(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	write(t, p,
		msg("m1", "claude-sonnet-5-5", "2026-10-11T10:00:00Z", 10, 3, 200, 0),
		msg("m1", "claude-sonnet-5-5", "2026-10-11T10:00:00Z", 10, 90, 200, 0),
	)
	var l Ledger
	if got := l.Account(p).Main["claude-sonnet-5-5"]; got.Output != 90 || got.Input != 10 || got.CacheCreation != 200 {
		t.Errorf("tokens = %+v", got)
	}
}

func TestLedgerModelSwitchMidSession(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	write(t, p,
		msg("a", "claude-opus-5-5", "2026-10-11T10:00:00Z", 1, 10, 0, 0),
		msg("b", "claude-sonnet-5-5", "2026-10-11T10:01:00Z", 2, 20, 0, 0),
		msg("c", "claude-opus-5-5", "2026-10-11T10:02:00Z", 4, 40, 0, 0),
	)
	var l Ledger
	a := l.Account(p)
	if a.Main["claude-opus-5-5"].Output != 50 || a.Main["claude-sonnet-5-5"].Output != 20 || len(a.Main) != 2 {
		t.Errorf("by model = %+v", a.Main)
	}
}

func TestLedgerReadsOnlyWhatWasAppended(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	write(t, p, msg("a", "claude-opus-5-5", "2026-10-11T10:00:00Z", 1, 10, 0, 0))
	var l Ledger
	l.Account(p)
	off := l.files[p].offset
	if off == 0 {
		t.Fatal("offset not remembered")
	}
	// A half-written line is left for the next refresh.
	line := msg("b", "claude-opus-5-5", "2026-10-11T10:01:00Z", 2, 20, 0, 0)
	appendTo(t, p, line[:30])
	if got := l.Account(p).Main["claude-opus-5-5"].Output; got != 10 {
		t.Errorf("partial line counted: output = %d", got)
	}
	if l.files[p].offset != off {
		t.Errorf("offset moved past an incomplete line")
	}
	appendTo(t, p, line[30:]+"\n")
	if got := l.Account(p).Main["claude-opus-5-5"].Output; got != 30 {
		t.Errorf("output after append = %d, want 30", got)
	}
	// The file was replaced by a shorter one: start over.
	write(t, p, msg("z", "claude-opus-5-5", "2026-10-11T10:05:00Z", 0, 7, 0, 0))
	if got := l.Account(p).Main["claude-opus-5-5"].Output; got != 7 {
		t.Errorf("output after rewrite = %d, want 7", got)
	}
}

func TestLedgerSubagentsAreSeparate(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "sess.jsonl")
	write(t, p, msg("a", "claude-opus-5-5", "2026-10-11T10:00:00Z", 1, 10, 0, 0))
	write(t, filepath.Join(dir, "sess", "subagents", "agent-1.jsonl"), msg("s1", "claude-haiku-4-5-20251001", "2026-10-11T10:00:30Z", 5, 50, 0, 0))
	write(t, filepath.Join(dir, "sess", "subagents", "agent-2.jsonl"), msg("s2", "claude-haiku-4-5-20251001", "2026-10-11T10:00:40Z", 1, 5, 0, 0))
	var l Ledger
	a := l.Account(p)
	if a.Main["claude-opus-5-5"].Output != 10 || len(a.Main) != 1 {
		t.Errorf("main = %+v", a.Main)
	}
	if got := a.Sub["claude-haiku-4-5-20251001"]; got.Input != 6 || got.Output != 55 {
		t.Errorf("sub = %+v", a.Sub)
	}
}

func TestLedgerWindow(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	write(t, p,
		msg("a", "claude-opus-5-5", "2026-10-11T10:00:00Z", 0, 1, 0, 0),
		msg("b", "claude-opus-5-5", "2026-10-11T11:00:00Z", 0, 10, 0, 0),
		msg("c", "claude-opus-5-5", "2026-10-11T12:00:00Z", 0, 100, 0, 0),
	)
	var l Ledger
	from, _ := time.Parse(time.RFC3339, "2026-10-11T10:30:00Z")
	to, _ := time.Parse(time.RFC3339, "2026-10-11T11:30:00Z")
	if got := l.Window(p, from, to)["claude-opus-5-5"].Output; got != 10 {
		t.Errorf("window output = %d, want 10", got)
	}
	if got := l.Window(p, from, time.Time{})["claude-opus-5-5"].Output; got != 110 {
		t.Errorf("open-ended window output = %d, want 110", got)
	}
}

func TestLedgerReportedCost(t *testing.T) {
	p := filepath.Join(t.TempDir(), "s.jsonl")
	write(t, p,
		msg("a", "claude-opus-5-5", "2026-10-11T10:00:00Z", 0, 1, 0, 0),
		`{"type":"cost-state","totalCostUSD":1.5,"hasUnknownModelCost":false,"modelUsage":{}}`,
		`{"type":"cost-state","totalCostUSD":2.25,"hasUnknownModelCost":true,"modelUsage":{}}`,
	)
	var l Ledger
	r := l.Account(p).Reported
	if r == nil || r.USD != 2.25 || !r.Unknown {
		t.Errorf("reported = %+v", r)
	}
}

func TestLedgerMissingFile(t *testing.T) {
	var l Ledger
	if a := l.Account(filepath.Join(t.TempDir(), "none.jsonl")); len(a.Main) != 0 {
		t.Errorf("account = %+v", a)
	}
	if a := l.Account(""); len(a.Main) != 0 {
		t.Errorf("account = %+v", a)
	}
}
