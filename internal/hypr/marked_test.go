package hypr

import "testing"

// The agent's terminal is found by the marker class it was created with,
// which names no terminal program: NewTerminal misses it (the bug a real
// Ghostty showed), NewMarked finds exactly that one new window.
func TestNewMarkedFindsTheAgentsTerminal(t *testing.T) {
	marker := Marker("desktop")
	before := []Client{{Address: "0xden", Class: "com.mitchellh.ghostty", InitialClass: "com.mitchellh.ghostty"}}
	after := append(append([]Client(nil), before...),
		Client{Address: "0xview", Class: marker, InitialClass: marker},
		Client{Address: "0xother", Class: "com.mitchellh.ghostty", InitialClass: "com.mitchellh.ghostty"},
	)
	if c, ok := NewMarked(before, after, marker); !ok || c.Address != "0xview" {
		t.Fatalf("NewMarked: %+v %v", c, ok)
	}
	if c, ok := NewTerminal(before, after); ok && c.Address == "0xview" {
		t.Fatal("NewTerminal now sees the marker class; NewMarked would no longer be needed")
	}
	// A window that changed its class later still counts by its initial one;
	// one that only took the marker as its class later does not.
	after[1].Class = "renamed"
	after[2].Class = marker
	if c, ok := NewMarked(before, after, marker); !ok || c.Address != "0xview" {
		t.Fatalf("by initial class: %+v %v", c, ok)
	}
	// Two new windows with the marker, an old one, or no marker: none.
	two := append(after, Client{Address: "0xdup", InitialClass: marker})
	if _, ok := NewMarked(before, two, marker); ok {
		t.Error("two marked windows were taken")
	}
	if _, ok := NewMarked(after, after, marker); ok {
		t.Error("a window that was already there was taken")
	}
	if _, ok := NewMarked(before, after, ""); ok {
		t.Error("no marker matched a window")
	}
}
