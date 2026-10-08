package main

import (
	"fmt"
	"os"

	"github.com/GlacierHubAB/den/internal/mods"
)

// listMods is `den mods`: what ships in this binary and where it is loaded from.
func listMods() {
	list, err := mods.List()
	if err != nil {
		fail("den: %v", err)
	}
	for _, m := range list {
		state := "not installed"
		if v := mods.InstalledVersion(m); v != "" {
			state = "installed " + v + " at " + m.Installs
			if v != m.Version {
				state += " (bundled " + m.Version + ", run den install-mods)"
			}
		}
		if m.Loaded != "" {
			state += "; also loaded from " + m.Loaded + " via CLAUDE_CODE_PLUGIN_DIRS"
		}
		fmt.Printf("%s %s  %s\n", m.Name, m.Version, state)
	}
}

// installMods is `den install-mods`: copies every bundled mod into
// ~/.claude/skills, which Claude Code loads without any further setup.
func installMods() {
	list, err := mods.List()
	if err != nil {
		fail("den: %v", err)
	}
	force := len(os.Args) > 2 && os.Args[2] == "--force"
	installed := 0
	for _, m := range list {
		if m.Loaded != "" && !force {
			fmt.Printf("%s: already loaded from %s (CLAUDE_CODE_PLUGIN_DIRS); not installing a second copy. Use --force to install anyway.\n", m.Name, m.Loaded)
			continue
		}
		if err := mods.Install(m); err != nil {
			fail("den: %s: %v", m.Name, err)
		}
		fmt.Printf("Installed %s %s into %s\n", m.Name, m.Version, m.Installs)
		installed++
	}
	if installed > 0 {
		fmt.Println("Claude Code sessions started from now on load it. Undo with: den uninstall-mods")
	}
}

func uninstallMods() {
	list, err := mods.List()
	if err != nil {
		fail("den: %v", err)
	}
	for _, m := range list {
		if err := mods.Uninstall(m); err != nil {
			fmt.Println(err)
			continue
		}
		fmt.Printf("Removed %s from %s\n", m.Name, m.Installs)
	}
}
