package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/GlacierHubAB/den/internal/remote"
)

const remoteUsage = `usage: den remote add <target> [--label L] [--policy observe|approve|full] [--den PATH] [--skip-checks]
       den remote list [-json] | status <label> | install <label> [-y]
       den remote remove <label> | disable <label> | enable <label>
       den remote serve --proto 1      run by Den over ssh on the host; not for humans`

// remoteCmd is `den remote`: the hosts in remotes.toml, the read-only
// checks, installing den on a host, and serve, the host side of a session.
func remoteCmd(args []string) {
	if len(args) == 0 {
		fail(remoteUsage)
	}
	switch args[0] {
	case "add":
		remoteAdd(args[1:])
	case "list":
		fs := flag.NewFlagSet("remote list", flag.ExitOnError)
		asJSON := fs.Bool("json", false, "print JSON")
		fs.Parse(args[1:])
		remoteList(*asJSON)
	case "remove", "disable", "enable":
		if len(args) != 2 {
			fail("usage: den remote %s <label>", args[0])
		}
		remoteEdit(args[0], args[1])
	case "status":
		if len(args) != 2 {
			fail("usage: den remote status <label>")
		}
		remoteStatus(args[1])
	case "install":
		remoteInstall(args[1:])
	case "serve":
		remoteServe(args[1:])
	case "-h", "--help", "help":
		fmt.Println(remoteUsage)
	default:
		fail("den: unknown remote command %q\n%s", args[0], remoteUsage)
	}
}

func loadRemotes() remote.Config {
	cfg, err := remote.Load()
	if err != nil {
		fail("den: %v", err)
	}
	return cfg
}

// firstArg splits a leading positional argument from flags, so both
// `add host --label x` and `add --label x host` work.
func firstArg(fs *flag.FlagSet, args []string) string {
	pos := ""
	if len(args) > 0 && args[0] != "" && args[0][0] != '-' {
		pos, args = args[0], args[1:]
	}
	fs.Parse(args)
	if pos == "" && fs.NArg() > 0 {
		pos = fs.Arg(0)
	}
	return pos
}

func remoteAdd(args []string) {
	fs := flag.NewFlagSet("remote add", flag.ExitOnError)
	label := fs.String("label", "", "the host's name in Den (default: from the target)")
	policy := fs.String("policy", "", "observe, approve or full (default: default_policy, else observe)")
	denPath := fs.String("den", "", "den's absolute path on the host (default: found through a login shell)")
	skip := fs.Bool("skip-checks", false, "add the host without connecting to it")
	target := firstArg(fs, args)
	if target == "" {
		fail(remoteUsage)
	}
	if err := remote.CheckTarget(target); err != nil {
		fail("den: %v", err)
	}
	cfg := loadRemotes()
	h := remote.Host{Label: *label, Target: target, Policy: remote.Policy(*policy), DenPath: *denPath, Labels: true, Enabled: true}
	if h.Label == "" {
		h.Label = remote.LabelFor(target)
	}
	if h.Policy == "" {
		h.Policy = cfg.Default()
	}
	probe := cfg // validate before connecting
	if err := probe.Add(h); err != nil {
		fail("den: %v", err)
	}
	if !*skip {
		fmt.Printf("Checking %s (read-only)…\n", target)
		p, err := remote.DefaultSSH().Probe(context.Background(), h)
		if err != nil {
			reportSSHFailure(h, err)
			fail("den: %s was not added. Fix that, or add it anyway with --skip-checks.", h.Label)
		}
		printProbe(h, p)
	}
	if err := cfg.Add(h); err != nil {
		fail("den: %v", err)
	}
	if err := cfg.Save(remote.Path()); err != nil {
		fail("den: %v", err)
	}
	fmt.Printf("Added %s (%s, policy %s) to %s. Den connects to it while it runs.\n", h.Label, h.Target, h.Policy, remote.Path())
	if h.Policy != remote.Observe {
		fmt.Printf("This Den applies observe to every host for now; %s takes effect when a later version supports it.\n", h.Policy)
	}
}

