package tools

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// ---------------------------------------------------------------------------
// Types
// ---------------------------------------------------------------------------

// LSPDiagnostic represents a single diagnostic from the language server.
type LSPDiagnostic struct {
	Range   LSPRange `json:"range"`
	Message string   `json:"message"`
	Code    any      `json:"code,omitempty"`
}

// LSPRange represents a text range in an LSP document.
type LSPRange struct {
	Start LSPPosition `json:"start"`
	End   LSPPosition `json:"end"`
}

// LSPPosition represents a position in an LSP document (0-based).
type LSPPosition struct {
	Line      int `json:"line"`
	Character int `json:"character"`
}

// LSPLocation represents a symbol location (definition, reference).
type LSPLocation struct {
	URI   string   `json:"uri"`
	Range LSPRange `json:"range"`
}

// LSPHover represents hover information.
type LSPHover struct {
	Contents []LSPMarkupContent `json:"contents,omitempty"`
	Range    *LSPRange          `json:"range,omitempty"`
}

// LSPMarkupContent represents formatted content from the language server.
type LSPMarkupContent struct {
	Kind  string `json:"kind"` // "plaintext" or "markdown"
	Value string `json:"value"`
}

// LSPCompletionItem represents a completion item.
type LSPCompletionItem struct {
	Label         string `json:"label"`
	Detail        string `json:"detail,omitempty"`
	InsertText    string `json:"insertText,omitempty"`
	Documentation string `json:"documentation,omitempty"`
}

// LSPSymbol represents a document or workspace symbol.
type LSPSymbol struct {
	Name          string     `json:"name"`
	Kind          int        `json:"kind"`
	Detail        string     `json:"detail,omitempty"`
	ContainerName string     `json:"containerName,omitempty"`
	Location      LSPLocation `json:"location"`
}

// ---------------------------------------------------------------------------
// Language server configuration
// ---------------------------------------------------------------------------

type lspServerConfig struct {
	Language    string   // e.g. "go", "python", "rust", "typescript"
	Command     string   // e.g. "gopls", "pyright-langserver", "rust-analyzer"
	Args        []string // e.g. {"--stdio"} for pyright
	Extensions  []string // file extensions that trigger this server
}

// languageIDmaps file extensions to LSP language identifiers.
var extensionToLanguageID = map[string]string{
	".go":   "go",
	".ts":   "typescript",
	".tsx":  "typescriptreact",
	".js":   "javascript",
	".jsx":  "javascriptreact",
	".mjs":  "javascript",
	".cjs":  "javascript",
	".mts":  "typescript",
	".cts":  "typescript",
	".py":   "python",
	".pyi":  "python",
	".rs":   "rust",
}

var lspConfigs = []lspServerConfig{
	{
		Language:   "go",
		Command:    "gopls",
		Args:       nil,
		Extensions: []string{".go"},
	},
	{
		Language:   "typescript",
		Command:    "typescript-language-server",
		Args:       []string{"--stdio"},
		Extensions: []string{".ts", ".tsx", ".js", ".jsx", ".mjs", ".cjs", ".mts", ".cts"},
	},
	{
		Language:   "python",
		Command:    "pyright-langserver",
		Args:       []string{"--stdio"},
		Extensions: []string{".py", ".pyi"},
	},
	{
		Language:   "rust",
		Command:    "rust-analyzer",
		Args:       nil,
		Extensions: []string{".rs"},
	},
}

// ---------------------------------------------------------------------------
// LSP server instance
// ---------------------------------------------------------------------------

// openedDoc tracks a document that has been opened via didOpen.
type openedDoc struct {
	uri     string
	version int
	content string
}

type lspServer struct {
	config       lspServerConfig
	workspace    string
	cmd          *exec.Cmd
	stdin        io.WriteCloser
	stdout       *bufio.Scanner
	stderr       io.ReadCloser
	caps         map[string]interface{} // server capabilities from initialize response
	mu           sync.Mutex
	pending      map[int]chan<- json.RawMessage
	nextID       int
	closed       bool
	initOnce     sync.Once
	initErr      error

	// Track opened documents so we can do didChange/didClose
	openedDocs  map[string]*openedDoc // uri -> doc state

	// Cached diagnostics from textDocument/publishDiagnostics notifications
	cachedDiags map[string][]LSPDiagnostic // uri -> diagnostics

	// Call timeout duration
	callTimeoutDur time.Duration
}

