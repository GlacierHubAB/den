package review

import (
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/transcript"
)

func at(min int) time.Time {
	return time.Date(2026, 10, 8, 9, min, 0, 0, time.UTC)
}

func change(path string, min int, tool string, lines ...string) transcript.TimedChange {
	return transcript.TimedChange{At: at(min), Tool: tool, FileChange: transcript.FileChange{
		Path: path, Hunks: []transcript.Hunk{{OldStart: 1, NewStart: 1, Lines: lines}},
	}}
}

// owners renders one hunk's attribution as "line=owner" pairs for compact
// comparisons; context lines are left out.
func owners(f File, hunk int) string {
	var out []string
	for i, l := range f.Hunks[hunk].Lines {
		if l[0] == ' ' {
			continue
		}
		o := f.Owner[hunk][i]
		if o == "" {
			o = "-"
		}
		out = append(out, l[:1]+strings.TrimSpace(l[1:])+"="+o)
	}
	return strings.Join(out, " | ")
}

func TestAttributeTwoAgentsOneFile(t *testing.T) {
	files := ParseUnified(`diff --git a/cmd/list.go b/cmd/list.go
--- a/cmd/list.go
+++ b/cmd/list.go
@@ -1,4 +1,9 @@
 package cmd
-func old() {}
+func runList(pending bool) error {
+	if pending {
+		tasks = store.Pending(tasks)
+	}
+	return nil
+}
+// TODO(you): wire the flag
 var x = 1
`)
	sources := []Source{
		{SessionID: "mossy", Changes: []transcript.TimedChange{
			change("/repo/cmd/list.go", 1, "Write", "-func old() {}", "+func runList(pending bool) error {", "+	return nil", "+}"),
		}},
		{SessionID: "maple", Changes: []transcript.TimedChange{
			change("/repo/cmd/list.go", 5, "Edit", "+	if pending {", "+		tasks = store.Pending(tasks)", "+	}"),
			// Edits to other files never leak in.
			change("/repo/cmd/root.go", 6, "Edit", "+// TODO(you): wire the flag"),
		}},
	}
	Attribute("/repo", files, sources)

	want := "-func old() {}=mossy | +func runList(pending bool) error {=mossy | +if pending {=maple | " +
		"+tasks = store.Pending(tasks)=maple | +}=maple | +return nil=mossy | +}=mossy | +// TODO(you): wire the flag=-"
	if got := owners(files[0], 0); got != want {
		t.Errorf("owners\n got %s\nwant %s", got, want)
	}
}

func TestAttributeNewestEditWinsAndBashCounts(t *testing.T) {
	files := ParseUnified(`diff --git a/a.go b/a.go
--- a/a.go
+++ b/a.go
@@ -1,1 +1,1 @@
-const v = 1
+const v = 3
`)
	sources := []Source{
		{SessionID: "first", Changes: []transcript.TimedChange{
			change("/repo/a.go", 1, "Edit", "-const v = 1", "+const v = 3"),
		}},
		{SessionID: "second", Changes: []transcript.TimedChange{
			// A later sed that ended on the same text takes the line.
			change("/repo/a.go", 9, "Bash", "-const v = 2", "+const v = 3"),
		}},
	}
	Attribute("/repo", files, sources)
	if got := owners(files[0], 0); got != "-const v = 1=first | +const v = 3=second" {
		t.Errorf("owners = %s", got)
	}
}

func TestAttributeEditedByHandAndReverted(t *testing.T) {
	// The agent wrote "timeout := 5" and you changed it to 30 afterwards;
	// it also added a debug line and then removed it again, so git never
	// shows that one at all.
	files := ParseUnified(`diff --git a/srv.go b/srv.go
--- a/srv.go
+++ b/srv.go
@@ -1,1 +1,2 @@
 package srv
+timeout := 30
`)
	sources := []Source{{SessionID: "agent", Changes: []transcript.TimedChange{
		change("/repo/srv.go", 1, "Edit", "+timeout := 5", "+log.Println(\"debug\")"),
		change("/repo/srv.go", 2, "Edit", "-log.Println(\"debug\")"),
	}}}
	Attribute("/repo", files, sources)
	if got := owners(files[0], 0); got != "+timeout := 30=-" {
		t.Errorf("owners = %s", got)
	}
}

func TestAttributeRenamedFileAndTrailingSpace(t *testing.T) {
	files := ParseUnified(`diff --git a/old.go b/new.go
similarity 80%
rename from old.go
rename to new.go
--- a/old.go
+++ b/new.go
@@ -1,1 +1,1 @@
-x := 1
+x := 2
`)
	sources := []Source{{SessionID: "agent", Changes: []transcript.TimedChange{
		change("/repo/old.go", 1, "Edit", "-x := 1  ", "+x := 2\r"),
	}}}
	Attribute("/repo", files, sources)
	if got := owners(files[0], 0); got != "-x := 1=agent | +x := 2=agent" {
		t.Errorf("owners = %s", got)
	}
}
