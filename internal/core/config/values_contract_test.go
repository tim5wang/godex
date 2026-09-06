package config

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

func TestBaseSchemaFieldsHaveStoredValueSetter(t *testing.T) {
	setterPaths, setterPrefixes := storedValueSetterPaths(t)
	seenSchemaPaths := make(map[string]struct{})
	var duplicateSchemaPaths []string
	var missingSetters []string

	for _, section := range baseSchema() {
		for _, field := range section.Fields {
			path := strings.TrimSpace(field.Path)
			if _, exists := seenSchemaPaths[path]; exists {
				duplicateSchemaPaths = append(duplicateSchemaPaths, path)
			}
			seenSchemaPaths[path] = struct{}{}
			if _, ok := setterPaths[path]; ok || hasSetterPrefix(path, setterPrefixes) {
				continue
			}
			missingSetters = append(missingSetters, path)
		}
	}

	sort.Strings(duplicateSchemaPaths)
	sort.Strings(missingSetters)
	if len(duplicateSchemaPaths) > 0 {
		t.Fatalf("duplicate base schema paths:\n%s", strings.Join(duplicateSchemaPaths, "\n"))
	}
	if len(missingSetters) > 0 {
		t.Fatalf("base schema fields without setStoredValue handling:\n%s", strings.Join(missingSetters, "\n"))
	}
}

func TestACPBridgeClientMCPServersValueRoundTrip(t *testing.T) {
	file := ConfigFile{}
	if err := setACPStoredValue(&file, "acp.bridge_client_mcp_servers", true); err != nil {
		t.Fatalf("set ACP bridge value: %v", err)
	}
	if !file.ACP.BridgeClientMCPServers {
		t.Fatal("expected ACP bridge setting stored")
	}
	if got := storedValues(file)["acp.bridge_client_mcp_servers"]; got != true {
		t.Fatalf("stored bridge value = %#v", got)
	}
}

func TestACPAgentMCPServerSecretsAreMaskedAndPreserved(t *testing.T) {
	file := ConfigFile{ACP: ACPSection{Agents: map[string]ACPAgentSection{
		"codex": {
			Command: "codex",
			McpServers: []ACPMcpServerSection{{
				Name: "remote", Command: "mcp-remote", Env: map[string]string{"API_TOKEN": "secret", "MODE": "safe"},
			}},
		},
	}}}
	masked := storedValues(file)["acp.agents"].(map[string]ACPAgentSection)
	if got := masked["codex"].McpServers[0].Env["API_TOKEN"]; got != "********" {
		t.Fatalf("masked MCP token = %q", got)
	}
	maskedAgent := masked["codex"]
	maskedAgent.McpServers[0].Env["MODE"] = "fast"
	if err := setACPStoredValue(&file, "acp.agents", map[string]ACPAgentSection{"codex": maskedAgent}); err != nil {
		t.Fatalf("save masked ACP agent: %v", err)
	}
	got := file.ACP.Agents["codex"].McpServers[0].Env
	if got["API_TOKEN"] != "secret" || got["MODE"] != "fast" {
		t.Fatalf("saved MCP env = %#v", got)
	}
}

func TestSchemaStoredAndEffectiveValueRoundTrip(t *testing.T) {
	want := map[string]any{
		"security.screener.enabled":         true,
		"security.screener.shadow":          false,
		"security.screener.provider":        "custom-screener",
		"security.screener.timeout_ms":      1200,
		"security.screener.max_tokens":      77,
		"tools.execution.scope_write":       false,
		"heartbeat.default_watchdog_script": "check-health.sh",
	}
	updates := make(map[string]any, len(want)+1)
	for path, value := range want {
		updates[path] = value
	}
	updates["control.credential"] = "relay-secret"
	file := defaultConfigFile()
	if err := applyStoredValues(&file, UpdateRequest{Values: updates}); err != nil {
		t.Fatalf("apply stored values: %v", err)
	}
	resolved := resolveConfigFile(file, "", "", "", "", "", "", "", "")
	stored := storedValues(file)
	effective := effectiveValues(resolved)
	assertConfigValues(t, "stored", stored, want)
	assertConfigValues(t, "effective", effective, want)
	if got := stored["control.credential"]; got != "" {
		t.Errorf("stored control credential = %#v, want masked empty value", got)
	}
	if got := effective["control.credential"]; got != "relay-secret" {
		t.Errorf("effective control credential = %#v, want relay-secret before view masking", got)
	}
}

