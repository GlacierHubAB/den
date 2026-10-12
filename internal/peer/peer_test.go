package peer

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSendWritesOneJSONLine(t *testing.T) {
	path := filepath.Join(t.TempDir(), "cc.sock")
	ln, err := net.Listen("unix", path)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		c, err := ln.Accept()
		if err != nil {
			return
		}
		defer c.Close()
		line, _ := bufio.NewReader(c).ReadString('\n')
		got <- line
	}()
	if err := Send(path, "  rebase on main first  "); err != nil {
		t.Fatal(err)
	}
	line := <-got
	var msg struct {
		Type    string `json:"type"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
	}
	if err := json.Unmarshal([]byte(line), &msg); err != nil {
		t.Fatalf("not JSON: %v: %q", err, line)
	}
	if msg.Type != "user" || msg.Message.Role != "user" || !strings.HasSuffix(msg.Message.Content, "rebase on main first") || !strings.HasPrefix(msg.Message.Content, Header) {
		t.Errorf("message = %+v", msg)
	}
	if err := Send(filepath.Join(t.TempDir(), "nope.sock"), "x"); err == nil {
		t.Error("expected an error for a missing socket")
	}
	if err := Send("", "x"); err == nil {
		t.Error("expected an error for no socket")
	}
}

// A path that is a symlink is refused, even to a socket of ours: the text
// must not be steerable to another service. Errors do not name the path.
func TestSendRefusesLinksAndOtherFiles(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "SECRET-real.sock")
	ln, err := net.Listen("unix", real)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	got := make(chan string, 1)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			line, _ := bufio.NewReader(c).ReadString('\n')
			c.Close()
			got <- line
		}
	}()
	link := filepath.Join(dir, "SECRET-link.sock")
	os.Symlink(real, link)
	file := filepath.Join(dir, "SECRET-file")
	os.WriteFile(file, []byte("x"), 0o600)
	for name, p := range map[string]string{"symlink": link, "regular file": file, "missing": filepath.Join(dir, "SECRET-none")} {
		err := Send(p, "hi")
		if err == nil || strings.Contains(err.Error(), "SECRET") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	select {
	case l := <-got:
		t.Errorf("a refused send reached the socket: %q", l)
	case <-time.After(200 * time.Millisecond):
	}
	if err := Send(real, "hi"); err != nil {
		t.Errorf("a plain socket of ours: %v", err)
	}
	<-got
}
