package inbox

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
)

func TestEditInScope(t *testing.T) {
	const home, cwd = "/home/user", "/home/user/proj"
	in := []string{
		"/home/user/proj/main.go", "/home/user/proj/sub/dir/x.txt",
		"main.go", "./sub/x.txt", "~/proj/main.go", // relative to cwd, ~ to home
		"/home/user/proj//sub/./x.txt",                                                                // cleaned
		"/home/user/proj/Makefile", "/home/user/proj/.envrc", "/home/user/proj/.vscode/settings.json", // accepted residual risk
	}
	out := []string{
		"/home/user/other/x", "/etc/passwd", "/home/user/proj", "/home/user/proj/", // outside, or cwd itself
		"/home/user/proj/../other/x", "../x", "sub/../../x", // ".." refused
		"~root/x", "", // another user's home, nothing
		// sensitive, case-insensitive, after cleaning
		"/home/user/proj/.git/config", "/home/user/proj/.git/hooks/pre-commit", "/home/user/proj/.GIT/HEAD",
		"/home/user/proj/sub/.git", "/home/user/proj/.gitattributes", "/home/user/proj/.gitmodules", "/home/user/proj/.githooks/pre-push",
		"/home/user/proj/.claude/settings.json", "/home/user/proj/.claude/settings.local.json", "/home/user/proj/.mcp.json",
		"/home/user/proj/.Claude/commands/x.md", "/home/user/proj/sub/.bashrc", "/home/user/proj/.zshrc",
	}
	for _, p := range in {
		if !EditInScope(p, cwd, home) {
			t.Errorf("%q: refused", p)
		}
	}
	for _, p := range out {
		if EditInScope(p, cwd, home) {
			t.Errorf("%q: allowed", p)
		}
	}
	// A cwd of the home directory, or above it, takes no edit rule: every
	// start-up and config file of the user is under it.
	for _, dir := range []string{home, "/home", "/"} {
		for _, p := range []string{"/home/user/notes.txt", "/home/user/proj/main.go"} {
			if EditInScope(p, dir, home) {
				t.Errorf("cwd %q, %q: allowed", dir, p)
			}
		}
	}
	// Under ~/.config and ~/.local everything is sensitive, and so are
	// files that run code wherever they are (the security review's list).
	for _, cwd := range []string{"/home/user/.config", "/home/user/.config/nvim", "/home/user/.local", "/home/user/proj"} {
		for _, p := range []string{".config/git/config", ".local/bin/git", ".config/hypr/hyprland.conf", ".config/systemd/user/x.service",
			".config/autostart/x.desktop", ".config/environment.d/x.conf", ".config/uwsm/env", ".config/nvim/init.lua", ".local/share/x"} {
			if EditInScope("/home/user/"+p, cwd, home) {
				t.Errorf("cwd %q, ~/%s: allowed", cwd, p)
			}
		}
	}
	for _, name := range []string{".gitconfig", ".npmrc", ".netrc", ".tmux.conf", ".vimrc", ".pam_environment", ".xprofile", ".xinitrc", ".xsession", ".XSESSIONRC"} {
		if EditInScope("/home/user/proj/"+name, cwd, home) {
			t.Errorf("%s: allowed", name)
		}
	}
}

// A symlink inside the working directory that points out of it, or at a
// sensitive place, is refused once resolved.
func TestEditRealInScopeResolvesSymlinks(t *testing.T) {
	root := t.TempDir()
	if r, err := filepath.EvalSymlinks(root); err == nil {
		root = r
	}
	home, cwd, outside := filepath.Join(root, "home"), filepath.Join(root, "home", "proj"), filepath.Join(root, "elsewhere")
	for _, d := range []string{cwd, filepath.Join(home, ".ssh"), outside} {
		os.MkdirAll(d, 0o700)
	}
	os.Symlink(filepath.Join(home, ".ssh"), filepath.Join(cwd, "keys"))
	os.Symlink(outside, filepath.Join(cwd, "out"))
	os.Symlink(filepath.Join(home, ".bashrc"), filepath.Join(cwd, "rc"))
	for _, p := range []string{"keys/authorized_keys", "out/new.txt", "rc"} {
		if !EditInScope(filepath.Join(cwd, p), cwd, home) {
			t.Fatalf("%q: lexically out already", p)
		}
		if editRealInScope(filepath.Join(cwd, p), cwd, home) {
			t.Errorf("%q: allowed through a symlink", p)
		}
	}
	if !editRealInScope(filepath.Join(cwd, "new", "file.go"), cwd, home) {
		t.Error("a new file inside: refused")
	}
}

