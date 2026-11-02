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
		"checks_test.go":    "e5f201fa83e74cc4a9deb951dd975427ed0380a32cfce82a7f8a995d1cccf6e8",
		"client.go":         "3d68274ba66a3193b91f6b5bde600cf993567ca3b57e42bdcdeb00f08fbd8a30",
		"console.go":        "82a12f17120bb47da5bea819f5cd51612f3831331748361afa344084a8843068",
		"d1.go":             "e56461e6d42ca713e91f193fae816d600d48e16263e36d6a9ecb0c6a2e5e2416",
		"denconformance.go": "d2b71446ae741b6bd340add1eb4a4c43bbb51506d42c1e374a3ae3b697641b0d",
		"mention.go":        "0657dcef747ef52fd99a231f03bf1c79946381bea0b294b82df7bb77fb2cb041",
		"README.md":         "48c9f66ee9d7c3cc43862153978300d0791af8ce9f47c716c0cc667f20e92ab6",
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
	if got := hex.EncodeToString(h[:]); got != "93efc0cb4f1637faa31396ea6f987981453d0fdd9b54ea601ef487dd09aa966e" {
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
