package runs

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// TestGitAndSecurityOnlyThroughTheHelpers keeps git on GitCommand (a
// context, and no terminal prompt) and security on hostctl's Keychain (a
// timeout): no other non-test code may exec either by name.
func TestGitAndSecurityOnlyThroughTheHelpers(t *testing.T) {
	allowed := map[string]string{
		"git":      filepath.Join("internal", "runs", "workspace.go"),
		"security": filepath.Join("internal", "hostctl", "secrets.go"),
	}
	root := filepath.Join("..", "..")
	fset := token.NewFileSet()
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if n := d.Name(); n == "node_modules" || n == ".git" || n == ".bottown" || (strings.HasPrefix(n, ".") && path != root) {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		f, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (sel.Sel.Name != "Command" && sel.Sel.Name != "CommandContext") {
				return true
			}
			if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != "exec" {
				return true
			}
			args := call.Args
			if sel.Sel.Name == "CommandContext" && len(args) > 0 {
				args = args[1:]
			}
			if len(args) == 0 {
				return true
			}
			lit, ok := args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			name, _ := strconv.Unquote(lit.Value)
			if want, ok := allowed[name]; ok && rel != want {
				t.Errorf("%s: exec.%s(%q, …): use the shared helper in %s", fset.Position(call.Pos()), sel.Sel.Name, name, want)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
