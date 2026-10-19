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
		"checks_test.go":    "1e9b4e804308ce9f5140ac40ada8809634df3449f946fcad7e5370f1421d91db",
		"client.go":         "912bea7e150aace693040f9c37df49f3ceb3180b7f692fe417f5066ad41ab8c0",
		"console.go":        "e7d185cb248b581f701f597f25f0e6c24b81522d2380ed28e5ddbab8ed2a036f",
		"d1.go":             "e56461e6d42ca713e91f193fae816d600d48e16263e36d6a9ecb0c6a2e5e2416",
		"denconformance.go": "d2b71446ae741b6bd340add1eb4a4c43bbb51506d42c1e374a3ae3b697641b0d",
		"README.md":         "840322a8be7689ee1b17fc1a576983a8cb63a16e04b10bf1fe20410bee248137",
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
	if got := hex.EncodeToString(h[:]); got != "6c2244979c6a6403d41d31af449829d08b360b79f0702b456b501a36576415b6" {
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