type lspRPCRequest struct {
	JSONRPC string      `json:"jsonrpc"`
	ID      any         `json:"id"`
	Method  string      `json:"method"`
	Params  any         `json:"params,omitempty"`
}

type lspRPCResponse struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      any             `json:"id"`
	Result  json.RawMessage `json:"result,omitempty"`
	Error   *lspRPCError    `json:"error,omitempty"`
	Method  string          `json:"method,omitempty"`
	Params  json.RawMessage `json:"params,omitempty"`
}

type lspRPCError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------
// LSPClient
// ---------------------------------------------------------------------------

// LSPClient manages LSP server processes for the agent.
type LSPClient struct {
	mu        sync.Mutex
	servers   map[string]*lspServer
	ready     map[string]bool
	workspace string
}

// NewLSPClient creates a new LSP service for the given workspace.
func NewLSPClient(workspace string) *LSPClient {
	return &LSPClient{
		servers:   make(map[string]*lspServer),
		ready:     make(map[string]bool),
		workspace: workspace,
	}
}

// detectLanguageFromPath returns the language key for the given file path.
func (c *LSPClient) detectLanguageFromPath(path string) string {
	ext := strings.ToLower(filepath.Ext(path))
	for _, cfg := range lspConfigs {
		for _, e := range cfg.Extensions {
			if e == ext {
				return cfg.Language
			}
		}
	}
	return ""
}

// languageID returns the LSP language identifier for a given file path.
func (c *LSPClient) languageID(path string) string {
	if id, ok := extensionToLanguageID[strings.ToLower(filepath.Ext(path))]; ok {
		return id
	}
	// Fallback: use the language key from detect
	return c.detectLanguageFromPath(path)
}

// StartServer starts the LSP server for the given language.
func (c *LSPClient) StartServer(ctx context.Context, language string) error {
	c.mu.Lock()
	if c.ready[language] {
		c.mu.Unlock()
		return nil
	}
	if _, exists := c.servers[language]; exists {
		c.mu.Unlock()
		sv := c.servers[language]
		sv.initOnce.Do(func() {})
		return sv.initErr
	}
	c.mu.Unlock()

	var cfg *lspServerConfig
	for i, cc := range lspConfigs {
		if cc.Language == language {
			cfg = &lspConfigs[i]
			break
		}
	}
	if cfg == nil {
		return fmt.Errorf("unsupported language: %s", language)
	}

	cmdPath, err := exec.LookPath(cfg.Command)
	if err != nil {
		return fmt.Errorf("LSP server %q not found in PATH (language: %s). Install it first: go install golang.org/x/tools/gopls@latest / npm install -g typescript-language-server / npm install -g pyright / rustup component add rust-analyzer",
			cfg.Command, language)
	}

	workspaceURI := pathToURI(c.workspace)

	sv := &lspServer{
		config:       *cfg,
		workspace:    c.workspace,
		pending:      make(map[int]chan<- json.RawMessage),
		nextID:       1,
		openedDocs:   make(map[string]*openedDoc),
		cachedDiags:  make(map[string][]LSPDiagnostic),
		callTimeoutDur: 30 * time.Second,
	}

	sv.cmd = exec.CommandContext(ctx, cmdPath, cfg.Args...)
	sv.cmd.Dir = c.workspace
	sv.cmd.Env = os.Environ()

	sv.stdin, err = sv.cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("failed to create stdin pipe for %s: %w", cfg.Command, err)
	}
	sv.stdout, err = createScanner(sv.cmd)
	if err != nil {
		return fmt.Errorf("failed to create stdout pipe for %s: %w", cfg.Command, err)
	}
	sv.stderr, err = sv.cmd.StderrPipe()
	if err != nil {
		return fmt.Errorf("failed to create stderr pipe for %s: %w", cfg.Command, err)
	}

	if err := sv.cmd.Start(); err != nil {
		return fmt.Errorf("failed to start %s: %w", cfg.Command, err)
	}

	go sv.readLoop()

	c.mu.Lock()
	c.servers[language] = sv
	c.mu.Unlock()

	var initErr error
	sv.initOnce.Do(func() {
		initErr = sv.initialize(ctx, workspaceURI)
		if initErr != nil {
			sv.close()
		}
	})

	c.mu.Lock()
	if initErr == nil {
		c.ready[language] = true
	}
	c.mu.Unlock()

	return initErr
}

// CloseAll stops all LSP servers.
func (c *LSPClient) CloseAll() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for lang, sv := range c.servers {
		sv.close()
		delete(c.servers, lang)
		delete(c.ready, lang)
	}
}

