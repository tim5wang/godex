package agent

import (
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"path/filepath"
	"sort"
	"strings"

	"github.com/tim5wang/godex/internal/core/compress"
)

const repoMapTokenBudget = 2500

func (a *Agent) buildRepoMapPrompt(query string) string {
	if a == nil || a.cfg == nil || strings.TrimSpace(a.cfg.WorkspaceDir) == "" {
		return ""
	}
	entries := collectRepoMapEntries(a.cfg.WorkspaceDir, query)
	if len(entries) == 0 {
		return ""
	}
	var builder strings.Builder
	builder.WriteString("# Repo Map\n")
	builder.WriteString("Lightweight coding context. Use read/search tools for exact file contents.\n")
	for _, entry := range entries {
		next := builder.String() + entry + "\n"
		if compress.CountTokens(next) > repoMapTokenBudget {
			break
		}
		builder.WriteString(entry)
		builder.WriteByte('\n')
	}
	return strings.TrimSpace(builder.String())
}

func collectRepoMapEntries(root, query string) []string {
	root = filepath.Clean(root)
	queryTokens := repoMapQueryTokens(query)
	entries := make([]string, 0, 128)
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		name := d.Name()
		if d.IsDir() {
			if shouldSkipRepoMapDir(name) && path != root {
				return filepath.SkipDir
			}
			return nil
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil
		}
		rel = filepath.ToSlash(rel)
		if shouldSkipRepoMapFile(rel) {
			return nil
		}
		if len(queryTokens) > 0 && !repoMapRelevant(rel, queryTokens) && len(entries) > 80 {
			return nil
		}
		entry := "- " + rel
		if filepath.Ext(path) == ".go" {
			if symbols := goFileSymbolSummary(path); symbols != "" {
				entry += " :: " + symbols
			}
		}
		entries = append(entries, entry)
		if len(entries) >= 160 {
			return fs.SkipAll
		}
		return nil
	})
	sort.SliceStable(entries, func(i, j int) bool {
		return repoMapScore(entries[i], queryTokens) > repoMapScore(entries[j], queryTokens)
	})
	return entries
}

func shouldSkipRepoMapDir(name string) bool {
	switch name {
	case ".git", ".godex", "node_modules", "vendor", "dist", "build", ".next", "coverage", "tmp", "log":
		return true
	default:
		return false
	}
}

func shouldSkipRepoMapFile(rel string) bool {
	base := filepath.Base(rel)
	if strings.HasPrefix(base, ".") && base != ".env.example" {
		return true
	}
	switch filepath.Ext(base) {
	case ".go", ".ts", ".tsx", ".js", ".jsx", ".md", ".yaml", ".yml", ".json", ".toml", ".css":
		return false
	default:
		return true
	}
}

func goFileSymbolSummary(path string) string {
	file, err := parser.ParseFile(token.NewFileSet(), path, nil, parser.SkipObjectResolution)
	if err != nil {
		return ""
	}
	symbols := make([]string, 0, 8)
	for _, decl := range file.Decls {
		switch d := decl.(type) {
		case *ast.GenDecl:
			for _, spec := range d.Specs {
				typeSpec, ok := spec.(*ast.TypeSpec)
				if !ok || typeSpec.Name == nil || !typeSpec.Name.IsExported() {
					continue
				}
				symbols = append(symbols, "type "+typeSpec.Name.Name)
			}
		case *ast.FuncDecl:
			if d.Name != nil && d.Name.IsExported() {
				symbols = append(symbols, "func "+d.Name.Name)
			}
		}
		if len(symbols) >= 8 {
			break
		}
	}
	return strings.Join(symbols, ", ")
}

func repoMapQueryTokens(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '_' && r != '-' && r != '/'
	})
	out := make([]string, 0, len(fields))
	seen := map[string]struct{}{}
	for _, field := range fields {
		if len(field) < 3 {
			continue
		}
		if _, ok := seen[field]; ok {
			continue
		}
		seen[field] = struct{}{}
		out = append(out, field)
	}
	return out
}

func repoMapRelevant(entry string, tokens []string) bool {
	lower := strings.ToLower(entry)
	for _, token := range tokens {
		if strings.Contains(lower, token) {
			return true
		}
	}
	return false
}

func repoMapScore(entry string, tokens []string) int {
	score := 0
	lower := strings.ToLower(entry)
	for _, token := range tokens {
		if strings.Contains(lower, token) {
			score += 10
		}
	}
	if strings.Contains(entry, " :: ") {
		score += 2
	}
	if strings.HasPrefix(entry, "- internal/") || strings.HasPrefix(entry, "- cmd/") {
		score++
	}
	return score
}
