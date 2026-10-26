package api

import (
	"testing"

	"github.com/GlacierHubAB/den/internal/api/convcases"
)

// Every v2 row hashes to its hash, or is refused, by CardHashV2; no two
// rows that hash share a hash.
func TestCardHashV2Table(t *testing.T) {
	seen := map[string]string{}
	for _, c := range convcases.CardHashV2Cases {
		var crit []Criterion
		for _, k := range c.Criteria {
			crit = append(crit, Criterion{ID: k.ID, State: k.State, Text: k.Text, Evidence: k.Evidence})
		}
		got, err := CardHashV2(c.Kind, c.Agent, c.Tool, c.Title, c.Note, c.Class, c.Step, c.Mission, crit, c.Body)
		switch {
		case c.Refused && err == nil:
			t.Errorf("%s: hashed to %s, the table says it is refused", c.Name, got)
		case !c.Refused && (err != nil || got != c.TextSHA256):
			t.Errorf("%s: %q (err %v), the table says %s", c.Name, got, err, c.TextSHA256)
		case !c.Refused:
			if other, dup := seen[got]; dup {
				t.Errorf("%s and %s share a hash", c.Name, other)
			}
			seen[got] = c.Name
		}
	}
}

// Den sends the v2 table's fields and hash for the raw cards it makes
// today: projectCard cleans them, keeps Den's class and step, and hashes
// exactly what it sends; a class or step that does not fit becomes den_only.
func TestCardHashV2TableFromDen(t *testing.T) {
	raw := map[string]Card{
		"a command, code 1": {Kind: "permission", Agent: "Mossy Lemur", Tool: "Bash", Title: "Run the tests?", Class: "code", Step: 1, Body: "go test ./...\nin /home/x/den"},
		"a read inside the working directory, routine 3":      {Kind: "permission", Agent: "Mossy\u202e Lemur", Tool: "Read", Title: "Read README.md?", Class: "routine", Step: 3, Body: "/home/x/den/README.md"},
		"a write to the project's Claude settings, den_only":  {Kind: "permission", Agent: "Mossy Lemur", Tool: "Write", Title: "Write settings.json?", Class: "code", Step: 7, Body: "/home/x/den/.claude/settings.json"},
		"a cleanup that deletes ignored files, destructive 2": {Kind: "cleanup", Agent: "Mossy Lemur", Tool: "worktree_cleanup", Title: "Remove the worktree of feature?", Class: "destructive", Step: 2, Body: "Ignored files it deletes:\nnode_modules"},
	}
	found := 0
	for _, c := range convcases.CardHashV2Cases {
		in, ok := raw[c.Name]
		if !ok {
			continue
		}
		found++
		got := projectCard(in)
		if got.Kind != c.Kind || got.Agent != c.Agent || got.Tool != c.Tool || got.Title != c.Title || got.Note != c.Note || got.Class != c.Class || got.Step != c.Step || got.Body != c.Body {
			t.Errorf("%s: Den sends %q %q %q %q %q %q %d %q", c.Name, got.Kind, got.Agent, got.Tool, got.Title, got.Note, got.Class, got.Step, got.Body)
		}
		if got.HashV2 != c.TextSHA256 {
			t.Errorf("%s: v2 hash %s, the table says %s", c.Name, got.HashV2, c.TextSHA256)
		}
		if got.forVersion(2).Hash != c.TextSHA256 || got.forVersion(1).Hash != got.Hash {
			t.Errorf("%s: forVersion mixes the hashes", c.Name)
		}
	}
	if found != len(raw) {
		t.Errorf("%d of %d raw cards have a row", found, len(raw))
	}
}
