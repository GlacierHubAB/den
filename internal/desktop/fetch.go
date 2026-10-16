package desktop

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Den can fetch the pinned hypruse itself: GitHub's archive of the pinned
// commit. Only the paths the pin names are written, each a regular file
// whose SHA-256 is the pinned one, checked as it is read; every other path
// in the archive is skipped. A written path is built from the pin's own
// keys, never from the archive, so nothing can land outside the folder.
// Nothing of hypruse runs here.

// ArchiveURL is GitHub's archive of the pinned commit.
func (pin Pin) ArchiveURL() string {
	return strings.TrimSuffix(pin.URL, "/") + "/archive/" + pin.Commit + ".tar.gz"
}

// Limits on what a fetch reads: the whole download, and one file.
const (
	maxArchive = 64 << 20
	maxFile    = 4 << 20
)

// fetchHosts are the only hosts a fetch talks to, over HTTPS: GitHub, and
// the host it sends archives from.
var fetchHosts = map[string]bool{"github.com": true, "codeload.github.com": true}

// fetchAllowed says why a URL is not one a fetch may open, "" when it is.
func fetchAllowed(u *url.URL) string {
	if u.Scheme != "https" || !fetchHosts[u.Hostname()] || (u.Port() != "" && u.Port() != "443") || u.User != nil {
		return "only https://github.com and https://codeload.github.com are fetched from, not " + u.Redacted()
	}
	return ""
}

// fetchClient refuses any request, redirects included, to anything but
// fetchHosts over HTTPS; the system's roots verify the certificates.
var fetchClient = &http.Client{
	CheckRedirect: func(req *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("too many redirects")
		}
		if why := fetchAllowed(req.URL); why != "" {
			return errors.New(why)
		}
		return nil
	},
}

// fetchGet opens the archive; tests replace it.
var fetchGet = func(ctx context.Context, raw string) (io.ReadCloser, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, err
	}
	if why := fetchAllowed(u); why != "" {
		return nil, errors.New(why)
	}
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
	if err != nil {
		cancel()
		return nil, err
	}
	resp, err := fetchClient.Do(req)
	if err != nil {
		cancel()
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		cancel()
		return nil, fmt.Errorf("GET %s: %s", raw, resp.Status)
	}
	return cancelBody{resp.Body, cancel}, nil
}

type cancelBody struct {
	io.ReadCloser
	cancel context.CancelFunc
}

func (b cancelBody) Close() error { err := b.ReadCloser.Close(); b.cancel(); return err }

// FetchPinned writes the pinned hypruse files into dir (made 0700), from
// GitHub's archive of the pinned commit, and checks the result as a clone.
func FetchPinned(ctx context.Context, pin Pin, dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	body, err := fetchGet(ctx, pin.ArchiveURL())
	if err != nil {
		return fmt.Errorf("download hypruse: %v", err)
	}
	defer body.Close()
	gz, err := gzip.NewReader(io.LimitReader(body, maxArchive))
	if err != nil {
		return fmt.Errorf("download hypruse: %v", err)
	}
	tr := tar.NewReader(gz)
	written := map[string]bool{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return fmt.Errorf("download hypruse: %v", err)
		}
		// GitHub puts everything under one top folder (<repo>-<commit>/).
		_, rel, ok := strings.Cut(h.Name, "/")
		want, pinned := pin.Files[rel]
		if !ok || !pinned {
			continue
		}
		if h.Typeflag != tar.TypeReg || h.Size > maxFile || written[rel] {
			return fmt.Errorf("download hypruse: %s is not a plain file of the pinned commit", rel)
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxFile+1))
		if err != nil {
			return fmt.Errorf("download hypruse: %v", err)
		}
		got := sha256.Sum256(data)
		if hex.EncodeToString(got[:]) != want {
			return fmt.Errorf("download hypruse: %s is not the pinned file; nothing was installed", rel)
		}
		dst := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
			return err
		}
		if err := os.WriteFile(dst, data, 0o600); err != nil {
			return err
		}
		written[rel] = true
	}
	var missing []string
	for rel := range pin.Files {
		if !written[rel] {
			missing = append(missing, rel)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return errors.New("download hypruse: the archive lacks " + strings.Join(missing, ", "))
	}
	return VerifyClone(pin, dir)
}

// FetchAndInstall installs the pinned hypruse with no clone from the
// human: it fetches the pinned files into a private folder under the
// venv's root, plans and installs from there (Install checks them once
// more), and removes the folder.
func FetchAndInstall(ctx context.Context, paths Paths, pin Pin, run Runner) (string, error) {
	if st, err := os.Lstat(paths.Root); err == nil && st.Mode()&os.ModeSymlink != 0 {
		return "", fmt.Errorf("%s is a symbolic link; Den installs only into a real folder", paths.Root)
	}
	if err := os.MkdirAll(paths.Root, 0o700); err != nil {
		return "", err
	}
	if err := os.Chmod(paths.Root, 0o700); err != nil {
		return "", err
	}
	src, err := os.MkdirTemp(paths.Root, "src-")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(src)
	if err := FetchPinned(ctx, pin, src); err != nil {
		return "", err
	}
	plan, err := makePlan(paths, src, "", pin)
	if err != nil {
		return "", err
	}
	return Install(ctx, plan, run)
}
