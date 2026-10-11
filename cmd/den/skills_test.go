package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/skills"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// den listens on a test socket and answers each skills offer with verdict.
func den(t *testing.T, verdict string) *[]bridge.SkillsInput {
	t.Helper()
	sock := filepath.Join(t.TempDir(), "den.sock")
	t.Setenv("DEN_SOCKET", sock)
	srv, err := bridge.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { srv.Close() })
	var seen []bridge.SkillsInput
	go func() {
		for r := range srv.Requests() {
			var in bridge.SkillsInput
			json.Unmarshal(r.Payload, &in)
			seen = append(seen, in)
			msg := "installed frontend-design"
			if verdict != "allow" {
				msg = "declined in Den; nothing installed"
			}
			r.Reply(bridge.Decision{Behavior: verdict, Message: msg})
		}
	}()
	return &seen
}

func TestSpawnRoleOffersSkillsForThatAgentOnly(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	seen := den(t, "allow")
	req := spawn.Request{Dir: t.TempDir(), Agent: true, Brief: "make it pretty", Name: "ui-dev"}
	note, err := offerAgentSkills(&req, "ui designer", nil, "orchestrator")
	if err != nil {
		t.Fatal(err)
	}
	if len(*seen) != 1 {
		t.Fatalf("offers: %+v", *seen)
	}
	in := (*seen)[0]
	if in.Scope != "agent" || in.Peer != "ui-dev" || len(in.Skills) != 1 || in.Skills[0] != "frontend-design" {
		t.Errorf("offer: %+v", in)
	}
	if len(req.AddDirs) != 1 || req.AddDirs[0] != in.Root || !strings.HasPrefix(in.Root, filepath.Join(skills.StateDir(), "skills", "agents", "ui-dev-")) {
		t.Errorf("add dirs %v for root %s", req.AddDirs, in.Root)
	}
	if !strings.Contains(note, "for this agent only") {
		t.Errorf("note: %s", note)
	}
}

func TestSpawnRoleDeclinedStartsWithoutSkills(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", t.TempDir())
	den(t, "deny")
	req := spawn.Request{Dir: t.TempDir(), Agent: true, Brief: "x"}
	note, err := offerAgentSkills(&req, "coding agent", nil, "lead-1")
	if err != nil || len(req.AddDirs) != 0 || !strings.Contains(note, "started without") {
		t.Errorf("declined: %v %v %s", err, req.AddDirs, note)
	}
	// No role match: nothing is asked.
	req = spawn.Request{Dir: t.TempDir(), Agent: true, Brief: "x"}
	note, _ = offerAgentSkills(&req, "assistant", nil, "orchestrator")
	if !strings.Contains(note, "No catalogue skill fits") {
		t.Errorf("no match: %s", note)
	}
	// A skill outside the catalogue is refused before Den is asked.
	if _, err := offerProjectSkills(orch.SkillsInput{Role: "coding agent", Dir: t.TempDir(), Skills: []string{"curl-installer"}}, "orchestrator"); err == nil {
		t.Error("a non-catalogue skill was offered")
	}
}
