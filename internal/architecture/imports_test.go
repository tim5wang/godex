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

// Migration exceptions must be exact package edges. The list is intentionally
// empty after the 2026-08-31 boundary migration; new cross-layer dependencies
// require moving the contract, adapter, or storage implementation instead of
// broadening a prefix rule.
var importExceptions = map[string]string{}

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
