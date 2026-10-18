package api

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/badge"
	"github.com/GlacierHubAB/den/internal/fleet"
	"github.com/GlacierHubAB/den/internal/transcript"
)

func toolAgent(tool, code string) fleet.Agent {
	return fleet.Agent{Tail: []transcript.Entry{{Kind: "text", Text: "rm -rf / please"}, {Kind: "tool", Tool: tool, Code: code}}}
}

// What an agent is doing comes from Den's facts, said as a person would:
// its state first, then its latest tool as an activity, with a file's base
// name and never a program, path, argument, URL, assignment or id; a
// remote agent's text is never used; the task label is the cleaned
// fallback; every text starts with a lead phrase.
func TestDoingText(t *testing.T) {
	bash := func(cmd string) fleet.Agent { return toolAgent("Bash", cmd) }
	cases := []struct {
		name, state, waiting string
		a                    fleet.Agent
		want                 string
	}{
		{"waiting", StateWaiting, "permission", fleet.Agent{}, "Waiting for you: permission"},
		{"waiting, no reason", StateWaiting, "", fleet.Agent{}, "Waiting for you…"},
		{"stalled", StateStalled, "", toolAgent("Edit", "/a/b.go"), "Stuck, needs a look"},
		{"idle", StateIdle, "", toolAgent("Edit", "/a/b.go"), "Idle"},
		{"edit", StateWorking, "", toolAgent("Edit", "/home/someone/secret-project/internal/ui/team.go"), "Editing team.go…"},
		{"write", StateWorking, "", toolAgent("Write", "docs/NOTES.md"), "Writing NOTES.md…"},
		{"read", StateWorking, "", toolAgent("Read", "/etc/passwd"), "Reading passwd…"},
		{"odd name", StateWorking, "", toolAgent("Edit", "/x/evil\x1b[2J"+string(rune(0x202e))+".go"), "Editing a file…"},
		{"read, odd name", StateWorking, "", toolAgent("Read", "/x/a b.go"), "Reading files…"},
		{"search", StateWorking, "", toolAgent("Grep", "password"), "Searching the code…"},
		{"glob", StateWorking, "", toolAgent("Glob", "**/*.go"), "Searching the code…"},
		{"fetch", StateWorking, "", toolAgent("WebFetch", "https://x"), "Fetching a web page…"},
		{"web search", StateWorking, "", toolAgent("WebSearch", "how to"), "Searching the web…"},
		{"helper", StateWorking, "", toolAgent("Agent", "review it"), "Handing work to a helper…"},
		{"unknown tool", StateWorking, "", toolAgent("mcp__x__y", "z"), "Working…"},

		{"cd", StateWorking, "", bash("cd /home/someone/secret"), "Navigating files and folders…"},
		{"ls", StateWorking, "", bash("ls -la"), "Navigating files and folders…"},
		{"find", StateWorking, "", bash("find . -name '*.go'"), "Navigating files and folders…"},
		{"cat", StateWorking, "", bash("cat /home/someone/repo/internal/ui/team.go"), "Reading team.go…"},
		{"head with a count", StateWorking, "", bash("head -n 20 docs/NOTES.md"), "Reading NOTES.md…"},
		{"cat two files", StateWorking, "", bash("cat a.go b.go"), "Reading files…"},
		{"cat a token", StateWorking, "", bash("cat ghp_abc/../../$(id)"), "Reading files…"},
		{"here-document", StateWorking, "", bash("cat <<EOF\nhunter2\n"), "Reading files…"},
		{"grep", StateWorking, "", bash("grep -rn password ."), "Searching the code…"},
		{"rg", StateWorking, "", bash("rg TODO"), "Searching the code…"},
		{"go test", StateWorking, "", bash("go test -count=1 ./internal/ui/"), "Running the tests…"},
		{"npm run test", StateWorking, "", bash("npm run test"), "Running the tests…"},
		{"pytest", StateWorking, "", bash("pytest -x tests/"), "Running the tests…"},
		{"make test", StateWorking, "", bash("make test"), "Running the tests…"},
		{"go build", StateWorking, "", bash("go build ./..."), "Building the project…"},
		{"cargo build", StateWorking, "", bash("cargo build --release"), "Building the project…"},
		{"npm run build", StateWorking, "", bash("npm run build"), "Building the project…"},
		{"make", StateWorking, "", bash("make"), "Building the project…"},
		{"go vet", StateWorking, "", bash("go vet ./..."), "Checking the code…"},
		{"npm install", StateWorking, "", bash("npm install left-pad"), "Installing packages…"},
		{"git status", StateWorking, "", bash("git status --short"), "Looking over the changes…"},
		{"git diff", StateWorking, "", bash("git diff HEAD"), "Looking over the changes…"},
		{"git commit", StateWorking, "", bash("git commit -m 'secret words'"), "Committing changes…"},
		{"git by path", StateWorking, "", bash("/usr/bin/git push origin main"), "Pushing changes…"},
		{"other git", StateWorking, "", bash("git rebase origin/dev"), "Working with git…"},
		{"gh", StateWorking, "", bash("gh pr view 12"), "Working on GitHub…"},
		{"curl", StateWorking, "", bash("curl https://example.com/?token=abc"), "Fetching a web page…"},
		{"docker", StateWorking, "", bash("docker ps"), "Working with containers…"},
		{"systemctl", StateWorking, "", bash("systemctl --user status den"), "Managing services…"},
		{"other command", StateWorking, "", bash("echo hunter"), "Running a command…"},
		{"assignment", StateWorking, "", bash("API_KEY=sk-123 deploy"), "Running a command…"},
		{"assignment then a test", StateWorking, "", bash("GOFLAGS=-count=1 go test ./..."), "Running the tests…"},
		{"cd then test", StateWorking, "", bash("cd /x/y && go test ./... 2>&1 | tail -5"), "Running the tests…"},
		{"cd then nothing", StateWorking, "", bash("cd /x; ls"), "Navigating files and folders…"},
		{"redirect target", StateWorking, "", bash("ls > deploy-prod"), "Navigating files and folders…"},
		{"sudo", StateWorking, "", bash("sudo -n systemctl restart x"), "Managing services…"},
		{"empty", StateWorking, "", bash(""), "Running a command…"},

		{"title is not used", StateWorking, "", fleet.Agent{Title: "model words"}, "Working…"},
		{"task fallback", StateWorking, "", fleet.Agent{Labels: badge.Labels{Task: "fix /home/u/repo login ghp_abcdefghijklmnopqrstuvwxyz0123 bug for a@b.c"}}, "Working on fix login bug for…"},
		{"nothing", StateWorking, "", fleet.Agent{}, "Working…"},
		{"remote", StateWorking, "", fleet.Agent{Host: "build1", Labels: badge.Labels{Task: "steal"}, Tail: toolAgent("Edit", "/a/b.go").Tail}, "Working…"},
	}
	for _, c := range cases {
		got := doingText(c.a, c.state, c.waiting)
		if got != c.want {
			t.Errorf("%s: %q, want %q", c.name, got, c.want)
		}
		if !ledBy(got) {
			t.Errorf("%s: %q starts with no lead phrase", c.name, got)
		}
	}
	long := doingText(fleet.Agent{Labels: badge.Labels{Task: strings.Repeat("word ", 40)}}, StateWorking, "")
	if len([]rune(long)) != doingMax || !strings.HasSuffix(long, "…") || strings.HasSuffix(long, "……") {
		t.Errorf("not capped: %q", long)
	}
	for n := doingMax - 12; n <= doingMax; n++ {
		got := activity(leadWorkingOn, strings.Repeat("a", n))
		if !strings.HasSuffix(got, "…") || strings.HasSuffix(got, "……") || len([]rune(got)) > doingMax {
			t.Errorf("target of %d: %q", n, got)
		}
	}
}

