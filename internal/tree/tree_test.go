package tree

import (
	"path/filepath"
	"testing"
	"time"
)

func TestAddKeepsNewestParentAndRefusesCycles(t *testing.T) {
	s := New()
	t0 := time.Now()
	if !s.Add(Edge{Parent: Orchestrator, Child: "a", Brief: "lead", At: t0}) {
		t.Fatal("first edge not added")
	}
	if !s.Add(Edge{Parent: "a", Child: "b", At: t0.Add(time.Second)}) {
		t.Fatal("child edge not added")
	}
	// b replying to a must not make a a child of b.
	if s.Add(Edge{Parent: "b", Child: "a", At: t0.Add(2 * time.Second)}) {
		t.Error("a reply inverted the tree")
	}
	if got := s.Chain("b"); len(got) != 2 || got[0] != Orchestrator || got[1] != "a" {
		t.Errorf("chain = %v", got)
	}
	// Someone else takes b over.
	if !s.Add(Edge{Parent: "c", Child: "b", At: t0.Add(3 * time.Second)}) {
		t.Error("new parent not recorded")
	}
	if e, _ := s.Parent("b"); e.Parent != "c" {
		t.Errorf("parent of b = %q", e.Parent)
	}
	if n := len(s.Children("a")); n != 0 {
		t.Errorf("a still has %d children", n)
	}
	if !s.Add(Edge{Child: "b", Parent: "c", Drop: true}) || s.Len() != 1 {
		t.Errorf("drop did not remove b; len=%d", s.Len())
	}
}

func TestPruneAndRecord(t *testing.T) {
	path := filepath.Join(t.TempDir(), "edges.jsonl")
	now := time.Now()
	for _, e := range []Edge{
		{Parent: Orchestrator, Child: "a", At: now},
		{Parent: "a", Child: "b", At: now.Add(time.Second)},
		{Parent: "gone", Child: "c", At: now.Add(2 * time.Second)},
		{Parent: "a", Child: "d", At: now.Add(3 * time.Second)},
	} {
		if err := Append(path, e); err != nil {
			t.Fatal(err)
		}
	}
	s := Load(path)
	if s.Len() != 4 {
		t.Fatalf("loaded %d edges, want 4", s.Len())
	}
	alive := map[string]bool{"a": true, "b": true, "c": true}
	s.Prune(func(id string) bool { return alive[id] })
	if s.Len() != 2 {
		t.Errorf("after prune %d edges, want 2 (c lost its parent, d is gone)", s.Len())
	}
	if kids := s.Children("a"); len(kids) != 1 || kids[0].Child != "b" {
		t.Errorf("children of a = %+v", kids)
	}
}

func TestPeer(t *testing.T) {
	prompt := `<cross-session-message from="uds:/run/user/1000/cc-socks/3127800.sock" from-name="den-orchestrator" from-mode="prompting">
hello`
	pid, name, brief, ok := Peer(prompt)
	if !ok || pid != 3127800 || name != "den-orchestrator" || brief != "hello" {
		t.Errorf("got %d %q %q %v", pid, name, brief, ok)
	}
	if _, _, _, ok := Peer("plain prompt"); ok {
		t.Error("plain prompt parsed as a peer message")
	}
}
