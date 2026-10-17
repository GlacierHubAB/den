// Package tuning is the orchestrator's bounded say over Den's process
// numbers and template defaults, per project: a fixed list of keys, each
// within bounds Den checks when a change is made and again when the file is
// read. Den alone writes the file, in its state folder; the human's config
// and templates are never written, and a value the human set in config.toml
// always wins. Nothing here reaches a prompt, Den's code, the hard limits,
// the permission mode, God mode, console powers or budgets.
package tuning

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
	"unicode/utf8"

	"github.com/GlacierHubAB/den/internal/mission"
	"github.com/GlacierHubAB/den/internal/sanitize"
	"github.com/GlacierHubAB/den/internal/spawn"
	"github.com/GlacierHubAB/den/internal/templates"
)

// The team keys and their bounds.
var teamBounds = map[string][2]int{
	"team.max_agents":   {1, 6},
	"team.branch_hours": {4, 72},
	"team.behind_max":   {10, 100},
	"team.idle_minutes": {15, 240},
}

// TeamKeys are the team keys, sorted.
func TeamKeys() []string {
	var out []string
	for k := range teamBounds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Caps on what a template's tuning adds.
const (
	MaxText        = 300 // an added criterion or constraint, in characters
	MaxAddedCrit   = 3
	MaxAddedCons   = 6
	MaxAddedRoles  = 3
	MinCadence     = 30 * time.Minute
	MaxProposal    = 4096 // prompt.proposal, in bytes
	MaxPerDay      = 5    // changes a day per project
	maxLog         = 50
	maxFileBytes   = 64 << 10
	ProposalKey    = "prompt.proposal"
	templatePrefix = "template."
)

// Template is what tuning adds to or changes in one template.
type Template struct {
	AddCriteria    []string          `json:"add_criteria,omitempty"`
	AddConstraints []string          `json:"add_constraints,omitempty"`
	Models         map[string]string `json:"models,omitempty"` // role -> model
	Cadence        string            `json:"cadence,omitempty"`
	AddRoles       []mission.Member  `json:"add_roles,omitempty"`
}

// File is one project's tuning.
type File struct {
	V         int                 `json:"v"`
	Team      map[string]int      `json:"team,omitempty"`
	Templates map[string]Template `json:"templates,omitempty"`
	// Log is when each change was made, for the daily limit.
	Log []Made `json:"log,omitempty"`
}

// Made is one change, by key and time.
type Made struct {
	Key string    `json:"key"`
	At  time.Time `json:"at"`
}

// Change is one change as made: the key, the value before (Had false when
// there was none) and after. Undo takes it back.
type Change struct {
	Key string `json:"key"`
	Old string `json:"old,omitempty"`
	Had bool   `json:"had,omitempty"`
	New string `json:"new"`
}

// Context is what a change is checked against: the template, by name, as
// it ships or the human saved it (ok false when there is none), and the
// project's max_agents in force.
type Context struct {
	Template  func(name string) (templates.Template, bool)
	MaxAgents int
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

// Path is tuning.json for the project with repo id (process.RepoID, already
// a safe file name) in Den's state folder.
func Path(stateDir, repoID string) string {
	return filepath.Join(stateDir, "projects", repoID, "tuning.json")
}

// Load reads the file at path. A file that is missing gives an empty
// tuning; one that cannot be read whole or parsed gives an empty tuning and
// a note (never part of it); a key out of bounds or unknown is left out
// with a note.
func Load(path string) (File, []string) {
	f := File{V: 1}
	fh, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return f, nil
	}
	if err != nil {
		return f, []string{"tuning not read: " + err.Error()}
	}
	defer fh.Close()
	if fi, err := fh.Stat(); err != nil || !fi.Mode().IsRegular() {
		return f, []string{"tuning not read: not a regular file"}
	}
	raw, err := io.ReadAll(io.LimitReader(fh, maxFileBytes+1))
	if err != nil || len(raw) > maxFileBytes {
		return f, []string{"tuning not read: too large or unreadable"}
	}
	var in File
	if err := json.Unmarshal(raw, &in); err != nil {
		return f, []string{"tuning ignored: it does not parse"}
	}
	var notes []string
	for k, v := range in.Team {
		if b, ok := teamBounds[k]; !ok || v < b[0] || v > b[1] {
			notes = append(notes, "tuning key "+strconv.Quote(sanitize.Label(k, 60))+" ignored: unknown or out of bounds")
			continue
		}
		if f.Team == nil {
			f.Team = map[string]int{}
		}
		f.Team[k] = v
	}
	for name, t := range in.Templates {
		if !templates.ValidName(name) {
			notes = append(notes, "tuning for template "+strconv.Quote(sanitize.Label(name, 40))+" ignored: not a template name")
			continue
		}
		clean, dropped := cleanTemplate(t)
		for _, d := range dropped {
			notes = append(notes, "tuning of template "+name+": "+d+" ignored: out of bounds")
		}
		if f.Templates == nil {
			f.Templates = map[string]Template{}
		}
		f.Templates[name] = clean
	}
	for _, mk := range in.Log {
		if mk.Key != "" && !mk.At.IsZero() {
			f.Log = append(f.Log, Made{Key: sanitize.Label(mk.Key, 80), At: mk.At})
		}
	}
	sort.Strings(notes)
	return f, notes
}

// cleanTemplate keeps what is within bounds and names what it left out.
func cleanTemplate(t Template) (Template, []string) {
	var out Template
	var dropped []string
	for _, c := range t.AddCriteria {
		if s, ok := cleanText(c); ok && len(out.AddCriteria) < MaxAddedCrit {
			out.AddCriteria = append(out.AddCriteria, s)
		} else {
			dropped = append(dropped, "a criterion")
		}
	}
	for _, c := range t.AddConstraints {
		if s, ok := cleanText(c); ok && len(out.AddConstraints) < MaxAddedCons {
			out.AddConstraints = append(out.AddConstraints, s)
		} else {
			dropped = append(dropped, "a constraint")
		}
	}
	for role, model := range t.Models {
		if validRole(role) && spawn.ValidModel(model) {
			if out.Models == nil {
				out.Models = map[string]string{}
			}
			out.Models[role] = model
		} else {
			dropped = append(dropped, "a model")
		}
	}
	if t.Cadence != "" {
		if cadenceOK(t.Cadence) {
			out.Cadence = t.Cadence
		} else {
			dropped = append(dropped, "the cadence")
		}
	}
	for _, r := range t.AddRoles {
		if validRole(r.Role) && spawn.ValidModel(r.Model) && (r.Under == "" || validRole(r.Under)) && len(out.AddRoles) < MaxAddedRoles {
			out.AddRoles = append(out.AddRoles, r)
		} else {
			dropped = append(dropped, "a role")
		}
	}
	return out, dropped
}

// cleanText is an added criterion or constraint: one clean line, whole, at
// most MaxText characters, else refused (never cut).
func cleanText(s string) (string, bool) {
	if s != sanitize.Line(s) {
		return "", false
	}
	s = strings.TrimSpace(s)
	return s, s != "" && utf8.RuneCountInString(s) <= MaxText
}

// validRole is a role as a template names it: one clean line of a few
// words.
func validRole(r string) bool {
	return r != "" && r == sanitize.Label(r, 60) && strings.TrimSpace(r) == r
}

// cadenceOK reports whether a cadence parses and never fires twice within
// MinCadence, over its next several runs.
func cadenceOK(c string) bool {
	at := time.Date(2026, 1, 5, 0, 0, 0, 0, time.UTC)
	prev, err := mission.Next(c, at)
	if err != nil {
		return false
	}
	for range 50 {
		next, err := mission.Next(c, prev)
		if err != nil || next.Sub(prev) < MinCadence {
			return false
		}
		prev = next
	}
	return true
}

// ProposalText is a prompt.proposal as Den shows it: the text through
// sanitize.Text, whole, at most MaxProposal bytes, else refused.
func ProposalText(s string) (string, error) {
	s = strings.TrimSpace(sanitize.Text(s))
	if s == "" {
		return "", errors.New("the proposal is empty")
	}
	if len(s) > MaxProposal {
		return "", fmt.Errorf("the proposal is %d bytes; at most %d (Den never cuts it)", len(s), MaxProposal)
	}
	return s, nil
}

// Allowed reports whether a change to key may be made now: at most
// MaxPerDay a day, one a day per key. An undo is never counted.
func (f File) Allowed(key string, now time.Time) error {
	n := 0
	for _, mk := range f.Log {
		if now.Sub(mk.At) >= 24*time.Hour {
			continue
		}
		n++
		if mk.Key == key {
			return fmt.Errorf("%s was already changed in the last day", key)
		}
	}
	if n >= MaxPerDay {
		return fmt.Errorf("already %d changes in the last day for this project", MaxPerDay)
	}
	return nil
}

// Apply makes one change to f, checked against its bounds and ctx, and
// logs it. f is unchanged on an error.
func (f *File) Apply(key, value string, ctx Context, now time.Time) (Change, error) {
	if err := f.Allowed(key, now); err != nil {
		return Change{}, err
	}
	value = strings.TrimSpace(value)
	ch := Change{Key: key, New: value}
	if b, ok := teamBounds[key]; ok {
		n, err := strconv.Atoi(value)
		if err != nil || n < b[0] || n > b[1] {
			return Change{}, fmt.Errorf("%s is a whole number from %d to %d", key, b[0], b[1])
		}
		old, had := f.Team[key]
		ch.Old, ch.Had = strconv.Itoa(old), had
		if !had {
			ch.Old = ""
		}
		if f.Team == nil {
			f.Team = map[string]int{}
		}
		f.Team[key] = n
		f.log(key, now)
		return ch, nil
	}
	rest, ok := strings.CutPrefix(key, templatePrefix)
	if !ok {
		return Change{}, fmt.Errorf("unknown key %s", strconv.Quote(sanitize.Label(key, 60)))
	}
	name, field, _ := strings.Cut(rest, ".")
	base, ok := ctx.Template(name)
	if !templates.ValidName(name) || !ok {
		return Change{}, fmt.Errorf("no template %s", strconv.Quote(sanitize.Label(name, 40)))
	}
	t := f.Templates[name]
	t = cloneTemplate(t)
	switch {
	case field == "add_criterion" || field == "add_constraint":
		s, ok := cleanText(value)
		if !ok {
			return Change{}, fmt.Errorf("%s is one line of at most %d characters, whole", field, MaxText)
		}
		list, limit := &t.AddCriteria, MaxAddedCrit
		if field == "add_constraint" {
			list, limit = &t.AddConstraints, MaxAddedCons
		}
		if len(*list) >= limit {
			return Change{}, fmt.Errorf("template %s already has %d added", name, limit)
		}
		if slices.ContainsFunc(*list, func(x string) bool { return strings.EqualFold(x, s) }) {
			return Change{}, errors.New("already added")
		}
		*list = append(*list, s)
		ch.New = s
	case strings.HasPrefix(field, "model."):
		role := strings.TrimPrefix(field, "model.")
		if !hasRole(base, t, role) {
			return Change{}, fmt.Errorf("template %s has no role %s", name, strconv.Quote(sanitize.Label(role, 60)))
		}
		if !spawn.ValidModel(value) {
			return Change{}, fmt.Errorf("model is one of %s", strings.Join(spawn.ModelNames(), ", "))
		}
		ch.Old, ch.Had = ModelOf(base, t, role), true
		if t.Models == nil {
			t.Models = map[string]string{}
		}
		t.Models[role] = value
	case field == "cadence":
		if !cadenceOK(value) {
			return Change{}, fmt.Errorf("cadence must parse and be at least %s apart", MinCadence)
		}
		ch.Old, ch.Had = t.Cadence, t.Cadence != ""
		if !ch.Had {
			ch.Old, ch.Had = base.Cadence, base.Cadence != ""
		}
		t.Cadence = value
	case field == "add_role":
		r, err := ParseRole(value)
		if err != nil {
			return Change{}, err
		}
		if hasRole(base, t, r.Role) {
			return Change{}, fmt.Errorf("template %s already has the role %s", name, r.Role)
		}
		if r.Under != "" && !hasRole(base, t, r.Under) {
			return Change{}, fmt.Errorf("template %s has no role %s to report to", name, r.Under)
		}
		if len(t.AddRoles) >= MaxAddedRoles || len(base.Team)+len(t.AddRoles)+1 > ctx.MaxAgents {
			return Change{}, fmt.Errorf("the team would pass the project's cap of %d agents", ctx.MaxAgents)
		}
		t.AddRoles = append(t.AddRoles, r)
		ch.New = RoleText(r)
	default:
		return Change{}, fmt.Errorf("unknown key %s", strconv.Quote(sanitize.Label(key, 60)))
	}
	if f.Templates == nil {
		f.Templates = map[string]Template{}
	}
	f.Templates[name] = t
	f.log(key, now)
	return ch, nil
}

// Undo takes a change back: the value before, if the value is still the
// one the change made; an added entry, if it is still there. Undoing twice
// does nothing more. It reports whether anything changed.
func (f *File) Undo(ch Change) bool {
	if _, ok := teamBounds[ch.Key]; ok {
		if cur, ok := f.Team[ch.Key]; !ok || strconv.Itoa(cur) != ch.New {
			return false
		}
		if ch.Had {
			n, _ := strconv.Atoi(ch.Old)
			f.Team[ch.Key] = n
		} else {
			delete(f.Team, ch.Key)
		}
		return true
	}
	rest, _ := strings.CutPrefix(ch.Key, templatePrefix)
	name, field, _ := strings.Cut(rest, ".")
	t, ok := f.Templates[name]
	if !ok {
		return false
	}
	t = cloneTemplate(t)
	changed := false
	remove := func(list []string) []string {
		if i := slices.Index(list, ch.New); i >= 0 {
			changed = true
			return slices.Delete(list, i, i+1)
		}
		return list
	}
	switch {
	case field == "add_criterion":
		t.AddCriteria = remove(t.AddCriteria)
	case field == "add_constraint":
		t.AddConstraints = remove(t.AddConstraints)
	case strings.HasPrefix(field, "model."):
		role := strings.TrimPrefix(field, "model.")
		if t.Models[role] == ch.New {
			changed = true
			delete(t.Models, role)
			if ch.Had && ch.Old != "" {
				t.Models[role] = ch.Old
			}
		}
	case field == "cadence":
		if t.Cadence == ch.New {
			changed = true
			t.Cadence = ""
			if ch.Had {
				t.Cadence = ch.Old
			}
		}
	case field == "add_role":
		for i, r := range t.AddRoles {
			if RoleText(r) == ch.New {
				changed = true
				t.AddRoles = slices.Delete(t.AddRoles, i, i+1)
				break
			}
		}
	}
	if changed {
		f.Templates[name] = t
	}
	return changed
}

func (f *File) log(key string, now time.Time) {
	f.Log = append(f.Log, Made{Key: key, At: now})
	if n := len(f.Log) - maxLog; n > 0 {
		f.Log = append([]Made(nil), f.Log[n:]...)
	}
}

func cloneTemplate(t Template) Template {
	t.AddCriteria = slices.Clone(t.AddCriteria)
	t.AddConstraints = slices.Clone(t.AddConstraints)
	t.AddRoles = slices.Clone(t.AddRoles)
	if t.Models != nil {
		m := map[string]string{}
		for k, v := range t.Models {
			m[k] = v
		}
		t.Models = m
	}
	return t
}

func hasRole(base templates.Template, t Template, role string) bool {
	for _, r := range base.Team {
		if r.Role == role {
			return true
		}
	}
	for _, r := range t.AddRoles {
		if r.Role == role {
			return true
		}
	}
	return false
}

// ModelOf is the model a role runs on: tuned, else added, else the
// template's own.
func ModelOf(base templates.Template, t Template, role string) string {
	if m := t.Models[role]; m != "" {
		return m
	}
	for _, r := range append(slices.Clone(t.AddRoles), base.Team...) {
		if r.Role == role {
			return r.Model
		}
	}
	return ""
}

// ParseRole reads an add_role value: "role on model", optionally
// "under lead role" after it.
func ParseRole(s string) (mission.Member, error) {
	bad := errors.New(`add_role is "<role> on <model>", or "<role> on <model> under <lead role>"`)
	role, rest, ok := strings.Cut(strings.TrimSpace(s), " on ")
	if !ok {
		return mission.Member{}, bad
	}
	model, under, _ := strings.Cut(strings.TrimSpace(rest), " under ")
	r := mission.Member{Role: strings.TrimSpace(role), Model: strings.TrimSpace(model), Under: strings.TrimSpace(under)}
	if !validRole(r.Role) || !spawn.ValidModel(r.Model) || (r.Under != "" && !validRole(r.Under)) {
		return mission.Member{}, bad
	}
	return r, nil
}

// RoleText is an added role as its change names it.
func RoleText(r mission.Member) string {
	s := r.Role + " on " + r.Model
	if r.Under != "" {
		s += " under " + r.Under
	}
	return s
}

// Seed applies a template's tuning to it as Den seeds a proposal: added
// criteria and constraints marked as the orchestrator's tuning, tuned
// models, the cadence, and added roles while the team stays within
// maxAgents (the cap in force now). It reports the roles it left out.
func (f File) Seed(t templates.Template, maxAgents int) (templates.Template, []string) {
	tt, ok := f.Templates[t.Name]
	if !ok {
		return t, nil
	}
	t.Constraints = slices.Clone(t.Constraints)
	t.Team = slices.Clone(t.Team)
	for _, c := range tt.AddConstraints {
		t.Constraints = append(t.Constraints, c+" "+Mark)
	}
	if tt.Cadence != "" {
		t.Cadence = tt.Cadence
	}
	var left []string
	for _, r := range tt.AddRoles {
		if len(t.Team)+1 > maxAgents {
			left = append(left, r.Role)
			continue
		}
		t.Team = append(t.Team, r)
	}
	for i := range t.Team {
		if m := tt.Models[t.Team[i].Role]; m != "" {
			t.Team[i].Model = m
		}
	}
	return t, left
}

// Criteria are the criteria tuning adds to a template, marked.
func (f File) Criteria(name string) []string {
	var out []string
	for _, c := range f.Templates[name].AddCriteria {
		out = append(out, c+" "+Mark)
	}
	return out
}

// Mark follows each criterion and constraint tuning added, so the human
// sees where it came from before ^Y.
const Mark = "(added by the orchestrator, tuning)"

// CostWord says which way a model change moves the cost: "haiku → opus,
// costs more".
func CostWord(old, new string) string {
	rank := func(m string) int {
		return slices.IndexFunc(spawn.Models, func(x struct{ Name, When string }) bool { return x.Name == m })
	}
	s := orNone(old) + " → " + new
	switch a, b := rank(old), rank(new); {
	case a < 0 || b > a:
		return s + ", costs more"
	case b < a:
		return s + ", costs less"
	}
	return s
}

func orNone(s string) string {
	if s == "" {
		return "none"
	}
	return s
}

// Save writes f to path, which only Den reads and writes: the folder
// 0700, the file 0600, replaced whole by a rename.
func Save(path string, f File) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if err := os.Chmod(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f.V = 1
	raw, err := json.MarshalIndent(f, "", "  ")
	if err != nil {
		return err
	}
	tmp, err := os.CreateTemp(filepath.Dir(path), ".tuning-*")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(append(raw, '\n')); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return os.Rename(tmp.Name(), path)
}