func reportSSHFailure(h remote.Host, err error) {
	state, reason := remote.Classify(err)
	fmt.Fprintf(os.Stderr, "%s: %s\n", h.Label, reason)
	var f *remote.Failure
	if errors.As(err, &f) && f.Stderr != "" {
		fmt.Fprintf(os.Stderr, "  ssh said: %s\n", strings.ReplaceAll(f.Stderr, "\n", "\n  "))
	}
	if state == remote.NeedsAttention {
		fmt.Fprintf(os.Stderr, "  Den never answers ssh prompts. %s.\n", remote.AttentionHint(h.Target))
	}
}

func printProbe(h remote.Host, p remote.Probe) {
	none := func(s string) string {
		if s == "" {
			return "not found"
		}
		return s
	}
	fmt.Printf("  system       %s\n", none(p.Uname))
	fmt.Printf("  den          %s %s\n", none(p.DenPath), p.DenVersion)
	fmt.Printf("  claude       %s %s\n", none(p.ClaudePath), p.ClaudeVersion)
	fmt.Printf("  den hooks    %v\n", map[bool]string{true: "installed", false: "not installed"}[p.Hooks])
	for _, a := range p.Advice(h, version) {
		fmt.Println("  → " + a)
	}
}

// remoteStatus runs the checks of add again, then a serve handshake.
func remoteStatus(label string) {
	cfg := loadRemotes()
	h, ok := cfg.Find(label)
	if !ok {
		fail("den: no host labelled %q; see den remote list", label)
	}
	fmt.Printf("%s: %s, policy %s, labels %v, %s\n", h.Label, h.Target, h.Policy, h.Labels, map[bool]string{true: "enabled", false: "disabled"}[h.Enabled])
	if k, ok := remote.LoadKnown(remote.StateDir(), label); ok {
		fmt.Printf("  last known   %s", k.State)
		if k.Reason != "" {
			fmt.Printf(" (%s)", k.Reason)
		}
		fmt.Printf(", %d agents, since %s\n", len(k.Agents), k.Since.Local().Format("2006-01-02 15:04"))
	}
	s := remote.DefaultSSH()
	p, err := s.Probe(context.Background(), h)
	if err != nil {
		reportSSHFailure(h, err)
		os.Exit(1)
	}
	printProbe(h, p)
	if p.DenPath == "" {
		os.Exit(1)
	}
	hello, err := s.Handshake(context.Background(), h)
	if err != nil {
		state, reason := remote.Classify(err)
		fmt.Printf("  serve        %s: %s\n", state, reason)
		os.Exit(1)
	}
	offset := time.Until(hello.RemoteNow).Round(time.Second)
	fmt.Printf("  serve        proto %d, den %s on %s, policy %s, clock %+v\n", hello.Proto, hello.Den, hello.Hostname, hello.Policy, offset)
}

func remoteEdit(op, label string) {
	cfg := loadRemotes()
	var err error
	switch op {
	case "remove":
		err = cfg.Remove(label)
	case "disable":
		err = cfg.SetEnabled(label, false)
	case "enable":
		err = cfg.SetEnabled(label, true)
	}
	if err != nil {
		fail("den: %v", err)
	}
	if err := cfg.Save(remote.Path()); err != nil {
		fail("den: %v", err)
	}
	if op == "remove" {
		os.Remove(filepath.Join(remote.StateDir(), label+".json"))
	}
	fmt.Printf("%sd %s. A running Den picks this up when it restarts.\n", strings.ToUpper(op[:1])+op[1:], label)
}

