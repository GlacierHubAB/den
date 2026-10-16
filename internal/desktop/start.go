package desktop

import (
	"strconv"
	"strings"
)

// StartInfo is Den's answer to a proxy's start: how to run hypruse. Den
// checked the venv before it answered.
type StartInfo struct {
	Argv []string `json:"argv"`
	Env  []string `json:"env"` // HYPRUSE_* and ServerEnv; the proxy adds the session's display variables
	// Log is where the proxy writes hypruse's stderr and its own errors:
	// the mission's private folder, since Claude Code keeps neither.
	Log string `json:"log,omitempty"`
}

// childPassthrough are the variables of the agent's environment hypruse
// needs to reach the desktop and the accessibility bus; nothing else of it
// is passed on.
var childPassthrough = []string{"PATH", "HOME", "LANG", "LC_ALL", "LC_CTYPE", "XDG_RUNTIME_DIR", "WAYLAND_DISPLAY", "HYPRLAND_INSTANCE_SIGNATURE", "DBUS_SESSION_BUS_ADDRESS", "XDG_SESSION_TYPE", "XDG_CURRENT_DESKTOP"}

// ChildEnv is hypruse's environment: the passthrough variables from env,
// then Den's, which win.
func ChildEnv(env []string, den []string) []string {
	var out []string
	for _, kv := range env {
		k, _, ok := strings.Cut(kv, "=")
		if !ok {
			continue
		}
		for _, p := range childPassthrough {
			if k == p {
				out = append(out, kv)
			}
		}
	}
	return append(out, den...)
}

// HypruseEnv is what Den sets for hypruse in a mission: confinement to the
// mission workspace, strict mode (it stops when the human takes the seat),
// the journal, screenshots inline (the agent has no file tools), no
// bytecode. A task mission (workspace 0) has the whole desktop, so no
// confinement: Den's gate keeps its own surfaces off limits instead. The
// auth guard stays at its default (on), and the clipboard tool is never
// enabled.
func HypruseEnv(workspace int, journal string) []string {
	var env []string
	if workspace > 0 {
		env = append(env, "HYPRUSE_CONFINE=workspace:"+strconv.Itoa(workspace))
	}
	return append(append(env,
		"HYPRUSE_STRICT=1",
		"HYPRUSE_JOURNAL="+journal,
		"HYPRUSE_SCREENSHOT_MODE=image",
	), ServerEnv()...)
}
