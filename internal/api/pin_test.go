package api

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// The conformance copy's code is byte-identical to the pinned upstream
// files, and its README is as PROVENANCE says.
func TestConformanceCopyIsPinned(t *testing.T) {
	want := map[string]string{
		"client.go":         "00a38c499b49839a11f7d4aa22644fe8d9b494cf725a60f897c4b0f8d1484372",
		"d1.go":             "e56461e6d42ca713e91f193fae816d600d48e16263e36d6a9ecb0c6a2e5e2416",
		"denconformance.go": "31ee8a6292ced9dbed6eeb98ebb67dd01766ea62c192592259a62d0e0669ed65",
		"README.md":         "b98f23695951a0d149c1ff5ca9849e4d13cccfe3e10059f19ccf2c1ca6de782b",
	}
	var sums strings.Builder
	var goFiles []string
	for name, sum := range want {
		raw, err := os.ReadFile(filepath.Join("denconformance", name))
		if err != nil {
			t.Fatal(err)
		}
		h := sha256.Sum256(raw)
		if got := hex.EncodeToString(h[:]); got != sum {
			t.Errorf("%s: sha256 %s, pinned %s", name, got, sum)
		}
		if strings.HasSuffix(name, ".go") {
			goFiles = append(goFiles, name)
		}
	}
	all, _ := filepath.Glob(filepath.Join("denconformance", "*.go"))
	if len(all) != len(goFiles) {
		t.Errorf("the copy holds Go files the pin does not: %v", all)
	}
	// The package hash: `sha256sum $(ls *.go | sort) | sha256sum`.
	sort.Strings(goFiles)
	for _, name := range goFiles {
		fmt.Fprintf(&sums, "%s  %s\n", want[name], name)
	}
	h := sha256.Sum256([]byte(sums.String()))
	if got := hex.EncodeToString(h[:]); got != "d251218a9b2f46804bbca41e47439d2e783b990a5711aa979b1f5e9bb40b075f" {
		t.Errorf("package hash %s", got)
	}
}

// Nothing test-only reaches the den binary.
func TestConformanceCopyStaysOutOfTheBinary(t *testing.T) {
	if testing.Short() {
		t.Skip("runs go list")
	}
	out, err := exec.Command("go", "list", "-deps", "../../cmd/den").Output()
	if err != nil {
		t.Skip("go list: ", err)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.Contains(pkg, "conformance") || strings.Contains(pkg, "testharness") {
			t.Errorf("cmd/den depends on %s", pkg)
		}
	}
}

// The API never parses the lane's words into Den's state: no package file
// of internal/api, nor internal/ui/api.go, calls the mission parsers or
// the orchestrator's reply handler.
func TestLaneOutputIsNeverParsed(t *testing.T) {
	files, _ := filepath.Glob("*.go")
	files = append(files, filepath.Join("..", "ui", "api.go"))
	banned := map[string]bool{"ParseUpdate": true, "ParseProposal": true, "missionReply": true, "applyUpdate": true, "handleOrchEvent": true}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		file, err := parser.ParseFile(token.NewFileSet(), f, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(file, func(n ast.Node) bool {
			switch x := n.(type) {
			case *ast.SelectorExpr:
				if banned[x.Sel.Name] {
					t.Errorf("%s uses %s", f, x.Sel.Name)
				}
			case *ast.Ident:
				if banned[x.Name] {
					t.Errorf("%s uses %s", f, x.Name)
				}
			}
			return true
		})
	}
}
