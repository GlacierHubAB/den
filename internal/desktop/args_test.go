package desktop

import (
	"encoding/json"
	"strings"
	"testing"
)

// Every tool refuses an argument Den does not know, and one that would
// change where it acts (a coordinate space, a monitor, a delay).
func TestUnknownArgumentsDeny(t *testing.T) {
	calls := map[string]string{
		"desktop":    `{"monitor":"DP-2"}`,
		"screenshot": `{"window":"0xe","output":"DP-2"}`,
		"zoom":       `{"x":1000,"y":100,"window":"0xe","monitor":"DP-2"}`,
		"ui":         `{"window":"0xe","pid":400}`,
		"marks":      `{"window":"0xe","region":"0,0,10x10"}`,
		"pointer":    `{"action":"click","x":1000,"y":100,"relative":true}`,
		"keyboard":   `{"action":"type","text":"x","window":"0xe","delay":5}`,
		"click_ui":   `{"name":"Bold","window":"0xe","x":2000}`,
		"hypr":       `{"action":"focus_window","target":"0xe","address":"0xh"}`,
	}
	for tool, input := range calls {
		if v := decide(t, tool, input); v.Kind != Deny || !strings.Contains(v.Reason, "takes no argument") {
			t.Errorf("%s %s: %s (%s)", tool, input, v.Kind, v.Reason)
		}
	}
	typed := map[string]string{
		"pointer":    `{"action":"click","x":"1000","y":100}`,
		"keyboard":   `{"action":"type","text":["x"],"window":"0xe"}`,
		"screenshot": `{"window":"0xe","stable":"yes"}`,
	}
	for tool, input := range typed {
		if v := decide(t, tool, input); v.Kind != Deny || !strings.Contains(v.Reason, "wrong type") {
			t.Errorf("%s %s: %s (%s)", tool, input, v.Kind, v.Reason)
		}
	}
	for _, input := range []string{
		`{"action":"click","x":1000,"y":100,"button":"back"}`,
	} {
		if v := decide(t, "pointer", input); v.Kind != Deny {
			t.Errorf("pointer %s: %s", input, v.Kind)
		}
	}
	if v := decide(t, "zoom", `{"x":1000,"y":100,"window":"0xe","size":"1e9x1"}`); v.Kind != Deny {
		t.Errorf("odd size: %s", v.Kind)
	}
	if v := decide(t, "zoom", `{"x":1000,"y":100,"window":"0xe","size":"480x360"}`); v.Kind != Allow {
		t.Errorf("plain size: %s (%s)", v.Kind, v.Reason)
	}
}

// A notification or any top/overlay surface over the point takes the
// click; background and bottom surfaces do not.
func TestLayersOverThePointDeny(t *testing.T) {
	m, s := fixture()
	s.Layers = []Layer{
		{Monitor: "DP-1", Level: 3, X: 1500, Y: 10, W: 400, H: 100, Namespace: "notifications"},
		{Monitor: "DP-1", Level: 1, X: 0, Y: 200, W: 1920, H: 200, Namespace: "agent-glow"},
	}
	at := func(tool, input string) Verdict { return Decide(tool, json.RawMessage(input), m, s) }
	if v := at("pointer", `{"action":"click","x":1600,"y":50}`); v.Kind != Deny || !strings.Contains(v.Reason, "notifications") {
		t.Fatalf("click under a notification: %s (%s)", v.Kind, v.Reason)
	}
	if v := at("pointer", `{"action":"click","x":1000,"y":300}`); v.Kind != Allow {
		t.Fatalf("click over a bottom-layer glow: %s (%s)", v.Kind, v.Reason)
	}
	if v := at("pointer", `{"action":"drag","x":1000,"y":300,"to_x":1600,"to_y":50}`); v.Kind != Deny {
		t.Fatalf("drag ending under a notification: %s", v.Kind)
	}
	if v := at("click_ui", `{"name":"Bold","window":"0xe"}`); v.Kind != Deny || !strings.Contains(v.Reason, "notifications") {
		t.Fatalf("click_ui with a notification over the window: %s (%s)", v.Kind, v.Reason)
	}
	s.Layers = s.Layers[1:]
	if v := at("click_ui", `{"name":"Bold","window":"0xe"}`); v.Kind != Allow {
		t.Fatalf("click_ui with it gone: %s (%s)", v.Kind, v.Reason)
	}
}

// Keys and characters the human bound in Hyprland are refused, mod-less
// binds included.
func TestBindsDeny(t *testing.T) {
	m, s := fixture()
	s.Binds = []Bind{{Mods: modAlt, Key: "tab"}, {Mods: 0, Key: "print"}, {Mods: modCtrl | modAlt, Key: "t"}, {Mods: 0, Key: "f12"}}
	at := func(input string) Verdict { return Decide("keyboard", json.RawMessage(input), m, s) }
	for _, keys := range []string{"alt+Tab", "Print", "ctrl+alt+t", "F12"} {
		if v := at(`{"action":"key","keys":"` + keys + `"}`); v.Kind != Deny || !strings.Contains(v.Reason, "binds") {
			t.Errorf("%s: %s (%s)", keys, v.Kind, v.Reason)
		}
	}
	if v := at(`{"action":"key","keys":"ctrl+t"}`); v.Kind != Allow {
		t.Errorf("ctrl+t is not bound: %s (%s)", v.Kind, v.Reason)
	}
	if v := at(`{"action":"type","text":"hello"}`); v.Kind != Allow {
		t.Errorf("plain text: %s (%s)", v.Kind, v.Reason)
	}
	s.Binds = append(s.Binds, Bind{Mods: 0, Key: "q"})
	if v := at(`{"action":"type","text":"Quit"}`); v.Kind != Deny {
		t.Errorf("text with a mod-less bound character: %s", v.Kind)
	}
}

func TestReadLayersAndBinds(t *testing.T) {
	ls, err := readLayers([]byte(`{"DP-1":{"levels":{"0":[{"x":0,"y":0,"w":1920,"h":1080,"namespace":"wallpaper"}],"3":[{"x":1,"y":2,"w":3,"h":4,"namespace":"mako"}]}}}`))
	if err != nil || len(ls) != 2 {
		t.Fatalf("%v %+v", err, ls)
	}
	var top Layer
	for _, l := range ls {
		if l.Level == 3 {
			top = l
		}
	}
	if top.Namespace != "mako" || top.Monitor != "DP-1" || top.W != 3 {
		t.Fatalf("%+v", top)
	}
	bs, err := readBinds([]byte(`[{"modmask":72,"key":"Return"},{"modmask":0,"key":"mouse:272","mouse":true},{"modmask":24,"key":"Tab"}]`))
	if err != nil || len(bs) != 2 || bs[0].Key != "return" || bs[1].Mods != modAlt {
		t.Fatalf("%v %+v", err, bs)
	}
}
