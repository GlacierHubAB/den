package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/desktop"
	"github.com/GlacierHubAB/den/internal/procs"
)

// desktopArgs reads --mission and --socket from a desktop command's argv.
// The socket comes only from there, never from the environment, so the
// agent's environment cannot point these at another listener.
func desktopArgs(args []string) (mission, socket string, err error) {
	for i := 0; i+1 < len(args); i++ {
		switch args[i] {
		case "--mission":
			mission = args[i+1]
		case "--socket":
			socket = args[i+1]
		}
	}
	if socket == "" || !filepath.IsAbs(socket) {
		return mission, socket, errors.New("--socket must be the absolute path of den.sock")
	}
	return mission, socket, nil
}

// desktopCallWait bounds one call's wait for Den: a card the human leaves
// unanswered is ended by Den itself well before this.
const desktopCallWait = 15 * time.Minute

// runDesktopMCP is `den desktop-mcp --mission <id> --socket <den.sock>`:
// the only MCP server a desktop agent has. It asks Den how to start
// hypruse, starts it as its own child, and relays MCP between Claude Code
// and it, forwarding a tool call only on Den's allow (desktop.Proxy).
func runDesktopMCP(args []string) {
	mission, socket, err := desktopArgs(args)
	if err != nil || mission == "" {
		fmt.Fprintln(os.Stderr, "den desktop-mcp: needs --mission <id> --socket <absolute den.sock>")
		os.Exit(2)
	}
	ask := func(in bridge.DesktopInput, wait time.Duration) (bridge.Decision, error) {
		in.Mission = mission
		payload, _ := json.Marshal(in)
		return bridge.Call(socket, bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.DesktopEvent, Payload: payload}, wait)
	}
	d, err := ask(bridge.DesktopInput{Kind: "start"}, 30*time.Second)
	if err != nil || d.Behavior != "allow" {
		why := d.Message
		if err != nil {
			why = err.Error()
		}
		fmt.Fprintln(os.Stderr, "den desktop-mcp: Den did not start this mission's desktop: "+why)
		os.Exit(1)
	}
	var info desktop.StartInfo
	if json.Unmarshal(d.Data, &info) != nil || len(info.Argv) == 0 {
		fmt.Fprintln(os.Stderr, "den desktop-mcp: Den's start answer is unreadable")
		os.Exit(1)
	}
	// Pdeathsig fires when the thread that started the child ends, not the
	// process; Go may end idle threads. Pinned, this goroutine's thread
	// lives exactly as long as the proxy.
	runtime.LockOSThread()
	child := exec.Command(info.Argv[0], info.Argv[1:]...)
	child.Env = desktop.ChildEnv(os.Environ(), info.Env)
	child.Dir = "/"
	child.Stderr = os.Stderr
	if info.Log != "" && filepath.IsAbs(info.Log) {
		if f, err := os.OpenFile(info.Log, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
			defer f.Close()
			filter = &desktop.LogFilter{W: f}
			child.Stderr = filter
			logTo = f
			// A crash anywhere in the proxy writes its trace to fd 2:
			// into the log too, where it can be read.
			syscall.Dup2(int(f.Fd()), 2)
			defer func() {
				if r := recover(); r != nil {
					fmt.Fprintf(f, "%s den desktop-mcp: panic: %v\n", time.Now().Format(time.RFC3339), r)
					panic(r)
				}
			}()
		}
	}
	// hypruse ends with this proxy, however it ends.
	child.SysProcAttr = &syscall.SysProcAttr{Pdeathsig: syscall.SIGTERM}
	toChild, err := child.StdinPipe()
	if err != nil {
		fail("den desktop-mcp: %v", err)
	}
	fromChild, err := child.StdoutPipe()
	if err != nil {
		fail("den desktop-mcp: %v", err)
	}
	if err := child.Start(); err != nil {
		fail("den desktop-mcp: start hypruse: %v", err)
	}
	pid := child.Process.Pid
	proxyLog("started hypruse, pid %d", pid)
	if d, err := ask(bridge.DesktopInput{Kind: "child", ChildPID: pid, ChildStart: procs.StartTicks(pid)}, 10*time.Second); err != nil || d.Behavior != "allow" {
		child.Process.Signal(syscall.SIGTERM)
		child.Wait()
		fmt.Fprintln(os.Stderr, "den desktop-mcp: Den did not take hypruse's process; stopped it")
		os.Exit(1)
	}
	proxyLog("Den took hypruse's process; relaying")
	p := &desktop.Proxy{Gate: denGate{socket: socket, mission: mission}, Client: os.Stdin, ToClient: os.Stdout, Child: fromChild, ToChild: toChild,
		Cursor: cursorPos, Pointer: desktop.PointerPath(), Log: filter, Task: info.Task}
	if err := p.Run(context.Background()); err != nil {
		proxyLog("relay ended: %v", err)
	} else {
		proxyLog("relay ended: the client closed")
	}
	os.Remove(desktop.PointerPath())
	toChild.Close()
	done := make(chan struct{})
	go func() { child.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		child.Process.Signal(syscall.SIGTERM)
		<-done
	}
}

