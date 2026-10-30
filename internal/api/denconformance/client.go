// Copyright (c) 2026 Martin Granström
// SPDX-License-Identifier: MIT
// This file is part of the Den API conformance suite; the MIT licence text
// is in README.md beside it.

package denconformance

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"regexp"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// The wire contract the suite checks: JSON lines over one connection,
// hello first, requests {"v":1,"id":n,"op":"…","params":{…}}, replies
// {"id":n,"ok":true,"result":…} or {"id":n,"ok":false,"error":{"code":…}},
// and events {"event":"…","data":…} on a subscribed connection.
const (
	// V is the protocol version.
	V = 1
	// MaxLine is the longest line either side accepts.
	MaxLine = 1 << 20
	// StatusMax is the most characters a status line keeps.
	StatusMax = 120
)

// Error codes.
const (
	CodeUnsupported = "unsupported"
	CodeNotFound    = "not_found"
	CodeBadRequest  = "bad_request"
	CodeHelloFirst  = "hello_first"
	CodeTooLarge    = "too_large"
)

// WireError is a failed reply.
type WireError struct {
	Code    string `json:"code"`
	Message string `json:"message,omitempty"`
}

func (e *WireError) Error() string { return "den: " + e.Code + ": " + e.Message }

func codeOf(err error) string {
	var we *WireError
	if errors.As(err, &we) {
		return we.Code
	}
	return ""
}

type request struct {
	V      int    `json:"v"`
	ID     int64  `json:"id"`
	Op     string `json:"op"`
	Params any    `json:"params,omitempty"`
}

