package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCheckExampleHasNoIssues(t *testing.T) {
	kv, err := parseFrom(strings.NewReader(Example))
	if err != nil {
		t.Fatal(err)
	}
	if got := CheckKeys(kv); len(got) != 0 {
		t.Errorf("the documented example has issues: %+v", got)
	}
}

// A key Check calls fine must change what Load gives, and one it calls
// unread must leave the default: the two read the file the same way.
func TestCheckAgreesWithLoad(t *testing.T) {
	for _, v := range []string{"DEN_ORCHESTRATOR_MODEL", "DEN_ORCHESTRATOR_SPAWN", "DEN_PERMISSION_WAIT", "DEN_HOOK_LOG", "DEN_RESTORE", "DEN_NOTIFY"} {
		t.Setenv(v, "")
	}
	for k := range keyChecks {
		if strings.HasPrefix(k, "desktop.") || k == "agents.permission_mode" {
			continue // desktop's are read by their own functions; a refused mode is kept in ModeRefused for Den's note
		}
		section, name, _ := strings.Cut(k, ".")
		dir := t.TempDir()
		t.Setenv("DEN_CONFIG", filepath.Join(dir, "config.toml"))
		os.WriteFile(filepath.Join(dir, "config.toml"), []byte("["+section+"]\n"+name+" = \"not-a-value\"\n"), 0o644)
		issues, err := Check()
		if err != nil {
			t.Fatal(err)
		}
		unread := len(issues) == 1 && issues[0].Kind == IssueUnread && issues[0].Key == k
		if changed := Load() != Defaults(); changed == unread {
			t.Errorf("%s = \"not-a-value\": Load changed %v, Check unread %v (%+v)", k, changed, unread, issues)
		}
	}
}

// D2 (den-sec-review): a key is named only when every part of it is Den's.
func TestCheckNamesOnlyDensOwnKeys(t *testing.T) {
	kv := map[string]string{
		"notify.remind":                   "soon",     // unread: Den's key, named
		"notify.secret-host":              "1",        // unknown key in a known table
		"hosts.secret-host.target":        "a@b",      // unknown table
		"top-level-secret":                "1",        // no table
		"god.secret-switch":               "true",     // God
		"desktop.apps.secret-app.command": `["foot"]`, // fine
		"desktop.apps.secret-app.secret":  "1",        // unknown key of an app
		"prices.secret-model":             "1, 2",     // the cost table's
		"team.max_agents":                 "21",       // out of range
	}
	got := CheckKeys(kv)
	want := []Issue{
		{Kind: IssueGod},
		{Kind: IssueUnknown},
		{Kind: IssueUnknown},
		{Kind: IssueUnknown, Section: "desktop.apps"},
		{Kind: IssueUnknown, Section: "notify"},
		{Kind: IssueUnread, Key: "notify.remind", Section: "notify"},
		{Kind: IssueUnread, Key: "team.max_agents", Section: "team"},
	}
	if len(got) != len(want) {
		t.Fatalf("got %+v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("issue %d = %+v, want %+v", i, got[i], want[i])
		}
	}
	for _, is := range got {
		if strings.Contains(is.Key+is.Section, "secret") {
			t.Errorf("issue repeats a name Den does not define: %+v", is)
		}
	}
}

func TestCheckMissingFileIsFine(t *testing.T) {
	t.Setenv("DEN_CONFIG", filepath.Join(t.TempDir(), "none.toml"))
	if got, err := Check(); err != nil || got != nil {
		t.Errorf("Check = %v, %v", got, err)
	}
}