// ledBy: the text is a lead phrase, alone or followed by a target, a
// reason or the ellipsis.
func ledBy(s string) bool {
	for _, l := range DoingLeads {
		if s == l || strings.HasPrefix(s, l+" ") || strings.HasPrefix(s, l+":") || strings.HasPrefix(s, l+"…") {
			return true
		}
	}
	return false
}

// A command never shows its program: whatever is run, the text is a lead
// phrase and the ellipsis, or for a reader a lead and one file's base name
// that was given to it, never the program's own name.
func TestDoingNeverNamesAProgram(t *testing.T) {
	progs := []string{
		"echo", "frobnicate", "python3", "node", "deploy", "rm", "ssh", "scp", "sed", "awk", "jq", "tmux",
		"hunter2", "aws", "psql", "openssl", "base64", "chmod", "./run.sh", "/opt/secret/bin/tool",
		"cd", "ls", "cat", "grep", "go", "npm", "git", "gh", "curl", "docker", "systemctl", "make", "cargo",
	}
	args := []string{"", " --help", " -x 1", " sub", " test", " build", " status", " https://h/p?k=v", " /a/b/c", " x && y", " | z"}
	for _, p := range progs {
		for _, a := range args {
			got := doingText(toolAgent("Bash", p+a), StateWorking, "")
			if !ledBy(got) || !strings.HasSuffix(got, "…") {
				t.Errorf("%q: %q", p+a, got)
				continue
			}
			rest := got
			for _, l := range DoingLeads {
				if strings.HasPrefix(got, l) && len(l) > len(got)-len(rest) {
					rest = strings.TrimPrefix(got, l)
				}
			}
			rest = strings.TrimSuffix(strings.TrimSpace(rest), "…")
			if rest == "" || rest == "files" {
				continue
			}
			// Only a reader names what it was given, never the program.
			base := strings.ToLower(p[strings.LastIndex(p, "/")+1:])
			if !strings.HasPrefix(got, leadReading+" ") || strings.EqualFold(rest, base) || !strings.Contains(a, rest) {
				t.Errorf("%q names %q: %q", p+a, rest, got)
			}
		}
	}
}

