// Package mcp is a minimal Model Context Protocol server over stdio: enough
// for Claude Code to list and call a few tools. One JSON-RPC 2.0 message per
// line in, one per line out; requests are handled concurrently so a slow
// tool call never delays a ping.
package mcp

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"sync"
)

// Tool is one callable tool.
type Tool struct {
	Name        string
	Description string
	Schema      map[string]any // JSON Schema of the arguments
	// Call runs the tool. A returned error is reported to the model as a
	// failed call, not as a protocol error.
	Call func(ctx context.Context, args json.RawMessage) (string, error)
}

// Server serves a fixed set of tools.
type Server struct {
	Name    string
	Version string
	Tools   []Tool
}

// Protocol is the MCP revision the server answers with when the client does
// not name one.
const Protocol = "2025-06-18"

type request struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params"`
}

type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// Serve reads requests from r until EOF and writes replies to w.
func (s *Server) Serve(ctx context.Context, r io.Reader, w io.Writer) error {
	var mu sync.Mutex
	var wg sync.WaitGroup
	write := func(v any) {
		b, err := json.Marshal(v)
		if err != nil {
			return
		}
		mu.Lock()
		defer mu.Unlock()
		w.Write(append(b, '\n'))
	}
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 4<<20), 4<<20)
	for sc.Scan() {
		line := sc.Bytes()
		if len(line) == 0 {
			continue
		}
		var req request
		if err := json.Unmarshal(line, &req); err != nil {
			write(map[string]any{"jsonrpc": "2.0", "id": nil, "error": rpcError{-32700, "parse error: " + err.Error()}})
			continue
		}
		if len(req.ID) == 0 || string(req.ID) == "null" {
			continue // a notification: nothing to answer
		}
		wg.Add(1)
		go func(req request) {
			defer wg.Done()
			result, rerr := s.handle(ctx, req)
			if rerr != nil {
				write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "error": rerr})
				return
			}
			write(map[string]any{"jsonrpc": "2.0", "id": req.ID, "result": result})
		}(req)
	}
	wg.Wait()
	return sc.Err()
}

func (s *Server) handle(ctx context.Context, req request) (any, *rpcError) {
	switch req.Method {
	case "initialize":
		var p struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		json.Unmarshal(req.Params, &p)
		if p.ProtocolVersion == "" {
			p.ProtocolVersion = Protocol
		}
		return map[string]any{
			"protocolVersion": p.ProtocolVersion,
			"capabilities":    map[string]any{"tools": map[string]any{}},
			"serverInfo":      map[string]any{"name": s.Name, "version": s.Version},
		}, nil
	case "ping":
		return map[string]any{}, nil
	case "tools/list":
		tools := []map[string]any{}
		for _, t := range s.Tools {
			schema := t.Schema
			if schema == nil {
				schema = map[string]any{"type": "object", "properties": map[string]any{}}
			}
			tools = append(tools, map[string]any{"name": t.Name, "description": t.Description, "inputSchema": schema})
		}
		return map[string]any{"tools": tools}, nil
	case "tools/call":
		var p struct {
			Name      string          `json:"name"`
			Arguments json.RawMessage `json:"arguments"`
		}
		if err := json.Unmarshal(req.Params, &p); err != nil {
			return nil, &rpcError{-32602, "bad params: " + err.Error()}
		}
		for _, t := range s.Tools {
			if t.Name != p.Name {
				continue
			}
			text, err := t.Call(ctx, p.Arguments)
			isErr := err != nil
			if isErr {
				text = err.Error()
			}
			return map[string]any{
				"content": []map[string]any{{"type": "text", "text": text}},
				"isError": isErr,
			}, nil
		}
		return nil, &rpcError{-32602, fmt.Sprintf("unknown tool %q", p.Name)}
	}
	return nil, &rpcError{-32601, "method not found: " + req.Method}
}
