package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Seams for tests: whether Den's venv already holds the pinned hypruse,
// and whether a folder is a clone of the pinned commit (file by file, as
// Den checks it again before it installs).
var (
	hypruseInstalled = func() error { return desktop.Verify(desktop.DefaultPaths()) }
	hypruseClone     = func(dir string) bool { return desktop.VerifyClone(desktop.Pinned, dir) == nil }
)

// installSource is the clone to install from, and what to tell the human
// about it. "" with a note: hypruse is installed and verified already, so
// there is nothing to do. With no --from it takes the folder it runs in,
// else a verified clone it finds under home; a URL is refused with how to
// make a clone.
func installSource(from, cwd, home string) (clone, note string, err error) {
	if hypruseInstalled() == nil {
		return "", "hypruse " + desktop.Pinned.Version + " is already installed.", nil
	}
	get := fmt.Sprintf("git clone %s hypruse && cd hypruse && git checkout %s", desktop.Pinned.URL, desktop.Pinned.Commit)
	if isURL(from) {
		return "", "", fmt.Errorf("--from takes a local clone, not a URL:\n  %s\nthen den install-hypruse --from <that folder>", get)
	}
	if from != "" {
		if a, err := filepath.Abs(from); err == nil {
			from = a
		}
		return from, "", nil
	}
	if cwd != "" && hypruseClone(cwd) {
		return cwd, "Using the hypruse clone in " + cwd + ".", nil
	}
	if dir := findClone(home, hypruseClone); dir != "" {
		return dir, "Using the hypruse clone in " + dir + ".", nil
	}
	return "", "", fmt.Errorf("no hypruse clone found: name it with --from, or run this inside one. To get it:\n  %s\n  den install-hypruse --from .", get)
}

// isURL reports whether --from was given a remote address rather than a
// folder.
func isURL(s string) bool {
	s = strings.ToLower(strings.TrimSpace(s))
	for _, p := range []string{"https://", "http://", "ssh://", "git://", "file://", "git@", "github.com/", "www.github.com/"} {
		if strings.HasPrefix(s, p) {
			return true
		}
	}
	return false
}

// cloneDepth and cloneBudget bound the search for a clone under home: a
// folder named hypruse at most this deep, after looking at this many
// folders at most. Hidden folders are skipped, and nothing found is used
// unless it is a clone of the pinned commit.
const (
	cloneDepth  = 4
	cloneBudget = 20000
)

// findClone is the first folder named hypruse under home (in name order,
// at most cloneDepth deep) that ok accepts, "" when there is none.
func findClone(home string, ok func(string) bool) string {
	if home == "" {
		return ""
	}
	seen, found := 0, ""
	filepath.WalkDir(home, func(path string, d fs.DirEntry, err error) error {
		if err != nil || !d.IsDir() {
			return nil
		}
		if path == home {
			return nil
		}
		seen++
		if seen > cloneBudget {
			return filepath.SkipAll
		}
		name := d.Name()
		if strings.HasPrefix(name, ".") || name == "node_modules" {
			return filepath.SkipDir
		}
		if name == "hypruse" && ok(path) {
			found = path
			return filepath.SkipAll
		}
		rel, _ := filepath.Rel(home, path)
		if strings.Count(rel, string(filepath.Separator))+1 >= cloneDepth {
			return filepath.SkipDir
		}
		return nil
	})
	return found
}

// installHypruseCmd is `den install-hypruse --from <clone> [--wheels dir]`:
// ask the running Den to install hypruse into its own venv. Den shows the
// plan as a card and installs only on ^Y; this command waits for the
// outcome. With no Den running it prints the plan and changes nothing.
func installHypruseCmd(args []string) {
	flags := flag.NewFlagSet("install-hypruse", flag.ExitOnError)
	from := flags.String("from", "", "a local clone of hypruse at the pinned commit to copy its files from")
	wheels := flags.String("wheels", "", "a folder of wheels to install the dependencies from, offline")
	flags.Usage = func() {
		fmt.Fprintf(os.Stderr, "usage: den install-hypruse [--from <local hypruse clone>] [--wheels <dir>]\n\nInstalls hypruse %s (%s, commit %s) and its locked dependencies into Den's own venv, after you approve the plan in Den.\n", desktop.Pinned.Version, desktop.Pinned.URL, desktop.Pinned.Commit[:7])
	}
	flags.Parse(args)
	if flags.NArg() > 0 {
		flags.Usage()
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
	home, _ := os.UserHomeDir()
	clone, note, err := installSource(*from, cwd, home)
	if err != nil {
		fail("den install-hypruse: %v", err)
	}
	if clone == "" {
		fmt.Println(note) // already installed
		return
	}
	if note != "" {
		fmt.Println(note)
	}
	*from = clone
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
