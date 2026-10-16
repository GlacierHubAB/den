package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// installHypruseCmd is `den install-hypruse --from <clone> [--wheels dir]`:
// ask the running Den to install hypruse into its own venv. Den shows the
// plan as a card and installs only on ^Y; this command waits for the
// outcome. With no Den running it prints the plan and changes nothing.
func installHypruseCmd(args []string) {
	fs := flag.NewFlagSet("install-hypruse", flag.ExitOnError)
	from := fs.String("from", "", "a clone of hypruse at the pinned commit to copy its files from")
	wheels := fs.String("wheels", "", "a folder of wheels to install the dependencies from, offline")
	fs.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: den install-hypruse --from <hypruse clone> [--wheels <dir>]\n\nInstalls hypruse %s (%s, commit %s) and its locked dependencies into Den's own venv, after you approve the plan in Den.\n", desktop.Pinned.Version, desktop.Pinned.URL, desktop.Pinned.Commit[:7])
	}
	fs.Parse(args)
	if fs.NArg() > 0 {
		fs.Usage()
		os.Exit(2)
	}
	abs := func(p string) string {
		if p == "" {
			return ""
		}
		if a, err := filepath.Abs(p); err == nil {
			return a
		}
		return p
	}
	cwd, _ := os.Getwd()
	if *from == "" {
		// No --from: the folder this is run in, when it is a clone of the
		// pinned hypruse commit (checked file by file, as Den checks it).
		if cwd != "" && desktop.VerifyClone(desktop.Pinned, cwd) == nil {
			*from = cwd
			fmt.Printf("Using the hypruse clone in %s.\n", cwd)
		} else {
			fail("den install-hypruse: name the hypruse clone with --from, or run this inside one. To get it:\n  git clone %s hypruse && cd hypruse && git checkout %s\n  den install-hypruse --from .", desktop.Pinned.URL, desktop.Pinned.Commit)
		}
	}
	payload, _ := json.Marshal(bridge.DesktopInstallInput{Clone: abs(*from), Wheels: abs(*wheels), AskedBy: fmt.Sprintf("den install-hypruse, pid %d in %s", os.Getpid(), cwd)})
	conn, err := bridge.Connect(bridge.SocketPath())
	if errors.Is(err, bridge.ErrNoDen) {
		plan, perr := desktop.MakePlan(desktop.DefaultPaths(), abs(*from), abs(*wheels))
		if perr != nil {
			fail("den install-hypruse: %v", perr)
		}
		fmt.Println(plan.Text())
		fmt.Println("\nDen is not running. Start Den to approve this in its inbox; nothing was changed.")
		return
	}
	if err != nil {
		fail("den install-hypruse: %v", err)
	}
	fmt.Println("Asking Den to install hypruse: approve with ^Y in its inbox (Ctrl+C stops waiting).")
	env := bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.DesktopInstallEvent, Payload: payload}
	d, err := bridge.CallOn(conn, env, 30*time.Minute)
	switch {
	case err != nil:
		fail("den install-hypruse: no answer from Den (%v); nothing is known to have changed", err)
	case d.Behavior == "allow":
		fmt.Println(sanitize.Text(d.Message))
	default:
		fail("den install-hypruse: %s", sanitize.Text(d.Message))
	}
}
