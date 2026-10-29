package main

import (
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/config"
	"github.com/GlacierHubAB/den/internal/doctor"
	"github.com/GlacierHubAB/den/internal/hooks"
)

// doctorEnv is a fake machine; claude decides whether one check fails.
func doctorEnv(claude bool) doctor.Env {
	return doctor.Env{
		Home:   "/home/u",
		Getenv: func(string) string { return "" },
		LookPath: func(n string) (string, error) {
			if n == "claude" && !claude {
				return "", errors.New("not found")
			}
			return "/usr/bin/" + n, nil
		},
		Version: func(p string) (string, error) {
			if p == "/usr/bin/git" {
				return "git version 2.47.1", nil
			}
			return "2.1.309 (Claude Code)", nil
		},
		ReadFile: func(string) ([]byte, error) { return nil, fs.ErrNotExist },
		Tail:     func(string, int64) ([]byte, error) { return nil, fs.ErrNotExist },
		Exe:      "/home/u/.local/bin/den",
		Running:  func() bool { return false },
		Sessions: func() (int, error) { return 0, nil },
		Mods:     func() ([]doctor.Mod, error) { return nil, nil },
		Remotes:  func() ([]doctor.Remote, error) { return nil, nil },
		Config:   config.Defaults(),
		Issues:   func() ([]config.Issue, error) { return nil, nil },
		Now:      time.Date(2026, 10, 28, 19, 0, 0, 0, time.UTC),
	}
}

func TestDoctorExitCodes(t *testing.T) {
	var out bytes.Buffer
	// No hooks installed fails the hooks check either way.
	if code := runDoctor(doctorEnv(true), nil, &out); code != 1 {
		t.Errorf("exit %d with a failing check, want 1:\n%s", code, out.String())
	}
	e := doctorEnv(true)
	e.Exe = "/usr/bin/den"
	e.ReadFile = func(string) ([]byte, error) { return []byte(allHooks("/usr/bin/den")), nil }
	out.Reset()
	if code := runDoctor(e, nil, &out); code != 0 {
		t.Errorf("exit %d with warnings only, want 0:\n%s", code, out.String())
	}
	if code := runDoctor(e, []string{"--nope"}, &out); code != 2 {
		t.Errorf("exit %d on a bad flag, want 2", code)
	}
}

func TestDoctorJSONShape(t *testing.T) {
	var out bytes.Buffer
	runDoctor(doctorEnv(false), []string{"--json"}, &out)
	var rs []map[string]any
	if err := json.Unmarshal(out.Bytes(), &rs); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, out.String())
	}
	if len(rs) == 0 {
		t.Fatal("no results")
	}
	for _, r := range rs {
		for _, k := range []string{"id", "status", "summary"} {
			if _, ok := r[k].(string); !ok {
				t.Errorf("result lacks %s: %v", k, r)
			}
		}
		for k := range r {
			if k != "id" && k != "status" && k != "summary" && k != "fix" {
				t.Errorf("unexpected key %q in %v", k, r)
			}
		}
		if _, ok := r["fix"]; ok != (r["status"] == doctor.Warn || r["status"] == doctor.Fail) {
			t.Errorf("fix present %v for status %v", ok, r["status"])
		}
	}
	if rs[0]["id"] != "claude" || rs[0]["status"] != doctor.Fail {
		t.Errorf("first result = %v, want claude failing", rs[0])
	}
}

// allHooks is a settings.json with Den's hooks for every event.
func allHooks(den string) string {
	h := map[string]any{}
	for _, ev := range hooks.Events() {
		e := map[string]any{"hooks": []any{map[string]any{"type": "command", "command": den + " hook"}}}
		if ev == "PreToolUse" {
			e["matcher"] = "AskUserQuestion"
		}
		h[ev] = []any{e}
	}
	raw, _ := json.Marshal(map[string]any{"hooks": h})
	return string(raw)
}
