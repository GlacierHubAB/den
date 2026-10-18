package names

import (
	"strings"
	"testing"
)

func TestNormalise(t *testing.T) {
	for in, want := range map[string]string{
		"Backend Engineer":       "backend-engineer",
		"technical_writer":       "technical-writer",
		"  --SRE--  ":            "sre",
		"backend-engineer2":      "backend-engineer", // only Den numbers
		"backend engineer 2":     "backend-engineer",
		"k8s operator":           "k8s-operator",
		"b\u0430ckend":           "bckend",  // a Cyrillic letter is dropped, never read as a
		"back\u200bend":          "backend", // a zero-width space too
		"rev\u202eiewer":         "reviewer",
		"caf\u00e9 helper":       "caf-helper",
		"@orchestrator":          "orchestrator",
		"2fast":                  "",
		"x":                      "",
		"":                       "",
		"\u4e2d\u6587":           "",
		strings.Repeat("a", 200): strings.Repeat("a", MaxLen), // cut, as any name past MaxLen
		"technical writer for the public api docs": "technical-writer-for-the-public",
	} {
		if got := Normalise(in); got != want {
			t.Errorf("Normalise(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReserved(t *testing.T) {
	for _, w := range []string{"den", "orchestrator", "human", "you", "me", "system", "console", "dock", "drey", "api",
		"claude", "anthropic", "root", "admin", "user", "all", "everyone", "here"} {
		for _, name := range []string{w, w + "-bot", "my-" + w, "a-" + w + "-b"} {
			if Reserved(name) == "" {
				t.Errorf("%q was allowed", name)
			}
		}
	}
	for _, name := range []string{"the-human", "the-orchestrator", "close", "stop", "none", "every", "idle",
		"deadbeef", "3f9c1a2b", "20261018-105821-1d533d66", "a1b2c3d4-builder", "deaf", "cafe", "bead", "cafe-bead"} {
		if Reserved(name) == "" {
			t.Errorf("%q was allowed", name)
		}
	}
	for _, name := range []string{"backend-engineer", "technical-writer", "reviewer", "denver-guide", "users-guide", "sysadmin", "ace", "decoder"} {
		if why := Reserved(name); why != "" {
			t.Errorf("%q refused: %s", name, why)
		}
	}
	// A hostile role, through the whole path.
	for _, role := range []string{"Den", "den bot", "@orchestrator", "the human", "SYSTEM check", "\u202eden"} {
		if base, why := Base(role, ""); base != "" || why == "" {
			t.Errorf("Base(%q) = %q", role, base)
		}
	}
}

func TestGenericRolesTakeTheTitle(t *testing.T) {
	for _, role := range []string{"coding agent", "dev agent", "general engineer", "Engineer", "AI assistant", ""} {
		if !Generic(role) {
			t.Errorf("%q is not generic", role)
		}
	}
	for _, role := range []string{"backend engineer", "technical writer", "sysadmin", "security reviewer"} {
		if Generic(role) {
			t.Errorf("%q is generic", role)
		}
	}
	if base, _ := Base("coding agent", "Release notes writer"); base != "release-notes-writer" {
		t.Errorf("generic role with a title: %q", base)
	}
	if base, why := Base("coding agent", ""); base != "" || why == "" {
		t.Errorf("generic role, no title: %q %q", base, why)
	}
	if base, _ := Base("backend engineer", "ignored here"); base != "backend-engineer" {
		t.Errorf("a specific role keeps its name: %q", base)
	}
}
