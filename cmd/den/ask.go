package main

import (
	"context"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/transcript"
)

// ask is `den ask <text…>`: one orchestrator turn from the shell.
func ask(args []string) {
	if len(args) == 0 {
		fail("usage: den ask <question or instruction>")
	}
	agents, err := fleet.Snapshot()
	if err != nil {
		fail("den: %v", err)
	}
	var roster []orch.RosterAgent
	for _, a := range agents {
		if a.Status == fleet.Gone || a.Session.Name == "den-orchestrator" {
			continue
		}
		roster = append(roster, orch.RosterAgent{Critter: a.Name, Peer: a.Session.Name, Role: a.Labels.Role, Status: a.Status.String(), Task: a.Task(), Where: a.Where(), Terminal: a.Window != nil})
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	o, err := orch.Start(ctx, orch.Options{Name: "den-orchestrator"})
	if err != nil {
		fail("den: %v", err)
	}
	defer o.Close()
	if err := o.Send(orch.Compose(roster, strings.Join(args, " "))); err != nil {
		fail("den: %v", err)
	}
	fan := map[string]string{}
	for ev := range o.Events() {
		switch ev.Kind {
		case orch.Started:
			fmt.Fprintf(os.Stderr, "orchestrator ready (%s)\n", transcript.PrettyModel(ev.Model))
		case orch.Text:
			fmt.Println(strings.TrimSpace(ev.Text))
		case orch.ToolUse:
			if in, ok := orch.ParseSendMessage(ev.Input); ok {
				fan[ev.ToolUseID] = in.To
				fmt.Printf("→ %s · %s\n", in.To, firstN(in.Message, 100))
			} else {
				fmt.Printf("· %s\n", ev.Tool)
			}
		case orch.ToolResult:
			if to, ok := fan[ev.ToolUseID]; ok {
				fmt.Printf("  %s: %s\n", to, firstN(ev.Text, 100))
			}
		case orch.Result:
			fmt.Fprintf(os.Stderr, "done · $%.3f\n", ev.CostUSD)
			return
		case orch.Error:
			fail("den: %s", ev.Text)
		case orch.Exited:
			fail("den: %s", ev.Text)
		}
	}
}
