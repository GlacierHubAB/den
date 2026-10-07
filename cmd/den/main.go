// Command den is a TUI that tracks and manages the coding agents running in
// your terminals. See docs/PLAN.md.
package main

import (
	"fmt"
	"os"

	tea "charm.land/bubbletea/v2"

	"github.com/GlacierHubAB/den/internal/ui"
)

func main() {
	if _, err := tea.NewProgram(ui.New()).Run(); err != nil {
		fmt.Fprintln(os.Stderr, "den:", err)
		os.Exit(1)
	}
}
