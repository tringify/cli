package main

import (
	"encoding/json"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// Every store MCP call the CLI makes must name a real tool and send only the
// arguments its schema declares, including all required ones. The schemas in
// testdata/store-mcp-tools.json are copied from the live tools/list of
// https://api.tringify.com/mcp/store; refresh them when a tool changes.
func TestStoreToolCallsMatchTheirSchemas(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("testdata", "store-mcp-tools.json"))
	if err != nil {
		t.Fatal(err)
	}
	var tools []struct {
		Name        string `json:"name"`
		InputSchema struct {
			Properties map[string]any `json:"properties"`
			Required   []string       `json:"required"`
		} `json:"inputSchema"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		t.Fatal(err)
	}
	schemas := map[string]int{}
	for i, tool := range tools {
		schemas[tool.Name] = i
	}
	files, _ := filepath.Glob("*.go")
	calls := 0
	fset := token.NewFileSet()
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		parsed, err := parser.ParseFile(fset, file, nil, 0)
		if err != nil {
			t.Fatal(err)
		}
		ast.Inspect(parsed, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "Call" || len(call.Args) != 3 {
				return true
			}
			where := fset.Position(call.Pos()).String()
			lit, ok := call.Args[1].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				t.Errorf("%s: tool name must be a string literal so it can be checked", where)
				return true
			}
			name, _ := strconv.Unquote(lit.Value)
			calls++
			index, ok := schemas[name]
			if !ok {
				t.Errorf("%s: %s is not a store MCP tool (or is missing from testdata)", where, name)
				return true
			}
			schema := tools[index].InputSchema
			sent := map[string]bool{}
			switch args := call.Args[2].(type) {
			case *ast.Ident:
				if args.Name != "nil" {
					t.Errorf("%s: %s arguments must be a map literal or nil", where, name)
					return true
				}
			case *ast.CompositeLit:
				for _, element := range args.Elts {
					kv, ok := element.(*ast.KeyValueExpr)
					key, isLit := kv.Key.(*ast.BasicLit)
					if !ok || !isLit {
						t.Errorf("%s: %s argument keys must be string literals", where, name)
						continue
					}
					field, _ := strconv.Unquote(key.Value)
					sent[field] = true
					if _, declared := schema.Properties[field]; !declared {
						t.Errorf("%s: %s has no argument %q", where, name, field)
					}
				}
			default:
				t.Errorf("%s: %s arguments must be a map literal or nil", where, name)
				return true
			}
			for _, required := range schema.Required {
				if !sent[required] {
					t.Errorf("%s: %s requires %q", where, name, required)
				}
			}
			return true
		})
	}
	if calls < len(tools) {
		t.Errorf("found %d tool calls for %d tools in testdata; unused schemas should be removed", calls, len(tools))
	}
}
