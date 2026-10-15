package spawn

import (
	"strings"
	"testing"
)

// A guarded agent starts with Den's --settings file and the session id Den
// drew; neither goes on a resume, and a bad one is refused.
func TestCommandSettingsAndSessionID(t *testing.T) {
	sid := "0f2a3b4c-5d6e-4f70-8192-a3b4c5d6e7f8"
	argv := strings.Join(Command(Request{Agent: true, Brief: "go", PermissionMode: "bypassPermissions", Settings: "/state/m.json", SessionID: sid}, "ui"), "\x00")
	if !strings.Contains(argv, "\x00--settings\x00/state/m.json\x00") || !strings.Contains(argv, "\x00--session-id\x00"+sid) {
		t.Errorf("argv %q", strings.Split(argv, "\x00"))
	}
	argv = strings.Join(Command(Request{Agent: true, Resume: sid, SessionID: sid}, "ui"), "\x00")
	if strings.Contains(argv, "--session-id") {
		t.Errorf("a resume with a session id: %q", strings.Split(argv, "\x00"))
	}
}
