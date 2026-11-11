package tools

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// The lsp tool was silently dead: godex advertised its client capabilities as
// bare booleans, which violates the LSP schema, so gopls answered initialize
// with -32700 and every later request reported "server is closed". These tests
// pin the spec-shaped payload so a regression fails here instead of in the
// field (where it looked like a clean codebase, not a broken tool).

func TestLSPInitializeParamsUsesSpecTypes(t *testing.T) {
	params := lspInitializeParams("file:///tmp/ws")

	caps, ok := params["capabilities"].(map[string]interface{})
	if !ok {
		t.Fatalf("capabilities missing or wrong type: %T", params["capabilities"])
	}
	td, ok := caps["textDocument"].(map[string]interface{})
	if !ok {
		t.Fatalf("textDocument capabilities missing or wrong type: %T", caps["textDocument"])
	}

	// hover.contentFormat must be MarkupKind[], never a bool.
	hover, ok := td["hover"].(map[string]interface{})
	if !ok {
		t.Fatalf("hover capability missing or wrong type: %T", td["hover"])
	}
	kinds, ok := hover["contentFormat"].([]string)
	if !ok {
		t.Fatalf("contentFormat must be []string (MarkupKind[]), got %T", hover["contentFormat"])
	}
	if len(kinds) == 0 {
		t.Fatal("contentFormat must advertise at least one markup kind")
	}

	// Each of these is a capability OBJECT. A bool here is the original bug.
	for _, key := range []string{
		"definition", "references", "documentSymbol",
		"typeDefinition", "implementation", "diagnostic",
	} {
		value, present := td[key]
		if !present {
			t.Errorf("textDocument.%s capability is missing", key)
			continue
		}
		if _, isBool := value.(bool); isBool {
			t.Errorf("textDocument.%s must be an object, got bool (gopls rejects this with -32700)", key)
		}
		if _, isObj := value.(map[string]interface{}); !isObj {
			t.Errorf("textDocument.%s must be an object, got %T", key, value)
		}
	}

	// The pull-diagnostics client capability key is "diagnostic"; the server-side
	// request name "documentDiagnostic" is not a valid capability key.
	if _, wrong := td["documentDiagnostic"]; wrong {
		t.Error(`textDocument.documentDiagnostic is not a capability key; use "diagnostic"`)
	}

	ws, ok := caps["workspace"].(map[string]interface{})
	if !ok {
		t.Fatalf("workspace capabilities missing or wrong type: %T", caps["workspace"])
	}
	for _, key := range []string{"symbol", "diagnostics"} {
		if _, isBool := ws[key].(bool); isBool {
			t.Errorf("workspace.%s must be an object, got bool", key)
		}
	}
}

// The whole params payload must survive a round trip as JSON objects, which is
// what a strict server will actually unmarshal.
func TestLSPInitializeParamsAreJSONObjects(t *testing.T) {
	raw, err := json.Marshal(lspInitializeParams("file:///tmp/ws"))
	if err != nil {
		t.Fatalf("marshal initialize params: %v", err)
	}
	var decoded struct {
		Capabilities struct {
			TextDocument map[string]json.RawMessage `json:"textDocument"`
		} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal initialize params: %v", err)
	}
	for key, value := range decoded.Capabilities.TextDocument {
		if string(value) == "true" || string(value) == "false" {
			t.Errorf("textDocument.%s serialized as a raw boolean (%s)", key, value)
		}
	}
}

// gopls answers with {"kind":"","items":[...]}. Requiring kind == "full"
// discarded the diagnostics and reported the file as clean.
func TestParseDiagnosticsResultAcceptsEmptyKind(t *testing.T) {
	raw := json.RawMessage(`{"kind":"","items":[{"range":{"start":{"line":3,"character":13},"end":{"line":3,"character":25}},"severity":1,"message":"cannot use \"x\" as int"}]}`)

	diags, err := parseDiagnosticsResult(raw)
	if err != nil {
		t.Fatalf("parseDiagnosticsResult: %v", err)
	}
	if len(diags) != 1 {
		t.Fatalf("expected the empty-kind report to be honored, got %d diagnostics", len(diags))
	}
	if diags[0].Message != `cannot use "x" as int` {
		t.Errorf("unexpected message: %q", diags[0].Message)
	}
}

func TestParseDiagnosticsResultShapes(t *testing.T) {
	tests := []struct {
		name string
		raw  string
		want int
	}{
		{"full report", `{"kind":"full","items":[{"message":"a"}]}`, 1},
		{"empty kind with items", `{"kind":"","items":[{"message":"a"},{"message":"b"}]}`, 2},
		{"empty items", `{"kind":"full","items":[]}`, 0},
		{"null result", `null`, 0},
		{"bare array", `[{"message":"a"}]`, 1},
		{"bare empty array", `[]`, 0},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			diags, err := parseDiagnosticsResult(json.RawMessage(tc.raw))
			if err != nil {
				t.Fatalf("parseDiagnosticsResult(%s): %v", tc.raw, err)
			}
			if len(diags) != tc.want {
				t.Fatalf("got %d diagnostics, want %d", len(diags), tc.want)
			}
		})
	}
}

