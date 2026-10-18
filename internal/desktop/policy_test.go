package desktop

import (
	"encoding/json"
	"strings"
	"testing"
)

// A desktop with the mission on workspace 9: a browser (outward) and an
// editor Den launched, a stranger window on 9 Den did not launch, and the
// human's terminal on workspace 1 on the other monitor.
func fixture() (Mission, Snapshot) {
	m := Mission{
		ID: "m1", Workspace: 9,
		Apps: []App{
			{Name: "browser", Class: "zen", Outward: OutwardAsk},
			{Name: "editor", Class: "foot-den", Outward: ""},
		},
		Windows: map[string]bool{"0xb": true, "0xe": true},
		PIDs:    map[int]bool{100: true, 200: true},
	}
	s := Snapshot{
		Monitors: []Monitor{{Name: "DP-1", X: 0, Y: 0, W: 1920, H: 1080, Workspace: 9}, {Name: "DP-2", X: 1920, Y: 0, W: 1920, H: 1080, Workspace: 1}},
		Windows: []Window{
			{Address: "0xb", Class: "zen", PID: 100, Workspace: 9, At: [2]int{0, 0}, Size: [2]int{960, 1080}, Mapped: true},
			{Address: "0xe", Class: "foot-den", PID: 200, Workspace: 9, At: [2]int{960, 0}, Size: [2]int{960, 540}, Mapped: true},
			{Address: "0xs", Class: "stranger", PID: 300, Workspace: 9, At: [2]int{960, 540}, Size: [2]int{960, 540}, Mapped: true},
			{Address: "0xh", Class: "ghostty", PID: 400, Workspace: 1, At: [2]int{1920, 0}, Size: [2]int{1920, 1080}, Mapped: true},
		},
		Active: "0xe",
	}
	return m, s
}

func decide(t *testing.T, tool, input string) Verdict {
	t.Helper()
	m, s := fixture()
	return Decide(tool, json.RawMessage(input), m, s)
}

