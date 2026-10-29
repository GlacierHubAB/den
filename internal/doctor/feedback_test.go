package doctor

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/stats"
)

const stateHome = home + "/.local/state/den/"

// fbEnv is healthy() with den feedback's seams, and its state files read
// from files; a file not there is missing.
func fbEnv(files map[string]string) Env {
	e := healthy()
	e.DenVersion = "v0.6.2"
	e.GoVersion = "go1.26.9"
	e.Platform = "linux/amd64"
	e.Kernel = func() (string, error) { return "7.2.5-3-omarchy\n", nil }
	e.OSRelease = func() ([]byte, error) { return []byte("NAME=\"Arch Linux\"\nID=arch\nBUILD_ID=rolling\n"), nil }
	e.Stats = func(from, to time.Time) stats.Report { return stats.Report{} }
	e.Names = func() []string { return nil }
	prev := e.Tail
	e.Tail = func(p string, n int64) ([]byte, error) {
		if s, ok := files[p]; ok {
			return []byte(s), nil
		}
		if strings.HasPrefix(p, stateHome) {
			return nil, &fs.PathError{Op: "open", Path: p, Err: fs.ErrNotExist}
		}
		return prev(p, n)
	}
	return e
}

// hookLine is a hooks.log line d before e.Now.
func hookLine(e Env, d time.Duration, rest string) string {
	return e.Now.Add(-d).Format("2006-01-02 15:04:05") + " " + rest + "\n"
}

// eventLine is an events.jsonl line d before e.Now.
func eventLine(e Env, d time.Duration, fields string) string {
	return fmt.Sprintf(`{"v":1,"time":%q,"session":"0123abcd-ffff","agent":"Fizzy Wombat",%s}`+"\n", e.Now.Add(-d).UTC().Format(time.RFC3339Nano), fields)
}

func section(r Report, id string) string { return strings.Join(r.section(id).Lines, "\n") }

