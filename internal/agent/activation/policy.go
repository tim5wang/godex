// Package activation owns the pure capability-selection policy for agent templates.
package activation

import (
	"strings"

	"github.com/tim5wang/godex/internal/core/templates"
	"github.com/tim5wang/godex/internal/toolruntime"
)

// Mode describes how the host must apply a template capability plan.
type Mode uint8

const (
	Exact Mode = iota
	RegistrationDefaults
)

// Plan is the host-independent result of resolving a template's capabilities.
type Plan struct {
	Mode      Mode
	ToolNames []string
}

// Resolve applies exact Tools union Bundles semantics. The empty built-in
// default template is the only compatibility case that restores registration
// defaults; every other empty template intentionally gets no tools.
func Resolve(template templates.AgentTemplate, catalog toolruntime.ToolCatalog) Plan {
	if len(template.Tools) == 0 && len(template.Bundles) == 0 && strings.TrimSpace(template.ID) == templates.BuiltinDefault {
		return Plan{Mode: RegistrationDefaults}
	}

	names := toolNamesForBundles(catalog, template.Bundles)
	names = append(names, template.Tools...)
	return Plan{Mode: Exact, ToolNames: uniqueNames(names)}
}

func toolNamesForBundles(catalog toolruntime.ToolCatalog, bundles []string) []string {
	wanted := make(map[string]struct{}, len(bundles))
	for _, bundle := range bundles {
		wanted[strings.ToLower(strings.TrimSpace(bundle))] = struct{}{}
	}
	names := make([]string, 0, 16)
	for _, bundle := range catalog.Bundles {
		if _, ok := wanted[strings.ToLower(strings.TrimSpace(bundle.Name))]; ok {
			names = append(names, bundle.Tools...)
		}
	}
	return names
}

func uniqueNames(names []string) []string {
	out := make([]string, 0, len(names))
	seen := make(map[string]struct{}, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		out = append(out, name)
	}
	return out
}
