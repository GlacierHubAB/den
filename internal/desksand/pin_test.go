package desksand

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Den never reads, parses or runs anything a launched app can write: its
// work/ and profile-*/ folders. Every line of Den's code naming them is
// on this list, each one only making the folder, binding it into the
// sandbox, or running the app there; a new one fails here until it is
// looked at.
var writableUses = map[string]int{
	"internal/desksand/args.go":      2, // Work and Profile themselves
	"internal/desktop/agent.go":      1, // a configured app's profile flag
	"internal/ui/desktop_mission.go": 1, // a plan app's service runs in work/
	"internal/ui/desk_sand.go":       4, // Mkdir of each, never MkdirAll; the walk runs in work/
	"internal/ui/desk_slice.go":      1, // a launch's service runs in work/
}

func TestNothingReadsTheWritableFolders(t *testing.T) {
	use := regexp.MustCompile(`desksand\.Work\(|desksand\.Profile\(|"profile-"|"work"`)
	root, _ := filepath.Abs("../..")
	got := map[string]int{}
	for _, top := range []string{"internal", "cmd"} {
		filepath.WalkDir(filepath.Join(root, top), func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
				return nil
			}
			raw, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			rel, _ := filepath.Rel(root, p)
			if n := len(use.FindAllIndex(raw, -1)); n > 0 {
				got[filepath.ToSlash(rel)] = n
			}
			return nil
		})
	}
	for f, n := range got {
		if writableUses[f] != n {
			t.Errorf("%s names work/ or profile-*/ %d times, the list says %d: check none of them reads what an app wrote, then update the list", f, n, writableUses[f])
		}
	}
	for f, n := range writableUses {
		if got[f] != n {
			t.Errorf("%s: the list says %d, found %d", f, n, got[f])
		}
	}
}