func TestShownDen(t *testing.T) {
	for in, want := range map[string]string{
		"v0.6.2":                        "v0.6.2",
		"0.6.2":                         "v0.6.2",
		"v0.6.2-rc.1":                   "v0.6.2-rc.1",
		"v0.6.1-12-gabc1234":            "v0.6.1-12-gabc1234",
		"v0.6.1-12-gabc1234-dirty":      "v0.6.1-12-gabc1234-dirty",
		"v00.06.01":                     "v0.6.1",
		"dev":                           "a development build",
		"v0.6.1-3-gabc1234-host":        "a custom build",
		"v0.6.1-alice":                  "a custom build",
		"v0.6.1\nalice":                 "a custom build",
		"my-fork-v0.6.1":                "a custom build",
		"v0.6.1-3-gABC1234":             "a custom build",
		"":                              "a custom build",
		"v0.6.1-3-gabc1234-dirty\u202e": "a custom build",
	} {
		if got := shownDen(in); got != want {
			t.Errorf("shownDen(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestDistroIsAClosedList(t *testing.T) {
	for in, want := range map[string]string{
		"ID=arch\nBUILD_ID=rolling":                                "arch",
		"ID=ubuntu\nVERSION_ID=\"24.04\"":                          "ubuntu 24.04",
		"ID='fedora'\nVERSION_ID=41":                               "fedora 41",
		"ID=debian\nVERSION_ID=\"12.01\"":                          "debian 12.01",
		"ID=omarchy\nID_LIKE=arch\nVERSION_ID=\"4.0.4\"":           "omarchy 4.0.4",
		"ID=acmeos\nID_LIKE=\"arch\"\nVERSION_ID=\"1.2\"":          "other, like arch",
		"ID=acmeos\nID_LIKE=\"acmebase arch\"\nVERSION_ID=\"1.2\"": "other",
		"ID=acmeos\nID_LIKE=Arch":                                  "other",
		"ID=arch\nVERSION_ID=\"2024.01-alice\"":                    "arch",
		"ID=acmeos\nVERSION_ID=1":                                  "other",
		"ID=\"arch-alice\"":                                        "other",
		"PRETTY_NAME=\"devbox-secret\"":                            "other",
		"":                                                         "other",
	} {
		if got := distro([]byte(in)); got != want {
			t.Errorf("distro(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestSystemLine(t *testing.T) {
	e := fbEnv(nil)
	if got := systemLine(e); got != "kernel 7.2.5, arch, Hyprland: yes, terminal: through xdg-terminal-exec" {
		t.Errorf("system: %q", got)
	}
	e.Kernel = func() (string, error) { return "alice-kernel", nil }
	e.OSRelease = func() ([]byte, error) { return nil, fs.ErrPermission }
	e.Getenv = func(k string) string { return map[string]string{"TERMINAL": "/usr/bin/kitty -1"}[k] }
	e.LookPath = func(n string) (string, error) { return "", fs.ErrNotExist }
	if got := systemLine(e); got != "kernel unknown, distro unknown, Hyprland: no, terminal: kitty" {
		t.Errorf("system: %q", got)
	}
	e.Getenv = func(k string) string { return map[string]string{"TERMINAL": "/home/alice/acme-term"}[k] }
	if got := systemLine(e); !strings.HasSuffix(got, "terminal: another terminal") {
		t.Errorf("system: %q", got)
	}
}

func TestRemotesAreCountsOnly(t *testing.T) {
	e := fbEnv(nil)
	withRemotes(Remote{Enabled: true, State: "needs_attention"}, Remote{Enabled: true, State: "version_mismatch"}, Remote{})(&e)
	r := Feedback(e, 7)
	if got := section(r, "remotes"); got != "3 hosts, 2 enabled, 1 need you, 1 with a version mismatch (saved state; not dialled)" {
		t.Errorf("remotes: %q", got)
	}
	for _, c := range r.Checks {
		if c.ID == "remotes" {
			t.Errorf("the doctor's remotes line, with host numbers, is in the report: %q", c.Summary)
		}
	}
}

func TestConfigLine(t *testing.T) {
	e := fbEnv(nil)
	e.Config.Usage.StopPercent = 90
	e.Config.Orchestrator.Model = "acme-internal-opus"
	got := configLine(e)
	for _, want := range []string{"hooks.log on", "agents start in auto", "orchestrator a custom model", "usage stop at 90%"} {
		if !strings.Contains(got, want) {
			t.Errorf("config: %q lacks %q", got, want)
		}
	}
}

func TestHookTrouble(t *testing.T) {
	e := fbEnv(nil)
	log := hookLine(e, 8*24*time.Hour, "Stop               0123abcd no den listening") + // before the window
		hookLine(e, 3*time.Hour, "Stop               0123abcd delivered") +
		hookLine(e, 2*time.Hour, "SessionStart       0123abcd no den listening") +
		hookLine(e, 50*time.Hour, "on.done            0123abcd [on] command failed: exit status 1: /home/alice/acme-project/notify.sh delivered") +
		hookLine(e, 49*time.Hour, "on.acme_kind       0123abcd [on] command failed: timed out") +
		"garbage line\n" +
		hookLine(e, time.Hour, strings.Repeat("x", lineMax)+" no den listening")
	e = fbEnv(map[string]string{stateHome + "hooks.log": log})
	got := strings.Join(hookTrouble(e, e.Now.Add(-7*24*time.Hour), e.Now), "\n")
	want := "hooks: 2 runs, 1 found no Den listening, the last 2 h ago\n[on] commands: 2 failed (1 on.done, 1 on.other), the last 2 days ago"
	if got != want {
		t.Errorf("hooks:\n%s\nwant:\n%s", got, want)
	}
	e.Config.Hooks.Log = false
	if got := hookTrouble(e, e.Now.Add(-time.Hour), e.Now); got[0] != "hooks: hooks.log is off, not counted" {
		t.Errorf("off: %q", got)
	}
}

func TestEventTrouble(t *testing.T) {
	e := fbEnv(nil)
	from := e.Now.Add(-7 * 24 * time.Hour)
	old := eventLine(e, 6*24*time.Hour, `"kind":"turn_failed","error":"rate_limit"`) +
		eventLine(e, 8*24*time.Hour, `"kind":"turn_failed","error":"rate_limit"`) // before the window
	cur := eventLine(e, 7*24*time.Hour, `"kind":"turn_failed","error":"overloaded"`) + // exactly at from: counted
		eventLine(e, 5*time.Hour, `"kind":"turn_failed","error":"acme_gateway_timeout"`) +
		eventLine(e, 0, `"kind":"turn_failed","error":"rate_limit"`) + // exactly at to: not counted
		eventLine(e, time.Hour, `"kind":"tool_failed","tool":"Bash"`) +
		eventLine(e, time.Hour, `"kind":"tool_failed","tool":"Bash"`) +
		eventLine(e, time.Hour, `"kind":"tool_failed","tool":"mcp__acme__deploy"`) +
		eventLine(e, time.Hour, `"kind":"tool_failed","tool":"AcmeTool"`) +
		eventLine(e, time.Hour, `"kind":"stalled","minutes":25`) +
		eventLine(e, time.Hour, `"kind":"stalled","minutes":99999999`) +
		eventLine(e, time.Hour, `"kind":"still_waiting","request":"permission","minutes":10`) +
		"{not json\n" +
		eventLine(e, time.Hour, `"kind":"turn_failed","error":"`+strings.Repeat("x", lineMax)+`"`)
	e = fbEnv(map[string]string{stateHome + "events.jsonl.1": old, stateHome + "events.jsonl": cur})
	got := strings.Join(eventTrouble(e, from, e.Now), "\n")
	want := "turns: 3 failed (1 other, 1 overloaded, 1 rate_limit), the last 5 h ago\n" +
		"tools: 4 failed (2 Bash, 1 an MCP tool, 1 another tool)\n" +
		"stalls: 2, the longest 25 min\n" +
		"still waiting: 1 reminder, the longest 10 min"
	if got != want {
		t.Errorf("events:\n%s\nwant:\n%s", got, want)
	}
	if got := eventTrouble(fbEnv(nil), from, e.Now); got[0] != "turns: no event recorded yet" {
		t.Errorf("none: %q", got)
	}
}

// A stream cut by the tail loses its first, partial line.
func TestEventTroubleDropsTheCutLine(t *testing.T) {
	e := fbEnv(nil)
	line := eventLine(e, time.Hour, `"kind":"turn_failed","error":"rate_limit"`)
	body := strings.Repeat(line, int(eventsN)/len(line)+1)
	cut := body[len(body)-int(eventsN):]
	e = fbEnv(map[string]string{stateHome + "events.jsonl": cut})
	n := strings.Count(cut, "\n") - 1
	if got := eventTrouble(e, e.Now.Add(-24*time.Hour), e.Now)[0]; !strings.HasPrefix(got, fmt.Sprintf("turns: %d failed", n)) {
		t.Errorf("cut stream: %q, want %d", got, n)
	}
}

func TestActivity(t *testing.T) {
	e := fbEnv(nil)
	med, long := 40.0, 18*60.0
	e.Stats = func(from, to time.Time) stats.Report {
		return stats.Report{
			Turns:  &stats.Turns{Agents: 6, Prompts: 212, Finished: 205, Failed: 4},
			Waits:  &stats.Waits{Requests: 31, Permission: 24, Plan: 2, Question: 5, MedianSeconds: &med, LongestSeconds: &long},
			Merges: &stats.Merges{Count: 1},
		}
	}
	got := strings.Join(activity(e, e.Now.Add(-time.Hour), e.Now), "\n")
	want := "6 agents; 212 prompts, 205 finished, 4 failed\n31 waits (24 permission, 2 plan, 5 question, 0 other), median 40 s, longest 18 min\n1 mission branch merged"
	if got != want {
		t.Errorf("activity:\n%s\nwant:\n%s", got, want)
	}
}

// F5: every field of the stats sections feedback prints is a number, so a
// string added to stats later cannot flow into the report.
func TestActivityFieldsAreNumbers(t *testing.T) {
	for _, v := range []any{stats.Turns{}, stats.Waits{}, stats.Merges{}} {
		ty := reflect.TypeOf(v)
		for i := 0; i < ty.NumField(); i++ {
			f := ty.Field(i)
			k := f.Type.Kind()
			if k == reflect.Pointer {
				k = f.Type.Elem().Kind()
			}
			switch k {
			case reflect.Int, reflect.Float64, reflect.Bool:
			default:
				t.Errorf("stats.%s.%s is a %s: feedback prints only numbers", ty.Name(), f.Name, f.Type)
			}
		}
	}
}

func TestAgo(t *testing.T) {
	now := time.Date(2026, 10, 28, 19, 0, 0, 0, time.UTC)
	for d, want := range map[time.Duration]string{
		-time.Hour:          "just now",
		30 * time.Second:    "just now",
		time.Minute:         "1 min ago",
		59 * time.Minute:    "59 min ago",
		20 * time.Hour:      "20 h ago",
		47 * time.Hour:      "47 h ago",
		48 * time.Hour:      "2 days ago",
		24 * 30 * time.Hour: "30 days ago",
	} {
		if got := ago(now, now.Add(-d)); got != want {
			t.Errorf("ago(%s) = %q, want %q", d, got, want)
		}
	}
}

// leakyFeedback is leaky(variant) with feedback's seams full of
// identifying values too, and no backstop: rule 1 alone must hold.
func leakyFeedback(variant int) Env {
	e := leaky(variant)
	e.DenVersion = "v0.6.1-3-gabc1234-devbox-secret"
	e.GoVersion = "go1.26.9 alice@devbox-secret"
	e.Platform = "linux/amd64 devbox-secret"
	e.Kernel = func() (string, error) { return "7.2.5-3-devbox-secret\n", nil }
	e.OSRelease = func() ([]byte, error) {
		return []byte("ID=acmeos\nVERSION_ID=\"1.0-alice\"\nPRETTY_NAME=\"devbox-secret\"\n"), nil
	}
	e.Stats = func(from, to time.Time) stats.Report {
		return stats.Report{Turns: &stats.Turns{Agents: 1}, NotRecorded: []string{"alice"}, Sources: []stats.Source{{File: "/home/alice/acme-project"}}}
	}
	e.Names = func() []string { return nil }
	if variant == 1 {
		return e // every seam fails with an error that names things
	}
	e.Config.Events.Log = true
	hooksLog := "2026-10-28 18:40:00 on.done\u202e         0123abcd [on] command failed: /home/alice/acme-project/x.sh: devbox-secret 203.0.113.7\n" +
		"2026-10-28 18:41:00 on.secret-host     0123abcd [on] command failed: alice\n" +
		"2026-10-28 18:42:00 Stop               0123abcd no den listening\n"
	ev := func(fields string) string {
		return `{"v":1,"time":"2026-10-28T17:30:00Z","session":"0123abcd","agent":"alice","host":"secret-host",` + fields + "}\n"
	}
	evs := ev(`"kind":"turn_failed","error":"acme-project devbox-secret"`) +
		ev(`"kind":"tool_failed","tool":"mcp__acme-project__secret-host"`) +
		ev(`"kind":"tool_failed","tool":"alice\u202e"`) +
		ev(`"kind":"stalled","minutes":77`)
	e.Tail = func(p string, n int64) ([]byte, error) {
		switch {
		case strings.HasSuffix(p, "/hooks.log"):
			return []byte(hooksLog), nil
		case strings.HasSuffix(p, "/events.jsonl"):
			return []byte(evs), nil
		}
		return nil, fs.ErrNotExist
	}
	return e
}

func TestFeedbackCarriesNoIdentifyingValues(t *testing.T) {
	extra := []string{"acme_gateway_timeout", "acmeos", "mcp__acme", "secret-host", "0123abcd", "Fizzy"}
	for variant := 0; variant <= 3; variant++ {
		r := Feedback(leakyFeedback(variant), 7)
		raw, err := r.JSON(nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, out := range []string{r.Text(nil), string(raw)} {
			for _, s := range append(append([]string(nil), secrets...), extra...) {
				if strings.Contains(out, s) {
					t.Errorf("variant %d: the report holds %q:\n%s", variant, s, out)
				}
			}
			if strings.Contains(out, Withheld) {
				t.Errorf("variant %d: the backstop withheld a line with no names given:\n%s", variant, out)
			}
			noClock(t, out)
		}
	}
}

// F3: a name that is a word of the report withholds its line, in the text
// and in the JSON, which stays valid; neither says which name matched.
func TestBackstopWithholds(t *testing.T) {
	r := Feedback(fbEnv(nil), 7)
	names := []string{"KERNEL", "abc", ""} // a hostname that is a word of one line, the system line
	text := r.Text(names)
	if strings.Contains(strings.ToLower(text), "kernel") || !strings.Contains(text, Withheld) || !strings.Contains(text, "withheld   1 line\n") {
		t.Errorf("text:\n%s", text)
	}
	if !strings.HasPrefix(text, "```text\n") || !strings.HasSuffix(text, "```\n") {
		t.Errorf("the text is not a fenced block:\n%s", text)
	}
	raw, err := r.JSON(names)
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Withheld int       `json:"withheld"`
		Sections []Section `json:"sections"`
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		t.Fatalf("the JSON does not parse: %v\n%s", err, raw)
	}
	if v.Withheld != 1 || strings.Contains(strings.ToLower(string(raw)), "kernel") || !strings.Contains(string(raw), Withheld) {
		t.Errorf("json: withheld %d\n%s", v.Withheld, raw)
	}
	if clean := r.Text(nil); strings.Contains(clean, Withheld) || !strings.Contains(clean, "withheld   0 lines\n") {
		t.Errorf("no names, but:\n%s", clean)
	}
}

// A name of digits only (a uid) matches only as a whole number; a count
// that equals it is withheld too, which errs on the safe side.
func TestBackstopNumbersMatchWhole(t *testing.T) {
	d := newDenylist([]string{"1000", "machine0123456789", "Alice"})
	for s, want := range map[string]bool{
		"1000 prompts":            true,
		"21000 prompts":           false,
		"/run/user/1000/den.sock": true,
		"uid 1000":                true,
		"1000":                    true,
		"id machine0123456789abc": true,
		"ALICE was here":          true,
		"nothing identifying":     false,
	} {
		if got := d.hit(s); got != want {
			t.Errorf("hit(%q) = %v, want %v", s, got, want)
		}
	}
}

func TestReportShape(t *testing.T) {
	e := fbEnv(nil)
	text := Feedback(e, 1).Text(nil)
	for _, want := range []string{
		"den feedback, last 1 day\n",
		"den        v0.6.2 (go1.26.9, linux/amd64)\n",
		"system     kernel 7.2.5, arch, Hyprland: yes, terminal: through xdg-terminal-exec\n",
		"\nchecks\nok    claude",
		"remotes    no remote hosts\n",
		"trouble    hooks: no hook has run yet\n",
	} {
		if !strings.Contains(text, want) {
			t.Errorf("report lacks %q:\n%s", want, text)
		}
	}
}
