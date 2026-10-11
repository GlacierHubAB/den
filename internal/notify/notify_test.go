package notify

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fake writes a stand-in notify-send that records its arguments and prints
// what a server would report.
func fake(t *testing.T, prints string) (argsFile string) {
	t.Helper()
	dir := t.TempDir()
	argsFile = filepath.Join(dir, "args")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + argsFile + "\n" + prints + "\n"
	bin := filepath.Join(dir, "notify-send")
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	old := Command
	Command = bin
	t.Cleanup(func() { Command = old })
	return argsFile
}

func TestSendReportsTheDefaultAction(t *testing.T) {
	argsFile := fake(t, "echo default")
	if !Send(Note{Summary: "Mossy Lemur needs your permission"}) {
		t.Fatal("a click on the default action should report clicked")
	}
	raw, _ := os.ReadFile(argsFile)
	got := strings.Split(strings.TrimSpace(string(raw)), "\n")
	want := []string{"--app-name=Den", "--urgency=normal", "--action=default=Open", "--wait", "--", "Mossy Lemur needs your permission"}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("args = %q", got)
	}
}

func TestSendWithoutAClickIsQuiet(t *testing.T) {
	for _, prints := range []string{
		"",                                // closed, or a server without actions
		"echo 'Wait timeout expired' >&2", // expired
		"echo other",                      // some other action
		"exit 1",                          // no server at all
	} {
		fake(t, prints)
		if Send(Note{Summary: "Den test"}) {
			t.Errorf("%q: should not count as a click", prints)
		}
	}
	Command = filepath.Join(t.TempDir(), "missing")
	if Send(Note{Summary: "Den test"}) || Available() {
		t.Error("a missing notify-send is neither available nor clicked")
	}
}

func TestPostHasNoAction(t *testing.T) {
	argsFile := fake(t, "")
	Post(Note{Summary: "Den test", Body: "second line", Urgent: true})
	raw, _ := os.ReadFile(argsFile)
	s := string(raw)
	if strings.Contains(s, "--action") || strings.Contains(s, "--wait") || !strings.Contains(s, "--urgency=critical") || !strings.HasSuffix(strings.TrimSpace(s), "second line") {
		t.Errorf("args = %q", s)
	}
}