// AvailableLanguages returns initialized language servers.
func (c *LSPClient) AvailableLanguages() []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	var langs []string
	for lang := range c.ready {
		langs = append(langs, lang)
	}
	return langs
}

// CheckServers checks which LSP servers are available on the system.
func (c *LSPClient) CheckServers() map[string]bool {
	result := make(map[string]bool)
	for _, cfg := range lspConfigs {
		_, err := exec.LookPath(cfg.Command)
		result[cfg.Language] = err == nil
	}
	return result
}

// ---------------------------------------------------------------------------
// Public LSP operations
// ---------------------------------------------------------------------------

// Definition finds the definition of a symbol at the given position.
func (c *LSPClient) Definition(ctx context.Context, filePath, language string, line, character int) ([]LSPLocation, error) {
	sv, err := c.getServer(ctx, language)
	if err != nil {
		return nil, err
	}
	return sv.definition(ctx, filePath, line, character)
}

// References finds all references to a symbol.
func (c *LSPClient) References(ctx context.Context, filePath, language string, line, character int, includeDeclaration bool) ([]LSPLocation, error) {
	sv, err := c.getServer(ctx, language)
	if err != nil {
		return nil, err
	}
	return sv.references(ctx, filePath, line, character, includeDeclaration)
}

// Hover returns hover info.
func (c *LSPClient) Hover(ctx context.Context, filePath, language string, line, character int) (*LSPHover, error) {
	sv, err := c.getServer(ctx, language)
	if err != nil {
		return nil, err
	}
	return sv.hover(ctx, filePath, line, character)
}

// Diagnostics returns cached diagnostics for a file, optionally requesting fresh ones.
func (c *LSPClient) Diagnostics(ctx context.Context, filePath, language string) ([]LSPDiagnostic, error) {
	sv, err := c.getServer(ctx, language)
	if err != nil {
		return nil, err
	}
	return sv.diagnostics(ctx, filePath, language)
}

// Completion returns completions.
func (c *LSPClient) Completion(ctx context.Context, filePath, language string, line, character int) ([]LSPCompletionItem, error) {
	sv, err := c.getServer(ctx, language)
	if err != nil {
		return nil, err
	}
	return sv.completion(ctx, filePath, line, character)
}

// WorkspaceSymbol searches workspace-wide symbols matching a query.
func (c *LSPClient) WorkspaceSymbol(ctx context.Context, language, query string) ([]LSPSymbol, error) {
	sv, err := c.getServer(ctx, language)
	if err != nil {
		return nil, err
	}
	return sv.workspaceSymbol(ctx, query)
}

// DocumentSymbols returns all symbols defined in a file.
func (c *LSPClient) DocumentSymbols(ctx context.Context, filePath, language string) ([]LSPSymbol, error) {
	sv, err := c.getServer(ctx, language)
	if err != nil {
		return nil, err
	}
	return sv.documentSymbols(ctx, filePath)
}

// TypeDefinition finds the type definition of a symbol.
func (c *LSPClient) TypeDefinition(ctx context.Context, filePath, language string, line, character int) ([]LSPLocation, error) {
	sv, err := c.getServer(ctx, language)
	if err != nil {
		return nil, err
	}
	return sv.typeDefinition(ctx, filePath, line, character)
}

// Implementation finds implementations of a symbol.
func (c *LSPClient) Implementation(ctx context.Context, filePath, language string, line, character int) ([]LSPLocation, error) {
	sv, err := c.getServer(ctx, language)
	if err != nil {
		return nil, err
	}
	return sv.implementation(ctx, filePath, line, character)
}

// ---------------------------------------------------------------------------
// lspServer – public operation implementations
// ---------------------------------------------------------------------------

func (s *lspServer) definition(ctx context.Context, filePath string, line, character int) ([]LSPLocation, error) {
	uri := pathToURI(filePath)
	if err := s.ensureOpen(ctx, filePath); err != nil {
		return nil, err
	}

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": line, "character": character},
	}

	raw, err := s.callWithTimeout(ctx, "textDocument/definition", params)
	if err != nil {
		return nil, fmt.Errorf("definition request failed: %w", err)
	}

	var locations []LSPLocation
	if err := parseLSPLocations(raw, &locations); err != nil {
		return nil, fmt.Errorf("parse definition result: %w", err)
	}
	return locations, nil
}

