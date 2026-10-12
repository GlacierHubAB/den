package remote

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/GlacierHubAB/den/internal/sanitize"
)

// ReleaseURL is where release binaries and their SHA256SUMS are published.
const ReleaseURL = "https://github.com/GlacierHubAB/den/releases"

// Plan is what installing den on a host would do (docs/REMOTE.md). Text is
// shown to the human exactly as it is, and only their yes (Ctrl+Y in the
// inbox, or the CLI's prompt) runs Install. A background connection never
// installs.
type Plan struct {
	Host    Host
	Probe   Probe
	Copy    bool   // this binary can be copied: same OS and architecture
	Local   string // the running binary
	SHA256  string // its hex sha256
	Version string // its version
	Temp    string // the name it is copied to first, under ~/.local/bin
	Text    string
}

// remoteArch maps `uname -sm` to Go's GOOS/GOARCH; "" when unknown.
func remoteArch(uname string) (goos, goarch string) {
	f := strings.Fields(strings.ToLower(uname))
	if len(f) != 2 {
		return "", ""
	}
	goos = f[0]
	switch f[1] {
	case "x86_64", "amd64":
		goarch = "amd64"
	case "aarch64", "arm64":
		goarch = "arm64"
	}
	return goos, goarch
}

// PlanInstall checks the host (read-only) and says what installing would
// do. It refuses when den on the host is already this version or newer,
// or its version cannot be compared. When the architecture differs, Copy
// is false and Text says how to install by hand.
func PlanInstall(ctx context.Context, s SSH, h Host, localVersion string) (Plan, error) {
	p, err := s.Probe(ctx, h)
	if err != nil {
		return Plan{}, err
	}
	return planFor(h, p, localVersion)
}

func planFor(h Host, p Probe, localVersion string) (Plan, error) {
	plan := Plan{Host: h, Probe: p, Version: localVersion}
	// The host reports its own version, so it is only ever a reason not to
	// install: a version that does not parse, is huge or is newer refuses,
	// and what is shown of it is sanitised and capped.
	p.DenVersion = sanitize.Label(p.DenVersion, 40)
	if p.DenPath != "" && !Older(p.DenVersion, localVersion) {
		return plan, fmt.Errorf("den on %s is %q, which is not known to be older than this %q; nothing to install", h.Label, p.DenVersion, localVersion)
	}
	goos, goarch := remoteArch(p.Uname)
	if goos != runtime.GOOS || goarch != runtime.GOARCH {
		arch := goarch
		if arch == "" {
			arch = "<arch>"
		}
		plan.Text = fmt.Sprintf(`%s reports %q; this den is %s/%s, so it cannot be copied there.
Install it by hand on the host:
  1. download den_linux_%s and SHA256SUMS from %s
  2. check it: sha256sum -c SHA256SUMS --ignore-missing
  3. chmod +x den_linux_%s && mv den_linux_%s ~/.local/bin/den`, h.Label, p.Uname, runtime.GOOS, runtime.GOARCH, arch, ReleaseURL, arch, arch)
		return plan, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return plan, err
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		exe = real
	}
	sum, err := fileSHA256(exe)
	if err != nil {
		return plan, err
	}
	var rnd [6]byte
	rand.Read(rnd[:])
	plan.Copy, plan.Local, plan.SHA256 = true, exe, sum
	plan.Temp = ".den-install-" + hex.EncodeToString(rnd[:])
	replacing := "where there is none yet"
	if p.DenPath != "" {
		replacing = fmt.Sprintf("replacing den %q", p.DenVersion)
	}
	text := fmt.Sprintf(`Install den %s on %s (%s):
  1. copy this binary (%s/%s, sha256 %s) with scp to ~/.local/bin/%s on the host
  2. check its sha256 there with sha256sum; on a mismatch it is deleted and nothing else happens
  3. move it to ~/.local/bin/den, %s
Nothing else on the host changes. Claude Code's hooks there are not touched.`,
		localVersion, h.Label, h.Target, runtime.GOOS, runtime.GOARCH, sum[:16], plan.Temp, replacing)
	if h.DenPath != "" && h.DenPath != "~/.local/bin/den" {
		text += fmt.Sprintf("\nThe host's den_path (%s) stays in remotes.toml; remove it to use ~/.local/bin/den.", h.DenPath)
	}
	plan.Text = text
	return plan, nil
}

func fileSHA256(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// Install carries out a plan that copies: scp to the temporary name, its
// sha256 checked on the host against the local one, then an atomic mv to
// ~/.local/bin/den. It returns the installed den's version line.
func (s SSH) Install(ctx context.Context, p Plan) (string, error) {
	if !p.Copy {
		return "", errors.New("this plan installs by hand; see its text")
	}
	if strings.ContainsAny(p.Temp, "'\"$`\\ /") || !strings.HasPrefix(p.Temp, ".den-install-") {
		return "", fmt.Errorf("bad temporary name %q", p.Temp)
	}
	if sum, err := fileSHA256(p.Local); err != nil || sum != p.SHA256 {
		return "", fmt.Errorf("the local binary changed since the plan was made")
	}
	tmp := `"$HOME/.local/bin/` + p.Temp + `"`
	t := p.Host.Target
	if _, err := s.Run(ctx, t, `sh -c 'mkdir -p "$HOME/.local/bin"'`); err != nil {
		return "", fmt.Errorf("making ~/.local/bin: %w", err)
	}
	if err := s.Copy(ctx, p.Local, t, ".local/bin/"+p.Temp); err != nil {
		return "", fmt.Errorf("copying: %w", err)
	}
	out, err := s.Run(ctx, t, `sh -c 'sha256sum `+tmp+`'`)
	remote, _, _ := strings.Cut(strings.TrimSpace(string(out)), " ")
	if err != nil || remote != p.SHA256 {
		s.Run(ctx, t, `sh -c 'rm -f `+tmp+`'`)
		if err != nil {
			return "", fmt.Errorf("checking the copy: %w (the copy was deleted)", err)
		}
		return "", fmt.Errorf("the copy's sha256 on the host is %q, not %q; it was deleted", remote, p.SHA256)
	}
	if _, err := s.Run(ctx, t, `sh -c 'chmod 755 `+tmp+` && mv -f `+tmp+` "$HOME/.local/bin/den"'`); err != nil {
		s.Run(ctx, t, `sh -c 'rm -f `+tmp+`'`)
		return "", fmt.Errorf("moving it into place: %w", err)
	}
	out, err = s.Run(ctx, t, `sh -c '"$HOME/.local/bin/den" version'`)
	if err != nil {
		return "", fmt.Errorf("installed, but it does not run: %w", err)
	}
	return strings.TrimSpace(string(out)), nil
}
