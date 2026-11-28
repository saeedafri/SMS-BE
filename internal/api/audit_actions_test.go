package api

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	gen "github.com/saeedafri/sms-be/internal/gen/api"
)

// Every action written to the audit log must be an AuditAction the contract
// declares, or the audit-log filter refuses it with a 422 and the row can be
// served but never searched for. gen.AuditAction(entry.Action) accepts any
// string, so neither compiler sees the gap: rcs_agent.review and the two
// channel actions sat in it until ask 73. This test walks the module's source
// from every RecordOperatorAction call back to the string literals that reach
// its action argument, following wrappers that pass the action on as a
// parameter, and fails on any argument it cannot trace to a literal.
func TestEveryAuditedActionIsADeclaredAuditAction(t *testing.T) {
	if _, err := os.Stat("../../cmd"); err != nil {
		t.Skip("reads the source tree, which make test does not upload; the deploy workflow's go test runs it")
	}
	fset := token.NewFileSet()
	var funcs []*ast.FuncDecl
	visit := func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() && path == filepath.Join("..", "gen") {
			return filepath.SkipDir
		}
		if entry.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		for _, decl := range file.Decls {
			if fn, ok := decl.(*ast.FuncDecl); ok && fn.Body != nil {
				funcs = append(funcs, fn)
			}
		}
		return nil
	}
	for _, root := range []string{"../../cmd", ".."} {
		if err := filepath.WalkDir(root, visit); err != nil {
			t.Fatal(err)
		}
	}

	// Functions keyed by name, valued by the index of their action argument.
	// ponytail: keyed by bare name, so two functions sharing one name in
	// different packages would be treated as one; go/types if that ever happens.
	sinks := map[string]int{"RecordOperatorAction": 3}
	actions := map[string]string{}
	unresolved := map[string]bool{}
	for grew := true; grew; {
		grew = false
		for _, fn := range funcs {
			ast.Inspect(fn.Body, func(node ast.Node) bool {
				call, ok := node.(*ast.CallExpr)
				if !ok {
					return true
				}
				index, ok := sinks[calleeName(call.Fun)]
				if !ok || index >= len(call.Args) {
					return true
				}
				where := fset.Position(call.Pos()).String()
				switch arg := call.Args[index].(type) {
				case *ast.BasicLit:
					actions[unquote(t, arg)] = where
				case *ast.Ident:
					if param := paramIndex(fn, arg.Name); param >= 0 {
						if _, known := sinks[fn.Name.Name]; !known {
							sinks[fn.Name.Name] = param
							grew = true
						}
						return true
					}
					literals := assignedLiterals(fn, arg.Name)
					if literals == nil {
						unresolved[where] = true
					}
					for _, lit := range literals {
						actions[unquote(t, lit)] = where
					}
				default:
					unresolved[where] = true
				}
				return true
			})
		}
	}

	for where := range unresolved {
		t.Errorf("%s: the audit action is not a literal, a parameter, or a local assigned only literals", where)
	}
	if len(actions) == 0 {
		t.Fatal("found no audit actions: the walk is not reading the source")
	}
	for action, where := range actions {
		if !gen.AuditAction(action).Valid() {
			t.Errorf("%s writes %q, which is not a declared AuditAction", where, action)
		}
	}
}

func calleeName(fun ast.Expr) string {
	switch callee := fun.(type) {
	case *ast.Ident:
		return callee.Name
	case *ast.SelectorExpr:
		return callee.Sel.Name
	}
	return ""
}

func paramIndex(fn *ast.FuncDecl, name string) int {
	index := 0
	for _, field := range fn.Type.Params.List {
		for _, param := range field.Names {
			if param.Name == name {
				return index
			}
			index++
		}
	}
	return -1
}

// assignedLiterals returns the string literals assigned to a local variable in
// fn, or nil if anything other than a string literal is ever assigned to it.
func assignedLiterals(fn *ast.FuncDecl, name string) []*ast.BasicLit {
	var literals []*ast.BasicLit
	clean := true
	ast.Inspect(fn.Body, func(node ast.Node) bool {
		assign, ok := node.(*ast.AssignStmt)
		if !ok {
			return true
		}
		for i, lhs := range assign.Lhs {
			if ident, ok := lhs.(*ast.Ident); !ok || ident.Name != name {
				continue
			}
			if len(assign.Lhs) != len(assign.Rhs) {
				clean = false
				continue
			}
			lit, ok := assign.Rhs[i].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				clean = false
				continue
			}
			literals = append(literals, lit)
		}
		return true
	})
	if !clean {
		return nil
	}
	return literals
}

func unquote(t *testing.T, lit *ast.BasicLit) string {
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		t.Fatalf("%s: %v", lit.Value, err)
	}
	return value
}