func (s *lspServer) references(ctx context.Context, filePath string, line, character int, includeDeclaration bool) ([]LSPLocation, error) {
	uri := pathToURI(filePath)
	if err := s.ensureOpen(ctx, filePath); err != nil {
		return nil, err
	}

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": line, "character": character},
		"context": map[string]bool{
			"includeDeclaration": includeDeclaration,
		},
	}

	raw, err := s.callWithTimeout(ctx, "textDocument/references", params)
	if err != nil {
		return nil, fmt.Errorf("references request failed: %w", err)
	}

	var locations []LSPLocation
	if err := parseLSPLocations(raw, &locations); err != nil {
		return nil, fmt.Errorf("parse references result: %w", err)
	}
	return locations, nil
}

func (s *lspServer) hover(ctx context.Context, filePath string, line, character int) (*LSPHover, error) {
	uri := pathToURI(filePath)
	if err := s.ensureOpen(ctx, filePath); err != nil {
		return nil, err
	}

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": line, "character": character},
	}

	raw, err := s.callWithTimeout(ctx, "textDocument/hover", params)
	if err != nil {
		return nil, fmt.Errorf("hover request failed: %w", err)
	}

	if raw == nil || string(raw) == "null" {
		return nil, nil
	}

	var hover LSPHover
	if err := json.Unmarshal(raw, &hover); err != nil {
		return nil, fmt.Errorf("parse hover result: %w", err)
	}

	hover.Contents = flattenHoverContents(raw)
	return &hover, nil
}

func (s *lspServer) diagnostics(ctx context.Context, filePath, language string) ([]LSPDiagnostic, error) {
	uri := pathToURI(filePath)

	// First, ensure the document is open (with full content)
	if err := s.ensureOpen(ctx, filePath); err != nil {
		return nil, err
	}

	// Check cache first
	s.mu.Lock()
	cached, hasCached := s.cachedDiags[uri]
	s.mu.Unlock()
	if hasCached {
		return cached, nil
	}

	// If no cache entry yet, try requesting diagnostics explicitly (LSP 3.17+)
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
	}
	raw, err := s.callWithTimeout(ctx, "textDocument/diagnostic", params)
	if err != nil {
		return nil, nil // silently fallback
	}

	var diagResult struct {
		Kind  string          `json:"kind"`
		Items json.RawMessage `json:"items,omitempty"`
	}
	if err := json.Unmarshal(raw, &diagResult); err != nil || diagResult.Kind != "full" {
		// Also try the older format (direct array)
		var diags []LSPDiagnostic
		if err := json.Unmarshal(raw, &diags); err == nil {
			s.mu.Lock()
			s.cachedDiags[uri] = diags
			s.mu.Unlock()
			return diags, nil
		}
		return nil, nil
	}

	if len(diagResult.Items) > 0 {
		var diags []LSPDiagnostic
		if err := json.Unmarshal(diagResult.Items, &diags); err == nil {
			s.mu.Lock()
			s.cachedDiags[uri] = diags
			s.mu.Unlock()
			return diags, nil
		}
	}

	return nil, nil
}

func (s *lspServer) completion(ctx context.Context, filePath string, line, character int) ([]LSPCompletionItem, error) {
	uri := pathToURI(filePath)
	if err := s.ensureOpen(ctx, filePath); err != nil {
		return nil, err
	}

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": line, "character": character},
	}

	raw, err := s.callWithTimeout(ctx, "textDocument/completion", params)
	if err != nil {
		return nil, fmt.Errorf("completion request failed: %w", err)
	}

	if raw == nil || string(raw) == "null" {
		return nil, nil
	}

	var completionList struct {
		IsIncomplete bool                `json:"isIncomplete"`
		Items        []LSPCompletionItem `json:"items"`
	}
	if err := json.Unmarshal(raw, &completionList); err == nil && completionList.Items != nil {
		return completionList.Items, nil
	}

	var items []LSPCompletionItem
	if err := json.Unmarshal(raw, &items); err != nil {
		return nil, fmt.Errorf("parse completion result: %w", err)
	}
	return items, nil
}

func (s *lspServer) workspaceSymbol(ctx context.Context, query string) ([]LSPSymbol, error) {
	if query == "" {
		return nil, fmt.Errorf("query is required for workspace symbol search")
	}

	params := map[string]interface{}{
		"query": query,
	}

	raw, err := s.callWithTimeout(ctx, "workspace/symbol", params)
	if err != nil {
		return nil, fmt.Errorf("workspace symbol request failed: %w", err)
	}

	if raw == nil || string(raw) == "null" {
		return nil, nil
	}

	var symbols []LSPSymbol
	if err := json.Unmarshal(raw, &symbols); err != nil {
		return nil, fmt.Errorf("parse workspace symbols: %w", err)
	}
	return symbols, nil
}