// Characterization test: a publish carrying an empty diagnostics array is how a
// server retracts an earlier report, and the cache must honor that.
//
// This already worked before the fix. `Diagnostics` is a json.RawMessage, so the
// existing len(...) == 0 guard measured JSON BYTES: an explicit empty array is
// `[]` (2 bytes) and therefore passed through. The guard only skipped an absent
// field. Kept to pin the retraction path against future edits.
func TestHandlePublishDiagnosticsClearsCacheOnEmptyPublish(t *testing.T) {
	s := &lspServer{cachedDiags: map[string][]LSPDiagnostic{}}
	uri := "file:///tmp/broken.go"

	s.handlePublishDiagnostics(json.RawMessage(
		`{"uri":"` + uri + `","diagnostics":[{"message":"undefined: x"}]}`))
	if got := len(s.cachedDiags[uri]); got != 1 {
		t.Fatalf("expected 1 cached diagnostic, got %d", got)
	}

	// Code fixed: server retracts the diagnostic.
	s.handlePublishDiagnostics(json.RawMessage(`{"uri":"` + uri + `","diagnostics":[]}`))
	if got := len(s.cachedDiags[uri]); got != 0 {
		t.Fatalf("empty publish must clear the cache, still have %d diagnostic(s)", got)
	}
}

// A dead server must surface as an error, not as an empty (\"clean file\") result.
func TestCallOnClosedServerReportsUnavailable(t *testing.T) {
	s := &lspServer{
		config:  lspServerConfig{Language: "go"},
		closed:  true,
		pending: map[int]chan lspRPCResult{},
	}

	_, err := s.call(context.Background(), "textDocument/documentSymbol", nil)
	if err == nil {
		t.Fatal("expected an error from a closed server, got nil")
	}
	if !errors.Is(err, ErrLSPServerUnavailable) {
		t.Fatalf("expected error to wrap ErrLSPServerUnavailable, got %v", err)
	}
}

// A transport failure must not be reported as "no diagnostics".
func TestDiagnosticsOnClosedServerReturnsError(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "broken.go")
	if err := os.WriteFile(path, []byte("package broken\n\nfunc f() { var x int = \"s\"; _ = x }\n"), 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	s := &lspServer{
		config:      lspServerConfig{Language: "go"},
		workspace:   dir,
		closed:      true,
		pending:     map[int]chan lspRPCResult{},
		openedDocs:  map[string]*openedDoc{},
		cachedDiags: map[string][]LSPDiagnostic{},
	}

	diags, err := s.diagnostics(context.Background(), path, "go")
	if err == nil {
		t.Fatalf("expected an error for an unusable server, got diags=%v", diags)
	}
	if diags != nil {
		t.Fatalf("expected nil diagnostics alongside the error, got %v", diags)
	}
}

// End-to-end: the real server must accept our capabilities and answer a request.
// This is the test that would have caught the original outage.
func TestLSPClientAgainstRealServer(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping real language server test in short mode")
	}
	if _, err := exec.LookPath("gopls"); err != nil {
		t.Skip("gopls not installed")
	}

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "go.mod"),
		[]byte("module lspintegrationtest\n\ngo 1.21\n"), 0o644); err != nil {
		t.Fatalf("write go.mod: %v", err)
	}
	target := filepath.Join(dir, "lib.go")
	if err := os.WriteFile(target, []byte(
		"package lspintegrationtest\n\n// Marker is exported.\ntype Marker struct{ Name string }\n\nfunc Exported() Marker { return Marker{Name: \"x\"} }\n",
	), 0o644); err != nil {
		t.Fatalf("write lib.go: %v", err)
	}

	client := NewLSPClient(dir)
	defer client.CloseAll()

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	// initialize must succeed; the old payload failed here with -32700.
	if err := client.StartServer(ctx, "go"); err != nil {
		t.Fatalf("StartServer(go) failed: %v", err)
	}

	var syms []LSPSymbol
	var lastErr error
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		syms, lastErr = client.DocumentSymbols(ctx, target, "go")
		if lastErr == nil && len(syms) > 0 {
			break
		}
		time.Sleep(500 * time.Millisecond)
	}
	if lastErr != nil {
		t.Fatalf("DocumentSymbols failed: %v", lastErr)
	}
	if len(syms) == 0 {
		t.Fatal("expected document symbols from gopls, got none")
	}

	names := map[string]bool{}
	for _, s := range syms {
		names[s.Name] = true
	}
	for _, want := range []string{"Marker", "Exported"} {
		if !names[want] {
			t.Errorf("expected symbol %q in %v", want, names)
		}
	}
}
