package architecture_test

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

const goFunctionComplexityLimit = 40

// Existing hotspots are capped at their current score. Do not raise these
// limits; split the function instead. Once a function reaches the default
// limit, this test requires removing its exception.
var goFunctionComplexityExceptions = map[string]int{
	"internal/acp/server/handler.go:BackendPromptHandlerWithOptions": 78,
	"internal/core/config/doctor.go:Manager.Doctor":                  110,
	"internal/core/conversation/runner.go:Runner.Run":                72,
	"internal/plugins/taskboard/tools.go:dispatchTaskboard":          68,
	"internal/tools/browser_tool.go:NewBrowserTool":                  42,
	"internal/tools/cron.go:NewCronTool":                             44,
	"internal/tools/lsp_tool.go:NewLSPTool":                          57,
}

func TestGoFunctionComplexityBudget(t *testing.T) {
	repoRoot := architectureRepoRoot(t)
	seenExceptions := make(map[string]bool, len(goFunctionComplexityExceptions))
	var violations []string
	for _, sourceDir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(repoRoot, sourceDir), func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if entry.IsDir() || filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			return inspectGoFunctionComplexity(repoRoot, path, seenExceptions, &violations)
		})
		if err != nil {
			t.Fatalf("scan Go source complexity: %v", err)
		}
	}

	for name := range goFunctionComplexityExceptions {
		if !seenExceptions[name] {
			violations = append(violations, name+": remove missing function exception")
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("Go function complexity budget violations:\n%s", strings.Join(violations, "\n"))
	}
}

func inspectGoFunctionComplexity(repoRoot, path string, seenExceptions map[string]bool, violations *[]string) error {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, nil, 0)
	if err != nil {
		return err
	}
	rel, err := filepath.Rel(repoRoot, path)
	if err != nil {
		return err
	}
	rel = filepath.ToSlash(rel)
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Body == nil {
			continue
		}
		name := rel + ":" + goFunctionName(function)
		score := goCyclomaticComplexity(function.Body)
		limit := goFunctionComplexityLimit
		if exceptionLimit, exists := goFunctionComplexityExceptions[name]; exists {
			seenExceptions[name] = true
			limit = exceptionLimit
			if score <= goFunctionComplexityLimit {
				*violations = append(*violations, fmt.Sprintf("%s: remove resolved exception (score %d)", name, score))
				continue
			}
		}
		if score > limit {
			*violations = append(*violations, fmt.Sprintf("%s: score %d (limit %d)", name, score, limit))
		}
	}
	return nil
}

func goFunctionName(function *ast.FuncDecl) string {
	if function.Recv == nil || len(function.Recv.List) == 0 {
		return function.Name.Name
	}
	receiver := function.Recv.List[0].Type
	if pointer, ok := receiver.(*ast.StarExpr); ok {
		receiver = pointer.X
	}
	if identifier, ok := receiver.(*ast.Ident); ok {
		return identifier.Name + "." + function.Name.Name
	}
	return function.Name.Name
}

func goCyclomaticComplexity(body *ast.BlockStmt) int {
	score := 1
	ast.Inspect(body, func(node ast.Node) bool {
		switch value := node.(type) {
		case *ast.IfStmt, *ast.ForStmt, *ast.RangeStmt:
			score++
		case *ast.CaseClause:
			if len(value.List) > 0 {
				score++
			}
		case *ast.CommClause:
			if value.Comm != nil {
				score++
			}
		case *ast.BinaryExpr:
			if value.Op == token.LAND || value.Op == token.LOR {
				score++
			}
		}
		return true
	})
	return score
}
