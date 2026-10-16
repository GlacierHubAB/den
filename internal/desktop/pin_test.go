package desktop

import (
	"path"
	"path/filepath"
	"strings"
	"testing"
)

// A fetch writes files at the pin's own keys, so every key must stay a
// clean, local, slash-separated path inside the folder, whatever a later
// regeneration of the pin puts there.
func TestPinKeysAreLocal(t *testing.T) {
	for rel := range Pinned.Files {
		if !filepath.IsLocal(rel) || path.Clean(rel) != rel || strings.Contains(rel, `\`) || strings.HasPrefix(rel, "/") {
			t.Errorf("pin key %q is not a clean local path", rel)
		}
		if rel != "LICENSE" && path.Dir(rel) != PackageDir {
			t.Errorf("pin key %q is outside %s", rel, PackageDir)
		}
	}
}