func (s *lspServer) documentSymbols(ctx context.Context, filePath string) ([]LSPSymbol, error) {
	uri := pathToURI(filePath)
	if err := s.ensureOpen(ctx, filePath); err != nil {
		return nil, err
	}

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
	}

	raw, err := s.callWithTimeout(ctx, "textDocument/documentSymbol", params)
	if err != nil {
		return nil, fmt.Errorf("document symbol request failed: %w", err)
	}

	if raw == nil || string(raw) == "null" {
		return nil, nil
	}

	// LSP can return DocumentSymbol[] or SymbolInformation[]
	// Try DocumentSymbol first (nested)
	var docSymbols []struct {
		Name           string      `json:"name"`
		Kind           int         `json:"kind"`
		Detail         string      `json:"detail,omitempty"`
		Range          LSPRange    `json:"range"`
		SelectionRange LSPRange    `json:"selectionRange"`
		Children       []LSPSymbol `json:"children,omitempty"`
	}
	if err := json.Unmarshal(raw, &docSymbols); err == nil && len(docSymbols) > 0 {
		var flat []LSPSymbol
		flattenDocSymbols(docSymbols, "", &flat)
		return flat, nil
	}

	// Fallback: SymbolInformation[] (flat)
	var symbols []LSPSymbol
	if err := json.Unmarshal(raw, &symbols); err != nil {
		return nil, fmt.Errorf("parse document symbols: %w", err)
	}
	return symbols, nil
}

func (s *lspServer) typeDefinition(ctx context.Context, filePath string, line, character int) ([]LSPLocation, error) {
	uri := pathToURI(filePath)
	if err := s.ensureOpen(ctx, filePath); err != nil {
		return nil, err
	}

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": line, "character": character},
	}

	raw, err := s.callWithTimeout(ctx, "textDocument/typeDefinition", params)
	if err != nil {
		return nil, fmt.Errorf("type definition request failed: %w", err)
	}

	var locations []LSPLocation
	if err := parseLSPLocations(raw, &locations); err != nil {
		return nil, fmt.Errorf("parse type definition result: %w", err)
	}
	return locations, nil
}

func (s *lspServer) implementation(ctx context.Context, filePath string, line, character int) ([]LSPLocation, error) {
	uri := pathToURI(filePath)
	if err := s.ensureOpen(ctx, filePath); err != nil {
		return nil, err
	}

	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
		"position":     map[string]int{"line": line, "character": character},
	}

	raw, err := s.callWithTimeout(ctx, "textDocument/implementation", params)
	if err != nil {
		return nil, fmt.Errorf("implementation request failed: %w", err)
	}

	var locations []LSPLocation
	if err := parseLSPLocations(raw, &locations); err != nil {
		return nil, fmt.Errorf("parse implementation result: %w", err)
	}
	return locations, nil
}

// ---------------------------------------------------------------------------
// Document management (didOpen / didChange / didClose)
// ---------------------------------------------------------------------------

// ensureOpen reads the file content and sends didOpen (or didChange if already open).
func (s *lspServer) ensureOpen(ctx context.Context, filePath string) error {
	uri := pathToURI(filePath)
	content, err := readFileContent(filePath)
	if err != nil {
		// File may not exist yet; open with empty content
		content = ""
	}

	s.mu.Lock()
	doc, exists := s.openedDocs[uri]
	if exists && doc.content == content {
		// Content hasn't changed, no need to re-open
		s.mu.Unlock()
		return nil
	}
	s.mu.Unlock()

	if !exists {
		langID := detectLanguageID(filePath)
		return s.didOpen(ctx, uri, langID, content)
	}
	return s.didChange(ctx, uri, content, doc.version+1)
}

func (s *lspServer) didOpen(ctx context.Context, uri, languageID, content string) error {
	params := map[string]interface{}{
		"textDocument": map[string]interface{}{
			"uri":        uri,
			"languageId": languageID,
			"version":    1,
			"text":       content,
		},
	}
	s.notify(ctx, "textDocument/didOpen", params)

	s.mu.Lock()
	s.openedDocs[uri] = &openedDoc{uri: uri, version: 1, content: content}
	s.mu.Unlock()
	return nil
}

