package desktop

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// entry is one member of a fake archive.
type entry struct {
	name, body string
	kind       byte
	size       int64 // the header's size when set, else len(body)
}

func archive(t *testing.T, es ...entry) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range es {
		h := &tar.Header{Name: e.name, Mode: 0o644, Size: int64(len(e.body)), Typeflag: tar.TypeReg}
		if e.kind != 0 {
			h.Typeflag = e.kind
		}
		if h.Typeflag == tar.TypeSymlink {
			h.Linkname, h.Size = e.body, 0
		}
		if e.size != 0 {
			h.Size = e.size
		}
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		if h.Typeflag == tar.TypeReg {
			tw.Write([]byte(e.body))
		}
	}
	tw.Close()
	gz.Close()
	return buf.Bytes()
}

// serve makes fetchGet return data, and records the URL asked for.
func serve(t *testing.T, data []byte) *string {
	t.Helper()
	var asked string
	old := fetchGet
	fetchGet = func(_ context.Context, u string) (io.ReadCloser, error) {
		asked = u
		return io.NopCloser(bytes.NewReader(data)), nil
	}
	t.Cleanup(func() { fetchGet = old })
	return &asked
}

func pinnedEntries() []entry {
	return []entry{
		{name: "hypruse-0123/LICENSE", body: "MIT License\n"},
		{name: "hypruse-0123/src/hypruse/__init__.py", body: "__version__ = \"0\"\n"},
		{name: "hypruse-0123/src/hypruse/server.py", body: "def main(): pass\n"},
	}
}

// Only the pinned paths are written, each checked; the rest of the archive
// is skipped, wherever it points.
func TestFetchPinnedWritesOnlyThePin(t *testing.T) {
	_, pin := fakeClone(t)
	es := append(pinnedEntries(),
		entry{name: "hypruse-0123/src/hypruse/evil.py", body: "import os\n"},
		entry{name: "hypruse-0123/../../escape", body: "x"},
		entry{name: "/etc/passwd-den-test", body: "x"},
		entry{name: "hypruse-0123/setup.py", body: "x"},
		entry{name: "hypruse-0123/link", body: "/etc", kind: tar.TypeSymlink},
	)
	asked := serve(t, archive(t, es...))
	dir := filepath.Join(t.TempDir(), "src")
	if err := FetchPinned(context.Background(), pin, dir); err != nil {
		t.Fatalf("fetch: %v", err)
	}
	if *asked != "https://example.invalid/hypruse/archive/0123456789abcdef.tar.gz" {
		t.Errorf("asked %s", *asked)
	}
	var got []string
	filepath.WalkDir(filepath.Dir(dir), func(p string, d os.DirEntry, err error) error {
		if err == nil && !d.IsDir() {
			rel, _ := filepath.Rel(dir, p)
			got = append(got, rel)
		}
		return nil
	})
	if strings.Join(got, ",") != "LICENSE,src/hypruse/__init__.py,src/hypruse/server.py" {
		t.Fatalf("written %v", got)
	}
	if st, _ := os.Stat(dir); st.Mode().Perm() != 0o700 {
		t.Errorf("folder mode %v", st.Mode())
	}
}

// Any mismatch fails closed: a changed file, a missing one, a pinned name
// that is a link, an oversized member.
func TestFetchPinnedFailsClosed(t *testing.T) {
	_, pin := fakeClone(t)
	good := pinnedEntries()
	cases := map[string][]entry{
		"changed": {good[0], good[1], {name: good[2].name, body: "def main(): import os\n"}},
		"missing": {good[0], good[1]},
		"a link":  {good[0], good[1], {name: good[2].name, body: "/etc/passwd", kind: tar.TypeSymlink}},
		"huge":    {good[0], good[1], {name: good[2].name, body: good[2].body, size: maxFile + 1}},
		"twice":   {good[0], good[1], good[2], good[2]},
	}
	for name, es := range cases {
		serve(t, archive(t, es...))
		if err := FetchPinned(context.Background(), pin, filepath.Join(t.TempDir(), "src")); err == nil {
			t.Errorf("%s: fetched", name)
		}
	}
	serve(t, []byte("not gzip"))
	if err := FetchPinned(context.Background(), pin, filepath.Join(t.TempDir(), "src")); err == nil {
		t.Error("not an archive: fetched")
	}
}

