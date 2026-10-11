package events

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"time"
)

// OnTimeout bounds an [on] command. It is killed after that.
var OnTimeout = 30 * time.Second

// RunOn runs a user's [on] command for an event through sh -c. The command
// gets the event as one JSON line on stdin and as DEN_* environment
// variables, never anything else; its output is discarded, so it cannot
// answer anything. A failure is logged to hooks.log. Blocks until the
// command ends: call it from a goroutine.
func RunOn(command string, ev Event) error {
	ctx, cancel := context.WithTimeout(context.Background(), OnTimeout)
	defer cancel()
	raw, _ := json.Marshal(ev)
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Stdin = bytes.NewReader(append(raw, '\n'))
	cmd.Env = append(os.Environ(), Env(ev)...)
	err := cmd.Run()
	if ctx.Err() != nil {
		err = fmt.Errorf("timed out after %s", OnTimeout)
	}
	if err != nil {
		logOn(ev, err)
	}
	return err
}

// Env is the event as environment variables for an [on] command.
func Env(ev Event) []string {
	env := []string{
		"DEN_EVENT=" + ev.Kind,
		"DEN_EVENT_VERSION=" + strconv.Itoa(ev.V),
		"DEN_SESSION=" + ev.Session,
		"DEN_AGENT=" + ev.Agent,
		"DEN_TIME=" + ev.Time.Format(time.RFC3339),
	}
	for k, v := range map[string]string{"DEN_REQUEST": ev.Request, "DEN_TOOL": ev.Tool, "DEN_ERROR": ev.Error, "DEN_REASON": ev.Reason} {
		if v != "" {
			env = append(env, k+"="+v)
		}
	}
	if ev.Minutes > 0 {
		env = append(env, "DEN_MINUTES="+strconv.Itoa(ev.Minutes))
	}
	return env
}

// logOn appends a failed [on] command to hooks.log beside the hook runs.
func logOn(ev Event, err error) {
	dir := filepath.Dir(Path())
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	f, ferr := os.OpenFile(filepath.Join(dir, "hooks.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if ferr != nil {
		return
	}
	defer f.Close()
	sid := ev.Session
	if len(sid) > 8 {
		sid = sid[:8]
	}
	fmt.Fprintf(f, "%s %-18s %s [on] command failed: %v\n", time.Now().Format("2006-01-02 15:04:05"), "on."+ev.Kind, sid, err)
}
