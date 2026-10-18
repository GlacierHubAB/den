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

// Doing is what an agent is doing now, in one short line for the dock,
// said the way a person would say it: built from Den's own facts (its
// state, its latest tool and, for a file, that file's base name), never
// from what the agent said, except its task label when there is nothing
// else, cleaned. A program's name is never shown.
type Doing struct {
	Text string    `json:"text"`
	At   time.Time `json:"at"` // when Text last changed
}

// The lead phrases. A Doing text is one of them, then for some a target
// (a file's base name, the task label) or, for leadWaiting, ": " and the
// reason, then "…" for an activity.
const (
	leadWaiting    = "Waiting for you"
	leadStalled    = "Stuck, needs a look"
	leadIdle       = "Idle"
	leadNavigating = "Navigating files and folders"
	leadReading    = "Reading"
	leadEditing    = "Editing"
	leadWriting    = "Writing"
	leadSearching  = "Searching the code"
	leadTesting    = "Running the tests"
	leadChecking   = "Checking the code"
	leadBuilding   = "Building the project"
	leadInstalling = "Installing packages"
	leadReviewing  = "Looking over the changes"
	leadCommitting = "Committing changes"
	leadPushing    = "Pushing changes"
	leadGit        = "Working with git"
	leadGitHub     = "Working on GitHub"
	leadFetching   = "Fetching a web page"
	leadWeb        = "Searching the web"
	leadHelper     = "Handing work to a helper"
	leadContainers = "Working with containers"
	leadServices   = "Managing services"
	leadCommand    = "Running a command"
	leadWorkingOn  = "Working on"
	leadWorking    = "Working"
)

// DoingLeads are the phrases a Doing text starts with, a closed set, so
// a console can style them.
var DoingLeads = []string{
	leadWaiting, leadStalled, leadIdle,
	leadNavigating, leadReading, leadEditing, leadWriting, leadSearching,
	leadTesting, leadChecking, leadBuilding, leadInstalling,
	leadReviewing, leadCommitting, leadPushing, leadGit, leadGitHub,
	leadFetching, leadWeb, leadHelper, leadContainers, leadServices, leadCommand,
	leadWorkingOn, leadWorking,
}

// ellipsis ends an activity.
const ellipsis = "…"

// Caps on a Doing text.
const (
	doingMax  = 80
	targetMax = 40
)

