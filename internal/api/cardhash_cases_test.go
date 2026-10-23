package api

import (
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/api/convcases"
)

// Den sends the card-hash table's fields and hash for each row's raw
// card: projectCard cleans the raw fields, and CardHash is over exactly
// what it sends.
func TestCardHashTable(t *testing.T) {
	raw := map[string]Card{
		"a plain card":                            {Kind: "permission", Agent: "Mossy Lemur", Tool: "Bash", Title: "Run the tests?", Body: "go test ./...\nin /home/x/den"},
		"a plain card with a note":                {Kind: "permission", Agent: "Mossy Lemur", Tool: "Bash", Title: "Run the tests?", Note: "Busy Otter asked the same.", Body: "go test ./..."},
		"a name with controls and bidi, stripped": {Kind: "permission", Agent: "Mossy\x07 Lemur\u202e", Tool: "Bash", Title: "Run the tests?", Body: "go test ./..."},
		"an over-long name, capped":               {Kind: "permission", Agent: strings.Repeat("abcdefghij", 9), Tool: "Bash", Title: "Run the tests?", Body: "go test ./..."},
	}
	if len(raw) != len(convcases.CardHashCases) {
		t.Fatalf("%d raw cards for %d rows", len(raw), len(convcases.CardHashCases))
	}
	for _, c := range convcases.CardHashCases {
		in, ok := raw[c.Name]
		if !ok {
			t.Errorf("%s: no raw card", c.Name)
			continue
		}
		got := projectCard(in)
		if got.Kind != c.Kind || got.Agent != c.Agent || got.Tool != c.Tool || got.Title != c.Title || got.Note != c.Note || got.Body != c.Body {
			t.Errorf("%s: Den sends %q %q %q %q %q %q", c.Name, got.Kind, got.Agent, got.Tool, got.Title, got.Note, got.Body)
		}
		if got.Hash != c.TextSHA256 || CardHash(c.Kind, c.Agent, c.Tool, c.Title, c.Note, c.Body) != c.TextSHA256 {
			t.Errorf("%s: hash %s, the table says %s", c.Name, got.Hash, c.TextSHA256)
		}
	}
}
