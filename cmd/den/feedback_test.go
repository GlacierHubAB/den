package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/GlacierHubAB/den/internal/doctor"
	"github.com/GlacierHubAB/den/internal/stats"
)

// feedbackEnv is doctorEnv with den feedback's seams.
func feedbackEnv() doctor.Env {
	e := doctorEnv(true)
	e.DenVersion = "v0.6.2"
	e.GoVersion = "go1.26.9"
	e.Platform = "linux/amd64"
	e.Kernel = func() (string, error) { return "7.2.5", nil }
	e.OSRelease = func() ([]byte, error) { return []byte("ID=arch\n"), nil }
	e.Stats = func(from, to time.Time) stats.Report { return stats.Report{} }
	e.Names = func() []string { return []string{"/home/u"} }
	return e
}

// F6 (den-sec-review): the notice goes to stderr only, so a file or a pipe
// holds the report alone; on a terminal it comes again after the report.
func TestFeedbackNoticeNeverOnStdout(t *testing.T) {
	for _, args := range [][]string{nil, {"--json"}, {"--days", "30"}} {
		for _, tty := range []bool{false, true} {
			var out, errw bytes.Buffer
			if code := runFeedback(feedbackEnv(), args, &out, &errw, tty); code != 0 {
				t.Fatalf("%v: exit %d: %s", args, code, errw.String())
			}
			if strings.Contains(out.String(), "Read this before") {
				t.Errorf("%v tty=%v: the notice is on stdout:\n%s", args, tty, out.String())
			}
			want := 1
			if tty {
				want = 2
			}
			if n := strings.Count(errw.String(), doctor.Notice); n != want {
				t.Errorf("%v tty=%v: the notice is on stderr %d times, want %d", args, tty, n, want)
			}
		}
	}
}

func TestFeedbackOutput(t *testing.T) {
	var out, errw bytes.Buffer
	runFeedback(feedbackEnv(), nil, &out, &errw, false)
	if s := out.String(); !strings.HasPrefix(s, "```text\nden feedback, last 7 days\nden        v0.6.2 (go1.26.9, linux/amd64)\n") || !strings.HasSuffix(s, "withheld   0 lines\n```\n") {
		t.Errorf("text:\n%s", s)
	}
	out.Reset()
	runFeedback(feedbackEnv(), []string{"--json", "--days", "3"}, &out, &errw, false)
	var v struct {
		Days     int `json:"days"`
		Withheld int `json:"withheld"`
		Sections []struct {
			ID    string   `json:"id"`
			Lines []string `json:"lines"`
		} `json:"sections"`
		Checks []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"checks"`
	}
	if err := json.Unmarshal(out.Bytes(), &v); err != nil {
		t.Fatalf("json: %v\n%s", err, out.String())
	}
	if v.Days != 3 || len(v.Sections) != 6 || len(v.Checks) == 0 {
		t.Errorf("json: %+v", v)
	}
	if strings.Contains(out.String(), "/home/u") {
		t.Errorf("json holds the home folder:\n%s", out.String())
	}
}

func TestFeedbackFlags(t *testing.T) {
	for _, args := range [][]string{{"--days", "0"}, {"--days", "31"}, {"--nope"}} {
		var out, errw bytes.Buffer
		if code := runFeedback(feedbackEnv(), args, &out, &errw, false); code != 2 {
			t.Errorf("%v: exit %d, want 2", args, code)
		}
		if out.Len() != 0 {
			t.Errorf("%v: printed a report:\n%s", args, out.String())
		}
	}
}