func TestDecideTiers(t *testing.T) {
	cases := []struct {
		name, tool, input string
		want              Kind
	}{
		{"desktop observes", "desktop", `{}`, Allow},
		{"screenshot of a mission window", "screenshot", `{"window":"0xe"}`, Allow},
		{"screenshot of the human's terminal", "screenshot", `{"window":"0xh"}`, Deny},
		{"screenshot of the screen", "screenshot", `{}`, Deny},
		{"screenshot of a region", "screenshot", `{"window":"0xe","region":"0,0,10x10"}`, Deny},
		{"screenshot of active", "screenshot", `{"window":"active"}`, Deny},
		{"zoom inside the editor", "zoom", `{"x":1000,"y":100,"window":"0xe"}`, Allow},
		{"zoom without a window", "zoom", `{"x":1000,"y":100}`, Deny},
		{"zoom point outside its window", "zoom", `{"x":10,"y":100,"window":"0xe"}`, Deny},
		{"ui of a stranger on the workspace", "ui", `{"window":"0xs"}`, Deny},
		{"marks of the editor", "marks", `{"window":"0xe"}`, Allow},
		{"move on the mission monitor", "pointer", `{"action":"move","x":100,"y":100}`, Allow},
		{"move onto the human's monitor", "pointer", `{"action":"move","x":2000,"y":100}`, Deny},
		{"click in the editor", "pointer", `{"action":"click","x":1000,"y":100}`, Allow},
		{"click in the browser asks", "pointer", `{"action":"click","x":100,"y":100}`, Ask},
		{"scroll in the browser", "pointer", `{"action":"scroll","x":100,"y":100,"scroll_dy":3}`, Allow},
		{"click on the stranger", "pointer", `{"action":"click","x":1000,"y":700}`, Deny},
		{"click at the cursor", "pointer", `{"action":"click"}`, Deny},
		{"drag out of a window", "pointer", `{"action":"drag","x":1000,"y":100,"to_x":1000,"to_y":700}`, Deny},
		{"type in the editor", "keyboard", `{"action":"type","text":"hello"}`, Allow},
		{"type in the browser asks", "keyboard", `{"action":"type","text":"hello","window":"0xb"}`, Ask},
		{"type into the human's terminal", "keyboard", `{"action":"type","text":"rm -rf ~\n","window":"0xh"}`, Deny},
		{"key in the editor", "keyboard", `{"action":"key","keys":"ctrl+s"}`, Allow},
		{"super combo", "keyboard", `{"action":"key","keys":"super+Return"}`, Deny},
		{"vt switch", "keyboard", `{"action":"key","keys":"ctrl+alt+F2"}`, Deny},
		{"click_ui in the editor", "click_ui", `{"name":"Bold","window":"0xe"}`, Allow},
		{"click_ui Send anywhere asks", "click_ui", `{"name":"Send","window":"0xe"}`, Ask},
		{"click_ui by mark without marks", "click_ui", `{"mark":3,"window":"0xe"}`, Deny},
		{"click_ui active", "click_ui", `{"name":"Bold"}`, Deny},
		{"hypr to the mission workspace", "hypr", `{"action":"workspace","workspace":"9"}`, Allow},
		{"hypr to another workspace", "hypr", `{"action":"workspace","workspace":"1"}`, Deny},
		{"hypr focus a mission window", "hypr", `{"action":"focus_window","target":"0xb"}`, Allow},
		{"hypr focus the human's terminal", "hypr", `{"action":"focus_window","target":"0xh"}`, Deny},
		{"hypr move", "hypr", `{"action":"move_window","target":"0xe","workspace":"9"}`, Deny},
		{"hypr close", "hypr", `{"action":"close_window","target":"0xe"}`, Deny},
		{"hypr fullscreen without target", "hypr", `{"action":"fullscreen"}`, Deny},
		{"launch", "launch", `{"command":"ghostty"}`, Deny},
		{"use_bind", "use_bind", `{"combo":"super+q"}`, Deny},
		{"sequence", "sequence", `{"steps":[]}`, Deny},
		{"clipboard", "clipboard", `{"action":"read"}`, Deny},
		{"binds", "binds", `{}`, Deny},
		{"wait_for", "wait_for", `{"event":"openwindow"}`, Deny},
		{"unknown", "rm", `{}`, Deny},
		{"allow_auth", "keyboard", `{"action":"type","text":"x","allow_auth":true}`, Deny},
		{"request_approval asks", "request_approval", `{"intent":"send the mail"}`, Ask},
		{"not an object", "desktop", `[1]`, Deny},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			v := decide(t, c.tool, c.input)
			if v.Kind != c.want {
				t.Fatalf("%s %s: got %s (%s), want %s", c.tool, c.input, v.Kind, v.Reason, c.want)
			}
			if v.Kind == Deny && v.Reason == "" {
				t.Fatal("a deny must say why")
			}
		})
	}
}

// Den rewrites what it forwards: the keyboard's target window, and then
// = "none" on acting tools.
func TestDecideRewrites(t *testing.T) {
	v := decide(t, "keyboard", `{"action":"type","text":"hi","then":"screenshot"}`)
	if v.Args["window"] != "0xe" || v.Args["then"] != "none" {
		t.Fatalf("args %v", v.Args)
	}
	v = decide(t, "pointer", `{"action":"click","x":1000,"y":100,"then":"desktop"}`)
	if v.Args["then"] != "none" {
		t.Fatalf("then %v", v.Args["then"])
	}
	v = decide(t, "click_ui", `{"name":"Bold","window":"0xe","then":"ui","allow_auth":false}`)
	if v.Args["then"] != "none" {
		t.Fatalf("then %v", v.Args["then"])
	}
	if _, ok := v.Args["allow_auth"]; ok {
		t.Fatal("allow_auth is not forwarded")
	}
}

