package tools

import (
	"context"
	"fmt"
	"path/filepath"
	"strings"
)

// ---------------------------------------------------------------------------
// LSP tool
// ---------------------------------------------------------------------------

type lspToolArgs struct {
	Operation string `json:"operation"` // definition | references | hover | diagnostics | completion | status | workspace_symbol | document_symbols | type_definition | implementation
	FilePath  string `json:"file_path"`
	Line      int    `json:"line,omitempty"`
	Character int    `json:"character,omitempty"`
	Symbol    string `json:"symbol,omitempty"`
	Query     string `json:"query,omitempty"` // used for workspace_symbol
}

type lspStatusResult struct {
	AvailableServers map[string]bool   `json:"available_servers"`
	ActiveLanguages  []string          `json:"active_languages"`
	SupportedLangs   map[string]string `json:"supported_languages"` // language -> command
}

var lspSupportedLangs = func() map[string]string {
	m := make(map[string]string)
	for _, cfg := range lspConfigs {
		m[cfg.Language] = cfg.Command
	}
	return m
}()

// NewLSPTool creates a new LSP tool.
func NewLSPTool(workspace string) Tool {
	client := NewLSPClient(workspace)
	return NewTypedTool(NewToolSpec("lsp", "Precise code intelligence tool. Use this INSTEAD of grep+read when you need: finding where a function/variable is defined (definition), finding all usages of a symbol (references), getting type signatures and docs (hover), checking compile errors (diagnostics), listing all symbols in a file (document_symbols), or searching project-wide symbols by name (workspace_symbol). Returns structured results with zero noise. Falls back to grep/read automatically if the LSP server is not installed. Auto-detects language from file extension. Supported: gopls (Go), typescript-language-server (TS/JS), pyright-langserver (Python), rust-analyzer (Rust).", map[string]interface{}{
		"type": "object",
		"properties": map[string]interface{}{
			"operation": map[string]interface{}{
				"type":        "string",
				"enum":        []string{"definition", "references", "hover", "diagnostics", "completion", "status", "workspace_symbol", "document_symbols", "type_definition", "implementation"},
				"description": "LSP operation. Use 'status' to check available servers.",
			},
			"file_path": map[string]interface{}{
				"type":        "string",
				"description": "Path to the source file. Used to auto-detect the language. Required for: definition, references, hover, diagnostics, completion, document_symbols, type_definition, implementation.",
			},
			"line": map[string]interface{}{
				"type":        "integer",
				"description": "0-based line number for the symbol position. Required for: definition, references, hover, completion, type_definition, implementation.",
			},
			"character": map[string]interface{}{
				"type":        "integer",
				"description": "0-based character offset for the symbol position. Required for: definition, references, hover, completion, type_definition, implementation.",
			},
			"symbol": map[string]interface{}{
				"type":        "string",
				"description": "Symbol name (for display only, LSP uses position).",
			},
			"query": map[string]interface{}{
				"type":        "string",
				"description": "Search query for workspace_symbol operation.",
			},
		},
		"required": []string{"operation"},
	}, nil), func(ctx context.Context, args lspToolArgs) (ToolResult, error) {
		operation := strings.TrimSpace(args.Operation)
		if operation == "" {
			return ToolResult{}, fmt.Errorf("missing operation argument")
		}

		if operation == "status" {
			available := client.CheckServers()
			active := client.AvailableLanguages()
			return ToolResult{Structured: lspStatusResult{
				AvailableServers: available,
				ActiveLanguages:  active,
				SupportedLangs:   lspSupportedLangs,
			}}, nil
		}

		// Operations that need a file path
		needsFilePath := map[string]bool{
			"definition": true, "references": true, "hover": true,
			"diagnostics": true, "completion": true,
			"document_symbols": true, "type_definition": true, "implementation": true,
		}
		needsPosition := map[string]bool{
			"definition": true, "references": true, "hover": true,
			"completion": true, "type_definition": true, "implementation": true,
		}

		var language string
		var filePath string

		if needsFilePath[operation] {
			filePath = strings.TrimSpace(args.FilePath)
			if filePath == "" {
				return ToolResult{}, fmt.Errorf("file_path is required for %q operation", operation)
			}
			language = client.detectLanguageFromPath(filePath)
			if language == "" {
				ext := filepath.Ext(filePath)
				return ToolResult{}, fmt.Errorf("unsupported file extension %q for LSP operations. Supported: .go, .ts, .tsx, .js, .jsx, .py, .rs", ext)
			}
			if err := client.StartServer(ctx, language); err != nil {
				return ToolResult{}, fmt.Errorf("failed to start %s language server: %w", language, err)
			}
		}

		if needsPosition[operation] && (args.Line < 0 || args.Character < 0) {
			return ToolResult{}, fmt.Errorf("line and character are required for %q operation", operation)
		}

		switch operation {
		case "definition":
			locations, err := client.Definition(ctx, filePath, language, args.Line, args.Character)
			if err != nil {
				return ToolResult{}, err
			}
			if len(locations) == 0 {
				return ToolResult{Text: "No definition found at this position."}, nil
			}
			return formatLocationsResult("definition", language, locations), nil

		case "references":
			locations, err := client.References(ctx, filePath, language, args.Line, args.Character, true)
			if err != nil {
				return ToolResult{}, err
			}
			if len(locations) == 0 {
				return ToolResult{Text: "No references found."}, nil
			}
			return formatLocationsResult("references", language, locations), nil

		case "hover":
			hover, err := client.Hover(ctx, filePath, language, args.Line, args.Character)
			if err != nil {
				return ToolResult{}, err
			}
			if hover == nil || len(hover.Contents) == 0 {
				return ToolResult{Text: "No hover information available at this position."}, nil
			}
			var parts []string
			for _, c := range hover.Contents {
				if c.Value != "" {
					parts = append(parts, c.Value)
				}
			}
			text := strings.Join(parts, "\n\n")
			if text == "" {
				return ToolResult{Text: "No hover information available at this position."}, nil
			}
			// Truncate extremely long hover responses
			if len(text) > 10000 {
				text = text[:10000] + "\n\n// ... [truncated at 10KB]"
			}
			return ToolResult{
				Text: text,
				Structured: map[string]interface{}{
					"operation": "hover",
					"language":  language,
					"contents":  hover.Contents,
				},
			}, nil

		case "diagnostics":
			diags, err := client.Diagnostics(ctx, filePath, language)
			if err != nil {
				return ToolResult{}, err
			}
			if len(diags) == 0 {
				return ToolResult{Text: "No diagnostics (no errors or warnings)."}, nil
			}
			// Format diagnostics as text for readability
			var lines []string
			for _, d := range diags {
				loc := fmt.Sprintf("  %d:%d-%d:%d", d.Range.Start.Line, d.Range.Start.Character, d.Range.End.Line, d.Range.End.Character)
				msg := strings.ReplaceAll(d.Message, "\n", " ")
				lines = append(lines, fmt.Sprintf("- [Ln %s] %s", loc, msg))
			}
			text := fmt.Sprintf("Found %d diagnostic(s):\n%s", len(diags), strings.Join(lines, "\n"))
			return ToolResult{
				Text: text,
				Structured: map[string]interface{}{
					"operation":   "diagnostics",
					"language":    language,
					"diagnostics": diags,
				},
			}, nil

		case "completion":
			items, err := client.Completion(ctx, filePath, language, args.Line, args.Character)
			if err != nil {
				return ToolResult{}, err
			}
			if len(items) == 0 {
				return ToolResult{Text: "No completions available at this position."}, nil
			}
			if len(items) > 20 {
				items = items[:20]
			}
			// Format as text
			var lines []string
			for _, item := range items {
				detail := ""
				if item.Detail != "" {
					detail = " (" + item.Detail + ")"
				}
				lines = append(lines, fmt.Sprintf("  - %s%s", item.Label, detail))
			}
			text := fmt.Sprintf("Completions at %d:%d:\n%s", args.Line, args.Character, strings.Join(lines, "\n"))
			return ToolResult{
				Text: text,
				Structured: map[string]interface{}{
					"operation":   "completion",
					"language":    language,
					"completions": items,
				},
			}, nil

		case "workspace_symbol":
			query := strings.TrimSpace(args.Query)
			if query == "" {
				query = strings.TrimSpace(args.Symbol)
			}
			if query == "" {
				return ToolResult{}, fmt.Errorf("query or symbol is required for 'workspace_symbol' operation")
			}
			// For workspace symbols, we need a language. If none specified, try all active ones.
			var symbols []LSPSymbol
			// Try each active server
			activeLangs := client.AvailableLanguages()
			if len(activeLangs) == 0 {
				// Try all available servers
				available := client.CheckServers()
				for lang, ok := range available {
					if !ok {
						continue
					}
					if startErr := client.StartServer(ctx, lang); startErr != nil {
						continue
					}
					syms, symErr := client.WorkspaceSymbol(ctx, lang, query)
					if symErr == nil && len(syms) > 0 {
						symbols = append(symbols, syms...)
					}
				}
			} else {
				for _, lang := range activeLangs {
					syms, symErr := client.WorkspaceSymbol(ctx, lang, query)
					if symErr == nil {
						symbols = append(symbols, syms...)
					}
				}
			}
			if len(symbols) == 0 {
				return ToolResult{Text: fmt.Sprintf("No symbols found matching %q.", query)}, nil
			}
			if len(symbols) > 50 {
				symbols = symbols[:50]
			}
			return formatSymbolsResult("workspace_symbol", symbols, query), nil

		case "document_symbols":
			symbols, err := client.DocumentSymbols(ctx, filePath, language)
			if err != nil {
				return ToolResult{}, err
			}
			if len(symbols) == 0 {
				return ToolResult{Text: fmt.Sprintf("No symbols found in %s.", filePath)}, nil
			}
			if len(symbols) > 100 {
				symbols = symbols[:100]
			}
			return formatSymbolsResult("document_symbols", symbols, filePath), nil

		case "type_definition":
			locations, err := client.TypeDefinition(ctx, filePath, language, args.Line, args.Character)
			if err != nil {
				return ToolResult{}, err
			}
			if len(locations) == 0 {
				return ToolResult{Text: "No type definition found at this position."}, nil
			}
			return formatLocationsResult("type_definition", language, locations), nil

		case "implementation":
			locations, err := client.Implementation(ctx, filePath, language, args.Line, args.Character)
			if err != nil {
				return ToolResult{}, err
			}
			if len(locations) == 0 {
				return ToolResult{Text: "No implementations found at this position."}, nil
			}
			return formatLocationsResult("implementation", language, locations), nil

		default:
			return ToolResult{}, fmt.Errorf("unsupported LSP operation %q. Supported: definition, references, hover, diagnostics, completion, status, workspace_symbol, document_symbols, type_definition, implementation", operation)
		}
	})
}

