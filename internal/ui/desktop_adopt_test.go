package ui

import "testing"

// With no session file (print mode), the agent is adopted only when the
// proxy's parent is Claude Code started with the mission's session id.
func TestAdoptAgentByCommandLine(t *testing.T) {
	deskRig(t)
	oldS, oldC, oldE := deskSession, deskCmdline, deskExeOf
	t.Cleanup(func() { deskSession, deskCmdline, deskExeOf = oldS, oldC, oldE })
	deskSession = func(string) (int, bool) { return 0, false }
	deskParent = func(int) (int, string, bool) { return 500, "claude", true }
	deskExeOf = func(int) string { return "/usr/bin/claude" }
	deskCmdline = func(int) []string { return []string{"claude", "-p", "--session-id", "sess-x"} }
	dm := &deskMission{SessionID: "sess-x"}
	if !adoptAgent(dm, 600) || dm.AgentPID != 500 || dm.AgentStart != "5000" {
		t.Fatalf("not adopted: %+v", dm)
	}
	if adoptAgent(&deskMission{SessionID: "sess-other"}, 600) {
		t.Fatal("adopted a Claude Code of another session")
	}
	deskExeOf = func(int) string { return "/usr/bin/python3" }
	deskCmdline = func(int) []string { return []string{"claude", "--session-id", "sess-x"} }
	if adoptAgent(&deskMission{SessionID: "sess-x"}, 600) {
		t.Fatal("adopted a process that only calls itself claude")
	}
}
