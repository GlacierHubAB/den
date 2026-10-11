package ui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPinnedProjectIsListedWithNobodyOnIt(t *testing.T) {
	m := missionModel()
	m.proj.path = filepath.Join(t.TempDir(), "overview.json")
	dir := t.TempDir()
	p, err := m.pinProject(dir)
	if err != nil || p.Root != dir || len(p.Agents) != 0 {
		t.Fatalf("pin: %+v %v", p, err)
	}
	if m.proj.cur != dir {
		t.Errorf("pick = %q", m.proj.cur)
	}
	raw, _ := os.ReadFile(m.proj.path)
	if !strings.Contains(string(raw), `"pinned":["`+dir+`"]`) {
		t.Errorf("saved %s", raw)
	}
	m2 := missionModel()
	m2.proj.path = m.proj.path
	m2.loadProjectPick()
	m2.buildProjects()
	if _, ok := m2.projectByRoot(dir); !ok || m2.proj.cur != dir {
		t.Errorf("pin not restored: %+v cur %q", m2.proj.list, m2.proj.cur)
	}
	if !m.unpinProject() {
		t.Fatal("unpin")
	}
	if _, ok := m.projectByRoot(dir); ok || m.proj.cur == dir {
		t.Errorf("still listed after unpin: %+v", m.proj.list)
	}
	if _, err := m.pinProject(filepath.Join(dir, "missing")); err == nil {
		t.Error("a missing folder should fail")
	}
}