func (s *lspServer) didChange(ctx context.Context, uri, content string, version int) error {
	params := map[string]interface{}{
		"textDocument": map[string]interface{}{
			"uri":     uri,
			"version": version,
		},
		"contentChanges": []map[string]interface{}{
			{"text": content},
		},
	}
	s.notify(ctx, "textDocument/didChange", params)

	s.mu.Lock()
	s.openedDocs[uri] = &openedDoc{uri: uri, version: version, content: content}
	// Invalidate cached diagnostics for this file
	delete(s.cachedDiags, uri)
	s.mu.Unlock()
	return nil
}

func (s *lspServer) didClose(ctx context.Context, uri string) {
	params := map[string]interface{}{
		"textDocument": map[string]string{"uri": uri},
	}
	s.notify(ctx, "textDocument/didClose", params)

	s.mu.Lock()
	delete(s.openedDocs, uri)
	delete(s.cachedDiags, uri)
	s.mu.Unlock()
}

// ---------------------------------------------------------------------------
// lspServer – initialize / call / notify
// ---------------------------------------------------------------------------

func (s *lspServer) initialize(ctx context.Context, workspaceURI string) error {
	params := map[string]interface{}{
		"processId": nil,
		"clientInfo": map[string]string{
			"name":    "godex",
			"version": "dev",
		},
		"capabilities": map[string]interface{}{
			"textDocument": map[string]interface{}{
				"completion": map[string]interface{}{
					"completionItem": map[string]bool{
						"snippetSupport": false,
					},
				},
				"hover": map[string]bool{
					"contentFormat": true,
				},
				"definition":       true,
				"references":       true,
				"documentSymbol":   true,
				"typeDefinition":   true,
				"implementation":   true,
				"documentDiagnostic": true,
			},
			"workspace": map[string]interface{}{
				"symbol":      true,
				"diagnostics": true,
			},
		},
		"workspaceFolders": []map[string]string{
			{"uri": workspaceURI, "name": "workspace"},
		},
	}

	raw, err := s.call(ctx, "initialize", params)
	if err != nil {
		return fmt.Errorf("LSP initialize failed: %w", err)
	}

	var initResult struct {
		Capabilities map[string]interface{} `json:"capabilities"`
	}
	if err := json.Unmarshal(raw, &initResult); err != nil {
		return fmt.Errorf("parse initialize result: %w", err)
	}
	s.caps = initResult.Capabilities

	s.notify(ctx, "initialized", map[string]interface{}{})
	return nil
}

// call sends a request and waits for the response with no extra timeout.
func (s *lspServer) call(ctx context.Context, method string, params any) (json.RawMessage, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, fmt.Errorf("LSP server for %s is closed", s.config.Language)
	}
	id := s.nextID
	s.nextID++
	respCh := make(chan json.RawMessage, 1)
	s.pending[id] = respCh
	s.mu.Unlock()

	req := lspRPCRequest{
		JSONRPC: "2.0",
		ID:      id,
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	if err := s.writeMessage(data); err != nil {
		return nil, fmt.Errorf("write request: %w", err)
	}

	select {
	case result := <-respCh:
		return result, nil
	case <-ctx.Done():
		s.mu.Lock()
		if ch, ok := s.pending[id]; ok {
			delete(s.pending, id)
			close(ch)
		}
		s.mu.Unlock()
		return nil, ctx.Err()
	}
}

// callWithTimeout sends a request with a per-call timeout.
func (s *lspServer) callWithTimeout(ctx context.Context, method string, params any) (json.RawMessage, error) {
	timeout := s.callTimeoutDur
	if timeout <= 0 {
		timeout = 30 * time.Second
	}

	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	return s.call(callCtx, method, params)
}

func (s *lspServer) notify(ctx context.Context, method string, params any) {
	req := lspRPCRequest{
		JSONRPC: "2.0",
		Method:  method,
		Params:  params,
	}

	data, err := json.Marshal(req)
	if err != nil {
		return
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return
	}
	_ = s.writeMessage(data)
}

func (s *lspServer) writeMessage(data []byte) error {
	header := fmt.Sprintf("Content-Length: %d\r\n\r\n", len(data))
	if _, err := s.stdin.Write([]byte(header)); err != nil {
		return err
	}
	_, err := s.stdin.Write(data)
	return err
}

