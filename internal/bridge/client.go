package bridge

import (
	"bufio"
	"encoding/json"
	"errors"
	"net"
	"time"
)

// Send posts an envelope and does not wait beyond the acknowledgement.
// It returns false when no Den of this user is running.
func Send(path string, env Envelope) bool {
	conn, err := Dial(path, 500*time.Millisecond)
	if err != nil {
		return false
	}
	defer conn.Close()
	b, _ := json.Marshal(env)
	conn.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return false
	}
	conn.SetReadDeadline(time.Now().Add(time.Second))
	bufio.NewReader(conn).ReadBytes('\n')
	return true
}

// Ask posts a PermissionRequest and waits up to wait for Den's decision.
// ok is false when no Den of this user is running or it did not answer in
// time.
func Ask(path string, env Envelope, wait time.Duration) (d Decision, ok bool) {
	d, err := Call(path, env, wait)
	return d, err == nil
}

// ErrNoDen means no Den of this user listens on the socket.
var ErrNoDen = errors.New("den is not running")

// Call posts an envelope that wants an answer and waits up to wait for it.
// The error is ErrNoDen when no Den listens, so a caller can tell that
// from a Den that did not answer in time.
func Call(path string, env Envelope, wait time.Duration) (Decision, error) {
	conn, err := Connect(path)
	if err != nil {
		return Decision{}, err
	}
	return CallOn(conn, env, wait)
}

// Connect reaches the running Den, ErrNoDen when none listens. With
// CallOn it lets a caller tell the human it is asking only once a Den
// will hear the question.
func Connect(path string) (net.Conn, error) {
	conn, err := Dial(path, 500*time.Millisecond)
	if err != nil {
		return nil, ErrNoDen
	}
	return conn, nil
}

// CallOn is Call on a connection from Connect, which it closes.
func CallOn(conn net.Conn, env Envelope, wait time.Duration) (Decision, error) {
	return ask(conn, env, wait)
}

// ask posts env on conn and waits for the decision, then closes conn.
func ask(conn net.Conn, env Envelope, wait time.Duration) (Decision, error) {
	defer conn.Close()
	b, _ := json.Marshal(env)
	conn.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := conn.Write(append(b, '\n')); err != nil {
		return Decision{}, err
	}
	conn.SetReadDeadline(time.Now().Add(wait))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		return Decision{}, err
	}
	var d Decision
	if err := json.Unmarshal(line, &d); err != nil {
		return Decision{}, err
	}
	return d, nil
}
