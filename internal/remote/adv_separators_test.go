package remote_test

import (
	"strings"
	"testing"
)

// sanitize keeps U+2028 and U+2029 (line and paragraph separators), so a file
// name or a line of code carrying one crosses to Den and is shown as it is.
func TestAdvLineSeparatorsInPaths(t *testing.T) {
	d, err, _ := attackDiff(t, func(t *testing.T, g *rig) scene {
		repo := mkRepo(t, g, "work")
		names := []string{"sep\u2028arator.txt", "para\u2029graph.txt"}
		var written []string
		for _, n := range names {
			g.r.WriteFile("work/"+n, "x\n")
			written = append(written, repo+"/"+n)
		}
		return scene{cwd: repo, written: written}
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range d.Files {
		if strings.ContainsAny(f.Path, "\u2028\u2029") {
			t.Errorf("a line or paragraph separator in the path %q", f.Path)
		}
	}
}
