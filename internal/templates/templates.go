// Package templates holds Den's mission templates: the shape and team of a
// mission saved under a name. Templates are data the orchestrator reads when
// it proposes a mission, not a language: a purpose pattern with one blank
// ({target}), a shape, constraints, a done-when and a tree of roles with
// their models. Built-ins ship inside the binary; yours live in
// templates.json next to config.toml and win over a built-in of the same name.
package templates

import (
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/mission"
)

//go:embed builtin.json
var builtinJSON []byte

// Target is the blank in a template's purpose that the human fills in.
const Target = "{target}"

// Template is one saved mission shape and team.
type Template struct {
	Name        string           `json:"name"`
	About       string           `json:"about,omitempty"` // one line for the list
	Mission     string           `json:"mission"`         // the purpose, with {target} where the human's subject goes
	DoneWhen    string           `json:"done_when,omitempty"`
	Constraints []string         `json:"constraints,omitempty"`
	Shape       mission.Shape    `json:"shape,omitempty"`
	Cadence     string           `json:"cadence,omitempty"`
	Team        []mission.Member `json:"team,omitempty"`
	Builtin     bool             `json:"-"`
}

var nameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,31}$`)

// ValidName reports whether s can name a template: lower-case letters,
// digits and hyphens, so it is easy to type after /template.
func ValidName(s string) bool { return nameRe.MatchString(s) }

// Path is the user's file: templates.json beside the config file.
func Path() string {
	p := config.Path()
	if p == "" {
		return ""
	}
	return filepath.Join(filepath.Dir(p), "templates.json")
}

// Builtins are the templates shipped in the binary.
func Builtins() []Template {
	var out []Template
	if err := json.Unmarshal(builtinJSON, &out); err != nil {
		return nil
	}
	for i := range out {
		out[i].Builtin = true
	}
	return out
}

// Load returns the built-ins with the user's file laid over them, sorted by
// name. A missing file is fine; an unreadable or malformed one is reported
// and the built-ins still load.
func Load(path string) ([]Template, error) {
	byName := map[string]Template{}
	for _, t := range Builtins() {
		byName[t.Name] = t
	}
	user, err := loadUser(path)
	for _, t := range user {
		byName[t.Name] = t
	}
	out := make([]Template, 0, len(byName))
	for _, t := range byName {
		out = append(out, t)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, err
}

// loadUser reads the user's templates, skipping entries without a valid
// name or a purpose.
func loadUser(path string) ([]Template, error) {
	if path == "" {
		return nil, nil
	}
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var all []Template
	if err := json.Unmarshal(raw, &all); err != nil {
		return nil, fmt.Errorf("%s: %w", filepath.Base(path), err)
	}
	var out []Template
	for _, t := range all {
		t.Name = strings.ToLower(strings.TrimSpace(t.Name))
		if !ValidName(t.Name) || strings.TrimSpace(t.Mission) == "" {
			continue
		}
		t.Shape = mission.Shape(strings.ToLower(strings.ReplaceAll(string(t.Shape), "-", "")))
		switch t.Shape {
		case mission.Once, mission.Loop, mission.FanOut, mission.Schedule:
		default:
			t.Shape = mission.Once
		}
		out = append(out, t)
	}
	return out, nil
}

// Find looks a template up by name.
func Find(list []Template, name string) (Template, bool) {
	name = strings.ToLower(strings.TrimSpace(name))
	for _, t := range list {
		if t.Name == name {
			return t, true
		}
	}
	return Template{}, false
}

// FromMission is a template made from a mission's shape and team.
func FromMission(name string, m *mission.Mission) Template {
	return Template{
		Name:        name,
		About:       m.Name,
		Mission:     m.Mission,
		DoneWhen:    m.DoneWhen,
		Constraints: append([]string(nil), m.Constraints...),
		Shape:       m.Shape,
		Cadence:     m.Cadence,
		Team:        append([]mission.Member(nil), m.Team...),
	}
}

// Save writes t into the user's file, replacing a template of the same
// name and keeping the others. It refuses to overwrite a file it cannot
// parse, so a hand-edit with a typo is never lost.
func Save(path string, t Template) error {
	if path == "" {
		return errors.New("no config directory")
	}
	if !ValidName(t.Name) {
		return fmt.Errorf("%q is not a template name: use lower-case letters, digits and hyphens", t.Name)
	}
	if strings.TrimSpace(t.Mission) == "" {
		return errors.New("the mission has no purpose yet")
	}
	user, err := loadUser(path)
	if err != nil {
		return err
	}
	replaced := false
	for i := range user {
		if user[i].Name == t.Name {
			user[i], replaced = t, true
		}
	}
	if !replaced {
		user = append(user, t)
	}
	raw, err := json.MarshalIndent(user, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(raw, '\n'), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// Fill puts the human's subject where the purpose has its blank. A purpose
// without the blank gets the subject after it.
func (t Template) Fill(target string) string {
	target = strings.TrimSpace(target)
	switch {
	case target == "":
		return t.Mission
	case strings.Contains(t.Mission, Target):
		return strings.ReplaceAll(t.Mission, Target, target)
	}
	return strings.TrimRight(t.Mission, " ") + " (" + target + ")"
}

// Describe is the template as the orchestrator is told it, for a proposal
// seeded from it.
func (t Template) Describe(target string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "Template %q\n", t.Name)
	fmt.Fprintf(&b, "- mission: %s\n", t.Fill(target))
	if t.DoneWhen != "" {
		fmt.Fprintf(&b, "- done when: %s\n", t.DoneWhen)
	}
	if len(t.Constraints) > 0 {
		fmt.Fprintf(&b, "- constraints: %s\n", strings.Join(t.Constraints, "; "))
	}
	fmt.Fprintf(&b, "- shape: %s", t.Shape.Label())
	if t.Cadence != "" {
		fmt.Fprintf(&b, " (%s)", t.Cadence)
	}
	b.WriteString("\n")
	for _, m := range t.Team {
		line := "- team: " + m.Role
		if m.Model != "" {
			line += " on " + m.Model
		}
		u := strings.TrimSpace(m.Under)
		if u != "" && !strings.EqualFold(u, "orchestrator") {
			line += ", under " + u
		}
		b.WriteString(line + "\n")
	}
	return b.String()
}

// Line is the template as one row of the list.
func (t Template) Line() string {
	src := "yours"
	if t.Builtin {
		src = "built in"
	}
	about := t.About
	if about == "" {
		about = t.Shape.Label()
	}
	return fmt.Sprintf("%s · %s · %s", t.Name, about, src)
}
