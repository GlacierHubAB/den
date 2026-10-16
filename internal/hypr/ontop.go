package hypr

import (
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

// dispatchRun runs `hyprctl dispatch` with the arguments; tests replace it.
var dispatchRun = func(args ...string) ([]byte, error) {
	return exec.Command("hyprctl", append([]string{"dispatch"}, args...)...).CombinedOutput()
}

// dispatch runs a Lua dispatcher (Hyprland 0.56+) and, when the compositor
// does not take it, the legacy one, like Focus.
func dispatch(lua string, legacy ...string) error {
	out, err := dispatchRun(lua)
	msg := strings.TrimSpace(string(out))
	if err == nil && (msg == "ok" || msg == "") {
		return nil
	}
	if strings.Contains(msg, "window not found") {
		return errors.New("hyprland: window not found (it may have closed)")
	}
	out, err = dispatchRun(legacy...)
	msg = strings.TrimSpace(string(out))
	if err != nil || (msg != "ok" && msg != "") {
		return fmt.Errorf("hyprctl %s: %s", legacy[0], firstLine(msg))
	}
	return nil
}

// KeepOnTop makes a window float, pins it (shown on every workspace) and
// raises it above other floating windows. Float and pin are toggles in
// Hyprland, so the caller says which the window lacks now, as read from
// the compositor; the raise is always sent.
func KeepOnTop(address string, floating, pinned bool) error {
	if err := checkAddress(address); err != nil {
		return err
	}
	win := `window = "address:` + address + `"`
	if !floating {
		if err := dispatch(`hl.dsp.window.float({ action = "toggle", `+win+` })`, "togglefloating", "address:"+address); err != nil {
			return err
		}
	}
	if !pinned {
		if err := dispatch(`hl.dsp.window.pin({ action = "toggle", `+win+` })`, "pin", "address:"+address); err != nil {
			return err
		}
	}
	return dispatch(`hl.dsp.window.alter_zorder({ mode = "top", `+win+` })`, "alterzorder", "top,address:"+address)
}
