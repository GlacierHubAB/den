package hypr

import (
	"errors"
	"os/exec"
	"regexp"
	"strings"
)

// NoFocusRule is the one window rule Den keeps: while a desktop agent's
// terminal opens, a window of its marker class does not take the
// keyboard from the window the human is typing in.
const NoFocusRule = "den-nofocus"

// markerClass is what a class must look like to go into the rule: Den's
// own markers, letters, digits, dots and dashes only.
var markerClass = regexp.MustCompile(`^[a-z0-9][a-z0-9.-]{0,79}$`)

// noFocusRun runs hyprctl with the arguments; tests replace it.
var noFocusRun = func(args ...string) ([]byte, error) {
	return exec.Command("hyprctl", args...).CombinedOutput()
}

// NoInitialFocus declares the rule for windows of class (Hyprland 0.56,
// through `hyprctl eval`), or, with class "", disables it. Declaring it
// again under the same name replaces it, so there is never more than one.
func NoInitialFocus(class string) error {
	lua := `hl.window_rule({ name = "` + NoFocusRule + `", enabled = false, match = { class = "^den-nofocus-none$" }, no_initial_focus = true })`
	if class != "" {
		if !markerClass.MatchString(class) {
			return errors.New("not a marker class: " + class)
		}
		re := "^" + strings.ReplaceAll(class, ".", `\\.`) + "$"
		lua = `hl.window_rule({ name = "` + NoFocusRule + `", match = { class = "` + re + `" }, no_initial_focus = true })`
	}
	out, err := noFocusRun("eval", lua)
	if msg := strings.TrimSpace(string(out)); err != nil || msg != "ok" {
		return errors.New("hyprctl eval: " + firstLine(msg))
	}
	return nil
}
