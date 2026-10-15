package desktop

import (
	"bytes"
	"strings"
	"testing"
)

func TestView(t *testing.T) {
	in := strings.Join([]string{
		`{"type":"system","subtype":"init","tools":["mcp__hypruse__desktop","mcp__hypruse__keyboard"]}`,
		`{"type":"assistant","message":{"content":[{"type":"text","text":"I will type.\u001b[31m"},{"type":"tool_use","name":"mcp__hypruse__keyboard","input":{"action":"type","text":"hi\nthere","window":"0xe"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","content":[{"type":"text","text":"typed 8 characters"}]}]}}`,
		`{"type":"assistant","message":{"content":[{"type":"tool_use","name":"mcp__hypruse__click_ui","input":{"name":"Send","window":"0xe"}}]}}`,
		`{"type":"user","message":{"content":[{"type":"tool_result","is_error":true,"content":"Den: deny: the human said no"}]}}`,
		`{"type":"result","subtype":"success","result":"done"}`,
		`not json`,
	}, "\n")
	var out bytes.Buffer
	if err := View(strings.NewReader(in), &out); err != nil {
		t.Fatal(err)
	}
	got := out.String()
	for _, want := range []string{"2 tools", "I will type.", `keyboard: type "hi⏎there"`, "✓ typed 8 characters", `click_ui: "Send"`, "✗ Den: deny: the human said no", "── done ──"} {
		if !strings.Contains(got, want) {
			t.Errorf("view lacks %q:\n%s", want, got)
		}
	}
	if strings.ContainsRune(got, 0x1b) {
		t.Error("an escape sequence reached the terminal")
	}
}
