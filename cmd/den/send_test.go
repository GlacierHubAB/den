package main

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/GlacierHubAB/den/internal/bridge"
)

func TestRemoteHostOf(t *testing.T) {
	for who, want := range map[string]string{"mossy@devbox": "devbox", "Mossy Lemur@gpu": "gpu", "mossy": "", "mossy@local": "", "@devbox": "", "mossy@": ""} {
		if host, ok := remoteHostOf(who); host != want && ok || (want != "") != ok {
			t.Errorf("%q -> %q %v", who, host, ok)
		}
	}
}

// den send name@host asks the running Den; the text travels in the
// envelope and the answer is printed.
func TestSendViaDen(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "den.sock")
	srv, err := bridge.Listen(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer srv.Close()
	got := make(chan bridge.SendInput, 1)
	go func() {
		for r := range srv.Requests() {
			var in bridge.SendInput
			json.Unmarshal(r.Payload, &in)
			if r.Event == bridge.SendEvent && r.WantsReply() {
				got <- in
			}
			switch {
			case strings.Contains(in.Text, "refuse"):
				r.Reply(bridge.Decision{Behavior: "deny", Message: "devbox is offline; nothing was sent"})
				continue
			case strings.Contains(in.Text, "decline"):
				r.Reply(bridge.Decision{Behavior: bridge.SendDeclined, Message: "declined in Den; nothing was sent"})
				continue
			case strings.Contains(in.Text, "session ended"):
				r.Reply(bridge.Decision{Behavior: bridge.SendCancelled, Message: "the request was cancelled: the session on devbox ended; nothing was sent"})
				continue
			case strings.Contains(in.Text, "empty"):
				r.Reply(bridge.Decision{})
				continue
			}
			r.Reply(bridge.Decision{Behavior: "allow", Message: "delivered to Mossy Lemur on devbox"})
		}
	}()
	out, code, err := sendViaDen(sock, "mossy@devbox", "rebase `id` $(id)")
	if err != nil || code != 0 || out != "delivered to Mossy Lemur on devbox" {
		t.Fatalf("out %q code %d err %v", out, code, err)
	}
	if in := <-got; in.Who != "mossy@devbox" || in.Text != "rebase `id` $(id)" || !strings.Contains(in.AskedBy, "den send") {
		t.Errorf("request = %+v", in)
	}
	for text, want := range map[string]struct {
		code int
		msg  string
	}{
		"refuse":        {sendNotSent, "den: devbox is offline; nothing was sent"},
		"decline":       {sendDeclined, "den: declined in Den; nothing was sent"},
		"session ended": {sendCancelled, "den: the request was cancelled: the session on devbox ended; nothing was sent"},
		"empty":         {sendCancelled, "den: Den ended the request before you answered; nothing was sent"},
	} {
		_, code, err := sendViaDen(sock, "mossy@devbox", text)
		<-got
		if err == nil || code != want.code || err.Error() != want.msg {
			t.Errorf("%s: exit %d, %v", text, code, err)
		}
	}
}

func TestSendViaDenWithoutDen(t *testing.T) {
	_, code, err := sendViaDen(filepath.Join(t.TempDir(), "none.sock"), "mossy@devbox", "hi")
	if err == nil || code != sendNoDen || !strings.Contains(err.Error(), "Den is not attached to devbox") {
		t.Errorf("exit %d, err = %v", code, err)
	}
}
