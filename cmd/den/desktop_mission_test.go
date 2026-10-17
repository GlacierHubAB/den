package main

import (
	"strings"
	"testing"
)

// desktop_mission takes a task, or a plan and apps; nothing in it sets a
// task's limits, which come from the human's config alone (P7). The tool
// is not called here: its call reaches den.sock.
func TestDesktopMissionToolSchema(t *testing.T) {
	tool := desktopMissionTool("den-orchestrator")
	props, _ := tool.Schema["properties"].(map[string]any)
	for _, want := range []string{"task", "plan", "apps", "dir"} {
		if _, ok := props[want]; !ok {
			t.Errorf("no %s", want)
		}
	}
	for name := range props {
		for _, w := range []string{"minute", "action", "limit", "time"} {
			if strings.Contains(strings.ToLower(name), w) {
				t.Errorf("%s: a limit the caller could set", name)
			}
		}
	}
	if req, _ := tool.Schema["required"].([]string); len(req) != 1 || req[0] != "dir" {
		t.Errorf("required %v", req)
	}
	if !strings.Contains(tool.Description, "Control your mouse and keyboard to:") {
		t.Error("the description does not show the card's sentence")
	}
}
