package api

import (
	"path/filepath"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/sanitize"
)

// Doing is what an agent is doing now, in one short line, for the dock:
// built from Den's own facts (its state, its latest tool and that tool's
// target, as a base name or a command word), never from what the agent
// said, except its task label when there is nothing else, cleaned.
type Doing struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"` // when Text last changed
}

// DoingLeads are the phrases a Doing text starts with, a closed set, so
// a console can style them; what follows is the target, if any.
var DoingLeads = []string{
	"waiting for you", "stalled", "idle",
	"editing", "writing", "reading", "running", "searching", "fetching", "delegating",
	"working on", "working",
}

// Caps on a Doing text.
const (
	doingMax   = 80
	targetMax  = 40
	doingEvery = 10 * time.Second // the least time between two changes
)

// doingText is an agent's Doing text from Den's facts. A remote agent's
// tail is its host's report, so only its state is used.
func doingText(a fleet.Agent, state, waiting string) string {
	switch state {
	case StateWaiting:
		if waiting != "" {
			return "waiting for you: " + sanitize.Label(waiting, 20)
		}
		return "waiting for you"
	case StateStalled:
		return "stalled"
	case StateIdle:
		return "idle"
	}
	if a.Host != "" {
		return "working"
	}
	for i := len(a.Tail) - 1; i >= 0; i-- {
		e := a.Tail[i]
		if e.Kind != "tool" {
			continue
		}
		if s := toolDoing(e.Tool, e.Code); s != "" {
			return s
		}
		break
	}
	if task := cleanTask(a.Task()); task != "" {
		return clip("working on " + task)
	}
	return "working"
}

// toolDoing says a tool call as a lead phrase and its target: a file's
// base name, or a command's first word and a plain sub-command. No path,
// argument, URL or id is ever kept.
func toolDoing(tool, code string) string {
	switch tool {
	case "Edit", "MultiEdit", "NotebookEdit":
		return withTarget("editing", baseName(code))
	case "Write":
		return withTarget("writing", baseName(code))
	case "Read":
		return withTarget("reading", baseName(code))
	case "Bash":
		return withTarget("running", commandWords(code))
	case "Grep", "Glob":
		return "searching"
	case "WebFetch", "WebSearch":
		return "fetching"
	case "Task", "Agent":
		return "delegating"
	}
	return ""
}

func withTarget(lead, target string) string {
	if target == "" {
		return lead
	}
	return clip(lead + " " + target)
}

// plainWord is a sub-command: lower-case letters and hyphens only.
var plainWord = regexp.MustCompile(`^[a-z][a-z-]{0,19}$`)

// fileName is a base name that says nothing beyond itself.
var fileName = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,40}$`)

// baseName is a path's last element, when it is a plain file name.
func baseName(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	b := filepath.Base(path)
	if !fileName.MatchString(b) || b == "." || b == ".." {
		return ""
	}
	return b
}

// commandWords is a command's program (its base name) and, when the next
// word is a plain sub-command, that word: "go test", "git rebase".
func commandWords(cmd string) string {
	f := strings.Fields(sanitize.Line(cmd))
	if len(f) == 0 {
		return ""
	}
	prog := baseName(f[0])
	if prog == "" || strings.Contains(f[0], "=") {
		return ""
	}
	if len(f) > 1 && plainWord.MatchString(f[1]) {
		return prog + " " + f[1]
	}
	return prog
}

// idLike is a word that may be an id, a token or a secret: long, or with
// digits and letters mixed in a long run.
var idLike = regexp.MustCompile(`[0-9a-fA-F]{8,}|[A-Za-z0-9_-]{24,}`)

// cleanTask is an agent's task label, the fallback: one clean line with
// every word that could be a path, a URL, an address, an assignment or an
// id dropped.
func cleanTask(task string) string {
	var keep []string
	for _, w := range strings.Fields(sanitize.Label(task, 200)) {
		if strings.ContainsAny(w, `/\:=@<>`) || idLike.MatchString(w) {
			continue
		}
		keep = append(keep, w)
	}
	return strings.Join(keep, " ")
}

// clip caps a Doing text at doingMax runes, with "…" when cut.
func clip(s string) string {
	if utf8.RuneCountInString(s) <= doingMax {
		return s
	}
	return string([]rune(s)[:doingMax-1]) + "…"
}

// doingSeen is the Doing Den last published for an agent.
type doingSeen struct {
	text string
	at   time.Time
}

// settleDoing gives each agent its Doing: the new text once it has held
// for doingEvery since the last change, else the one before; At is when
// the published text last changed. It keeps seen up to date.
func settleDoing(agents []Agent, seen map[string]doingSeen, now time.Time) {
	live := map[string]bool{}
	for i := range agents {
		a := &agents[i]
		live[a.ID] = true
		want := a.Doing.Text
		prev, ok := seen[a.ID]
		switch {
		case !ok:
			seen[a.ID] = doingSeen{want, now}
		case prev.text != want && now.Sub(prev.at) >= doingEvery:
			seen[a.ID] = doingSeen{want, now}
		}
		a.Doing = Doing{Text: seen[a.ID].text, At: seen[a.ID].at.UTC()}
	}
	for id := range seen {
		if !live[id] {
			delete(seen, id)
		}
	}
}