// formatLocationsResult produces both text and structured output for location results.
func formatLocationsResult(operation, language string, locations []LSPLocation) ToolResult {
	var lines []string
	for i, loc := range locations {
		path := uriToPath(loc.URI)
		line := loc.Range.Start.Line
		col := loc.Range.Start.Character
		lines = append(lines, fmt.Sprintf("  %d. %s:%d:%d", i+1, path, line, col))
	}
	text := fmt.Sprintf("Found %d result(s):\n%s", len(locations), strings.Join(lines, "\n"))
	return ToolResult{
		Text: text,
		Structured: map[string]interface{}{
			"operation": operation,
			"language":  language,
			"locations": locations,
		},
	}
}

// formatSymbolsResult produces both text and structured output for symbol results.
func formatSymbolsResult(operation string, symbols []LSPSymbol, context string) ToolResult {
	_ = context // used for text format below
	var lines []string
	for i, sym := range symbols {
		loc := ""
		if sym.Location.URI != "" {
			path := uriToPath(sym.Location.URI)
			loc = fmt.Sprintf(" at %s:%d", path, sym.Location.Range.Start.Line)
		} else {
			loc = fmt.Sprintf(" at %d:%d", sym.Location.Range.Start.Line, sym.Location.Range.Start.Character)
		}
		detail := ""
		if sym.Detail != "" {
			detail = " (" + sym.Detail + ")"
		}
		container := ""
		if sym.ContainerName != "" {
			container = " ← " + sym.ContainerName
		}
		lines = append(lines, fmt.Sprintf("  %d. %s%s%s%s", i+1, sym.Name, detail, container, loc))
	}
	text := fmt.Sprintf("Found %d symbol(s):\n%s", len(symbols), strings.Join(lines, "\n"))
	if len(symbols) >= 50 {
		text += "\n// ... [truncated at 50 symbols, use a more specific query]"
	}
	return ToolResult{
		Text: text,
		Structured: map[string]interface{}{
			"operation": operation,
			"symbols":   symbols,
		},
	}
}


