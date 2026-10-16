package desktop

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Manifest is what Den installed: every file in the venv with its SHA-256,
// every symlink with its target, and the interpreter the venv was made
// from. It is kept in Den's state, not in the venv.
type Manifest struct {
	Interpreter string            `json:"interpreter"`
	Files       map[string]string `json:"files"` // path in the venv -> sha256
	Links       map[string]string `json:"links"` // path in the venv -> link target
}

// Scan reads the venv as it is now.
func Scan(venv string) (Manifest, error) {
	m := Manifest{Files: map[string]string{}, Links: map[string]string{}}
	err := filepath.WalkDir(venv, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(venv, path)
		if err != nil || rel == "." {
			return err
		}
		switch t := d.Type(); {
		case t.IsDir():
			return nil
		case t&fs.ModeSymlink != 0:
			target, err := os.Readlink(path)
			if err != nil {
				return err
			}
			m.Links[rel] = target
		case t.IsRegular():
			h, err := fileSHA256(path)
			if err != nil {
				return err
			}
			m.Files[rel] = h
		default:
			return fmt.Errorf("%s is neither a file, a folder nor a link", rel)
		}
		return nil
	})
	return m, err
}

// Save writes the manifest at 0600 in a 0700 folder, through a rename.
func (m Manifest) Save(path string) error {
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o700); err != nil {
		return err
	}
	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// ErrNotInstalled and ErrNotAsInstalled are why Verify fails: no install
// at all, or one that is no longer as Den recorded it. Den offers to
// install, or reinstall, on the next desktop card.
var (
	ErrNotInstalled   = errors.New("hypruse is not installed")
	ErrNotAsInstalled = errors.New("Den's hypruse venv is not as installed")
)

// LoadManifest reads a saved manifest.
func LoadManifest(path string) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, ErrNotInstalled
	}
	if err != nil {
		return m, err
	}
	if err := json.Unmarshal(data, &m); err != nil || m.Files == nil {
		return m, fmt.Errorf("%w: the manifest at %s is unreadable", ErrNotAsInstalled, path)
	}
	return m, nil
}

// startupHooks are the files Python runs by itself at start-up, even under
// -I when they sit in site-packages: a .pth line that starts with "import",
// and the site and user customisation modules.
func startupHook(rel string) bool {
	base := filepath.Base(rel)
	return strings.HasSuffix(base, ".pth") || base == "sitecustomize.py" || base == "usercustomize.py" ||
		strings.HasPrefix(base, "sitecustomize.") || strings.HasPrefix(base, "usercustomize.")
}

// Verify checks the venv before a desktop agent starts: hypruse's files
// are the pinned ones; every file and link is exactly what the manifest
// recorded at install, none missing and none added; no start-up hook
// (sitecustomize, usercustomize) exists that pip did not put there; the
// interpreter link points where it did. Detection only: anything running
// as the same user could rewrite the manifest too.
func Verify(p Paths) error { return verify(p, Pinned) }

func verify(p Paths, pin Pin) error {
	want, err := LoadManifest(p.Manifest)
	if err != nil {
		return err
	}
	site, err := SitePackages(p.Venv)
	if err != nil {
		return err
	}
	var bad []string
	for rel, h := range pin.Files {
		name := filepath.Base(rel)
		if rel != "LICENSE" && filepath.Dir(rel) != PackageDir {
			continue
		}
		got, err := fileSHA256(filepath.Join(site, "hypruse", name))
		if err != nil || got != h {
			bad = append(bad, "hypruse/"+name+" is not the pinned file")
		}
	}
	got, err := Scan(p.Venv)
	if err != nil {
		return fmt.Errorf("cannot read the venv: %v", err)
	}
	for rel, h := range want.Files {
		g, ok := got.Files[rel]
		switch {
		case !ok:
			bad = append(bad, rel+" is missing")
		case g != h:
			bad = append(bad, rel+" changed")
		}
	}
	for rel := range got.Files {
		if _, ok := want.Files[rel]; !ok {
			what := " was added"
			if startupHook(rel) {
				what = " was added (Python runs it at start-up)"
			}
			bad = append(bad, rel+what)
		}
	}
	for rel, t := range want.Links {
		if got.Links[rel] != t {
			bad = append(bad, rel+" points elsewhere")
		}
	}
	for rel := range got.Links {
		if _, ok := want.Links[rel]; !ok {
			bad = append(bad, rel+" was added")
		}
	}
	for rel := range want.Files {
		// pip installs none of these for this lock; one in the record means
		// the record itself was edited.
		if b := filepath.Base(rel); b == "sitecustomize.py" || b == "usercustomize.py" {
			bad = append(bad, rel+" is a start-up hook Den does not install")
		}
	}
	if target, err := filepath.EvalSymlinks(p.Python()); err != nil || target != want.Interpreter {
		bad = append(bad, "bin/python is not "+want.Interpreter)
	}
	if len(bad) > 0 {
		sort.Strings(bad)
		if len(bad) > 8 {
			bad = append(bad[:8], fmt.Sprintf("and %d more", len(bad)-8))
		}
		return fmt.Errorf("%w: %s", ErrNotAsInstalled, strings.Join(bad, "; "))
	}
	return nil
}