type line struct {
	ID     *int64          `json:"id,omitempty"`
	OK     *bool           `json:"ok,omitempty"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  *WireError      `json:"error,omitempty"`
	Event  string          `json:"event,omitempty"`
	Data   json.RawMessage `json:"data,omitempty"`
	// size is the encoded line with its newline, as read.
	size int
}

// reply is the reply part of a line.
type reply struct {
	OK    bool
	Error *WireError
}

// event is one pushed line.
type event struct {
	Event string
	Data  json.RawMessage
}

// conn is one connection that has said hello.
type conn struct {
	nc   net.Conn
	r    *bufio.Reader
	next int64
	// lastConversationID: the id of the last conversation event on this connection; the next must be higher.
	lastConversationID int64
	// lastChallenge paces console.challenge to one a second on this
	// connection, as the server allows.
	lastChallenge time.Time
}

func (c *conn) close() { _ = c.nc.Close() }

func (c *conn) send(op string, params any) (int64, error) {
	c.next++
	b, err := json.Marshal(request{V: V, ID: c.next, Op: op, Params: params})
	if err != nil {
		return 0, err
	}
	_, err = c.nc.Write(append(b, '\n'))
	return c.next, err
}

func (c *conn) read() (*line, error) {
	var buf []byte
	for {
		chunk, err := c.r.ReadSlice('\n')
		buf = append(buf, chunk...)
		if len(buf) > MaxLine {
			return nil, fmt.Errorf("a line over %d bytes from the server", MaxLine)
		}
		if err == nil {
			break
		}
		if !errors.Is(err, bufio.ErrBufferFull) {
			return nil, err
		}
	}
	var l line
	if err := json.Unmarshal(buf, &l); err != nil {
		return nil, fmt.Errorf("not a JSON line: %q", buf)
	}
	l.size = len(buf)
	if err := c.checkEventLine(&l); err != nil {
		return nil, err
	}
	return &l, nil
}

// The limits of Den's conversation lines (DREY-API.md, "Conversation mirror"), as encoded JSON with the newline:
// a conversation event line at most 400 KiB, a conversation.history reply line at most 800 KiB.
const (
	maxConversationEventLine = 409600
	maxHistoryReplyLine      = 819200
	maxStreamChunk           = 64 * 1024 // a joined text chunk of orch or chat
)

// checkEventLine holds every event line this connection reads to what Den states about it: a conversation event
// within its line bound and with an id above the last one (the ids strictly increase; a gap is legal, as Den may
// drop conversation entries and say so), a conversation_gap with a closed shape (`from` an id), and a joined
// orch or chat text chunk of at most 64 KiB.
func (c *conn) checkEventLine(l *line) error {
	switch l.Event {
	case "conversation":
		if l.size > maxConversationEventLine {
			return fmt.Errorf("a conversation event line of %d bytes, the most is %d", l.size, maxConversationEventLine)
		}
		var head struct {
			ID int64 `json:"id"`
		}
		if json.Unmarshal(l.Data, &head) == nil && head.ID > 0 {
			if head.ID <= c.lastConversationID {
				return fmt.Errorf("conversation event id %d after %d: the ids strictly increase on a connection", head.ID, c.lastConversationID)
			}
			c.lastConversationID = head.ID
		}
	case "conversation_gap":
		if err := checkGapEvent(l.Data); err != nil {
			return err
		}
	case "orch", "chat":
		var ch struct {
			Delta string `json:"delta"`
		}
		if json.Unmarshal(l.Data, &ch) == nil && len(ch.Delta) > maxStreamChunk {
			return fmt.Errorf("a %s text chunk of %d bytes, the most is %d", l.Event, len(ch.Delta), maxStreamChunk)
		}
	}
	return nil
}

// checkGapEvent: `conversation_gap {from}` and no other field; from is the id of the first entry not sent.
func checkGapEvent(raw json.RawMessage) error {
	var g struct {
		From int64 `json:"from"`
	}
	if err := strictDecode(raw, &g); err != nil {
		return fmt.Errorf("conversation_gap %s: %w", raw, err)
	}
	if g.From <= 0 {
		return fmt.Errorf("conversation_gap from %d is not an entry id", g.From)
	}
	return nil
}

// call sends op and decodes its reply into result, skipping events.
func (c *conn) call(ctx context.Context, op string, params, result any) error {
	if dl, ok := ctx.Deadline(); ok {
		_ = c.nc.SetDeadline(dl)
	}
	id, err := c.send(op, params)
	if err != nil {
		return err
	}
	for {
		l, err := c.read()
		if err != nil {
			return err
		}
		if l.Event != "" || l.ID == nil || *l.ID != id || l.OK == nil {
			continue
		}
		if op == "conversation.history" && l.size > maxHistoryReplyLine {
			return fmt.Errorf("a conversation.history reply line of %d bytes, the most is %d", l.size, maxHistoryReplyLine)
		}
		if !*l.OK {
			if l.Error == nil {
				return &WireError{Code: "internal", Message: "a failed reply without an error object"}
			}
			return l.Error
		}
		if result != nil && len(l.Result) > 0 {
			if err := json.Unmarshal(l.Result, result); err != nil {
				return fmt.Errorf("%s: result: %w", op, err)
			}
		}
		return nil
	}
}

// open dials and says hello.
func open(ctx context.Context, dial Dialer) (*conn, info, error) {
	return openWith(ctx, dial, "", false)
}

// openV2 dials and says hello with card_hash 2: a console answers cards
// only on such a connection (card.answer is hash_version on any other).
func openV2(ctx context.Context, dial Dialer) (*conn, info, error) {
	return openWith(ctx, dial, "", true)
}

// openGroup dials and says hello with a group ("" sends none).
func openGroup(ctx context.Context, dial Dialer, group string) (*conn, info, error) {
	return openWith(ctx, dial, group, false)
}

// openWith dials and says hello with a group ("" sends none), and with
// card_hash 2 when v2.
func openWith(ctx context.Context, dial Dialer, group string, v2 bool) (*conn, info, error) {
	nc, err := dial(ctx)
	if err != nil {
		return nil, info{}, err
	}
	c := &conn{nc: nc, r: bufio.NewReaderSize(nc, 64*1024)}
	var in info
	params := map[string]any{"v": V, "client": "conformance", "version": "test"}
	if group != "" {
		params["group"] = group
	}
	if v2 {
		params["card_hash"] = 2
	}
	if err := c.call(ctx, "hello", params, &in); err != nil {
		c.close()
		return nil, info{}, err
	}
	return c, in, nil
}

// client runs unary verbs on one connection and opens one per stream.
type client struct {
	dial Dialer
	c    *conn
}

func (cl *client) close() {
	if cl.c != nil {
		cl.c.close()
	}
}

func (cl *client) call(ctx context.Context, op string, params, result any) error {
	if cl.c == nil {
		c, _, err := open(ctx, cl.dial)
		if err != nil {
			return err
		}
		cl.c = c
	}
	return cl.c.call(ctx, op, params, result)
}

// subscribe opens a connection, sends op and forwards its events until
// ctx ends.
func (cl *client) subscribe(ctx context.Context, op string, params any) (<-chan event, error) {
	c, _, err := open(ctx, cl.dial)
	if err != nil {
		return nil, err
	}
	if err := c.call(ctx, op, params, nil); err != nil {
		c.close()
		return nil, err
	}
	out := make(chan event, 16)
	go func() {
		defer close(out)
		defer c.close()
		stop := context.AfterFunc(ctx, c.close)
		defer stop()
		for {
			l, err := c.read()
			if err != nil {
				return
			}
			if l.Event == "" {
				continue
			}
			select {
			case out <- event{Event: l.Event, Data: l.Data}:
			case <-ctx.Done():
				return
			}
		}
	}()
	return out, nil
}

// ask sends a turn and collects its chunks until done or error; id is
// the ask id the reply named.
func (cl *client) ask(ctx context.Context, text string) (id string, out []chunk, err error) {
	c, _, err := open(ctx, cl.dial)
	if err != nil {
		return "", nil, err
	}
	defer c.close()
	var rep struct {
		Ask string `json:"ask"`
	}
	if err := c.call(ctx, "ask", map[string]string{"text": text}, &rep); err != nil {
		return "", nil, err
	}
	id = rep.Ask
	for {
		l, err := c.read()
		if errors.Is(err, io.EOF) {
			return id, out, fmt.Errorf("the connection closed before done")
		}
		if err != nil {
			return id, out, err
		}
		if l.Event != "chat" {
			continue
		}
		var ch chunk
		if err := json.Unmarshal(l.Data, &ch); err != nil {
			return id, out, fmt.Errorf("a chat event that is not a chunk: %s", l.Data)
		}
		out = append(out, ch)
		if ch.Type == "done" || ch.Type == "error" {
			return id, out, nil
		}
	}
}

// AskIDPattern is the shape of an ask id: "a-" and 128 random bits.
var askIDRe = regexp.MustCompile(`^a-[0-9a-f]{32}$`)

// The parts of the wire types the suite checks.
type info struct {
	Version  string   `json:"version"`
	Versions []int    `json:"versions"`
	Features []string `json:"features"`
	Control  string   `json:"control"` // "on" when an app's or a console's turn may run
	// ConsolePublicKey is Den's console key (hex), which signs a
	// console.reconnect's den_sig.
	ConsolePublicKey string `json:"console_public_key"`
}

func (i info) supports(v int) bool {
	for _, x := range i.Versions {
		if x == v {
			return true
		}
	}
	return false
}

type fleet struct {
	Taken  time.Time `json:"taken"`
	Agents []struct {
		ID    string `json:"id"`
		Name  string `json:"name"`
		State string `json:"state"`
	} `json:"agents"`
	Cost struct {
		Currency string `json:"currency"`
	} `json:"cost"`
}

type item struct {
	ID     string `json:"id"`
	Kind   string `json:"kind"`
	Digest struct {
		FirstLine string `json:"firstLine"`
		Bytes     int    `json:"bytes"`
		Hash      string `json:"hash"`
	} `json:"digest"`
}

type body struct {
	Text string `json:"text"`
	Hash string `json:"hash"`
}

type chunk struct {
	Type    string `json:"type"`
	Session string `json:"session,omitempty"`
	Message string `json:"message,omitempty"`
	Ask     string `json:"ask,omitempty"`
	Seq     int    `json:"seq,omitempty"`
	Text    string `json:"text,omitempty"`
	Card    *struct {
		ID      string `json:"id"`
		Actions []struct {
			ID string `json:"id"`
		} `json:"actions"`
	} `json:"card,omitempty"`
}

type chatSession struct {
	ID      string    `json:"id"`
	Last    time.Time `json:"last"`
	Current bool      `json:"current"`
}

type message struct {
	Role string `json:"role"`
}

type mission struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
}

type grantAnswer struct {
	ID     string `json:"id"`
	Answer string `json:"answer"`
	Hash   string `json:"hash"`
}

// HashOf is the digest and grant hash rule: "sha256:" + hex of the text.
func HashOf(text string) string {
	sum := sha256.Sum256([]byte(text))
	return "sha256:" + hex.EncodeToString(sum[:])
}

// StatusText is the rule a status line must already satisfy: one plain
// line, control, format (bidi, zero-width), private-use and unprintable
// characters removed, whitespace runs as one space, at most StatusMax
// characters, an ellipsis when cut.
func StatusText(s string) string {
	var b strings.Builder
	space := false
	for _, r := range s {
		switch {
		case unicode.IsSpace(r):
			space = true
			continue
		case r == utf8.RuneError, unicode.In(r, unicode.Cc, unicode.Cf, unicode.Co, unicode.Cs), !unicode.IsGraphic(r):
			continue
		}
		if space && b.Len() > 0 {
			b.WriteByte(' ')
		}
		space = false
		b.WriteRune(r)
	}
	out := []rune(b.String())
	if len(out) <= StatusMax {
		return string(out)
	}
	return strings.TrimRight(string(out[:StatusMax-1]), " ") + "…"
}