func TestDecidePausedDeniesAll(t *testing.T) {
	m, s := fixture()
	m.Paused = true
	for _, tool := range []string{"desktop", "screenshot", "pointer", "request_approval"} {
		if v := Decide(tool, json.RawMessage(`{"window":"0xe"}`), m, s); v.Kind != Deny || !strings.Contains(v.Reason, "paused") {
			t.Errorf("%s while paused: %s", tool, v.Kind)
		}
	}
}

func TestDecideHiddenWorkspace(t *testing.T) {
	m, s := fixture()
	s.Monitors[0].Workspace = 2 // the human switched away
	for _, in := range []struct{ tool, input string }{
		{"pointer", `{"action":"click","x":1000,"y":100}`},
		{"pointer", `{"action":"move","x":10,"y":10}`},
		{"zoom", `{"x":1000,"y":100,"window":"0xe"}`},
	} {
		if v := Decide(in.tool, json.RawMessage(in.input), m, s); v.Kind != Deny {
			t.Errorf("%s %s with the workspace hidden: %s", in.tool, in.input, v.Kind)
		}
	}
	s.Monitors[0].Workspace, s.Monitors[0].Special = 9, true
	if v := Decide("pointer", json.RawMessage(`{"action":"click","x":1000,"y":100}`), m, s); v.Kind != Deny {
		t.Errorf("click under a special workspace: %s", v.Kind)
	}
}

func TestDecideWordsMode(t *testing.T) {
	m, s := fixture()
	m.Apps[0].Outward = OutwardWords
	check := func(tool, input string, want Kind) {
		t.Helper()
		if v := Decide(tool, json.RawMessage(input), m, s); v.Kind != want {
			t.Errorf("%s %s: %s (%s), want %s", tool, input, v.Kind, v.Reason, want)
		}
	}
	check("keyboard", `{"action":"type","text":"search terms","window":"0xb"}`, Allow)
	check("keyboard", `{"action":"type","text":"terms\n","window":"0xb"}`, Ask)
	check("keyboard", `{"action":"key","keys":"ctrl+Return","window":"0xb"}`, Ask)
	check("keyboard", `{"action":"key","keys":"KP_Enter","window":"0xb"}`, Ask)
	check("keyboard", `{"action":"key","keys":"ctrl+l","window":"0xb"}`, Allow)
	check("pointer", `{"action":"click","x":100,"y":100}`, Allow)
	check("click_ui", `{"name":"Skicka","window":"0xb"}`, Ask)
	check("click_ui", `{"mark":2,"window":"0xb"}`, Deny)
	m.MarksWindow = "0xb"
	check("click_ui", `{"mark":2,"window":"0xb"}`, Ask)
}

// A window opened later by a process Den launched is the mission's; a
// window of an unknown class there asks as outward.
func TestDecideChildWindow(t *testing.T) {
	m, s := fixture()
	s.Windows = append(s.Windows, Window{Address: "0xd", Class: "zen-dialog", PID: 100, Workspace: 9, At: [2]int{100, 100}, Size: [2]int{300, 200}, Floating: true, Mapped: true})
	v := Decide("pointer", json.RawMessage(`{"action":"click","x":150,"y":150}`), m, s)
	if v.Kind != Ask || v.Target == nil {
		t.Fatalf("click on a dialog over the browser: %s %+v", v.Kind, v.Target)
	}
	v = Decide("keyboard", json.RawMessage(`{"action":"type","text":"x","window":"0xd"}`), m, s)
	if v.Kind != Ask {
		t.Fatalf("typing into an unknown class: %s", v.Kind)
	}
}

func TestToolNames(t *testing.T) {
	got := strings.Join(AllowedToolNames(false), ",")
	for _, closed := range ClosedTools {
		if strings.Contains(got, "__"+closed+",") || strings.HasSuffix(got, "__"+closed) {
			t.Errorf("%s is allowed", closed)
		}
	}
	if !strings.Contains(got, "mcp__hypruse__request_approval") || !strings.Contains(got, "mcp__hypruse__keyboard") {
		t.Fatalf("allowed: %s", got)
	}
}
