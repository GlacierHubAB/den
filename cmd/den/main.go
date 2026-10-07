// Command den is a TUI that tracks and manages the coding agents running in
// your terminals. See docs/PLAN.md.
//
//	den                  open the TUI
//	den list [-json]     print the agents Den sees
//	den focus <who>      bring an agent's terminal to the front
//	den explain <who>    say how Den arrived at what it shows for an agent
//	den send <who> <msg> deliver a message to one agent's session
//	den ask <text>       one orchestrator turn: ask about the fleet or have it relay
//	den install-hooks    wire Den into Claude Code's hooks (undo: uninstall-hooks)
//	den events           headless: print hook events as they arrive
//	den hook             used by Claude Code's hooks; not for humans
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/ui"
)

func main() {
	if len(os.Args) < 2 {
		runTUI()
		return
	}
	switch os.Args[1] {
	case "list":
		fs := flag.NewFlagSet("list", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "print JSON")
		fs.Parse(os.Args[2:])
		list(*asJSON)
	case "focus":
		if len(os.Args) < 3 {
			fail("usage: den focus <name|session-id|pid>")
		}
		focus(os.Args[2])
	case "explain":
		if len(os.Args) < 3 {
			fail("usage: den explain <name|session-id|pid>")
		}
		explain(os.Args[2])
	case "send":
		send(os.Args[2:])
	case "ask":
		ask(os.Args[2:])
	case "hook":
		runHook()
	case "install-hooks":
		installHooks()
	case "uninstall-hooks":
		uninstallHooks()
	case "events":
		runEvents()
	case "-h", "--help", "help":
		fmt.Println("usage: den | den list [-json] | den focus <who> | den explain <who> | den send <who> <msg> | den ask <text> | den install-hooks | den uninstall-hooks | den events")
	default:
		fail("den: unknown command %q", os.Args[1])
	}
}

func runTUI() {
	m := ui.New()
	defer m.Close()
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fail("den: %v", err)
	}
}

func list(asJSON bool) {
	agents, err := fleet.Snapshot()
	if err != nil {
		fail("den: %v", err)
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(agents)
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tNAME\tROLE\tTASK\tWHERE\tWINDOW\tPID\tSESSION")
	for _, a := range agents {
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			a.Status, since(a.Since()), a.Name, a.Labels.Role, trunc(a.Task(), 48), a.Where(), a.Place(), a.Session.PID, a.Session.SessionID[:8])
	}
	tw.Flush()
}

func explain(who string) {
	agents, err := fleet.Snapshot()
	if err != nil {
		fail("den: %v", err)
	}
	a, ok := fleet.Find(agents, who)
	if !ok {
		fail("den: no agent matches %q", who)
	}
	for _, l := range fleet.Explain(a) {
		fmt.Println(l)
	}
}

func focus(who string) {
	agents, err := fleet.Snapshot()
	if err != nil {
		fail("den: %v", err)
	}
	a, ok := fleet.Find(agents, who)
	if !ok {
		fail("den: no agent matches %q", who)
	}
	note, err := fleet.Focus(a)
	if err != nil {
		fail("den: %v", err)
	}
	fmt.Printf("focused %s: %s\n", a.Name, note)
}

func since(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	}
	return fmt.Sprintf("%dh", int(d.Hours()))
}

func trunc(s string, n int) string {
	r := []rune(s)
	if len(r) <= n {
		return s
	}
	return string(r[:n-1]) + "…"
}

func fail(format string, args ...any) {
	fmt.Fprintf(os.Stderr, format+"\n", args...)
	os.Exit(1)
}
