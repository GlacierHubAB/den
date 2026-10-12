// Command den is a TUI that tracks and manages the coding agents running in
// your terminals. See docs/PLAN.md.
//
//	den                  open the TUI
//	den list [-json]     print the agents Den sees
//	den focus <who>      bring an agent's terminal to the front
//	den explain <who>    say how Den arrived at what it shows for an agent
//	den send <who> <msg> deliver a message to one agent's session; --wait for its turn to end
//	den wait <who>       block until an agent is idle, needs you, or done
//	den ask <text>       one orchestrator turn: ask about the fleet or have it relay
//	den skills [...]     the skills catalogue, what Den installed, and removing it
//	den mcp              used by the orchestrator to open terminals and start agents; not for humans
//	den install-hooks    wire Den into Claude Code's hooks (undo: uninstall-hooks)
//	den events           headless: print hook events as they arrive; -json follows the event stream
//	den hook             used by Claude Code's hooks; not for humans
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"text/tabwriter"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/cost"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/tree"
	"github.com/GlacierHubAB/den/internal/ui"
)

// version is set by the Makefile from git describe.
var version = "dev"

func main() {
	if len(os.Args) < 2 {
		runTUI()
		return
	}
	switch os.Args[1] {
	case "version", "-v", "--version":
		fmt.Println("den " + version)
	case "config":
		configCmd(os.Args[2:])
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
	case "wait":
		waitCmd(os.Args[2:])
	case "ask":
		ask(os.Args[2:])
	case "mcp":
		runMCP()
	case "skills":
		skillsCmd(os.Args[2:])
	case "hook":
		runHook()
	case "install-hooks":
		installHooks()
	case "uninstall-hooks":
		uninstallHooks()
	case "events":
		fs := flag.NewFlagSet("events", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "follow the event stream, one JSON object per line")
		fs.Parse(os.Args[2:])
		if *asJSON {
			followEvents()
			return
		}
		runEvents()
	case "mods":
		listMods()
	case "install-mods":
		installMods()
	case "uninstall-mods":
		uninstallMods()
	case "-h", "--help", "help":
		fmt.Println("usage: den | den list [-json] | den focus <who> | den explain <who> | den send [--wait] <who> <msg> | den ask <text>\n       den wait <who> --until idle|needs-you|done [--timeout d] | den events [-json]\n       den install-hooks | den uninstall-hooks | den mods | den install-mods | den uninstall-mods\n       den skills [list | catalogue | match <role> | remove <skill> [where]]\n       den config [init] | den version")
	default:
		fail("den: unknown command %q", os.Args[1])
	}
}

func runTUI() {
	m := ui.New(config.Load())
	defer m.Close()
	if _, err := tea.NewProgram(m).Run(); err != nil {
		fail("den: %v", err)
	}
}

func list(asJSON bool) {
	fleet.Prices = cost.NewTable(config.Prices())
	agents, err := fleet.Snapshot()
	if err != nil {
		fail("den: %v", err)
	}
	if asJSON {
		edges := tree.Load(tree.Path())
		own := map[string]cost.Summary{}
		for _, a := range agents {
			own[a.Session.SessionID] = a.Cost
		}
		for i, a := range agents {
			if len(edges.Children(a.Session.SessionID)) > 0 {
				t := cost.Tree(edges, a.Session.SessionID, func(id string) cost.Summary { return own[id] })
				agents[i].TeamCost = &t
			}
		}
		out, _ := json.MarshalIndent(agents, "", "  ")
		os.Stdout.Write(append(sanitize.JSON(out), '\n'))
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "STATUS\tNAME\tROLE\tTASK\tWHERE\tWINDOW\tPID\tSESSION")
	for _, a := range agents {
		fmt.Fprintf(tw, "%s %s\t%s\t%s\t%s\t%s\t%s\t%d\t%s\n",
			a.Status, since(a.Since()), a.Name, a.Labels.Role, trunc(a.Task(), 48), a.Where(), a.Place(), a.Session.PID, sanitize.Line(a.Session.SessionID[:8]))
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
	fmt.Printf("focused %s: %s\n", a.Name, sanitize.Line(note))
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

// configCmd is `den config`: show the file path and the effective settings;
// `den config init` writes the documented example when there is no file.
func configCmd(args []string) {
	path := config.Path()
	if len(args) > 0 && args[0] == "init" {
		if _, err := os.Stat(path); err == nil {
			fail("den: %s already exists", path)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fail("den: %v", err)
		}
		if err := os.WriteFile(path, []byte(config.Example), 0o644); err != nil {
			fail("den: %v", err)
		}
		fmt.Println("Wrote " + path)
		return
	}
	c := config.Load()
	state := "not present, using defaults"
	if _, err := os.Stat(path); err == nil {
		state = "in use"
	}
	fmt.Printf("%s (%s)\n\n", path, state)
	fmt.Printf("[orchestrator]\nmodel = %q\nname = %q\nspawn = %v\n\n", c.Orchestrator.Model, c.Orchestrator.Name, c.Orchestrator.Spawn)
	fmt.Printf("[inbox]\nhistory = %d\n\n", c.Inbox.History)
	fmt.Printf("[detail]\nrecent = %d\n\n", c.Detail.Recent)
	fmt.Printf("[hooks]\npermission_wait = %d\nlog = %v\n\n", c.Hooks.PermissionWait, c.Hooks.Log)
	fmt.Printf("[notify]\nneeds_you = %v\nremind = %d\ndone = %v\nfailed = %v\nstalled = %v\nwithout_den = %v\n\n", c.Notify.NeedsYou, c.Notify.Remind, c.Notify.Done, c.Notify.Failed, c.Notify.Stalled, c.Notify.WithoutDen)
	fmt.Printf("[stuck]\nstalled = %d\ntool_failures = %d\n\n", c.Stuck.Stalled, c.Stuck.ToolFailures)
	fmt.Printf("[events]\nlog = %v\n\n[on]\n", c.Events.Log)
	for _, k := range events.Kinds {
		if cmd := c.OnCommand(k); cmd != "" {
			fmt.Printf("%s = %q\n", k, cmd)
		}
	}
	if prices := config.Prices(); len(prices) > 0 {
		fmt.Printf("\n[prices]\n")
		for k, v := range prices {
			fmt.Printf("%s = %q\n", k, v)
		}
	}
	fmt.Printf("\n[restore]\nask = %v\n", c.Restore.Ask)
}
