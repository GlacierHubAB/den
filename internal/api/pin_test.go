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
		"client.go":         "5187a75a26c52242dad23f2e310bb3d1add6c1296cc0a6b00f9444e8ed98a574",
		"console.go":        "f61155da0378696237a36db2eac62a77d1036d733eda8e076e068d2d38c35944",
		"d1.go":             "e56461e6d42ca713e91f193fae816d600d48e16263e36d6a9ecb0c6a2e5e2416",
		"denconformance.go": "a3baeeca05197103feece0a158320f489aa925e5bfbc45eefb958c6c85c61ccf",
		"README.md":         "725be8d4898d0cf49582ae14337074897b427d505955b596870ea53065dfbdd2",
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
	if got := hex.EncodeToString(h[:]); got != "c7eaf46d54b4131da35276ca9e8101ae9632c5768a670a384ec647210729a2b0" {
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