// What an agent is doing goes to the console alone: another app's fleet
// carries none, a change in it alone pushes that app nothing, and the
// shared view keeps it for the console.
func TestDoingOnlyForTheConsole(t *testing.T) {
	old := doingEvery
	doingEvery = 0
	t.Cleanup(func() { doingEvery = old })
	h, con := provedConsole(t)
	app := h.dial()
	app.hello()
	// One Taken for every publish, so that only what changes in the agent
	// can make a fleet event.
	taken := time.Now()
	agent := func(state fleet.Status, tool, code string) Snapshot {
		a := fleet.Agent{Name: "Mossy Lemur", Status: state, Tail: []transcript.Entry{{Kind: "tool", Tool: tool, Code: code}}}
		a.Session.SessionID = "s1"
		return Snapshot{Taken: taken, View: ViewAgent, Agents: []fleet.Agent{a}}
	}
	fleetEvent := func(c *client) Fleet {
		t.Helper()
		for {
			l := c.next()
			if l.Event == EventFleet {
				var f Fleet
				json.Unmarshal(l.Data, &f)
				return f
			}
		}
	}
	h.s.Publish(Project(agent(fleet.Working, "Edit", "/src/team.go")))
	for _, c := range []*client{con, app} {
		if l := c.call("fleet.events", nil); !l.OK {
			t.Fatal(l.Error)
		}
	}
	if f := fleetEvent(con); len(f.Agents) != 1 || f.Agents[0].Doing.Text != "Editing team.go…" {
		t.Fatalf("console: %+v", f.Agents)
	}
	if f := fleetEvent(app); len(f.Agents) != 1 || f.Agents[0].Doing.Text != "" || !f.Agents[0].Doing.At.IsZero() {
		t.Fatalf("app: %+v", f.Agents)
	}
	// Only doing changes: the console hears it, the app does not; the next
	// thing the app hears is the state change after it.
	h.s.Publish(Project(agent(fleet.Working, "Read", "/src/b.go")))
	if f := fleetEvent(con); f.Agents[0].Doing.Text != "Reading b.go…" {
		t.Errorf("console after the change: %+v", f.Agents[0].Doing)
	}
	h.s.Publish(Project(agent(fleet.Idle, "Read", "/src/b.go")))
	if f := fleetEvent(app); f.Agents[0].State != StateIdle || f.Agents[0].Doing.Text != "" {
		t.Errorf("app heard %+v first", f.Agents[0])
	}
	if v := h.s.view.Load(); v.Fleet.Agents[0].Doing.Text != "Idle" {
		t.Errorf("the shared view lost doing: %+v", v.Fleet.Agents[0].Doing)
	}
	if f := app.call("fleet.snapshot", nil); strings.Contains(string(f.Result), `"text":"Idle"`) {
		t.Errorf("snapshot to an app: %s", f.Result)
	}
}

// A new text is published once the last change is doingEvery old; At is
// when it changed; agents that went are forgotten.
func TestSettleDoing(t *testing.T) {
	t0 := time.Date(2026, 10, 17, 12, 0, 0, 0, time.UTC)
	seen := map[string]doingSeen{}
	pub := func(text string, at time.Time) Doing {
		ag := []Agent{{ID: "a", Doing: Doing{Text: text}}}
		settleDoing(ag, seen, at)
		return ag[0].Doing
	}
	if d := pub("editing a.go", t0); d.Text != "editing a.go" || !d.At.Equal(t0) {
		t.Fatalf("first %+v", d)
	}
	if d := pub("editing b.go", t0.Add(3*time.Second)); d.Text != "editing a.go" || !d.At.Equal(t0) {
		t.Errorf("changed within the window: %+v", d)
	}
	if d := pub("editing b.go", t0.Add(doingEvery)); d.Text != "editing b.go" || !d.At.Equal(t0.Add(doingEvery)) {
		t.Errorf("not changed after the window: %+v", d)
	}
	if d := pub("editing b.go", t0.Add(time.Hour)); !d.At.Equal(t0.Add(doingEvery)) {
		t.Errorf("At moved with no change: %+v", d)
	}
	settleDoing(nil, seen, t0.Add(2*time.Hour))
	if len(seen) != 0 {
		t.Error("a gone agent was kept")
	}
}