// The real pin is fetched from GitHub, and nothing but HTTPS to GitHub's
// two hosts is ever opened, redirects included.
func TestFetchOnlyFromGitHub(t *testing.T) {
	if got := Pinned.ArchiveURL(); got != "https://github.com/IlyasKhallouki/hypruse/archive/"+Pinned.Commit+".tar.gz" {
		t.Fatalf("archive URL %s", got)
	}
	for raw, ok := range map[string]bool{
		"https://github.com/IlyasKhallouki/hypruse/archive/x.tar.gz":  true,
		"https://codeload.github.com/IlyasKhallouki/hypruse/tar.gz/x": true,
		"https://github.com:443/x":                                    true,
		"http://github.com/x":                                         false,
		"https://evil.example/x":                                      false,
		"https://github.com.evil.example/x":                           false,
		"https://github.com:8443/x":                                   false,
		"https://user:pw@github.com/x":                                false,
		"https://objects.githubusercontent.com/x":                     false,
		"file:///etc/passwd":                                          false,
	} {
		u, _ := url.Parse(raw)
		if got := fetchAllowed(u) == ""; got != ok {
			t.Errorf("%s: allowed %v", raw, got)
		}
	}
	r, _ := http.NewRequest("GET", "http://github.com/x", nil)
	if fetchClient.CheckRedirect(r, []*http.Request{{}}) == nil {
		t.Error("a redirect to plain HTTP was followed")
	}
	r, _ = http.NewRequest("GET", "https://codeload.github.com/x", nil)
	if err := fetchClient.CheckRedirect(r, []*http.Request{{}}); err != nil {
		t.Errorf("GitHub's archive host: %v", err)
	}
	if _, err := fetchGet(context.Background(), "https://evil.example/x"); err == nil {
		t.Error("fetchGet opened another host")
	}
}

// With no clone from the human: fetched, installed, verified, and the
// fetched source folder is gone afterwards. A bad archive installs nothing.
func TestFetchAndInstall(t *testing.T) {
	_, pin := fakeClone(t)
	base := t.TempDir()
	py := filepath.Join(base, "python3.14")
	os.WriteFile(py, []byte("#!fake\n"), 0o755)
	old := systemPython
	systemPython = py
	t.Cleanup(func() { systemPython = old })
	paths := Paths{Root: filepath.Join(base, "data", "hypruse"), Manifest: filepath.Join(base, "state", "venv-manifest.json")}
	paths.Venv = filepath.Join(paths.Root, "venv")

	serve(t, archive(t, pinnedEntries()...))
	var calls [][]string
	if _, err := FetchAndInstall(context.Background(), paths, pin, fakeRun(t, py, &calls)); err != nil {
		t.Fatalf("install: %v", err)
	}
	if err := verify(paths, pin); err != nil {
		t.Fatalf("verify after a fetched install: %v", err)
	}
	if left, _ := filepath.Glob(filepath.Join(paths.Root, "src-*")); len(left) != 0 {
		t.Errorf("the fetched source stayed: %v", left)
	}

	good := pinnedEntries()
	serve(t, archive(t, good[0], good[1], entry{name: good[2].name, body: "evil\n"}))
	calls = nil
	os.Remove(paths.Manifest)
	if _, err := FetchAndInstall(context.Background(), paths, pin, fakeRun(t, py, &calls)); err == nil || len(calls) != 0 {
		t.Fatalf("a bad archive: %v, ran %q", err, calls)
	}
	if _, err := os.Stat(paths.Manifest); !os.IsNotExist(err) {
		t.Error("a manifest after a failed fetch")
	}
}