// doingText is an agent's Doing text from Den's facts. A remote agent's
// tail is its host's report, so only its state is used.
func doingText(a fleet.Agent, state, waiting string) string {
	switch state {
	case StateWaiting:
		if waiting != "" {
			return leadWaiting + ": " + sanitize.Label(waiting, 20)
		}
		return leadWaiting + ellipsis
	case StateStalled:
		return leadStalled
	case StateIdle:
		return leadIdle
	}
	if a.Host != "" {
		return leadWorking + ellipsis
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
	// Only the task label: a window title is the model's own words.
	if task := cleanTask(a.Labels.Task); task != "" {
		return activity(leadWorkingOn, task)
	}
	return leadWorking + ellipsis
}

// toolDoing says a tool call as an activity, with a file's base name for
// the file tools. No path, argument, URL or id is ever kept.
func toolDoing(tool, code string) string {
	switch tool {
	case "Edit", "MultiEdit", "NotebookEdit":
		return fileActivity(leadEditing, baseName(code))
	case "Write":
		return fileActivity(leadWriting, baseName(code))
	case "Read":
		return fileActivity(leadReading, baseName(code))
	case "Bash":
		return commandDoing(code)
	case "Grep", "Glob":
		return activity(leadSearching, "")
	case "WebFetch":
		return activity(leadFetching, "")
	case "WebSearch":
		return activity(leadWeb, "")
	case "Task", "Agent":
		return activity(leadHelper, "")
	}
	return ""
}

// activity is a lead, its target if any, and the ellipsis, capped.
func activity(lead, target string) string {
	if target == "" {
		return lead + ellipsis
	}
	r := []rune(lead + " " + target)
	if len(r) >= doingMax {
		r = r[:doingMax-1]
	}
	return string(r) + ellipsis
}

// fileActivity names the file, or says "a file" without one.
func fileActivity(lead, name string) string {
	if name == "" {
		if lead == leadReading {
			return activity(lead, "files")
		}
		return activity(lead, "a file")
	}
	return activity(lead, name)
}

// commandDoing says a shell command as an activity. A command line may
// chain several commands; the first that is more than moving around says
// it ("cd x && go test" is running the tests).
func commandDoing(cmd string) string {
	line := sanitize.Line(cmd)
	// A here-document's text follows the command as if it were arguments:
	// with one, a reader names no file.
	inline := strings.Contains(line, "<<")
	nav := false
	for _, seg := range commandSegments(line) {
		lead, name := commandLead(seg)
		switch lead {
		case "":
			continue
		case leadNavigating:
			nav = true
			continue
		case leadReading:
			if inline {
				name = ""
			}
			return fileActivity(lead, name)
		}
		return activity(lead, "")
	}
	if nav {
		return activity(leadNavigating, "")
	}
	return activity(leadCommand, "")
}

// redirect is a redirection and its target, which is not a command.
var redirect = regexp.MustCompile(`[0-9]*(>>|<<<|<<|>|<)&?\s*[^\s|;&<>()]*`)

// shellOp splits a command line at the shell's operators.
var shellOp = regexp.MustCompile(`&&|\|\||\$\(|[|;&()` + "`" + `]`)

// commandSegments are a command line's simple commands, each as words.
func commandSegments(cmd string) [][]string {
	var segs [][]string
	for _, part := range shellOp.Split(redirect.ReplaceAllString(cmd, " "), -1) {
		if f := strings.Fields(part); len(f) > 0 {
			segs = append(segs, f)
		}
	}
	return segs
}

// wrappers run the command after them, as do the shell's keywords; their
// own flags are skipped.
var wrappers = map[string]bool{
	"sudo": true, "env": true, "time": true, "nice": true, "nohup": true, "command": true, "exec": true, "timeout": true,
	"if": true, "then": true, "else": true, "elif": true, "do": true, "while": true, "until": true, "!": true,
}

// program is a simple command's program, as a base name, and the words
// after it, with leading assignments and wrappers skipped.
func program(words []string) (string, []string) {
	for i := 0; i < len(words); i++ {
		w := words[i]
		if strings.Contains(w, "=") || strings.HasPrefix(w, "-") {
			continue
		}
		p := strings.ToLower(filepath.Base(w))
		if wrappers[p] {
			if p == "timeout" && i+1 < len(words) {
				i++ // its duration
			}
			continue
		}
		return p, words[i+1:]
	}
	return "", nil
}

// navigators only move around or list what is there.
var navigators = map[string]bool{"cd": true, "ls": true, "find": true, "tree": true, "pwd": true, "fd": true, "pushd": true, "popd": true, "eza": true, "exa": true}

// readers show a file.
var readers = map[string]bool{"cat": true, "head": true, "tail": true, "less": true, "more": true, "bat": true, "wc": true}

// searchers search text.
var searchers = map[string]bool{"grep": true, "rg": true, "ag": true, "ack": true, "egrep": true, "fgrep": true}

// testers run tests whatever their arguments.
var testers = map[string]bool{"pytest": true, "jest": true, "vitest": true, "gotestsum": true}

// checkers vet code whatever their arguments.
var checkers = map[string]bool{"gofmt": true, "golangci-lint": true, "staticcheck": true, "eslint": true, "ruff": true, "mypy": true, "shellcheck": true, "prettier": true}

// containers and services.
var containers = map[string]bool{"docker": true, "podman": true, "kubectl": true, "docker-compose": true, "helm": true}

// commandLead is the lead phrase for one simple command, and for a reader
// the base name of the one file it shows. Empty for none.
func commandLead(words []string) (lead, name string) {
	prog, args := program(words)
	sub := ""
	if s, _ := program(args); s != "" {
		sub = s
	}
	switch {
	case prog == "":
		return "", ""
	case navigators[prog]:
		return leadNavigating, ""
	case readers[prog]:
		return leadReading, readFile(args)
	case searchers[prog]:
		return leadSearching, ""
	case testers[prog]:
		return leadTesting, ""
	case checkers[prog]:
		return leadChecking, ""
	case containers[prog]:
		return leadContainers, ""
	case prog == "systemctl" || prog == "journalctl":
		return leadServices, ""
	case prog == "gh":
		return leadGitHub, ""
	case prog == "curl" || prog == "wget":
		return leadFetching, ""
	case prog == "tsc":
		return leadBuilding, ""
	case prog == "git":
		switch sub {
		case "status", "diff", "log", "show", "blame":
			return leadReviewing, ""
		case "add", "commit":
			return leadCommitting, ""
		case "push":
			return leadPushing, ""
		}
		return leadGit, ""
	case prog == "go":
		switch sub {
		case "test":
			return leadTesting, ""
		case "build", "install":
			return leadBuilding, ""
		case "vet", "fmt":
			return leadChecking, ""
		case "get", "mod":
			return leadInstalling, ""
		}
	case prog == "cargo":
		switch sub {
		case "test":
			return leadTesting, ""
		case "build":
			return leadBuilding, ""
		case "check", "clippy", "fmt":
			return leadChecking, ""
		case "add", "install":
			return leadInstalling, ""
		}
	case prog == "npm" || prog == "pnpm" || prog == "yarn" || prog == "bun":
		if sub == "run" {
			sub, _ = program(args[1:])
		}
		switch sub {
		case "test":
			return leadTesting, ""
		case "build":
			return leadBuilding, ""
		case "lint", "check", "typecheck":
			return leadChecking, ""
		case "install", "i", "ci", "add":
			return leadInstalling, ""
		}
	case prog == "pip" || prog == "pip3" || prog == "uv":
		if sub == "install" || sub == "sync" || sub == "add" {
			return leadInstalling, ""
		}
	case prog == "make":
		if sub == "test" || sub == "check" {
			return leadTesting, ""
		}
		return leadBuilding, ""
	}
	return leadCommand, ""
}

// readFile is the base name of the one file a reader is given, when it
// is given exactly one plain name and no number (a flag's value).
func readFile(args []string) string {
	name := ""
	for _, a := range args {
		if strings.HasPrefix(a, "-") || strings.Trim(a, "0123456789") == "" {
			continue
		}
		if name != "" {
			return ""
		}
		name = a
	}
	return baseName(name)
}

// fileName is a base name that says nothing beyond itself.
var fileName = regexp.MustCompile(`^[A-Za-z0-9._+-]{1,40}$`)

// baseName is a path's last element, when it is a plain file name that
// does not look like an id, a token or a secret.
func baseName(path string) string {
	path = strings.TrimSpace(path)
	if path == "" {
		return ""
	}
	b := filepath.Base(path)
	if !fileName.MatchString(b) || b == "." || b == ".." || idLike.MatchString(b) {
		return ""
	}
	return b
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

// doingEvery is the least time between two changes of an agent's Doing;
// tests shorten it.
var doingEvery = 10 * time.Second

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
