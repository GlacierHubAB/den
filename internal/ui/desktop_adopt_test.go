package ui

import "testing"

// With no session file (print mode), the agent is adopted only when the
// proxy's parent is Claude Code started with the mission's session id.
func TestAdoptAgentByCommandLine(t *testing.T) {
	deskRig(t)
	oldS, oldC, oldE := deskSession, deskCmdline, deskExeOf
	t.Cleanup(func() { deskSession, deskCmdline, deskExeOf = oldS, oldC, oldE })
	oldSame := deskSameExe
	t.Cleanup(func() { deskSameExe = oldSame })
	deskSameExe = func(pid int, path string) bool { return deskExeOf(pid) == path }
	deskSession = func(string) (int, bool) { return 0, false }
	deskParent = func(int) (int, string, bool) { return 500, "claude", true }
	deskExeOf = func(int) string { return "/opt/claude/claude" }
	deskCmdline = func(int) []string { return []string{"claude", "-p", "--session-id", "sess-x"} }
	dm := &deskMission{SessionID: "sess-x", Claude: "/opt/claude/claude"}
	if !adoptAgent(dm, 600) || dm.AgentPID != 500 || dm.AgentStart != "5000" {
		t.Fatalf("not adopted: %+v", dm)
	}
	if adoptAgent(&deskMission{SessionID: "sess-other", Claude: "/opt/claude/claude"}, 600) {
		t.Fatal("adopted a Claude Code of another session")
	}
	// A binary named claude that is not the file Den launched.
	if adoptAgent(&deskMission{SessionID: "sess-x", Claude: "/home/u/bin/claude"}, 600) {
		t.Fatal("adopted another file named claude")
	}
	deskExeOf = func(int) string { return "/usr/bin/python3" }
	deskCmdline = func(int) []string { return []string{"claude", "--session-id", "sess-x"} }
	if adoptAgent(&deskMission{SessionID: "sess-x", Claude: "/opt/claude/claude"}, 600) {
		t.Fatal("adopted a process that only calls itself claude")
	}
	// The native installer: the binary's real path ends in its version.
	native := "/home/u/.local/share/claude/versions/2.1.296"
	deskExeOf = func(int) string { return native }
	deskCmdline = func(int) []string { return []string{"claude", "-p", "--session-id", "sess-x"} }
	if !adoptAgent(&deskMission{SessionID: "sess-x", Claude: native}, 600) {
		t.Fatal("a native install's agent was not adopted")
	}
}
