package review

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMessageQuotesAndCites(t *testing.T) {
	cs := []Comment{
		{ID: "b", Root: "/r/tasklog", Path: "internal/store/filter.go", Side: "new", Start: 2, End: 2, Quote: []string{"+func Pending() {}"}, Body: "Exclude archived tasks too."},
		{ID: "a", Root: "/r/tasklog", Path: "cmd/list.go", Side: "new", Start: 19, End: 20, Quote: []string{"-\t\tRunE:  runList,", "+\t\tRunE: run,", "+\t\tArgs: cobra.NoArgs,"}, Body: "Keep the old name.\nrun is too vague."},
		{ID: "c", Root: "/r/tasklog", Path: "cmd/list.go", Side: "old", Start: 7, End: 7, Quote: []string{"-// list prints tasks"}, Body: "Why remove the doc comment?"},
	}
	want := "Review of your changes: 3 comments.\n" +
		"Line numbers are from when I commented; the quoted text is what I mean.\n" +
		"\nIn /r/tasklog:\n" +
		"\ncmd/list.go:7 (removed lines, numbered as before the change)\n" +
		"  > (removed) // list prints tasks\n" +
		"  Why remove the doc comment?\n" +
		"\ncmd/list.go:19-20\n" +
		"  > (removed) \t\tRunE:  runList,\n" +
		"  > \t\tRunE: run,\n" +
		"  > \t\tArgs: cobra.NoArgs,\n" +
		"  Keep the old name.\n" +
		"  run is too vague.\n" +
		"\ninternal/store/filter.go:2\n" +
		"  > func Pending() {}\n" +
		"  Exclude archived tasks too.\n" +
		"\nFix these, then say which comment each change addresses."
	if got := Message(cs); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestOrchestratorMessage(t *testing.T) {
	c := Comment{Root: "/r/a", Path: "x.go", Side: "new", Start: 3, End: 3, Quote: []string{"+x := 1"}, Body: "Name it."}
	want := "I wrote 1 comment for Mossy Lemur, whose session has ended. Pass them to a live agent that can act on them, or tell me none can.\n" +
		"Line numbers are from when I commented; the quoted text is what I mean.\n" +
		"\nIn /r/a:\n\nx.go:3\n  > x := 1\n  Name it."
	if got := OrchestratorMessage("Mossy Lemur", []Comment{c}); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
	if got := OrchestratorMessage("", []Comment{c}); got[:44] != "I wrote 1 comment on lines no agent wrote. P" {
		t.Errorf("no agent: %q", got)
	}
}

func TestChanged(t *testing.T) {
	file := "package a\n\nfunc A() {\n\treturn 1\n}\n"
	kept := Comment{Quote: []string{"+func A() {", "+\treturn 1"}}
	removed := Comment{Side: "old", Quote: []string{"-func Old() {}"}}
	cases := []struct {
		name    string
		c       Comment
		content []byte
		want    bool
	}{
		{"lines still there", kept, []byte(file), false},
		{"lines moved down", kept, []byte("package a\n\n// moved\n\nfunc A() {\n\treturn 1\n}\n"), false},
		{"one line edited", kept, []byte("package a\n\nfunc A() {\n\treturn 2\n}\n"), true},
		{"block split", kept, []byte("package a\n\nfunc A() {\n\t// note\n\treturn 1\n}\n"), true},
		{"file gone", kept, nil, true},
		{"crlf file", kept, []byte("package a\r\n\r\nfunc A() {\r\n\treturn 1\r\n}\r\n"), false},
		{"removed lines still gone", removed, []byte(file), false},
		{"removed lines back", removed, []byte(file + "func Old() {}\n"), true},
		{"context only", Comment{Quote: []string{" package a"}}, []byte(file), false},
		{"no quote", Comment{}, nil, false},
	}
	for _, tc := range cases {
		if got := Changed(tc.c, tc.content); got != tc.want {
			t.Errorf("%s: Changed = %v, want %v", tc.name, got, tc.want)
		}
	}

	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "a.go"), []byte(file), 0o644)
	c := kept
	c.Root, c.Path = dir, "a.go"
	if FileChanged(c) {
		t.Error("unchanged file on disk reported changed")
	}
	os.WriteFile(filepath.Join(dir, "a.go"), []byte("package a\n"), 0o644)
	if !FileChanged(c) {
		t.Error("edited file on disk not reported changed")
	}
}

// A reviews.json written before sending existed has no via or addressed
// fields; it still loads, its drafts are drafts and its sent comments are
// open, and saving it again keeps it loadable.
func TestCommentsOldFileLoads(t *testing.T) {
	p := filepath.Join(t.TempDir(), "reviews.json")
	old := `{
  "comments": [
    {"id": "c1", "root": "/r", "path": "a.go", "side": "new", "start": 3, "end": 3, "quote": ["+x := 1"], "body": "name it", "sessionId": "s1", "created": "2026-10-08T09:00:00Z"},
    {"id": "c2", "root": "/r", "path": "a.go", "side": "new", "start": 5, "end": 5, "quote": ["+y := 2"], "body": "and this", "sessionId": "s1", "created": "2026-10-08T09:01:00Z", "sent": "2026-10-08T09:02:00Z"}
  ]
}`
	os.WriteFile(p, []byte(old), 0o600)
	cs := LoadComments(p)
	if len(cs) != 2 || !cs[0].Draft() || cs[0].Open() || cs[1].Draft() || !cs[1].Open() || cs[1].Via != "" {
		t.Fatalf("old file = %+v", cs)
	}
	cs[1].Addressed = time.Date(2026, 10, 8, 10, 0, 0, 0, time.UTC)
	cs[1].Via = "orchestrator"
	if err := SaveComments(p, cs); err != nil {
		t.Fatal(err)
	}
	again := LoadComments(p)
	if len(again) != 2 || again[1].Open() || !again[1].Addressed.Equal(cs[1].Addressed) || again[1].Via != "orchestrator" || !again[0].Draft() {
		t.Errorf("after save = %+v", again)
	}
	raw, _ := os.ReadFile(p)
	if n := strings.Count(string(raw), `"addressed"`); n != 1 {
		t.Errorf("addressed written %d times, want only for the addressed comment:\n%s", n, raw)
	}
}
