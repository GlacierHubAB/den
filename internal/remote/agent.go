package remote

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"github.com/GlacierHubAB/den/internal/bridge"
	"github.com/GlacierHubAB/den/internal/spawn"
)

// AgentExec is what `den remote agent <id>` runs in its tmux pane: it claims
// the spawn serve staged for the session id (spec.json, deleted as it is
// read) and returns Claude Code's absolute path, argument list, folder and
// environment, for the caller to exec. The brief stays in brief.md; Claude
// Code's first prompt only names that file. The agent gets no Den MCP
// server: it cannot start agents of its own on the host.
func AgentExec(sid string, environ []string) (argv []string, dir string, env []string, err error) {
	if err := CheckSessionID(sid); err != nil {
		return nil, "", nil, err
	}
	stage, err := agentStage(sid, envValue(environ, "DEN_SPAWN_DIR"))
	if err != nil {
		return nil, "", nil, err
	}
	specPath := filepath.Join(stage, "spec.json")
	f, err := os.OpenFile(specPath, os.O_RDONLY|syscall.O_NOFOLLOW, 0)
	if err != nil {
		return nil, "", nil, errors.New("no spawn is staged for that session (it was claimed already, or it expired)")
	}
	st, err := f.Stat()
	sys, _ := st.Sys().(*syscall.Stat_t)
	if err != nil || !st.Mode().IsRegular() || sys == nil || int(sys.Uid) != os.Getuid() || st.Mode().Perm()&0o077 != 0 {
		f.Close()
		return nil, "", nil, errors.New("the staged spawn is not a private file of this user's")
	}
	raw, _ := io.ReadAll(io.LimitReader(f, 64<<10))
	f.Close()
	os.Remove(specPath) // claimed: a spawn starts once
	var s agentSpec
	if err := json.Unmarshal(raw, &s); err != nil {
		return nil, "", nil, errors.New("the staged spawn does not parse")
	}
	switch {
	case s.SessionID != sid:
		return nil, "", nil, errors.New("the staged spawn is for another session")
	case time.Since(s.Created) > briefTTL:
		return nil, "", nil, errors.New("the staged spawn is over an hour old")
	case !filepath.IsAbs(s.Claude) || !filepath.IsAbs(s.Dir):
		return nil, "", nil, errors.New("the staged spawn names a relative path")
	}
	for _, err := range []error{CheckPeer(s.Peer), CheckSpawnModel(s.Model, false), CheckWorktreeName(s.Worktree)} {
		if err != nil {
			return nil, "", nil, err
		}
	}
	if fi, err := os.Stat(s.Claude); err != nil || !fi.Mode().IsRegular() || fi.Mode()&0o111 == 0 {
		return nil, "", nil, errors.New("claude is no longer where serve found it")
	}
	brief := filepath.Join(stage, "brief.md")
	if fi, err := os.Lstat(brief); err != nil || !fi.Mode().IsRegular() {
		return nil, "", nil, errors.New("the brief is missing")
	}
	argv = []string{s.Claude, "--session-id", sid, "--name", s.Peer}
	if s.Worktree != "" {
		argv = append(argv, "--worktree", s.Worktree)
	}
	if s.Model != "" {
		argv = append(argv, "--model", s.Model)
	}
	// The staging folder lets Claude Code read the brief without asking.
	argv = append(argv, "--add-dir", stage, "--", spawnFirstPrompt(brief))
	var keep []string
	for _, kv := range environ {
		if !strings.HasPrefix(kv, "DEN_SPAWN_DIR=") {
			keep = append(keep, kv)
		}
	}
	return argv, s.Dir, spawn.Environ(keep, []string{"DEN_PEER=" + s.Peer}), nil
}

func envValue(environ []string, key string) string {
	for _, kv := range environ {
		if v, ok := strings.CutPrefix(kv, key+"="); ok {
			return v
		}
	}
	return ""
}

// agentStage is the session's staging folder: the one serve named in
// DEN_SPAWN_DIR, when it has the shape serve makes and is private all the
// way up, else where serve stages spawns by default.
func agentStage(sid, named string) (string, error) {
	if named != "" {
		named = filepath.Clean(named)
		base := filepath.Dir(named)
		if !filepath.IsAbs(named) || filepath.Base(named) != sid || filepath.Base(base) != "spawn" {
			return "", errors.New("DEN_SPAWN_DIR is not a staging folder")
		}
		for _, d := range []string{named, base, filepath.Dir(base)} {
			if err := bridge.CheckPrivateDir(d); err != nil {
				return "", fmt.Errorf("the staging folder is not private: %v", err)
			}
		}
		return named, nil
	}
	base, err := spawnBase()
	if err != nil {
		return "", err
	}
	stage := filepath.Join(base, sid)
	if err := bridge.CheckPrivateDir(stage); err != nil {
		return "", errors.New("no spawn is staged for that session")
	}
	return stage, nil
}

// AttachExec is what `den remote attach <name>` runs: tmux attach-session to
// a session on Den's tmux server that Den started (tagged @den_spawn with
// the session id its name ends in). The = prefix makes tmux match the
// name exactly, never as a prefix of another.
func AttachExec(name string) (argv []string, env []string, err error) {
	if err := CheckTmuxName(name, ""); err != nil {
		return nil, nil, err
	}
	bin := findBin("tmux")
	if bin == "" {
		return nil, nil, errors.New("tmux is not installed on this host")
	}
	hs := &hostSpawn{tmuxBin: bin, server: TmuxServer()}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if hs.tmux(ctx, "has-session", "-t", "="+name).Run() != nil {
		return nil, nil, errors.New("the agent has ended: its tmux session is gone")
	}
	out, err := hs.tmux(ctx, "show-options", "-v", "-t", "="+name+":", "@den_spawn").Output()
	tag := strings.TrimSpace(string(out))
	if err != nil || CheckSessionID(tag) != nil || CheckTmuxName(name, tag) != nil {
		return nil, nil, errors.New("that tmux session was not started by Den; not attaching")
	}
	return []string{bin, "-L", hs.server, "attach-session", "-t", "=" + name}, tmuxEnvAttach(), nil
}

// tmuxEnvAttach is the attaching client's environment: the terminal's, with
// TMUX unset so tmux does not refuse to nest.
func tmuxEnvAttach() []string {
	var env []string
	for _, kv := range os.Environ() {
		if !strings.HasPrefix(kv, "TMUX=") && !strings.HasPrefix(kv, "TMUX_PANE=") {
			env = append(env, kv)
		}
	}
	return env
}
