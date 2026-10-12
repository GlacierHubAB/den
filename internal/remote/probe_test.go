package remote

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseProbe(t *testing.T) {
	p := parseProbe([]byte("uname=Linux x86_64\nden_path=/home/user/.local/bin/den\nden_version=den v0.1.0\nclaude_path=/usr/bin/claude\nclaude_version=2.1.300 (Claude Code)\nhooks=yes\nruntime_dir=/run/user/1000\nnoise\n"))
	if p.Uname != "Linux x86_64" || p.DenVersion != "v0.1.0" || !p.Hooks || !p.RuntimeDir || p.ClaudeVersion != "2.1.300 (Claude Code)" {
		t.Errorf("probe = %+v", p)
	}
	h := Host{Label: "b"}
	adv := strings.Join(p.Advice(h, "v0.2.0"), "\n")
	if !strings.Contains(adv, "older than this v0.2.0") {
		t.Errorf("advice = %s", adv)
	}
	if len(Probe{DenPath: "/x", DenVersion: "v0.2.0", ClaudePath: "/c", Hooks: true}.Advice(h, "v0.2.0")) != 0 {
		t.Error("a ready host got advice")
	}
}

// The probe script runs in a local sh against a scratch HOME: it reads
// only, and finds den and the hooks.
func TestProbeScriptRunsReadOnly(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	home := t.TempDir()
	bin := filepath.Join(home, ".local", "bin")
	os.MkdirAll(bin, 0o755)
	os.WriteFile(filepath.Join(bin, "den"), []byte("#!/bin/sh\necho den v0.1.0\n"), 0o755)
	os.MkdirAll(filepath.Join(home, ".claude"), 0o755)
	os.WriteFile(filepath.Join(home, ".claude", "settings.json"), []byte(`{"hooks":{"Stop":[{"hooks":[{"command":"/home/user/.local/bin/den hook"}]}]}}`), 0o644)
	script := probeScript(Host{})
	cmd := exec.Command("sh", "-c", script)
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v: %s", err, out)
	}
	p := parseProbe(out)
	if p.DenPath != filepath.Join(bin, "den") || p.DenVersion != "v0.1.0" || !p.Hooks || p.Uname == "" {
		t.Errorf("probe = %+v\n%s", p, out)
	}
	cmd = exec.Command("sh", "-c", probeScript(Host{DenPath: "~/.local/bin/den"}))
	cmd.Env = []string{"HOME=" + home, "PATH=/usr/bin:/bin"}
	out, _ = cmd.CombinedOutput()
	if p := parseProbe(out); p.DenVersion != "v0.1.0" {
		t.Errorf("den_path probe = %+v\n%s", p, out)
	}
}

func TestPlanInstall(t *testing.T) {
	h := Host{Label: "b", Target: "b"}
	uname := map[string]string{"amd64": "Linux x86_64", "arm64": "Linux aarch64"}[runtime.GOARCH]
	if uname == "" || runtime.GOOS != "linux" {
		t.Skip("plans copy only to linux amd64/arm64")
	}
	if _, err := planFor(h, Probe{Uname: uname, DenPath: "/x", DenVersion: "v0.2.0"}, "v0.2.0"); err == nil {
		t.Error("planned to install the same version")
	}
	if _, err := planFor(h, Probe{Uname: uname, DenPath: "/x", DenVersion: "v0.1.0"}, "dev"); err == nil {
		t.Error("planned to replace a release with a dev build")
	}
	if _, err := planFor(h, Probe{Uname: uname, DenPath: "/x", DenVersion: "v99999999999999999999.0.0"}, "v0.2.0"); err == nil {
		t.Error("planned over a host claiming a huge version")
	}
	p, err := planFor(h, Probe{Uname: uname}, "v0.2.0")
	if err != nil || !p.Copy || len(p.SHA256) != 64 || !strings.HasPrefix(p.Temp, ".den-install-") || !strings.Contains(p.Text, "~/.local/bin/den") {
		t.Errorf("plan = %+v, %v", p, err)
	}
	other := map[string]string{"amd64": "Linux aarch64", "arm64": "Linux x86_64"}[runtime.GOARCH]
	p, err = planFor(h, Probe{Uname: other}, "v0.2.0")
	if err != nil || p.Copy || !strings.Contains(p.Text, ReleaseURL) || !strings.Contains(p.Text, "sha256sum -c") {
		t.Errorf("cross-arch plan = %+v, %v", p, err)
	}
}

func TestClassifyKilledSSH(t *testing.T) {
	for _, f := range []*Failure{{Code: -1}, {Code: -1, Stderr: "Killed by signal 15."}} {
		if s, r := Classify(f); s != Offline || r != "the ssh session ended" {
			t.Errorf("%+v: %s, %q", f, s, r)
		}
	}
	if _, r := Classify(&Failure{Code: 2}); r != "exited 2" {
		t.Errorf("exit 2: %q", r)
	}
}
