package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const root = "../../.."

func TestGoModulesSectionMatchesBuild(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("go tool not available")
	}
	doc, err := os.ReadFile(filepath.Join(root, "THIRD_PARTY_LICENSES"))
	if err != nil {
		t.Fatal(err)
	}
	mods, err := Linked(root)
	if err != nil {
		t.Skipf("module list unavailable offline: %v", err)
	}
	sec, err := Section(mods)
	if err != nil {
		t.Fatal(err)
	}
	want, err := Splice(string(doc), sec)
	if err != nil {
		t.Fatal(err)
	}
	if want != string(doc) {
		// Say which modules differ before the generic hint.
		for _, m := range mods {
			if !strings.Contains(string(doc), "Module: "+m.Path+"\nVersion: "+m.Version+"\n") {
				t.Errorf("%s %s is linked into den but missing or at another version in THIRD_PARTY_LICENSES", m.Path, m.Version)
			}
		}
		t.Fatal("THIRD_PARTY_LICENSES is out of date; run: go run ./internal/tools/thirdparty")
	}
}

func TestCatalogueReposAreListed(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(root, "THIRD_PARTY_LICENSES"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "internal/skills/catalogue.json"))
	if err != nil {
		t.Fatal(err)
	}
	var c struct {
		Repos []struct{ ID, URL, Commit, License string }
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Repos) == 0 {
		t.Fatal("catalogue has no repos")
	}
	for _, r := range c.Repos {
		for _, need := range []string{r.ID, r.URL, r.Commit} {
			if !strings.Contains(string(doc), need) {
				t.Errorf("catalogue repo %s: %q is not in THIRD_PARTY_LICENSES", r.ID, need)
			}
		}
	}
}
