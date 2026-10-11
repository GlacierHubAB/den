// Package notify shows desktop notifications through notify-send and reports
// whether one was clicked. A notification only ever carries an agent's name
// and the kind of thing that happened, never a command, a path, a prompt or
// file text.
//
// Clicking uses the freedesktop "default" action. omarchy-shell, mako and
// swaync invoke it on a left click; dunst does on a middle click. A server
// without actions, a dismissed or expired notification, and a missing
// notify-send all read as "not clicked", never as an error.
package notify

import (
	"bufio"
	"context"
	"os/exec"
	"strings"
	"time"
)

// Command is the notify-send binary; tests point it at a stand-in.
var Command = "notify-send"

// App is the application name the notification server sees.
const App = "Den"

// MaxWait bounds how long Send waits for a click. Servers expire their
// notifications long before this; it only stops a stuck notify-send from
// lingering.
const MaxWait = 30 * time.Minute

// Note is one notification.
type Note struct {
	Summary string // "Mossy Lemur needs your permission"
	Body    string // optional second line, same rules as Summary
	Urgent  bool   // critical urgency: stays until dismissed on most servers
}

// Available reports whether notify-send is on the PATH.
func Available() bool {
	_, err := exec.LookPath(Command)
	return err == nil
}

// Send shows the note with a "default" action and blocks until it is
// clicked, closed or expires. It reports whether it was clicked.
func Send(n Note) bool {
	ctx, cancel := context.WithTimeout(context.Background(), MaxWait)
	defer cancel()
	cmd := exec.CommandContext(ctx, Command, args(n, true)...)
	out, err := cmd.StdoutPipe()
	if err != nil {
		return false
	}
	if cmd.Start() != nil {
		return false
	}
	clicked := false
	sc := bufio.NewScanner(out)
	for sc.Scan() {
		// notify-send prints the invoked action's key; the notification id
		// would come only with --print-id, which Den does not pass.
		if strings.TrimSpace(sc.Text()) == "default" {
			clicked = true
		}
	}
	cmd.Wait()
	return clicked
}

// Post shows the note without an action and returns at once. For callers
// that cannot wait, such as den hook.
func Post(n Note) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	exec.CommandContext(ctx, Command, args(n, false)...).Run()
}

func args(n Note, action bool) []string {
	urgency := "normal"
	if n.Urgent {
		urgency = "critical"
	}
	a := []string{"--app-name=" + App, "--urgency=" + urgency}
	if action {
		a = append(a, "--action=default=Open", "--wait")
	}
	// "--" so a summary can never be read as an option.
	a = append(a, "--", n.Summary)
	if n.Body != "" {
		a = append(a, n.Body)
	}
	return a
}
