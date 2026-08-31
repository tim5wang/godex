package httpapi

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestStaticRoutePatternsHaveSingleOwner(t *testing.T) {
	owners := staticRouteOwners(t)
	var duplicates []string
	for pattern, locations := range owners {
		if len(locations) > 1 {
			duplicates = append(duplicates, pattern+": "+strings.Join(locations, ", "))
		}
	}
	sort.Strings(duplicates)
	if len(duplicates) > 0 {
		t.Fatalf("static route patterns must have one owner:\n%s", strings.Join(duplicates, "\n"))
	}

	expectedOwners := map[string][]string{
		"routes_config.go": {
			"GET /config/meta", "GET /config/schema", "GET /config", "PUT /config",
			"POST /config/reload", "POST /config/reveal", "GET /config/doctor",
		},
		"routes_control.go": {
			"GET /runtime/service", "POST /runtime/service/restart",
			"GET /control/nodes", "GET /control/nodes/{id}", "DELETE /control/nodes/{id}",
			"POST /control/nodes/register", "POST /control/nodes/{id}/heartbeat",
			"POST /control/nodes/{id}/credential", "GET /control/nodes/{id}/overview",
		},
		"routes_providers.go": {
			"GET /providers", "POST /providers/{id}/test", "POST /providers/{id}/models",
			"GET /providers/import/codex", "POST /providers/import/codex",
		},
	}
	for file, patterns := range expectedOwners {
		for _, pattern := range patterns {
			locations := owners[pattern]
			if len(locations) != 1 || !strings.HasPrefix(locations[0], file+":") {
				t.Fatalf("route %q must be owned by %s, got %v", pattern, file, locations)
			}
		}
	}
}

func staticRouteOwners(t *testing.T) map[string][]string {
	t.Helper()
	_, currentFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve route ownership test path")
	}
	dir := filepath.Dir(currentFile)
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatalf("list httpapi source files: %v", err)
	}

	owners := make(map[string][]string)
	for _, path := range files {
		if strings.HasSuffix(path, "_test.go") {
			continue
		}
		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", filepath.Base(path), err)
		}
		ast.Inspect(file, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) == 0 {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || (selector.Sel.Name != "Handle" && selector.Sel.Name != "HandleFunc") {
				return true
			}
			literal, ok := call.Args[0].(*ast.BasicLit)
			if !ok || literal.Kind != token.STRING {
				return true
			}
			pattern, err := strconv.Unquote(literal.Value)
			if err != nil {
				t.Fatalf("decode route pattern in %s: %v", filepath.Base(path), err)
			}
			position := fset.Position(literal.Pos())
			owners[pattern] = append(owners[pattern], filepath.Base(path)+":"+strconv.Itoa(position.Line))
			return true
		})
	}
	return owners
}
