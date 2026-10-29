package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/doctor"
	"github.com/GlacierHubAB/den/internal/events"
	"github.com/GlacierHubAB/den/internal/hooks"
	"github.com/GlacierHubAB/den/internal/hypr"
	"github.com/GlacierHubAB/den/internal/notify"
	"github.com/GlacierHubAB/den/internal/orch"
	"github.com/GlacierHubAB/den/internal/question"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// runHook is `den hook`: Claude Code runs it for each subscribed event with
// the hook JSON on stdin. It always exits 0 so a missing or busy Den never
// changes what Claude Code does: even a panic ends in exit 0 with nothing
// on stdout. Go exits 2 on a panic, and Claude Code reads a hook's exit 2
// as blocking (for PermissionRequest, a denial), so a bug here would deny
// the agent's tool call instead of leaving it to the terminal.
//
// Exit paths it cannot catch: a fatal runtime error (out of memory, a
// concurrent map write), a signal, and SIGPIPE when Claude Code closed the
// pipe before the decision was written (it is no longer reading then). The
// hook starts no goroutine, so every panic is on this one.
func runHook() {
	defer hookRecover()
	hook()
}

// exitHook ends den hook after a panic; tests replace it.
var exitHook = os.Exit

// hookRecover turns a panic in the hook into a quiet exit 0: no decision,
// so Claude Code shows its own dialog.
func hookRecover() {
	if recover() != nil {
		exitHook(0)
	}
}

// hookPanic is a test seam: when set, hook calls it first.
var hookPanic func()

func hook() {
	if hookPanic != nil {
		hookPanic()
	}
	raw, err := io.ReadAll(os.Stdin)
	if err != nil || len(raw) == 0 {
		return
	}
	var in bridge.HookInput
	if json.Unmarshal(raw, &in) != nil || in.HookEventName == "" {
		return
	}
	if in.AgentID != "" {
		return // subagents are not fleet members
	}
	if os.Getenv(orch.LaneEnv) == "1" {
		// The read-only lane that answers apps: never a fleet member, never
		// in the inbox, and nothing it asks for is allowed. It runs with no
		// tools and no hooks, so this only catches a Claude Code that
		// ignored that.
		if in.HookEventName == "PermissionRequest" {
			if out := permissionOutput(in, bridge.Decision{Behavior: "deny", Message: "the lane has no tools"}, config.Load()); out != nil {
				os.Stdout.Write(out)
			}
		}
		return
	}
	// The stream for scripts. The orchestrator's own turns stay out of it.
	if ev, ok := events.FromHook(in); ok && os.Getenv("DEN_ORCHESTRATOR") != "1" && config.Load().Events.Log {
		events.Append(events.Path(), ev) // best effort
	}
	if raw = fitLine(raw, in.HookEventName); raw == nil {
		return // a request too large for den.sock: Claude Code's own dialog answers it
	}
	env := bridge.Envelope{Seq: time.Now().UnixNano(), Event: in.HookEventName, SessionID: in.SessionID, Payload: raw}
	path := bridge.SocketPath()
	if in.HookEventName != "PermissionRequest" {
		delivered := bridge.Send(path, env)
		logHook(in, delivered)
		if !delivered {
			notifyWithoutDen(in)
		}
		return
	}
	cfg := config.Load()
	wait := time.Duration(cfg.Hooks.PermissionWait) * time.Second
	// A Den running on this machine comes first; then a Den attached from
	// another machine through `den remote serve`. Neither: fail open.
	remotePath, _ := bridge.RemoteSocketPath()
	d, ok := bridge.AskFirst(path, remotePath, env, wait)
	logHook(in, ok)
	if !ok {
		return // no decision: Claude Code shows its own dialog
	}
	if out := permissionOutput(in, d, cfg); out != nil {
		os.Stdout.Write(out)
	}
}

// permissionOutput is what the hook prints for Den's decision, nil for
// nothing: Claude Code's own dialog then stays. An AskUserQuestion call
// answered in Den becomes allow plus the answers, only with
// hooks.answer_questions on and every question answered. An approved plan
// becomes allow plus the call's own input (bridge.PlanApproval).
func permissionOutput(in bridge.HookInput, d bridge.Decision, cfg config.Config) []byte {
	if d.Behavior == "" {
		return nil
	}
	if in.ToolName == question.Tool && len(d.Answers) > 0 {
		if !cfg.Hooks.AnswerQuestions || d.Behavior != "allow" {
			return nil
		}
		out, ok := question.Output(in.ToolInput, d.Answers)
		if !ok {
			return nil
		}
		return append(out, '\n')
	}
	decision := map[string]any{"behavior": d.Behavior, "message": d.Message}
	if in.ToolName == bridge.PlanTool && d.Behavior == "allow" {
		// A bare allow leaves Claude Code's plan dialog open.
		updated, ok := bridge.PlanApproval(in.ToolInput)
		if !ok {
			return nil
		}
		decision = map[string]any{"behavior": "allow", "updatedInput": updated}
	}
	out, err := json.Marshal(map[string]any{
		"hookSpecificOutput": map[string]any{
			"hookEventName": "PermissionRequest",
			"decision":      decision,
		},
	})
	if err != nil {
		return nil
	}
	return append(out, '\n')
}

// lineRoom is what an envelope leaves of bridge.MaxLine for the payload.
const lineRoom = bridge.MaxLine - 4096

