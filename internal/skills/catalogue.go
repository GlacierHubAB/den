// Package skills matches an agent's role to Claude Code skills from a
// vetted, allowlisted catalogue, installs the ones the human approves for
// one project or one agent, and records where each came from so it can be
// removed again.
//
// The catalogue ships inside Den (catalogue.json). Every entry names its
// repository, a pinned commit, its license, the role tags it serves, why,
// what was checked before it was admitted, and every file with its SHA-256.
// Den fetches exactly those files at that commit and refuses any that
// differ, so a moving branch or a changed file can never be installed.
package skills

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"regexp"
	"sort"
	"strings"
)

//go:embed catalogue.json
var catalogueJSON []byte

// Catalogue is the allowlist.
type Catalogue struct {
	Version  int     `json:"version"`
	Reviewed string  `json:"reviewed"`
	Repos    []Repo  `json:"repos"`
	Skills   []Skill `json:"skills"`
}

// Repo is a source repository and the commit the catalogue pins.
type Repo struct {
	ID          string `json:"id"` // owner/name on GitHub
	URL         string `json:"url"`
	Commit      string `json:"commit"`
	CommitDate  string `json:"commit_date"`
	License     string `json:"license"`
	Maintenance string `json:"maintenance"`
}

// Skill is one skill folder in a repository.
type Skill struct {
	Name          string   `json:"name"` // folder name as installed; equals SKILL.md's name
	Repo          string   `json:"repo"`
	Commit        string   `json:"commit"`
	Path          string   `json:"path"` // folder inside the repository
	License       string   `json:"license"`
	LicenseSource string   `json:"license_source"`
	Roles         []string `json:"roles"`
	Reason        string   `json:"reason"`
	Checks        string   `json:"checks"`
	Unverified    string   `json:"unverified"`
	Files         []File   `json:"files"`
}

// File is one file of a skill, relative to its folder.
type File struct {
	Path string `json:"path"`
	// Source is the file's path in the repository when it does not live in
	// the skill's folder: a repository LICENSE Den copies in beside the
	// skill so the installed copy carries its notice.
	Source string `json:"source,omitempty"`
	SHA256 string `json:"sha256"`
	Size   int64  `json:"size"`
}

// Tags is the fixed role vocabulary the catalogue uses.
var Tags = []string{"coding", "frontend", "design", "writing", "testing", "security", "devops", "research"}

// Licenses a skill may carry. Anything else is refused, including the
// source-available terms some published skills use.
var Licenses = []string{"Apache-2.0", "MIT", "BSD-2-Clause", "BSD-3-Clause", "ISC", "CC-BY-4.0", "CC-BY-SA-4.0", "CC0-1.0", "0BSD", "Unlicense"}

// MaxPerRole is how many skills one proposal offers at most.
const MaxPerRole = 4

