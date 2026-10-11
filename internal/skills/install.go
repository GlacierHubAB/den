package skills

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// Scope is where an approved skill goes.
type Scope string

const (
	// Project installs into <dir>/.claude/skills, which Claude Code loads
	// for every session working in that project.
	Project Scope = "project"
	// Agent installs into a folder of Den's own that one agent is started
	// with (claude --add-dir), so no other session sees it.
	Agent Scope = "agent"
)

// Target says where one approval installs.
type Target struct {
	Scope Scope
	Dir   string // Project: the project folder. Agent: the agent's working directory, for the record
	Peer  string // Agent: the peer name of the agent the folder is for
	Root  string // the folder whose .claude/skills receives the skills; AgentRoot for Agent
}

// SkillsDir is the folder the skills land in.
func (t Target) SkillsDir() string { return filepath.Join(t.Root, ".claude", "skills") }

// Fetch reads one URL; tests replace it so nothing touches the network.
var Fetch = fetchHTTP

// RawURL is where a file of a skill is read from: GitHub's raw host at the
// pinned commit, never a branch.
func RawURL(s Skill, f File) string {
	src := s.Path + "/" + f.Path
	if f.Source != "" {
		src = f.Source
	}
	var parts []string
	for _, p := range strings.Split(src, "/") {
		parts = append(parts, url.PathEscape(p))
	}
	return "https://raw.githubusercontent.com/" + s.Repo + "/" + s.Commit + "/" + strings.Join(parts, "/")
}

const (
	maxFile  = 1 << 20
	maxSkill = 8 << 20
)

func fetchHTTP(ctx context.Context, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, err
	}
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("%s: %s", u, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxFile+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxFile {
		return nil, fmt.Errorf("%s: larger than %d bytes", u, maxFile)
	}
	return b, nil
}

// Download fetches every file of a skill and checks each against the
// catalogue's size and SHA-256, then scans the text for what a skill must
// not carry. Nothing is written.
func Download(ctx context.Context, s Skill) (map[string][]byte, error) {
	out := map[string][]byte{}
	total := 0
	for _, f := range s.Files {
		b, err := Fetch(ctx, RawURL(s, f))
		if err != nil {
			return nil, fmt.Errorf("%s: fetch %s: %w", s.Name, f.Path, err)
		}
		sum := sha256.Sum256(b)
		if got := hex.EncodeToString(sum[:]); got != f.SHA256 {
			return nil, fmt.Errorf("%s: %s does not match the catalogue (sha256 %s, want %s); refusing", s.Name, f.Path, got[:12], f.SHA256[:12])
		}
		if f.Size > 0 && int64(len(b)) != f.Size {
			return nil, fmt.Errorf("%s: %s is %d bytes, the catalogue says %d; refusing", s.Name, f.Path, len(b), f.Size)
		}
		if total += len(b); total > maxSkill {
			return nil, fmt.Errorf("%s: larger than %d bytes; refusing", s.Name, maxSkill)
		}
		out[f.Path] = b
	}
	if err := Scan(s.Name, out); err != nil {
		return nil, err
	}
	return out, nil
}

var (
	// A line that runs a shell command when the skill loads: !`cmd`.
	inlineShell = regexp.MustCompile("(^|\\s)!`")
	frontKey    = regexp.MustCompile(`^([A-Za-z0-9_-]+)\s*:`)
)

// Refused frontmatter keys: they register hooks or pre-approve tools.
var refusedKeys = map[string]bool{"hooks": true, "allowed-tools": true}

// Scan refuses a skill whose markdown registers hooks, pre-approves tools
// or runs shell commands when it loads, and one whose SKILL.md names a
// different skill than its folder.
func Scan(name string, files map[string][]byte) error {
	md, ok := files["SKILL.md"]
	if !ok {
		return fmt.Errorf("%s: no SKILL.md", name)
	}
	front, _ := frontmatter(md)
	if n := front["name"]; n != "" && n != name {
		return fmt.Errorf("%s: SKILL.md is named %q; refusing", name, n)
	}
	for path, b := range files {
		if !strings.HasSuffix(strings.ToLower(path), ".md") {
			continue
		}
		f, _ := frontmatter(b)
		for k := range f {
			if refusedKeys[k] {
				return fmt.Errorf("%s: %s sets %q in its frontmatter; refusing", name, path, k)
			}
		}
		for i, line := range strings.Split(string(b), "\n") {
			t := strings.TrimSpace(line)
			if strings.HasPrefix(t, "```!") || strings.HasPrefix(t, "~~~!") || inlineShell.MatchString(line) {
				return fmt.Errorf("%s: %s line %d runs a shell command when the skill loads; refusing", name, path, i+1)
			}
		}
	}
	return nil
}

