package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// desktopCmd is `den desktop stop`: stop every desktop mission at once.
// Den does it when it runs (it also closes the mission's windows); with no
// Den, this command stops the processes Den recorded itself, each checked
// by pid, start and executable before a pidfd signal. Meant for a
// Hyprland bind, so it works with Den's window hidden or Den gone.
func desktopCmd(args []string) {
	if len(args) != 1 || args[0] != "stop" {
		fail("usage: den desktop stop")
	}
	d, err := bridge.Call(bridge.SocketPath(), bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.DesktopStopEvent}, 5*time.Second)
	if err == nil {
		fmt.Println(sanitize.Text(d.Message))
		if d.Behavior == "deny" {
			os.Exit(1) // a stop that could not signal: never reads as done
		}
		return
	}
	out := stopWithoutDen(desktopStateDir())
	fmt.Println(out)
	if strings.Contains(out, "could not stop") {
		os.Exit(1)
	}
	os.Remove(desktop.PointerPath())
}

// desktopStateDir is where Den keeps its desktop missions.
func desktopStateDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den", "desktop")
}

// stopWithoutDen stops the missions whose state file says they are live.
func stopWithoutDen(dir string) string {
	files, _ := filepath.Glob(filepath.Join(dir, "*", "mission.json"))
	out := ""
	for _, f := range files {
		raw, err := os.ReadFile(f)
		if err != nil {
			continue
		}
		var st struct {
			Mission string          `json:"mission"`
			Targets desktop.Targets `json:"targets"`
			Ended   bool            `json:"ended"`
		}
		if json.Unmarshal(raw, &st) != nil || st.Ended {
			continue
		}
		r := desktop.Stop(st.Targets, desktop.BeaconPath())
		out += "mission " + sanitize.Label(st.Mission, 40) + ": " + r.Line() + "\n"
	}
	if out == "" {
		r := desktop.Stop(desktop.Targets{}, desktop.BeaconPath())
		return "Den is not running and no desktop mission is recorded live; " + r.Line()
	}
	if strings.Contains(out, "could not stop") {
		return "Den is not running; from its records, NOT everything could be stopped:\n" + out
	}
	return "Den is not running; stopped from its records (its windows stay open):\n" + out
}
