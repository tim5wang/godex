package pluginrt

import (
	"fmt"
	"sort"
	"strings"
)

// Graph validates a candidate plugin's requires against the current plugin set
// plus platform-provided capabilities.
type Graph struct {
	// platform is an optional predicate answering "does the platform itself
	// provide this capability?" (e.g. builtin tool namespaces).
	platform func(capabilityName string) bool
}

// NewGraph creates a dependency validator. platform may be nil.
func NewGraph(platform func(capabilityName string) bool) *Graph {
	return &Graph{platform: platform}
}

// Report summarizes dependency validation for a candidate plugin.
type Report struct {
	Missing   []string   // unsatisfied capability requirements
	Conflicts []string   // version conflicts with provided capabilities
	Cycles    [][]string // dependency cycles among plugin ids
}

func (r Report) Empty() bool {
	return len(r.Missing) == 0 && len(r.Conflicts) == 0 && len(r.Cycles) == 0
}

func (r Report) Error() string {
	var lines []string
	for _, item := range r.Missing {
		lines = append(lines, "missing capability: "+item)
	}
	for _, item := range r.Conflicts {
		lines = append(lines, "capability conflict: "+item)
	}
	for _, cycle := range r.Cycles {
		lines = append(lines, "dependency cycle: "+strings.Join(cycle, " -> "))
	}
	return strings.Join(lines, "; ")
}

// Validate checks the candidate against the installed set. The candidate
// replaces any installed plugin with the same id (reload semantics).
func (g *Graph) Validate(candidate Manifest, installed []Manifest) Report {
	byID := make(map[string]Manifest, len(installed)+1)
	for _, item := range installed {
		byID[item.ID] = item
	}
	if strings.TrimSpace(candidate.ID) != "" {
		byID[candidate.ID] = candidate
	}

	// Collect capabilities provided by other plugins (not the candidate's own
	// provides, which would mask missing requirements).
	provided := make(map[string]struct{})
	for id, item := range byID {
		if id == candidate.ID {
			continue
		}
		for _, raw := range item.Provides {
			provided[strings.TrimSpace(raw)] = struct{}{}
		}
	}

	var report Report
	for _, raw := range candidate.Requires {
		req, err := parseCapability(raw)
		if err != nil {
			report.Missing = append(report.Missing, fmt.Sprintf("%s required by %s (bad capability: %v)", raw, candidate.ID, err))
			continue
		}
		if g.platform != nil && g.platform(req.Name) {
			continue
		}
		satisfied := false
		conflict := ""
		for providedRaw := range provided {
			if matches(raw, providedRaw) {
				satisfied = true
				break
			}
			// Version mismatch on the same capability name.
			providedCap, perr := parseCapability(providedRaw)
			if perr == nil && providedCap.Name == req.Name && !req.Any && providedCap.Major != req.Major {
				conflict = fmt.Sprintf("%s required by %s, but %s provides %s@%d", raw, candidate.ID, pluginProviding(byID, providedRaw), providedCap.Name, providedCap.Major)
			}
		}
		switch {
		case satisfied:
		case conflict != "":
			report.Conflicts = append(report.Conflicts, conflict)
		default:
			report.Missing = append(report.Missing, fmt.Sprintf("%s required by %s", raw, candidate.ID))
		}
	}
	report.Cycles = findCycles(byID)
	return report
}

func pluginProviding(byID map[string]Manifest, capability string) string {
	ids := make([]string, 0)
	for id, item := range byID {
		for _, raw := range item.Provides {
			if strings.TrimSpace(raw) == capability {
				ids = append(ids, id)
			}
		}
	}
	sort.Strings(ids)
	if len(ids) == 0 {
		return "?"
	}
	return ids[0]
}

// findCycles detects cycles in plugin requires edges (capability requirements
// resolved to provider plugin ids).
func findCycles(byID map[string]Manifest) [][]string {
	// Build id -> [ids it depends on]
	deps := make(map[string][]string, len(byID))
	providerIDs := make(map[string][]string) // capability name -> provider ids
	for id, item := range byID {
		for _, raw := range item.Provides {
			cap, err := parseCapability(raw)
			if err != nil {
				continue
			}
			providerIDs[cap.Name] = append(providerIDs[cap.Name], id)
		}
	}
	for id, item := range byID {
		var ids []string
		for _, raw := range item.Requires {
			req, err := parseCapability(raw)
			if err != nil {
				continue
			}
			seen := map[string]struct{}{}
			for _, provider := range providerIDs[req.Name] {
				if _, ok := seen[provider]; ok {
					continue
				}
				seen[provider] = struct{}{}
				ids = append(ids, provider)
			}
		}
		sort.Strings(ids)
		deps[id] = ids
	}

	const (
		white = 0
		gray  = 1
		black = 2
	)
	state := make(map[string]int, len(deps))
	var path []string
	var cycles [][]string
	seen := make(map[string]struct{})

	var visit func(id string)
	visit = func(id string) {
		state[id] = gray
		path = append(path, id)
		for _, next := range deps[id] {
			switch state[next] {
			case gray:
				start := 0
				for i, p := range path {
					if p == next {
						start = i
						break
					}
				}
				cycle := append(append([]string{}, path[start:]...), next)
				key := strings.Join(cycle, "\x00")
				if _, ok := seen[key]; !ok {
					seen[key] = struct{}{}
					cycles = append(cycles, cycle)
				}
			case white:
				visit(next)
			}
		}
		path = path[:len(path)-1]
		state[id] = black
	}

	ids := make([]string, 0, len(deps))
	for id := range deps {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	for _, id := range ids {
		if state[id] == white {
			visit(id)
		}
	}
	return cycles
}