// fitLine makes a hook payload fit den.sock's line limit. An event Den only
// follows loses its tool input and output, which it does not need; a
// permission request is never cut, since Den must show it whole: nil, and
// the hook leaves it to the terminal.
func fitLine(raw []byte, event string) []byte {
	if len(raw) <= lineRoom {
		return raw
	}
	if event == "PermissionRequest" {
		return nil
	}
	var m map[string]json.RawMessage
	if json.Unmarshal(raw, &m) != nil {
		return nil
	}
	delete(m, "tool_input")
	delete(m, "tool_response")
	out, err := json.Marshal(m)
	if err != nil || len(out) > lineRoom {
		return nil
	}
	return out
}

// notifyWithoutDen is the fallback for when no Den is listening: with
// notify.without_den on, Claude Code's own "waiting on you" notifications
// become a desktop notification naming the agent and the kind of request.
// Nothing to click: focusing needs Den.
func notifyWithoutDen(in bridge.HookInput) {
	if in.HookEventName != "Notification" {
		return
	}
	what := ""
	switch in.NotificationType {
	case "permission_prompt":
		what = "needs your permission"
	case "agent_needs_input", "elicitation_dialog", "elicitation_url_dialog":
		what = "has a question for you"
	default:
		return
	}
	if !config.Load().Notify.WithoutDen || hypr.ActiveHolds(os.Getpid()) {
		return
	}
	notify.Post(notify.Note{Summary: badge.Name(in.SessionID) + " " + what})
}

// logHook appends one line per hook run to the state directory so "did the
// hook fire for that session?" can be answered without a debugger. Set
// DEN_HOOK_LOG=off to disable it.
func logHook(in bridge.HookInput, delivered bool) {
	if !config.Load().Hooks.Log {
		return
	}
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		dir = filepath.Join(home, ".local", "state")
	}
	dir = filepath.Join(dir, "den")
	if os.MkdirAll(dir, 0o755) != nil {
		return
	}
	f, err := os.OpenFile(filepath.Join(dir, "hooks.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	status := "delivered"
	if !delivered {
		status = "no den listening"
	}
	sid := in.SessionID
	if len(sid) > 8 {
		sid = sid[:8]
	}
	fmt.Fprintf(f, "%s %-18s %s %s\n", time.Now().Format("2006-01-02 15:04:05"), sanitize.Line(in.HookEventName), sanitize.Line(sid), status)
}

func installHooks() {
	exe, err := hooks.Executable()
	if err != nil {
		fail("den: %v", err)
	}
	if hooks.TempBuild(exe) {
		fail("den: this binary lives in a temporary build directory (%s).\nBuild it somewhere stable first, e.g.\n  go build -o ~/.local/bin/den ./cmd/den && ~/.local/bin/den install-hooks", exe)
	}
	backup, err := hooks.Install(exe)
	if err != nil {
		fail("den: %v", err)
	}
	// Den's setup card, hidden by its ^N, asks again from the next start
	// if the hooks go missing later.
	os.Remove(doctor.Dismissed(doctor.System()))
	fmt.Printf("Installed den hooks into %s\n", hooks.SettingsPath())
	if backup != "" {
		fmt.Printf("Backup: %s\n", backup)
	}
	fmt.Println("Hooks apply to Claude Code sessions started from now on. Undo with: den uninstall-hooks")
}

func uninstallHooks() {
	backup, err := hooks.Uninstall()
	if err != nil {
		fail("den: %v", err)
	}
	if backup == "" {
		fmt.Println("No den hooks were installed.")
		return
	}
	fmt.Printf("Removed den hooks from %s (backup: %s)\n", hooks.SettingsPath(), backup)
}

// runEvents is `den events`: a headless Den that prints every hook event it
// receives and releases permission requests to the terminal straight away.
// Useful to see what Claude Code sends. For protocol testing only,
// DEN_EVENTS_DECISION=allow|deny answers every permission request instead.
func runEvents() {
	srv, err := bridge.Listen(bridge.SocketPath())
	if err != nil {
		fail("den: %v", err)
	}
	defer srv.Close()
	fmt.Fprintf(os.Stderr, "listening on %s (ctrl+c to stop)\n", bridge.SocketPath())
	for r := range srv.Requests() {
		fmt.Printf("%s  %-18s %s  %s %s %s\n", time.Now().Format("15:04:05"), sanitize.Line(r.Event), sanitize.Line(short(r.SessionID)), sanitize.Line(r.Input.ToolName), sanitize.Line(r.Input.NotificationType), firstN(sanitize.Text(r.Input.LastAssistant+r.Input.Message+r.Input.Prompt), 80))
		if r.WantsReply() {
			d := bridge.Decision{}
			if v := os.Getenv("DEN_EVENTS_DECISION"); v == "allow" || v == "deny" {
				d = bridge.Decision{Behavior: v, Message: "den events test decision"}
				fmt.Printf("          -> replied %s for %s %s\n", v, sanitize.Line(r.Input.ToolName), firstN(sanitize.Text(string(r.Input.ToolInput)), 100))
			}
			r.Reply(d)
		}
	}
}

// followEvents is `den events -json`: the event stream from now on, one
// JSON object per line, as den hook and Den append it. It does not listen
// on the socket, so it runs beside Den.
func followEvents() {
	stop := make(chan struct{}) // until killed
	events.Open(events.Path()).Run(stop, func(raw []byte, _ events.Event) {
		os.Stdout.Write(append(sanitize.JSON(raw), '\n'))
	})
}

func short(s string) string {
	if len(s) > 8 {
		return s[:8]
	}
	return s
}

func firstN(s string, n int) string {
	r := []rune(s)
	for i, c := range r {
		if c == '\n' {
			r = r[:i]
			break
		}
	}
	if len(r) > n {
		r = append(r[:n-1], '…')
	}
	return string(r)
}
