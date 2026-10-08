package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"testing"
)

func TestServeRoundTrip(t *testing.T) {
	s := &Server{Name: "den", Version: "test", Tools: []Tool{{
		Name:        "echo",
		Description: "Echoes.",
		Schema:      map[string]any{"type": "object", "properties": map[string]any{"text": map[string]any{"type": "string"}}},
		Call: func(_ context.Context, args json.RawMessage) (string, error) {
			var a struct{ Text string }
			json.Unmarshal(args, &a)
			if a.Text == "boom" {
				return "", errors.New("it broke")
			}
			return "echo: " + a.Text, nil
		},
	}}}
	in := strings.Join([]string{
		`{"jsonrpc":"2.0","id":1,"method":"initialize","params":{"protocolVersion":"2024-11-05","capabilities":{}}}`,
		`{"jsonrpc":"2.0","method":"notifications/initialized"}`,
		`{"jsonrpc":"2.0","id":2,"method":"tools/list"}`,
		`{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"echo","arguments":{"text":"hi"}}}`,
		`{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"echo","arguments":{"text":"boom"}}}`,
		`{"jsonrpc":"2.0","id":5,"method":"nope"}`,
		`{"jsonrpc":"2.0","id":"p","method":"ping"}`,
	}, "\n") + "\n"
	pr, pw := io.Pipe()
	done := make(chan error, 1)
	go func() { done <- s.Serve(context.Background(), strings.NewReader(in), pw); pw.Close() }()
	replies := map[string]map[string]any{}
	sc := bufio.NewScanner(pr)
	for sc.Scan() {
		var m map[string]any
		if err := json.Unmarshal(sc.Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		id, _ := json.Marshal(m["id"])
		replies[string(id)] = m
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(replies) != 6 {
		t.Fatalf("want 6 replies (the notification gets none), got %d: %v", len(replies), replies)
	}
	init := replies["1"]["result"].(map[string]any)
	if init["protocolVersion"] != "2024-11-05" {
		t.Errorf("initialize should echo the client's protocol version: %v", init)
	}
	tools := replies["2"]["result"].(map[string]any)["tools"].([]any)
	if len(tools) != 1 || tools[0].(map[string]any)["name"] != "echo" {
		t.Errorf("tools/list = %v", tools)
	}
	call := replies["3"]["result"].(map[string]any)
	if call["isError"] != false || call["content"].([]any)[0].(map[string]any)["text"] != "echo: hi" {
		t.Errorf("tools/call = %v", call)
	}
	failed := replies["4"]["result"].(map[string]any)
	if failed["isError"] != true || failed["content"].([]any)[0].(map[string]any)["text"] != "it broke" {
		t.Errorf("failed call should be a tool error, got %v", failed)
	}
	if replies["5"]["error"] == nil {
		t.Errorf("unknown method should be a protocol error: %v", replies["5"])
	}
	if replies[`"p"`]["result"] == nil {
		t.Errorf("ping: %v", replies[`"p"`])
	}
}
