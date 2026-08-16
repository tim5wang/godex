package packages

import (
	"fmt"
	"sort"
	"strings"
)

// DependencyReport summarizes install-time dependency validation for a
// candidate package against the currently installed set (with the candidate
// replacing any same-name entry).
type DependencyReport struct {
	Missing   []string   `json:"missing,omitempty"`   // unsatisfied package or capability requirements
	Conflicts []string   `json:"conflicts,omitempty"` // version conflicts with installed packages
	Cycles    [][]string `json:"cycles,omitempty"`    // dependency cycles (each is a path, e.g. ["a","b","a"])
}

func (r DependencyReport) Empty() bool {
	return len(r.Missing) == 0 && len(r.Conflicts) == 0 && len(r.Cycles) == 0
}

// Error renders the report as a single human-readable error message.
func (r DependencyReport) Error() string {
	var lines []string
	for _, item := range r.Missing {
		lines = append(lines, "missing dependency: "+item)
	}
	for _, item := range r.Conflicts {
		lines = append(lines, "dependency conflict: "+item)
	}
	for _, cycle := range r.Cycles {
		lines = append(lines, "dependency cycle: "+strings.Join(cycle, " -> "))
	}
	return strings.Join(lines, "; ")
}

// ValidateCandidateDependencies checks a candidate package's requires against
// the installed set. The candidate replaces any installed entry with the same
// name (reinstall semantics). Package requirements must resolve to an installed
// package whose version satisfies the constraint; capability requirements must
// resolve to a capability provided by the platform (knownCapability) or by an
// installed package's provides list.
func ValidateCandidateDependencies(candidate Entry, installed []Entry) DependencyReport {
	// Build a working set keyed by name: candidate replaces same-name entry.
	byName := make(map[string]Entry, len(installed)+1)
	for _, item := range installed {
		byName[item.Name] = item
	}
	if strings.TrimSpace(candidate.Name) != "" {
		byName[candidate.Name] = candidate
	}

	provides := make(map[string]struct{})
	for _, item := range byName {
		if item.Name == candidate.Name {
			continue
		}
		for _, capability := range item.Provides {
			provides[strings.TrimSpace(capability)] = struct{}{}
		}
	}

	var report DependencyReport
	for _, raw := range candidate.Requires {
		req, err := ParseRequirement(raw)
		if err != nil {
			report.Missing = append(report.Missing, fmt.Sprintf("%s (bad requirement: %v)", raw, err))
			continue
		}
		switch req.Kind {
		case "package":
			installedEntry, ok := byName[req.Name]
			if !ok {
				report.Missing = append(report.Missing, fmt.Sprintf("%s required by %s", raw, candidate.Name))
				continue
			}
			if !req.satisfiedByVersion(installedEntry.Version) {
				report.Conflicts = append(report.Conflicts,
					fmt.Sprintf("%s required by %s, installed %s@%s", raw, candidate.Name, installedEntry.Name, installedEntry.Version))
			}
		case "capability":
			if knownCapability(req.Name) {
				continue // provided by the platform itself
			}
			satisfied := false
			for provided := range provides {
				if req.satisfiedByCapability(provided) {
					satisfied = true
					break
				}
			}
			if !satisfied {
				report.Missing = append(report.Missing, fmt.Sprintf("%s required by %s", raw, candidate.Name))
			}
		}
	}
	report.Cycles = findDependencyCycles(byName)
	return report
}

// findDependencyCycles detects cycles in the package dependency graph using
// package-kind requires only. Each reported cycle is a path from a node back to
// itself, e.g. ["a", "b", "a"].
func findDependencyCycles(byName map[string]Entry) [][]string {
	graph := make(map[string][]string, len(byName))
	for name, entry := range byName {
		var deps []string
		for _, raw := range entry.Requires {
			req, err := ParseRequirement(raw)
			if err != nil || req.Kind != "package" {
				continue
			}
			if _, ok := byName[req.Name]; ok {
				deps = append(deps, req.Name)
			}
		}
		graph[name] = deps
	}

	const (
		white = 0 // unvisited
		gray  = 1 // on the current DFS path
		black = 2 // fully explored
	)
	state := make(map[string]int, len(graph))
	var path []string
	var cycles [][]string
	seenCycles := make(map[string]struct{})

	var visit func(node string)
	visit = func(node string) {
		state[node] = gray
		path = append(path, node)
		for _, next := range graph[node] {
			switch state[next] {
			case gray:
				// Found a cycle: emit the path from next..node..next.
				start := 0
				for i, p := range path {
					if p == next {
						start = i
						break
					}
				}
				cycle := append(append([]string{}, path[start:]...), next)
				key := strings.Join(cycle, "\x00")
				if _, ok := seenCycles[key]; !ok {
					seenCycles[key] = struct{}{}
					cycles = append(cycles, cycle)
				}
			case white:
				visit(next)
			}
		}
		path = path[:len(path)-1]
		state[node] = black
	}

	names := make([]string, 0, len(graph))
	for name := range graph {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if state[name] == white {
			visit(name)
		}
	}
	return cycles
}

// Dependents returns installed packages (other than exclude) whose package-kind
// requires reference name.
func Dependents(name string, installed []Entry, exclude string) []string {
	var out []string
	for _, item := range installed {
		if item.Name == exclude || item.Name == name {
			continue
		}
		for _, raw := range item.Requires {
			req, err := ParseRequirement(raw)
			if err != nil || req.Kind != "package" {
				continue
			}
			if req.Name == name {
				out = append(out, item.Name)
				break
			}
		}
	}
	sort.Strings(out)
	return out
}
