package fleet

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// A file whose name has control characters crosses under full by its
// cleaned name and marked, with no content, as Review sends it; Den drops
// any content a host sends for it.
func TestFullMetaSendsAFileWithAnUnsafeNameWithoutItsContent(t *testing.T) {
	raw := "/home/user/proj/evil\x1b[31mname\u202e.txt"
	now := time.Now()
	a := Agent{Tail: []transcript.Entry{{At: now, Kind: "tool", Tool: "Write", Code: sanitize.Code(raw), Changes: []transcript.FileChange{
		{Path: sanitize.Code(raw), Raw: raw, Created: true, Hunks: []transcript.Hunk{{NewStart: 1, Lines: []string{"+SECRET-unsafe-content"}}}},
		{Path: "/home/user/proj/plain.txt", Created: true, Hunks: []transcript.Hunk{{NewStart: 1, Lines: []string{"+plain content"}}}},
	}}}}
	f := a.FullMeta(now, 1<<20)
	b, _ := json.Marshal(f)
	if strings.Contains(string(b), "SECRET") || strings.Contains(string(b), `\u001b`) || strings.Contains(string(b), "\u202e") {
		t.Errorf("content or escapes crossed: %s", b)
	}
	if len(f.Tail) != 1 || len(f.Tail[0].Changes) != 2 {
		t.Fatalf("tail = %s", b)
	}
	if c := f.Tail[0].Changes[0]; !c.Unsafe || len(c.Hunks) > 0 || c.Path != "/home/user/proj/evilname.txt" || !c.Created {
		t.Errorf("unsafe change = %+v", c)
	}
	if c := f.Tail[0].Changes[1]; c.Unsafe || len(c.Hunks) != 1 {
		t.Errorf("plain change = %+v", c)
	}

	// A host that sends content for such a file anyway: none is kept.
	lying := *f
	lying.Tail = []MetaEntry{{Age: 0, Kind: "tool", Tool: "Write", Changes: []MetaChange{{Path: "evilname.txt", Unsafe: true, Hunks: []MetaHunk{{NewStart: 1, Lines: []string{"+SECRET-sent-anyway"}}}}}}}
	got := FromMeta(Meta{Session: "s1", Status: "idle", Full: &lying}, "devbox", now, cost.NewTable(nil))
	if len(got.Tail) != 1 || len(got.Tail[0].Changes) != 1 {
		t.Fatalf("tail = %+v", got.Tail)
	}
	if c := got.Tail[0].Changes[0]; !c.Unsafe || !c.Hidden() || len(c.Hunks) > 0 {
		t.Errorf("kept = %+v", c)
	}
}
