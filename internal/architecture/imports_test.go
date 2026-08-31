package architecture_test

import (
	"encoding/json"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"
)

const modulePath = "github.com/tim5wang/godex"

type listedPackage struct {
	ImportPath string
	Imports    []string
}

type importRule struct {
	fromPrefix string
	toPrefix   string
}

var forbiddenImportRules = []importRule{
	{fromPrefix: modulePath + "/internal/domain/", toPrefix: modulePath + "/internal/core/"},
	{fromPrefix: modulePath + "/internal/domain/", toPrefix: modulePath + "/internal/platform/"},
	{fromPrefix: modulePath + "/internal/platform/", toPrefix: modulePath + "/internal/core/"},
	{fromPrefix: modulePath + "/internal/core/", toPrefix: modulePath + "/internal/tools"},
}

// Existing migration exceptions are exact package edges. Do not broaden this
// list: new cross-layer dependencies require moving the contract or storage
// implementation to the correct layer.
var importExceptions = map[string]string{
	modulePath + "/internal/domain/events -> " + modulePath + "/internal/core/protocol":    "move shared event payload contracts out of core",
	modulePath + "/internal/domain/history -> " + modulePath + "/internal/core/protocol":   "move shared history contracts out of core",
	modulePath + "/internal/domain/message -> " + modulePath + "/internal/core/protocol":   "move shared message contracts out of core",
	modulePath + "/internal/domain/message -> " + modulePath + "/internal/platform/fsutil": "move JSON message storage behind a repository",
	modulePath + "/internal/domain/task -> " + modulePath + "/internal/platform/fsutil":    "move JSON task storage behind a repository",
	modulePath + "/internal/domain/todo -> " + modulePath + "/internal/platform/fsutil":    "move JSON todo storage behind a repository",
	modulePath + "/internal/platform/tooling -> " + modulePath + "/internal/core/protocol": "move tool wire contracts to a neutral contract package",
	modulePath + "/internal/core/teammate -> " + modulePath + "/internal/tools":            "inject a narrow teammate tool adapter",
}

func TestInternalImportBoundaries(t *testing.T) {
	packages := listInternalPackages(t)
	seenExceptions := make(map[string]bool, len(importExceptions))
	var violations []string
	for _, pkg := range packages {
		for _, imported := range pkg.Imports {
			if !forbiddenImport(pkg.ImportPath, imported) {
				continue
			}
			edge := pkg.ImportPath + " -> " + imported
			if _, ok := importExceptions[edge]; ok {
				seenExceptions[edge] = true
				continue
			}
			violations = append(violations, edge)
		}
	}
	sort.Strings(violations)
	if len(violations) > 0 {
		t.Fatalf("new forbidden internal imports:\n%s", strings.Join(violations, "\n"))
	}

	var stale []string
	for edge := range importExceptions {
		if !seenExceptions[edge] {
			stale = append(stale, edge)
		}
	}
	sort.Strings(stale)
	if len(stale) > 0 {
		t.Fatalf("remove resolved import exceptions:\n%s", strings.Join(stale, "\n"))
	}
}

func forbiddenImport(from, to string) bool {
	for _, rule := range forbiddenImportRules {
		if strings.HasPrefix(from, rule.fromPrefix) && strings.HasPrefix(to, rule.toPrefix) {
			return true
		}
	}
	return false
}

func listInternalPackages(t *testing.T) []listedPackage {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve architecture test path")
	}
	repoRoot := filepath.Clean(filepath.Join(filepath.Dir(file), "../.."))
	cmd := exec.Command("go", "list", "-json", "./internal/...")
	cmd.Dir = repoRoot
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("go list internal packages: %v", err)
	}

	decoder := json.NewDecoder(strings.NewReader(string(output)))
	var packages []listedPackage
	for decoder.More() {
		var pkg listedPackage
		if err := decoder.Decode(&pkg); err != nil {
			t.Fatalf("decode go list output: %v", err)
		}
		packages = append(packages, pkg)
	}
	if len(packages) == 0 {
		t.Fatal("go list returned no internal packages")
	}
	return packages
}
