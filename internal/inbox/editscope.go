package inbox

import (
	"os"
	"path/filepath"
	"strings"
)

// An always-rule for a file edit (Write, Edit, MultiEdit, NotebookEdit)
// covers the session's working directory only, as Claude Code's own "allow
// all edits" does, and never a file whose change runs code or raises trust
// (sensitivePath). An edit outside that is asked about as usual.
//
// Accepted residual risk, written in the README: inside the working
// directory an edit rule also covers files that tools run later, such as
// .envrc, .vscode/, a Makefile or package.json.

// editTools maps the edit tools to the input field that names their file.
var editTools = map[string]string{"Write": "file_path", "Edit": "file_path", "MultiEdit": "file_path", "NotebookEdit": "notebook_path"}

// sensitiveUnderHome are directories under the home directory no edit rule
// reaches, lower case.
var sensitiveUnderHome = []string{".ssh", ".config/den", ".local/state/den", ".config/fish"}

// sensitiveParts are path parts, lower case, that make a path sensitive
// wherever they are: Claude Code's settings, hooks and commands, user and
// project; a repository's git directory (or .git file) and its hooks.
var sensitiveParts = map[string]bool{".claude": true, ".git": true, ".githooks": true}

// sensitiveNames are file names, lower case, that are sensitive anywhere:
// shell start-up files, git attributes and submodules, MCP servers.
var sensitiveNames = map[string]bool{
	".bashrc": true, ".bash_profile": true, ".bash_login": true, ".bash_logout": true, ".profile": true,
	".zshrc": true, ".zshenv": true, ".zprofile": true, ".zlogin": true, ".zlogout": true,
	".gitattributes": true, ".gitmodules": true, ".mcp.json": true,
}

// EditInScope reports whether an always-rule for edits in the session
// working directory cwd may allow an edit of path, for a user whose home
// directory is home. It is lexical: a relative path is taken from cwd, a
// leading ~ from home, and the result is cleaned; a ".." part refuses. The
// path must lie strictly under cwd and not be sensitive, which wins over
// the scope (a cwd of home does not open ~/.ssh). Den checks a local
// agent's edits with symlinks resolved as well (editRealInScope); a
// host's agents are checked against their own cwd and home.
func EditInScope(path, cwd, home string) bool {
	p, ok := editPath(path, cwd, home)
	if !ok {
		return false
	}
	dir := filepath.Clean(cwd)
	if !filepath.IsAbs(dir) || dir == "/" || !strings.HasPrefix(p, dir+"/") {
		return false
	}
	return !sensitivePath(p, home)
}

// editPath is path made absolute against cwd and home, and cleaned; ok is
// false for a path with a ".." part or nothing to anchor it.
func editPath(path, cwd, home string) (string, bool) {
	if path == "" {
		return "", false
	}
	for _, part := range strings.Split(filepath.ToSlash(path), "/") {
		if part == ".." {
			return "", false
		}
	}
	switch {
	case path == "~" || strings.HasPrefix(path, "~/"):
		if !filepath.IsAbs(home) {
			return "", false
		}
		path = filepath.Join(home, strings.TrimPrefix(path, "~"))
	case strings.HasPrefix(path, "~"):
		return "", false // another user's home
	case !filepath.IsAbs(path):
		if !filepath.IsAbs(cwd) {
			return "", false
		}
		path = filepath.Join(cwd, path)
	}
	return filepath.Clean(path), true
}

// sensitivePath reports whether editing the cleaned absolute path p could
// run code or raise trust. Names match without regard to case.
func sensitivePath(p, home string) bool {
	lp := strings.ToLower(p)
	if h := strings.ToLower(filepath.Clean(home)); filepath.IsAbs(h) && h != "/" {
		for _, d := range sensitiveUnderHome {
			base := h + "/" + d
			if lp == base || strings.HasPrefix(lp, base+"/") {
				return true
			}
		}
	}
	for _, part := range strings.Split(lp, "/") {
		if sensitiveParts[part] || sensitiveNames[part] {
			return true
		}
	}
	return false
}

// editRealInScope is EditInScope for an edit on this machine, checked again
// with symlinks resolved: the deepest part of the path that exists, and
// cwd, are read through EvalSymlinks, so cwd/x pointing at ~/.ssh does not
// pass.
func editRealInScope(path, cwd, home string) bool {
	if !EditInScope(path, cwd, home) {
		return false
	}
	p, _ := editPath(path, cwd, home)
	realCwd, err := filepath.EvalSymlinks(cwd)
	if err != nil {
		return false
	}
	realHome := home
	if h, err := filepath.EvalSymlinks(home); err == nil {
		realHome = h
	}
	// Resolve the deepest existing parent, then add back what does not
	// exist yet (a file Write is about to create).
	rest := ""
	cur := p
	for {
		if real, err := filepath.EvalSymlinks(cur); err == nil {
			cur = filepath.Join(real, rest)
			break
		} else if !os.IsNotExist(err) {
			return false
		}
		if _, err := os.Lstat(cur); err == nil {
			return false // a symlink to nothing yet: a write would create its target, wherever that is
		}
		parent := filepath.Dir(cur)
		if parent == cur {
			return false
		}
		rest = filepath.Join(filepath.Base(cur), rest)
		cur = parent
	}
	return EditInScope(cur, realCwd, realHome) && !sensitivePath(cur, home)
}
