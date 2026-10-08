package mcpsrv

import (
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"testing"
)

func toolByName(t *testing.T, name string) mcpTool {
	t.Helper()
	for _, set := range [][]mcpTool{memberTools(), adminTools()} {
		for _, tool := range set {
			if tool.name == name {
				return tool
			}
		}
	}
	t.Fatalf("no tool %q", name)
	return mcpTool{}
}

func TestUnknownArgsErrorNamesTheIntendedKey(t *testing.T) {
	tool := toolByName(t, "create_session")
	want := `unknown argument "prompt" for create_session (did you mean "initial_prompt"?)`
	if got := unknownArgsError(tool, map[string]any{"dir": "/r", "prompt": "x"}); got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
	if got := unknownArgsError(tool, map[string]any{"dir": "/r", "initial_prompt": "x", "worktree": true}); got != "" {
		t.Fatalf("known keys refused: %q", got)
	}
	if got := unknownArgsError(tool, map[string]any{"zzzzzzzz": 1}); got == "" || got[len(got)-1] == ')' {
		t.Fatalf("got %q, want a refusal without a suggestion", got)
	}
}

// Every key a tool's run func reads through args must be in the schema it is advertised with,
// or the unknown-argument check would refuse a call the tool understands. Only direct reads
// (argX(args, "k") / args["k"]) are visible to a source scan.
func TestEveryArgReadByAToolIsAdvertised(t *testing.T) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "mcp.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	schemas := map[string]map[string]any{}
	for _, set := range [][]mcpTool{memberTools(), adminTools()} {
		for _, tool := range set {
			props, _ := tool.schema["properties"].(map[string]any)
			schemas[tool.name] = props
		}
	}
	checked := 0
	ast.Inspect(file, func(n ast.Node) bool {
		lit, ok := n.(*ast.CompositeLit)
		if !ok {
			return true
		}
		var name string
		var runs []*ast.FuncLit
		for _, e := range lit.Elts {
			kv, ok := e.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			key, _ := kv.Key.(*ast.Ident)
			if key == nil {
				continue
			}
			switch v := kv.Value.(type) {
			case *ast.BasicLit:
				if key.Name == "name" {
					name, _ = strconv.Unquote(v.Value)
				}
			case *ast.FuncLit:
				if key.Name == "run" || key.Name == "runAdmin" {
					runs = append(runs, v)
				}
			}
		}
		if name == "" || len(runs) == 0 {
			return true
		}
		props, known := schemas[name]
		if !known {
			t.Errorf("tool %q has no advertised schema", name)
			return true
		}
		read := func(k string) {
			checked++
			if _, ok := props[k]; !ok {
				t.Errorf("%s reads argument %q that its schema does not advertise", name, k)
			}
		}
		for _, fn := range runs {
			ast.Inspect(fn.Body, func(m ast.Node) bool {
				switch x := m.(type) {
				case *ast.CallExpr:
					if id, ok := x.Fun.(*ast.Ident); ok && len(x.Args) == 2 && len(id.Name) > 3 && id.Name[:3] == "arg" {
						if a, ok := x.Args[0].(*ast.Ident); ok && a.Name == "args" {
							if s, ok := x.Args[1].(*ast.BasicLit); ok {
								k, _ := strconv.Unquote(s.Value)
								read(k)
							}
						}
					}
				case *ast.IndexExpr:
					if a, ok := x.X.(*ast.Ident); ok && a.Name == "args" {
						if s, ok := x.Index.(*ast.BasicLit); ok {
							k, _ := strconv.Unquote(s.Value)
							read(k)
						}
					}
				}
				return true
			})
		}
		return true
	})
	if checked < 20 {
		t.Fatalf("only %d argument reads found; the scan is not seeing the tools", checked)
	}
}
