package desktop

import (
	"context"
	"encoding/json"
	"fmt"
	"time"
)

// ScopedCommit is the one Hyprland build a task may be held to its own
// windows on: 0.56.2, the build the security-context probe ran against.
// Any other build (or a dirty one) gets no narrowed task; the start card
// offers the whole desktop with its warning, or no task.
const ScopedCommit = "efb50993780079460b0cbed1363e2166a2de1d9f"

// ScopedBuild reads Hyprland's version from Den's side and says whether
// it is ScopedCommit, built clean: "" when it is, else why not. A failed or
// unparsable read is a no.
func ScopedBuild(ctx context.Context) string {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	out, err := hyprctl(ctx, "version", "-j")
	if err != nil {
		return fmt.Sprintf("Hyprland's version can't be read (%v)", err)
	}
	return scopedBuild(out)
}

func scopedBuild(out []byte) string {
	var v struct {
		Commit *string `json:"commit"`
		Dirty  *bool   `json:"dirty"`
		Tag    string  `json:"tag"`
	}
	if err := json.Unmarshal(out, &v); err != nil || v.Commit == nil || v.Dirty == nil {
		return "Hyprland's version can't be read"
	}
	if *v.Commit != ScopedCommit {
		return "this Hyprland build (" + sanitizeNS(v.Tag) + ") isn't the one checked"
	}
	if *v.Dirty {
		return "this Hyprland build has local changes"
	}
	return ""
}