func TestBaseSchemaFieldsHaveStoredAndEffectiveValues(t *testing.T) {
	file := defaultConfigFile()
	stored := storedValues(file)
	effective := effectiveValues(resolveConfigFile(file, "", "", "", "", "", "", "", ""))
	var missingStored []string
	var missingEffective []string
	for _, section := range baseSchema() {
		for _, field := range section.Fields {
			if _, ok := stored[field.Path]; !ok {
				missingStored = append(missingStored, field.Path)
			}
			if _, ok := effective[field.Path]; !ok {
				missingEffective = append(missingEffective, field.Path)
			}
		}
	}
	sort.Strings(missingStored)
	sort.Strings(missingEffective)
	if len(missingStored) > 0 {
		t.Errorf("base schema fields without stored values:\n%s", strings.Join(missingStored, "\n"))
	}
	if len(missingEffective) > 0 {
		t.Errorf("base schema fields without effective values:\n%s", strings.Join(missingEffective, "\n"))
	}
}

func assertConfigValues(t *testing.T, label string, got, want map[string]any) {
	t.Helper()
	for path, wantValue := range want {
		if gotValue := got[path]; !reflect.DeepEqual(gotValue, wantValue) {
			t.Errorf("%s value %s = %#v, want %#v", label, path, gotValue, wantValue)
		}
	}
}

func storedValueSetterPaths(t *testing.T) (map[string]struct{}, []string) {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve config test path")
	}
	fset := token.NewFileSet()
	var functions []*ast.FuncDecl
	foundDispatcher := false
	for _, name := range []string{"values_setters.go", "values_tool_setters.go"} {
		path := filepath.Join(filepath.Dir(testFile), name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", name, err)
		}
		for _, declaration := range file.Decls {
			candidate, ok := declaration.(*ast.FuncDecl)
			if !ok || !strings.HasSuffix(candidate.Name.Name, "StoredValue") {
				continue
			}
			functions = append(functions, candidate)
			foundDispatcher = foundDispatcher || candidate.Name.Name == "setStoredValue"
		}
	}
	if !foundDispatcher {
		t.Fatal("setStoredValue function not found")
	}

	paths := make(map[string]struct{})
	var prefixes []string
	for _, function := range functions {
		ast.Inspect(function.Body, func(node ast.Node) bool {
			switch value := node.(type) {
			case *ast.SwitchStmt:
				identifier, ok := value.Tag.(*ast.Ident)
				if !ok || identifier.Name != "path" {
					return true
				}
				for _, statement := range value.Body.List {
					clause, ok := statement.(*ast.CaseClause)
					if !ok {
						continue
					}
					for _, expression := range clause.List {
						if path, ok := stringLiteral(expression); ok {
							paths[path] = struct{}{}
						}
					}
				}
			case *ast.CallExpr:
				selector, ok := value.Fun.(*ast.SelectorExpr)
				if !ok || selector.Sel.Name != "HasPrefix" || len(value.Args) != 2 {
					return true
				}
				identifier, ok := value.Args[0].(*ast.Ident)
				if !ok || identifier.Name != "path" {
					return true
				}
				if prefix, ok := stringLiteral(value.Args[1]); ok {
					prefixes = append(prefixes, prefix)
				}
			}
			return true
		})
	}
	if len(paths) == 0 {
		t.Fatal("setStoredValue path switch is empty")
	}
	return paths, prefixes
}

func stringLiteral(expression ast.Expr) (string, bool) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(literal.Value)
	return value, err == nil
}

func hasSetterPrefix(path string, prefixes []string) bool {
	for _, prefix := range prefixes {
		if strings.HasPrefix(path, prefix) {
			return true
		}
	}
	return false
}