// edit is a held edit request of tool on path for a session in cwd.
func edit(tool, path, cwd string) *bridge.Request {
	field := "file_path"
	if tool == "NotebookEdit" {
		field = "notebook_path"
	}
	p, _ := json.Marshal(map[string]any{"session_id": "s1", "cwd": cwd, "hook_event_name": "PermissionRequest", "tool_name": tool, "tool_input": map[string]any{field: path, "content": "x"}})
	return bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: "s1", Payload: p})
}

// /always on an edit allows that tool's edits inside the working directory
// only, never a sensitive file there, for every edit tool.
func TestAlwaysOnEditsIsScopedToTheWorkingDirectory(t *testing.T) {
	home, _ := os.UserHomeDir()
	cwd := t.TempDir()
	if r, err := filepath.EvalSymlinks(cwd); err == nil {
		cwd = r
	}
	for _, tool := range []string{"Write", "Edit", "MultiEdit", "NotebookEdit"} {
		t.Run(tool, func(t *testing.T) {
			store := New()
			it := store.Apply(edit(tool, filepath.Join(cwd, "a.txt"), cwd))
			if AlwaysRefusal(it) != "" || !store.Answer(it, "always") || !strings.Contains(AlwaysScope(it), "under "+cwd) {
				t.Fatalf("always: %q, scope %q", AlwaysRefusal(it), AlwaysScope(it))
			}
			if it := store.Apply(edit(tool, filepath.Join(cwd, "sub", "b.txt"), cwd)); it != nil {
				t.Errorf("inside: asked again")
			}
			for _, p := range []string{filepath.Join(cwd, "..", "x"), "/etc/x", filepath.Join(cwd, ".git", "config"), filepath.Join(cwd, ".claude", "settings.json"), filepath.Join(home, ".ssh", "authorized_keys")} {
				if it := store.Apply(edit(tool, p, cwd)); it == nil || !it.Answerable() {
					t.Errorf("%q: not asked", p)
				}
			}
			// A rule made in one folder does not cover another session cwd.
			if it := store.Apply(edit(tool, "/tmp/elsewhere/x", "/tmp/elsewhere")); it == nil {
				t.Error("another cwd: not asked")
			}
		})
	}
	// /always on an edit of a sensitive file, or outside, is refused.
	store := New()
	it := store.Apply(edit("Write", filepath.Join(cwd, ".git", "hooks", "pre-commit"), cwd))
	if AlwaysRefusal(it) == "" || store.Answer(it, "always") {
		t.Error("always on a git hook was taken")
	}
}

// WebFetch is scoped to the URL's host, an MCP tool to its full name, any
// other tool to the tool, and a plan or question never.
func TestAlwaysScopes(t *testing.T) {
	call := func(tool string, input map[string]any) *bridge.Request {
		p, _ := json.Marshal(map[string]any{"session_id": "s1", "hook_event_name": "PermissionRequest", "tool_name": tool, "tool_input": input})
		return bridge.TestRequest(bridge.Envelope{Event: "PermissionRequest", SessionID: "s1", Payload: p})
	}
	store := New()
	it := store.Apply(call("WebFetch", map[string]any{"url": "https://docs.example.com/a", "prompt": "x"}))
	if !store.Answer(it, "always") || AlwaysScope(it) != "every WebFetch to docs.example.com" {
		t.Fatalf("WebFetch scope %q", AlwaysScope(it))
	}
	if store.Apply(call("WebFetch", map[string]any{"url": "https://DOCS.example.com/b"})) != nil {
		t.Error("same host: asked")
	}
	for _, u := range []string{"https://evil.example/x", "https://docs.example.com.evil.example/", "not a url"} {
		if store.Apply(call("WebFetch", map[string]any{"url": u})) == nil {
			t.Errorf("%q: not asked", u)
		}
	}
	it = store.Apply(call("mcp__github__create_issue", map[string]any{"title": "x"}))
	if !store.Answer(it, "always") || AlwaysScope(it) != "every create_issue call on github" {
		t.Fatalf("MCP scope %q", AlwaysScope(it))
	}
	if store.Apply(call("mcp__github__create_issue", map[string]any{"title": "y"})) != nil {
		t.Error("same MCP tool: asked")
	}
	if store.Apply(call("mcp__github__delete_repo", map[string]any{})) == nil {
		t.Error("another tool of the same server: not asked")
	}
	it = store.Apply(call("WebSearch", map[string]any{"query": "x"}))
	if !store.Answer(it, "always") || AlwaysScope(it) != "every WebSearch call, whatever it touches" {
		t.Fatalf("WebSearch scope %q", AlwaysScope(it))
	}
	plan := store.Apply(call("ExitPlanMode", map[string]any{"plan": "x"}))
	if AlwaysRefusal(plan) == "" || store.Answer(plan, "always") {
		t.Error("always on a plan was taken")
	}
	if store.Apply(call("ExitPlanMode", map[string]any{"plan": "y"})) == nil {
		t.Error("a later plan was not asked")
	}
}
