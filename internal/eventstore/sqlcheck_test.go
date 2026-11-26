package eventstore

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// sqlMethods maps database/sql-style methods to the index of their SQL argument.
var sqlMethods = map[string]int{
	"Exec": 0, "Query": 0, "QueryRow": 0, "Prepare": 0,
	"ExecContext": 1, "QueryContext": 1, "QueryRowContext": 1, "PrepareContext": 1,
}

// TestSQLIsParameterized enforces "parameterized SQL only" in this package: the SQL argument of
// every Exec, Query, QueryRow or Prepare call is a string constant (or a constant concatenation),
// or a parameter named query that a wrapper forwards. Values always travel as bind arguments, never
// inside the SQL text. A call that runs trusted embedded SQL is marked "sqlcheck:trusted" on its
// line.
func TestSQLIsParameterized(t *testing.T) {
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	var parsed []*ast.File
	consts := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		af, err := parser.ParseFile(fset, f, nil, parser.ParseComments)
		if err != nil {
			t.Fatal(err)
		}
		parsed = append(parsed, af)
		for _, d := range af.Decls {
			if g, ok := d.(*ast.GenDecl); ok && g.Tok == token.CONST {
				for _, spec := range g.Specs {
					for _, n := range spec.(*ast.ValueSpec).Names {
						consts[n.Name] = true
					}
				}
			}
		}
	}
	if len(parsed) == 0 {
		t.Fatal("no source files found")
	}
	var constant func(e ast.Expr) bool
	constant = func(e ast.Expr) bool {
		switch x := e.(type) {
		case *ast.BasicLit:
			return x.Kind == token.STRING
		case *ast.Ident:
			return consts[x.Name] || x.Name == "query"
		case *ast.BinaryExpr:
			return x.Op == token.ADD && constant(x.X) && constant(x.Y)
		case *ast.ParenExpr:
			return constant(x.X)
		}
		return false
	}
	calls := 0
	for _, af := range parsed {
		trusted := map[int]bool{}
		for _, cg := range af.Comments {
			for _, c := range cg.List {
				if strings.Contains(c.Text, "sqlcheck:trusted") {
					trusted[fset.Position(c.Pos()).Line] = true
				}
			}
		}
		ast.Inspect(af, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			idx, ok := sqlMethods[sel.Sel.Name]
			if !ok || len(call.Args) <= idx {
				return true
			}
			calls++
			pos := fset.Position(call.Pos())
			if trusted[pos.Line] {
				return true
			}
			if !constant(call.Args[idx]) {
				t.Errorf("%s: SQL passed to %s is not a constant; bind values as arguments", pos, sel.Sel.Name)
			}
			return true
		})
	}
	if calls < 10 {
		t.Fatalf("found only %d SQL calls; the check is not seeing the code", calls)
	}
	// The rule is only as good as the marker discipline: keep trusted calls rare.
	data, _ := os.ReadFile("migrate.go")
	if n := strings.Count(string(data), "sqlcheck:trusted"); n != 1 {
		t.Errorf("migrate.go has %d sqlcheck:trusted markers, want exactly 1 (the embedded migration body)", n)
	}
}