// frontmatter returns the top-level keys of a leading --- block.
func frontmatter(b []byte) (map[string]string, bool) {
	keys := map[string]string{}
	text := strings.ReplaceAll(string(b), "\r\n", "\n")
	if !strings.HasPrefix(text, "---\n") {
		return keys, false
	}
	rest := text[4:]
	end := strings.Index(rest, "\n---")
	if end < 0 {
		return keys, false
	}
	for _, line := range strings.Split(rest[:end], "\n") {
		if line == "" || line[0] == ' ' || line[0] == '\t' {
			continue
		}
		if m := frontKey.FindStringSubmatch(line); m != nil {
			v := strings.TrimSpace(line[len(m[0]):])
			keys[strings.ToLower(m[1])] = strings.Trim(v, `"'`)
		}
	}
	return keys, true
}

// Write puts downloaded files into the target, through a staging folder
// renamed into place, so a half-written skill is never visible. It refuses
// to replace a folder that is already there. Files are written without the
// executable bit.
func Write(t Target, s Skill, files map[string][]byte) (string, error) {
	base := t.SkillsDir()
	dest := filepath.Join(base, s.Name)
	if _, err := os.Lstat(dest); err == nil {
		return "", fmt.Errorf("%s already exists; Den does not replace it", dest)
	}
	if err := os.MkdirAll(base, 0o755); err != nil {
		return "", err
	}
	tag := make([]byte, 4)
	rand.Read(tag)
	stage := filepath.Join(base, ".den-staging-"+s.Name+"-"+hex.EncodeToString(tag))
	defer os.RemoveAll(stage)
	for _, f := range s.Files {
		p := filepath.Join(stage, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return "", err
		}
		if err := os.WriteFile(p, files[f.Path], 0o644); err != nil {
			return "", err
		}
	}
	if err := os.Rename(stage, dest); err != nil {
		return "", err
	}
	return dest, nil
}

// Remove deletes what Den installed for a record: each file that still has
// the hash it was installed with, then the folders left empty. A file that
// was changed since is kept and reported.
func Remove(r Record) (kept []string, err error) {
	if r.Path == "" || filepath.Base(r.Path) != r.Name {
		return nil, fmt.Errorf("record for %s has no install path", r.Name)
	}
	for _, f := range r.Files {
		p := filepath.Join(r.Path, filepath.FromSlash(f.Path))
		b, rerr := os.ReadFile(p)
		if errors.Is(rerr, os.ErrNotExist) {
			continue
		}
		if rerr != nil {
			kept = append(kept, f.Path)
			continue
		}
		sum := sha256.Sum256(b)
		if hex.EncodeToString(sum[:]) != f.SHA256 {
			kept = append(kept, f.Path)
			continue
		}
		if err := os.Remove(p); err != nil {
			kept = append(kept, f.Path)
		}
	}
	// Empty folders bottom-up, up to and including the skill's own, then
	// the agent's private root when nothing else is left in it.
	pruneEmpty(r.Path)
	if r.Scope == Agent && r.Root != "" {
		os.Remove(filepath.Join(r.Root, ".claude", "skills"))
		os.Remove(filepath.Join(r.Root, ".claude"))
		os.Remove(r.Root)
	}
	return kept, nil
}

func pruneEmpty(dir string) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() {
			pruneEmpty(filepath.Join(dir, e.Name()))
		}
	}
	os.Remove(dir) // fails, harmlessly, when not empty
}

// Lines describes a proposal for the inbox: per skill its name, repo,
// pinned commit and license, the reason, and where it will go.
func Lines(ms []Match, t Target) []string {
	var out []string
	for _, m := range ms {
		out = append(out, fmt.Sprintf("%s · %s @ %s · %s", m.Name, m.Repo, m.Commit, m.License))
		out = append(out, "  "+m.Reason)
		if m.Unverified != "" {
			out = append(out, "  unverified: "+m.Unverified)
		}
	}
	switch t.Scope {
	case Agent:
		out = append(out, "→ for this agent only: "+shortHome(t.SkillsDir()))
	default:
		out = append(out, "→ for this project: "+shortHome(t.SkillsDir()))
	}
	return out
}

func shortHome(p string) string {
	if home, _ := os.UserHomeDir(); home != "" && strings.HasPrefix(p, home+"/") {
		return "~" + strings.TrimPrefix(p, home)
	}
	return p
}
