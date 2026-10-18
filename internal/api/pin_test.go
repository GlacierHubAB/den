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

// The conformance copy is byte-identical to the pinned upstream files,
// README included.
func TestConformanceCopyIsPinned(t *testing.T) {
	want := map[string]string{
		"checks_test.go":    "9227d14c24618a82ee5b21b72057cf8af9c53e3ee46dd98deeeef2653ed96348",
		"client.go":         "803916dc11644943879be59917f21133f5fa7ef0163430361a770181d792aa40",
		"console.go":        "c3eabd4f6ad771fbefaf6bc38283bfa0774fb51a9258ad31ff88bd5a9271cf9c",
		"d1.go":             "e56461e6d42ca713e91f193fae816d600d48e16263e36d6a9ecb0c6a2e5e2416",
		"denconformance.go": "d2b71446ae741b6bd340add1eb4a4c43bbb51506d42c1e374a3ae3b697641b0d",
		"README.md":         "fa5ab2c12862fd749802cea8f468d4c0407a40c1df568f870149c401958c6dbc",
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
	if got := hex.EncodeToString(h[:]); got != "de22142798bb7df52a7f897eed98e2300821f29e810a3fa91f0a875efbf8bcd7" {
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
