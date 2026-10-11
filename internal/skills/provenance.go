package skills

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// Record is the provenance of one installed skill.
type Record struct {
	Name        string    `json:"name"`
	Repo        string    `json:"repo"`
	Commit      string    `json:"commit"`
	RepoPath    string    `json:"repo_path"`
	License     string    `json:"license"`
	Scope       Scope     `json:"scope"`
	Dir         string    `json:"dir"`            // the project, or the agent's working directory
	Peer        string    `json:"peer,omitempty"` // Agent scope: the agent it is for
	Root        string    `json:"root"`           // folder whose .claude/skills holds it
	Path        string    `json:"path"`           // the installed skill folder
	Role        string    `json:"role,omitempty"` // the role it was matched to
	Reason      string    `json:"reason"`
	InstalledAt time.Time `json:"installed_at"`
	ApprovedBy  string    `json:"approved_by"` // who pressed ^Y
	AskedBy     string    `json:"asked_by"`    // who proposed it
	Files       []File    `json:"files"`
}

// Provenance is the state file listing every skill Den installed.
type Provenance struct {
	Installed []Record `json:"installed"`
}

// StateDir is Den's state folder ($XDG_STATE_HOME/den).
func StateDir() string {
	dir := os.Getenv("XDG_STATE_HOME")
	if dir == "" {
		home, _ := os.UserHomeDir()
		dir = filepath.Join(home, ".local", "state")
	}
	return filepath.Join(dir, "den")
}

// ProvenancePath is skills.json in Den's state.
func ProvenancePath() string { return filepath.Join(StateDir(), "skills.json") }

// AgentRoot is a fresh private folder for one agent's skills. The time in
// the name keeps a later agent that reuses the peer name from inheriting
// them.
func AgentRoot(peer string, now time.Time) string {
	return filepath.Join(StateDir(), "skills", "agents", peer+"-"+now.Format("20060102-150405"))
}

var provMu sync.Mutex

// LoadProvenance reads the file; missing is empty.
func LoadProvenance(path string) (*Provenance, error) {
	p := &Provenance{}
	raw, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(raw, p); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return p, nil
}

func (p *Provenance) save(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Find returns the records whose name matches and, when where is set,
// whose project folder, agent peer or install path is where.
func (p *Provenance) Find(name, where string) []Record {
	var out []Record
	for _, r := range p.Installed {
		if name != "" && r.Name != name {
			continue
		}
		if where != "" && !r.at(where) {
			continue
		}
		out = append(out, r)
	}
	return out
}

func (r Record) at(where string) bool {
	if r.Peer != "" && r.Peer == where {
		return true
	}
	w := filepath.Clean(where)
	return w == filepath.Clean(r.Dir) || w == filepath.Clean(r.Path) || w == filepath.Clean(r.Root)
}

// Approval is the human's ^Y on one proposal.
type Approval struct {
	Target     Target
	Role       string
	Skills     []Match
	ApprovedBy string
	AskedBy    string
}

// Outcome is what one install did.
type Outcome struct {
	Name string
	Path string
	Err  error
}

// Install downloads, verifies and writes each approved skill, recording
// each one that landed in the provenance file at path.
func Install(ctx context.Context, a Approval, path string) []Outcome {
	var out []Outcome
	for _, m := range a.Skills {
		files, err := Download(ctx, m.Skill)
		if err != nil {
			out = append(out, Outcome{Name: m.Name, Err: err})
			continue
		}
		dest, err := Write(a.Target, m.Skill, files)
		if err != nil {
			out = append(out, Outcome{Name: m.Name, Err: err})
			continue
		}
		rec := Record{
			Name: m.Name, Repo: m.Repo, Commit: m.Commit, RepoPath: m.Path, License: m.License,
			Scope: a.Target.Scope, Dir: a.Target.Dir, Peer: a.Target.Peer, Root: a.Target.Root, Path: dest,
			Role: a.Role, Reason: m.Reason, InstalledAt: time.Now().UTC(), ApprovedBy: a.ApprovedBy, AskedBy: a.AskedBy,
			Files: m.Files,
		}
		if err := addRecord(path, rec); err != nil {
			out = append(out, Outcome{Name: m.Name, Path: dest, Err: fmt.Errorf("installed but not recorded: %w", err)})
			continue
		}
		out = append(out, Outcome{Name: m.Name, Path: dest})
	}
	return out
}

func addRecord(path string, r Record) error {
	provMu.Lock()
	defer provMu.Unlock()
	p, err := LoadProvenance(path)
	if err != nil {
		return err
	}
	p.Installed = append(p.Installed, r)
	return p.save(path)
}

// Uninstall removes the installed copies of a skill (all of them, or the
// ones at where: a project folder, an agent's peer name or the install
// path) and drops their records.
func Uninstall(path, name, where string) ([]Record, []string, error) {
	provMu.Lock()
	defer provMu.Unlock()
	p, err := LoadProvenance(path)
	if err != nil {
		return nil, nil, err
	}
	hits := p.Find(name, where)
	if len(hits) == 0 {
		return nil, nil, fmt.Errorf("Den has no record of installing %s%s", name, map[bool]string{true: " at " + where}[where != ""])
	}
	var kept []string
	var keep []Record
	for _, r := range p.Installed {
		if r.Name != name || (where != "" && !r.at(where)) {
			keep = append(keep, r)
			continue
		}
		k, err := Remove(r)
		if err != nil {
			return nil, nil, err
		}
		for _, f := range k {
			kept = append(kept, filepath.Join(r.Path, f))
		}
	}
	if keep == nil {
		keep = []Record{}
	}
	p.Installed = keep
	return hits, kept, p.save(path)
}

// Summary is one line for the human and the asker.
func Summary(outs []Outcome) string {
	var ok, bad []string
	for _, o := range outs {
		if o.Err != nil {
			bad = append(bad, o.Err.Error())
			continue
		}
		ok = append(ok, o.Name)
	}
	var parts []string
	if len(ok) > 0 {
		parts = append(parts, "installed "+strings.Join(ok, ", "))
	}
	if len(bad) > 0 {
		parts = append(parts, "failed: "+strings.Join(bad, "; "))
	}
	if len(parts) == 0 {
		return "nothing installed"
	}
	return strings.Join(parts, "; ")
}
