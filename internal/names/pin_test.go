package names

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"testing"
)

// decisions are the functions that decide what an agent may do or what
// happens to it: God mode's card rule, the dock's, the desktop agent's
// hooks, the team cap, the closer's guard, and who counts as the
// orchestrator. A name grants nothing (sec's N8, N10): none of them may
// compare, match or index on an agent's name, critter name or alias. A
// peer handle (x.Session.Name) is a session's address, not its name.
var decisions = map[string][]string{
	"../ui": {"dockMayAnswer", "godTTYOnly", "deskHooks", "handleTeamQuery", "applyTeamCap",
		"guardReason", "guardHeld", "guardKey", "staleAgents", "isOrchestrator", "fromOrchestratorSession"},
	"../closer":     {"Protected", "IsProtected", "Held", "Protect", "Unprotect", "SetFlag", "Unflag"},
	"../../cmd/den": {"teamRoom"},
}

func TestNoDecisionReadsAName(t *testing.T) {
	for dir, funcs := range decisions {
		want := map[string]bool{}
		for _, f := range funcs {
			want[f] = true
		}
		fset := token.NewFileSet()
		files, _ := filepath.Glob(filepath.Join(dir, "*.go"))
		for _, path := range files {
			if filepath.Ext(path) != ".go" || len(path) > 8 && path[len(path)-8:] == "_test.go" {
				continue
			}
			f, err := parser.ParseFile(fset, path, nil, 0)
			if err != nil {
				t.Fatal(err)
			}
			for _, d := range f.Decls {
				fn, ok := d.(*ast.FuncDecl)
				if !ok || !want[fn.Name.Name] || fn.Body == nil {
					continue
				}
				delete(want, fn.Name.Name)
				ast.Inspect(fn.Body, func(n ast.Node) bool {
					var operands []ast.Expr
					switch e := n.(type) {
					case *ast.BinaryExpr:
						// Asking whether a name is there at all, to word a
						// message, decides nothing.
						if (e.Op == token.EQL || e.Op == token.NEQ) && !emptyString(e.X) && !emptyString(e.Y) {
							operands = []ast.Expr{e.X, e.Y}
						}
					case *ast.IndexExpr:
						operands = []ast.Expr{e.Index}
					case *ast.CallExpr:
						if sel, ok := e.Fun.(*ast.SelectorExpr); ok {
							if pkg, ok := sel.X.(*ast.Ident); ok && pkg.Name == "strings" {
								operands = e.Args
							}
						}
					}
					for _, o := range operands {
						if name := readsName(o); name != "" {
							t.Errorf("%s: %s decides on %s", fset.Position(o.Pos()), fn.Name.Name, name)
						}
					}
					return true
				})
			}
		}
		for f := range want {
			t.Errorf("%s: no function %s to check (renamed? update the list)", dir, f)
		}
	}
}

// readsName is the name field an expression reads, "" for none.
func readsName(e ast.Expr) string {
	found := ""
	ast.Inspect(e, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch sel.Sel.Name {
		case "Critter", "Alias":
			found = sel.Sel.Name
		case "Name":
			if inner, ok := sel.X.(*ast.SelectorExpr); ok && inner.Sel.Name == "Session" {
				return true // a peer handle
			}
			found = "Name"
		}
		return found == ""
	})
	return found
}

func emptyString(e ast.Expr) bool {
	lit, ok := e.(*ast.BasicLit)
	return ok && lit.Kind == token.STRING && (lit.Value == `""` || lit.Value == "``")
}

// The pin itself tells a name from a peer handle.
func TestReadsName(t *testing.T) {
	for src, want := range map[string]string{
		`a.Name`:              "Name",
		`m.base[i].Name`:      "Name",
		`r.Critter`:           "Critter",
		`c.Alias`:             "Alias",
		`a.Session.Name`:      "",
		`a.Session.SessionID`: "",
	} {
		e, err := parser.ParseExpr(src)
		if err != nil {
			t.Fatal(err)
		}
		if got := readsName(e); got != want {
			t.Errorf("readsName(%s) = %q, want %q", src, got, want)
		}
	}
}