// denGate is Den, asked over den.sock for every call.
type denGate struct{ socket, mission string }

func (g denGate) Call(ctx context.Context, tool string, args json.RawMessage, token string) (desktop.Answer, error) {
	payload, err := json.Marshal(bridge.DesktopInput{Kind: "call", Mission: g.mission, Tool: tool, Args: args, Token: token})
	if err != nil {
		return desktop.Answer{}, err
	}
	if len(payload) > bridge.MaxLine-4096 {
		return desktop.Answer{Message: "the call is too large for Den to check"}, nil
	}
	conn, err := bridge.Connect(g.socket)
	if err != nil {
		return desktop.Answer{}, errors.New("Den is not running")
	}
	// A cancelled call hangs up, so Den drops its card.
	stop := context.AfterFunc(ctx, func() { conn.Close() })
	defer stop()
	d, err := bridge.CallOn(conn, bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.DesktopEvent, Payload: payload}, desktopCallWait)
	if err != nil {
		if ctx.Err() != nil {
			return desktop.Answer{}, errors.New("cancelled")
		}
		return desktop.Answer{}, err
	}
	a := desktop.Answer{Allow: d.Behavior == "allow", Message: d.Message}
	if a.Allow && len(d.Data) > 0 {
		if err := json.Unmarshal(d.Data, &a); err != nil {
			return desktop.Answer{}, errors.New("Den's answer is unreadable")
		}
		a.Allow, a.Message = true, d.Message
	}
	return a, nil
}

// runDesktopGate is `den hook desktop-gate --socket <den.sock>`, the
// desktop agent's PreToolUse hook (matcher ".*"), a second layer behind
// the proxy. It never answers allow (a hook's allow would override the
// agent's allow-list): for a tool Den allows it makes no decision, and
// everything else is denied, as JSON, whatever goes wrong, a panic
// included. Exit 2 is not used: Claude Code shows the model the hook's
// command line with it.
func runDesktopGate(args []string) {
	deny := func(why string) {
		out, _ := json.Marshal(map[string]any{"hookSpecificOutput": map[string]any{
			"hookEventName": "PreToolUse", "permissionDecision": "deny", "permissionDecisionReason": "Den: " + why,
		}})
		os.Stdout.Write(append(out, '\n'))
		os.Exit(0)
	}
	defer func() {
		if recover() != nil {
			deny("the desktop gate failed")
		}
	}()
	_, socket, err := desktopArgs(args)
	if err != nil {
		deny(err.Error())
	}
	raw, err := io.ReadAll(io.LimitReader(os.Stdin, bridge.MaxLine))
	if err != nil || len(raw) == 0 {
		deny("no hook input")
	}
	var in bridge.HookInput
	if json.Unmarshal(raw, &in) != nil || in.HookEventName != "PreToolUse" || in.ToolName == "" {
		deny("unreadable hook input")
	}
	small, _ := json.Marshal(map[string]any{"session_id": in.SessionID, "hook_event_name": in.HookEventName, "tool_name": in.ToolName, "agent_id": in.AgentID})
	d, err := bridge.Call(socket, bridge.Envelope{Seq: time.Now().UnixNano(), Event: bridge.DesktopGateEvent, SessionID: in.SessionID, Payload: small}, 8*time.Second)
	if err != nil {
		deny("Den is not answering, so nothing runs")
	}
	if d.Behavior != "allow" {
		deny(d.Message)
	}
	os.Exit(0) // no decision: the allow-list and the proxy decide
}

// cursorPos is where the pointer is, from Hyprland.
func cursorPos() (float64, float64, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "hyprctl", "cursorpos", "-j").Output()
	if err != nil {
		return 0, 0, false
	}
	var c struct{ X, Y float64 }
	if json.Unmarshal(out, &c) != nil {
		return 0, 0, false
	}
	return c.X, c.Y, true
}

// logTo is the mission's proxy log, once Den named it, and filter what
// hypruse's stderr goes through on its way there.
var (
	logTo  *os.File
	filter *desktop.LogFilter
)

// proxyLog writes one line to the proxy log, when there is one.
func proxyLog(format string, a ...any) {
	if logTo != nil {
		fmt.Fprintf(logTo, "%s den desktop-mcp: %s\n", time.Now().Format(time.RFC3339), fmt.Sprintf(format, a...))
	}
}
