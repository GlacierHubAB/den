package api

import "testing"

// A cancel and a turn's end decide together: a cancelled ask ends with
// "cancelled" even when its turn had finished, and an ask whose stream
// ended is not reported cancelled.
func TestAskCancelAndEndAgree(t *testing.T) {
	var got []Chunk
	job := func() *askJob {
		got = nil
		return &askJob{id: "a-1", cancelled: make(chan struct{}), send: func(c Chunk) bool { got = append(got, c); return true }}
	}
	j := job()
	if !j.cancel() {
		t.Fatal("a running ask not cancelled")
	}
	j.end("") // its turn finished after the cancel
	if len(got) != 1 || got[0].Type != "error" || got[0].Message != "cancelled" {
		t.Errorf("a cancelled ask ended with %+v", got)
	}
	j = job()
	j.end("")
	if j.cancel() {
		t.Error("an ended ask reported cancelled")
	}
	if len(got) != 1 || got[0].Type != "done" {
		t.Errorf("an ended ask's stream: %+v", got)
	}
	j = job()
	j.cancelWith(superseded)
	j.cancel()
	j.end("")
	if len(got) != 1 || got[0].Message != superseded {
		t.Errorf("the first reason is the end's: %+v", got)
	}
}