func remoteList(asJSON bool) {
	cfg := loadRemotes()
	type row struct {
		Label    string        `json:"label"`
		Target   string        `json:"target"`
		Policy   remote.Policy `json:"policy"`
		Labels   bool          `json:"labels"`
		DenPath  string        `json:"den_path,omitempty"`
		Enabled  bool          `json:"enabled"`
		State    string        `json:"state,omitempty"`
		Reason   string        `json:"reason,omitempty"`
		Since    time.Time     `json:"since,omitzero"`
		Attached bool          `json:"attached"`
		Agents   int           `json:"agents"`
		LastSeen time.Time     `json:"last_seen,omitzero"`
	}
	rows := []row{}
	now := time.Now()
	for _, h := range cfg.Hosts {
		r := row{Label: h.Label, Target: h.Target, Policy: h.Policy, Labels: h.Labels, DenPath: h.DenPath, Enabled: h.Enabled}
		if k, ok := remote.LoadKnown(remote.StateDir(), h.Label); ok {
			r.State, r.Reason, r.Since, r.Agents, r.LastSeen = string(k.State), k.Reason, k.Since, len(k.Agents), k.Received
			r.Attached = k.Attached(now)
			if k.State == remote.Live && !r.Attached {
				r.State = "detached"
			}
		}
		rows = append(rows, r)
	}
	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		enc.Encode(rows)
		return
	}
	if len(rows) == 0 {
		fmt.Printf("No remote hosts. Add one with: den remote add <ssh target>\n(%s)\n", remote.Path())
		return
	}
	tw := tabwriter.NewWriter(os.Stdout, 0, 2, 2, ' ', 0)
	fmt.Fprintln(tw, "LABEL\tTARGET\tPOLICY\tSTATE\tAGENTS\tLAST SEEN")
	for _, r := range rows {
		state := r.State
		switch {
		case !r.Enabled:
			state = "disabled"
		case state == "":
			state = "never connected"
		case r.Reason != "":
			state += " (" + r.Reason + ")"
		}
		seen := ""
		if !r.LastSeen.IsZero() {
			seen = since(time.Since(r.LastSeen)) + " ago"
		}
		policy := string(r.Policy)
		if !r.Labels {
			policy += ", no labels"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%d\t%s\n", r.Label, r.Target, policy, state, r.Agents, seen)
	}
	tw.Flush()
}

func remoteInstall(args []string) {
	fs := flag.NewFlagSet("remote install", flag.ExitOnError)
	yes := fs.Bool("y", false, "do not ask before installing")
	label := firstArg(fs, args)
	if label == "" {
		fail("usage: den remote install <label> [-y]")
	}
	h, ok := loadRemotes().Find(label)
	if !ok {
		fail("den: no host labelled %q; see den remote list", label)
	}
	s := remote.DefaultSSH()
	plan, err := remote.PlanInstall(context.Background(), s, h, version)
	if err != nil {
		var f *remote.Failure
		if errors.As(err, &f) {
			reportSSHFailure(h, err)
			os.Exit(1)
		}
		fail("den: %v", err)
	}
	fmt.Println(plan.Text)
	if !plan.Copy {
		return
	}
	if !*yes {
		fmt.Print("\nProceed? [y/N] ")
		answer, _ := bufio.NewReader(os.Stdin).ReadString('\n')
		if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
			fmt.Println("Nothing was changed.")
			return
		}
	}
	got, err := s.Install(context.Background(), plan)
	if err != nil {
		fail("den: %v", err)
	}
	fmt.Printf("Installed: %s\n", got)
}

// remoteServe is `den remote serve --proto 1`, started by a Den elsewhere
// over ssh. It lives exactly as long as that session.
func remoteServe(args []string) {
	fs := flag.NewFlagSet("remote serve", flag.ExitOnError)
	proto := fs.Int("proto", 0, "protocol version")
	fs.Parse(args)
	if *proto != remote.Proto {
		fail("den: unsupported remote proto %d (this den speaks %d)", *proto, remote.Proto)
	}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGHUP, os.Interrupt)
	defer stop()
	if err := remote.Serve(ctx, os.Stdin, os.Stdout, remote.ServeOptions{Version: version}); err != nil {
		fmt.Fprintf(os.Stderr, "den remote serve: %v\n", err)
		os.Exit(1)
	}
}
