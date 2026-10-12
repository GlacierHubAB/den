package transcript

import (
	"encoding/json"
	"strings"
	"testing"
)

// A path with control characters is cleaned for printing, and kept as it
// was only to match files by; it is never written out.
func TestChangeKeepsTheRawPathOnlyToMatch(t *testing.T) {
	raw := "/repo/evil\x1b[31mname\u202e.txt"
	cs := toolResult{Type: "create", FilePath: raw, Content: "x\n"}.changes("Write")
	if len(cs) != 1 {
		t.Fatalf("changes = %+v", cs)
	}
	c := cs[0]
	if c.Path != "/repo/evilname.txt" || c.Raw != raw || c.Real() != raw {
		t.Errorf("path %q raw %q real %q", c.Path, c.Raw, c.Real())
	}
	if b, _ := json.Marshal(c); strings.Contains(string(b), `\u001b`) || strings.Contains(string(b), "Raw") {
		t.Errorf("the raw path is written out: %s", b)
	}
	plain := toolResult{Type: "create", FilePath: "/repo/a.txt", Content: "x\n"}.changes("Write")[0]
	if plain.Raw != "" || plain.Real() != "/repo/a.txt" {
		t.Errorf("a plain path: raw %q real %q", plain.Raw, plain.Real())
	}
}
