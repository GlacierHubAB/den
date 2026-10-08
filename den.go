// Package den is the module root. It carries the Claude Code mods that ship
// with the binary so `den install-mods` needs nothing but itself.
package den

import "embed"

// Mods holds every bundled plugin under mods/<name>/, taken from the git
// submodules there at build time.
//
//go:embed all:mods/agent-badge/.claude-plugin
//go:embed mods/agent-badge/hooks mods/agent-badge/types
//go:embed mods/agent-badge/README.md mods/agent-badge/PRIVACY.md mods/agent-badge/LICENSE mods/agent-badge/tsconfig.json
var Mods embed.FS
