package main

import (
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/mcp"
	"github.com/GlacierHubAB/den/internal/orch"
)

// Every tool `den mcp` offers the orchestrator is one Den lets it use in
// the human's own turns, in every mode: with control on, Den's answer to
// its permission request (orch.HumanTurnTools); without, its
// --allowedTools (orch.Argv). The tools are the server's own list, so a
// tool added later is checked too.
func TestEveryOfferedToolIsAllowedInAHumanTurn(t *testing.T) {
	for _, spawn := range []bool{true, false} {
		for _, control := range []bool{true, false} {
			offered := mcpTools(append(baseTools("orchestrator", "", "/x/den"), desktopMissionTool("orchestrator")), true, !spawn, control)
			if len(offered) == 0 {
				t.Fatalf("spawn %v control %v: no tools", spawn, control)
			}
			allowed := orch.HumanTurnTools(spawn)
			if !control {
				allowed = map[string]bool{}
				argv := orch.Argv(orch.Options{Spawn: spawn, Executable: "/x/den"})
				for i, a := range argv {
					if a == "--allowedTools" && i+1 < len(argv) {
						for _, t := range strings.Split(argv[i+1], ",") {
							allowed[t] = true
						}
					}
				}
			}
			for _, tool := range offered {
				full := "mcp__" + orch.MCPServer + "__" + tool.Name
				if !allowed[full] {
					t.Errorf("spawn %v control %v: %s is offered but not allowed in a human turn", spawn, control, full)
				}
			}
		}
	}
}

// The test above would pass with no tools; make sure the notes tools,
// which the demo found refused, are among those offered with control on.
func TestNotesAreOfferedWithControl(t *testing.T) {
	var names []string
	for _, tool := range mcpTools(append(baseTools("o", "", "/x/den"), desktopMissionTool("o")), true, false, true) {
		names = append(names, tool.Name)
	}
	got := strings.Join(names, ",")
	for _, want := range []string{"notes", "note", "propose_rule", "tune_process", "send_message"} {
		if !strings.Contains(","+got+",", ","+want+",") {
			t.Errorf("%s not offered: %s", want, got)
		}
	}
	_ = mcp.Tool{}
}