func (s *lspServer) readLoop() {
	// Drain stderr in background (LSP servers log debug info there)
	go func() {
		reader := bufio.NewReader(s.stderr)
		for {
			_, err := reader.ReadString('\n')
			if err != nil {
				return
			}
		}
	}()

	scanner := s.stdout
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}

		var resp lspRPCResponse
		if err := json.Unmarshal(line, &resp); err != nil {
			continue
		}

		if resp.ID == nil {
			// Notification – handle it
			s.handleNotification(resp)
			continue
		}

		// Response – route to pending caller
		id := rpcIDToInt(resp.ID)
		s.mu.Lock()
		ch, ok := s.pending[id]
		if ok {
			delete(s.pending, id)
		}
		s.mu.Unlock()
		if !ok {
			continue
		}

		if resp.Error != nil {
			ch <- nil
			close(ch)
			continue
		}
		ch <- resp.Result
		close(ch)
	}

	// Scanner loop ended – server process likely died.
	// Close all pending channels so callers get immediate errors.
	s.mu.Lock()
	for id, ch := range s.pending {
		delete(s.pending, id)
		close(ch)
	}
	s.mu.Unlock()
}

func (s *lspServer) handleNotification(resp lspRPCResponse) {
	switch resp.Method {
	case "textDocument/publishDiagnostics":
		s.handlePublishDiagnostics(resp.Params)
	}
}

func (s *lspServer) handlePublishDiagnostics(params json.RawMessage) {
	if params == nil {
		return
	}
	var payload struct {
		URI         string          `json:"uri"`
		Diagnostics json.RawMessage `json:"diagnostics"`
	}
	if err := json.Unmarshal(params, &payload); err != nil {
		return
	}
	if payload.URI == "" || len(payload.Diagnostics) == 0 {
		return
	}

	var diags []LSPDiagnostic
	if err := json.Unmarshal(payload.Diagnostics, &diags); err != nil {
		return
	}

	s.mu.Lock()
	s.cachedDiags[payload.URI] = diags
	s.mu.Unlock()
}

func (s *lspServer) close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	for id, ch := range s.pending {
		delete(s.pending, id)
		close(ch)
	}
	s.mu.Unlock()

	// Send shutdown + exit
	shutdownData := []byte(`{"jsonrpc":"2.0","id":0,"method":"shutdown"}`)
	s.mu.Lock()
	_ = s.writeMessage(shutdownData)
	_ = s.writeMessage([]byte(`{"jsonrpc":"2.0","method":"exit"}`))
	s.mu.Unlock()

	if s.cmd != nil && s.cmd.Process != nil {
		_ = s.cmd.Process.Kill()
		_ = s.cmd.Wait()
	}
	_ = s.stdin.Close()
}

// ---------------------------------------------------------------------------
// Internal helpers
// ---------------------------------------------------------------------------

func (c *LSPClient) getServer(ctx context.Context, language string) (*lspServer, error) {
	if err := c.StartServer(ctx, language); err != nil {
		return nil, err
	}
	c.mu.Lock()
	sv := c.servers[language]
	c.mu.Unlock()
	return sv, nil
}

// readFileContent reads a file and returns its content as a string.
// Truncates at 500KB to avoid sending massive files to the LSP server.
func readFileContent(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()

	const maxSize = 500 * 1024 // 500KB
	buf := make([]byte, maxSize+1)
	n, err := f.Read(buf)
	if err != nil && err != io.EOF {
		return "", err
	}
	if n > maxSize {
		return string(buf[:maxSize]) + "\n// ... [truncated at 500KB]", nil
	}
	return string(buf[:n]), nil
}

// detectLanguageID returns the LSP language identifier for a file path.
func detectLanguageID(path string) string {
	if id, ok := extensionToLanguageID[strings.ToLower(filepath.Ext(path))]; ok {
		return id
	}
	return ""
}

// ---------------------------------------------------------------------------
// Scanner / LSP framing
// ---------------------------------------------------------------------------

func createScanner(cmd *exec.Cmd) (*bufio.Scanner, error) {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 0, 256*1024), 10*1024*1024)
	scanner.Split(scanLSPMessage)
	return scanner, nil
}

