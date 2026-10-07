package peer

import (
	"bufio"
	"encoding/json"
	"net"
	"path/filepath"
	"strings"
	"testing"
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