var (
	shaRe  = regexp.MustCompile(`^[0-9a-f]{40}$`)
	hashRe = regexp.MustCompile(`^[0-9a-f]{64}$`)
	nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,63}$`)
	repoRe = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

// Load parses and validates the catalogue that ships with Den.
func Load() (*Catalogue, error) { return Parse(catalogueJSON) }

// Parse reads a catalogue and validates it.
func Parse(raw []byte) (*Catalogue, error) {
	var c Catalogue
	if err := json.Unmarshal(raw, &c); err != nil {
		return nil, fmt.Errorf("skills catalogue: %w", err)
	}
	if err := c.Validate(); err != nil {
		return nil, err
	}
	return &c, nil
}

// Validate checks every rule an entry must meet to be offered at all.
func (c *Catalogue) Validate() error {
	repos := map[string]Repo{}
	for _, r := range c.Repos {
		if !repoRe.MatchString(r.ID) {
			return fmt.Errorf("skills catalogue: bad repo id %q", r.ID)
		}
		if !shaRe.MatchString(r.Commit) {
			return fmt.Errorf("skills catalogue: repo %s is not pinned to a full commit SHA", r.ID)
		}
		repos[r.ID] = r
	}
	names := map[string]bool{}
	for _, s := range c.Skills {
		where := "skill " + s.Name
		if !nameRe.MatchString(s.Name) {
			return fmt.Errorf("skills catalogue: bad skill name %q", s.Name)
		}
		if names[s.Name] {
			return fmt.Errorf("skills catalogue: %s listed twice", where)
		}
		names[s.Name] = true
		r, ok := repos[s.Repo]
		if !ok {
			return fmt.Errorf("skills catalogue: %s names repo %q, which is not listed", where, s.Repo)
		}
		if !shaRe.MatchString(s.Commit) || s.Commit != r.Commit {
			return fmt.Errorf("skills catalogue: %s must be pinned to its repo's commit %s", where, r.Commit)
		}
		if !allowedLicense(s.License) {
			return fmt.Errorf("skills catalogue: %s has license %q, not one of %s", where, s.License, strings.Join(Licenses, ", "))
		}
		if strings.TrimSpace(s.Reason) == "" || strings.TrimSpace(s.Checks) == "" {
			return fmt.Errorf("skills catalogue: %s needs a reason and the checks done", where)
		}
		if len(s.Roles) == 0 {
			return fmt.Errorf("skills catalogue: %s has no role tags", where)
		}
		for _, t := range s.Roles {
			if !isTag(t) {
				return fmt.Errorf("skills catalogue: %s has unknown role tag %q", where, t)
			}
		}
		if err := checkPath(s.Path); err != nil {
			return fmt.Errorf("skills catalogue: %s: %w", where, err)
		}
		hasSkillMD := false
		for _, f := range s.Files {
			if err := checkPath(f.Path); err != nil {
				return fmt.Errorf("skills catalogue: %s: %w", where, err)
			}
			if f.Source != "" {
				if err := checkPath(f.Source); err != nil {
					return fmt.Errorf("skills catalogue: %s: %w", where, err)
				}
			}
			if !hashRe.MatchString(f.SHA256) {
				return fmt.Errorf("skills catalogue: %s file %s has no SHA-256", where, f.Path)
			}
			if f.Path == "SKILL.md" {
				hasSkillMD = true
			}
		}
		if !hasSkillMD {
			return fmt.Errorf("skills catalogue: %s lists no SKILL.md", where)
		}
	}
	return nil
}

// checkPath refuses absolute paths and anything that climbs out.
func checkPath(p string) error {
	if p == "" || strings.HasPrefix(p, "/") || strings.Contains(p, "\\") {
		return fmt.Errorf("bad path %q", p)
	}
	for _, part := range strings.Split(p, "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("bad path %q", p)
		}
	}
	return nil
}

func allowedLicense(l string) bool {
	for _, a := range Licenses {
		if l == a {
			return true
		}
	}
	return false
}

func isTag(t string) bool {
	for _, x := range Tags {
		if x == t {
			return true
		}
	}
	return false
}

// Repo returns the repository entry by id.
func (c *Catalogue) Repo(id string) (Repo, bool) {
	for _, r := range c.Repos {
		if r.ID == id {
			return r, true
		}
	}
	return Repo{}, false
}

// Skill returns a skill by name.
func (c *Catalogue) Skill(name string) (Skill, bool) {
	for _, s := range c.Skills {
		if s.Name == name {
			return s, true
		}
	}
	return Skill{}, false
}

// words maps a word of a role label to the tags it implies. Den's role
// labels are free text ("coding agent", "ui designer", "sysadmin"), so
// they are matched word by word; a role with no known word gets nothing.
var words = map[string][]string{
	"coding": {"coding"}, "code": {"coding"}, "coder": {"coding"}, "developer": {"coding"},
	"engineer": {"coding"}, "programmer": {"coding"}, "backend": {"coding"}, "fullstack": {"coding", "frontend"},
	"frontend": {"frontend"}, "front": {"frontend"}, "ui": {"frontend"}, "web": {"frontend"},
	"design": {"design"}, "designer": {"design"}, "ux": {"design"}, "icon": {"design"}, "visual": {"design"}, "brand": {"design"},
	"writer": {"writing"}, "copywriter": {"writing"}, "documentation": {"writing"}, "docs": {"writing"},
	"editor": {"writing"}, "copy": {"writing"},
	"qa": {"testing"}, "test": {"testing"}, "tester": {"testing"}, "testing": {"testing"},
	"security": {"security"}, "auditor": {"security"}, "pentester": {"security"}, "appsec": {"security"},
	"sysadmin": {"devops"}, "devops": {"devops"}, "sre": {"devops"}, "ops": {"devops"}, "infrastructure": {"devops"},
	"release": {"devops"}, "deploy": {"devops"}, "platform": {"devops"},
	"researcher": {"research"}, "research": {"research"}, "analyst": {"research"},
}

var splitRe = regexp.MustCompile(`[^a-z0-9]+`)

// RoleTags normalises a role label to catalogue tags. A designer's "ui"
// means design, not frontend code.
func RoleTags(role string) []string {
	seen := map[string]bool{}
	ws := splitRe.Split(strings.ToLower(role), -1)
	designing := false
	for _, w := range ws {
		if w == "designer" || w == "design" {
			designing = true
		}
	}
	for _, w := range ws {
		if w == "ui" && designing {
			continue
		}
		if isTag(w) {
			seen[w] = true
		}
		for _, t := range words[w] {
			seen[t] = true
		}
	}
	var out []string
	for _, t := range Tags {
		if seen[t] {
			out = append(out, t)
		}
	}
	return out
}

// Match is a skill offered for a role, with the tags that matched.
type Match struct {
	Skill
	Matched []string
}

// Match returns at most MaxPerRole skills for the role: those sharing the
// most tags with it first, then in catalogue order.
func (c *Catalogue) Match(role string) []Match {
	tags := RoleTags(role)
	if len(tags) == 0 {
		return nil
	}
	var out []Match
	for _, s := range c.Skills {
		var hit []string
		for _, t := range s.Roles {
			for _, w := range tags {
				if t == w {
					hit = append(hit, t)
				}
			}
		}
		if len(hit) > 0 {
			out = append(out, Match{Skill: s, Matched: hit})
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return len(out[i].Matched) > len(out[j].Matched) })
	if len(out) > MaxPerRole {
		out = out[:MaxPerRole]
	}
	return out
}

// Pick narrows a match to the named skills, refusing names that are not in
// the catalogue or do not fit the role. With no names it is the match.
func (c *Catalogue) Pick(role string, names []string) ([]Match, error) {
	matches := c.Match(role)
	if len(names) == 0 {
		return matches, nil
	}
	var out []Match
	for _, n := range names {
		found := false
		for _, m := range matches {
			if m.Name == n {
				out, found = append(out, m), true
				break
			}
		}
		if found {
			continue
		}
		if _, ok := c.Skill(n); !ok {
			return nil, fmt.Errorf("%q is not in Den's skills catalogue; only catalogue skills can be offered", n)
		}
		return nil, fmt.Errorf("%q does not fit the role %q", n, role)
	}
	return out, nil
}