// scanLSPMessage is a bufio.SplitFunc that parses LSP's Content-Length framing.
func scanLSPMessage(data []byte, atEOF bool) (advance int, token []byte, err error) {
	headerEnd := strings.Index(string(data), "\r\n\r\n")
	if headerEnd < 0 {
		if atEOF {
			return 0, data, io.EOF
		}
		return 0, nil, nil
	}

	header := string(data[:headerEnd])
	contentLen := 0
	for _, line := range strings.Split(header, "\r\n") {
		if strings.HasPrefix(line, "Content-Length:") {
			cl := strings.TrimSpace(line[len("Content-Length:"):])
			fmt.Sscanf(cl, "%d", &contentLen)
			break
		}
	}

	bodyStart := headerEnd + 4
	bodyEnd := bodyStart + contentLen
	if len(data) < bodyEnd {
		if atEOF {
			end := bodyEnd
			if len(data) < end {
				end = len(data)
			}
			return len(data), data[bodyStart:end], io.ErrUnexpectedEOF
		}
		return 0, nil, nil
	}

	return bodyEnd, data[bodyStart:bodyEnd], nil
}

// ---------------------------------------------------------------------------
// Utility functions
// ---------------------------------------------------------------------------

func rpcIDToInt(id any) int {
	switch v := id.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	default:
		return 0
	}
}

func pathToURI(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}
	abs = filepath.ToSlash(abs)
	if !strings.HasPrefix(abs, "/") {
		abs = "/" + abs
	}
	return "file://" + abs
}

func uriToPath(uri string) string {
	if !strings.HasPrefix(uri, "file://") {
		return uri
	}
	path := uri[7:]
	return filepath.FromSlash(path)
}

func parseLSPLocations(raw json.RawMessage, locations *[]LSPLocation) error {
	if raw == nil || string(raw) == "null" {
		return nil
	}

	// Try LocationLink[] (has targetUri)
	var links []struct {
		TargetURI   string   `json:"targetUri"`
		TargetRange LSPRange `json:"targetRange"`
	}
	if err := json.Unmarshal(raw, &links); err == nil && len(links) > 0 && links[0].TargetURI != "" {
		for _, link := range links {
			*locations = append(*locations, LSPLocation{
				URI:   link.TargetURI,
				Range: link.TargetRange,
			})
		}
		return nil
	}

	// Try single Location
	var single LSPLocation
	if err := json.Unmarshal(raw, &single); err == nil && single.URI != "" {
		*locations = []LSPLocation{single}
		return nil
	}

	// Try Location[]
	var many []LSPLocation
	if err := json.Unmarshal(raw, &many); err == nil {
		*locations = many
		return nil
	}

	return fmt.Errorf("unrecognized location format")
}

func flattenHoverContents(raw json.RawMessage) []LSPMarkupContent {
	var result []LSPMarkupContent

	// The result field of hover can be {contents: ...} or the contents directly.
	// Extract the "contents" field if present.
	var contentsField struct {
		Contents json.RawMessage `json:"contents"`
		Range    json.RawMessage `json:"range"`
	}
	if err := json.Unmarshal(raw, &contentsField); err == nil && contentsField.Contents != nil {
		raw = contentsField.Contents
	}

	// Try single MarkupContent
	var single struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &single); err == nil && single.Value != "" {
		result = append(result, LSPMarkupContent{Kind: single.Kind, Value: single.Value})
		return result
	}

	// Try array of MarkupContent
	var arr []struct {
		Kind  string `json:"kind"`
		Value string `json:"value"`
	}
	if err := json.Unmarshal(raw, &arr); err == nil {
		for _, item := range arr {
			if item.Value != "" {
				result = append(result, LSPMarkupContent{Kind: item.Kind, Value: item.Value})
			}
		}
		if len(result) > 0 {
			return result
		}
	}

	// Try plain string
	var plain string
	if err := json.Unmarshal(raw, &plain); err == nil && plain != "" {
		result = append(result, LSPMarkupContent{Kind: "plaintext", Value: plain})
		return result
	}

	return result
}

func flattenDocSymbols(symbols []struct {
	Name           string      `json:"name"`
	Kind           int         `json:"kind"`
	Detail         string      `json:"detail,omitempty"`
	Range          LSPRange    `json:"range"`
	SelectionRange LSPRange    `json:"selectionRange"`
	Children       []LSPSymbol `json:"children,omitempty"`
}, container string, out *[]LSPSymbol) {
	for _, sym := range symbols {
		*out = append(*out, LSPSymbol{
			Name:          sym.Name,
			Kind:          sym.Kind,
			Detail:        sym.Detail,
			ContainerName: container,
			Location: LSPLocation{
				URI:   "",
				Range: sym.Range,
			},
		})
		if len(sym.Children) > 0 {
			// If children are LSPSymbol we can't type-switch; just flatten
		}
	}
}
