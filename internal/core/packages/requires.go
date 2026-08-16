package packages

import (
	"fmt"
	"strconv"
	"strings"
)

// Requirement is one parsed entry from a package manifest's requires list.
//
// Two forms are supported:
//
//   - package:   name@constraint (e.g. "review-kit@>=0.2.0", "toolkit@1", "lib")
//   - capability: capability-name@major (e.g. "godex:log@1", "tool:read_file")
//
// A capability requirement is satisfied when some installed package declares
// the same capability in its provides list, or when the platform itself
// provides it (see knownCapability).
type Requirement struct {
	Raw        string // original trimmed string
	Kind       string // "package" | "capability"
	Name       string // package name or capability name (without version)
	Constraint string // package: version constraint ("" = any); capability: major version ("" = any)
}

// ParseRequirement parses one requires entry.
func ParseRequirement(raw string) (Requirement, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return Requirement{}, fmt.Errorf("empty package requirement")
	}
	if strings.Contains(raw, ":") {
		// capability form: name@major (major optional)
		name, major, _ := strings.Cut(raw, "@")
		name = strings.TrimSpace(name)
		if name == "" {
			return Requirement{}, fmt.Errorf("invalid capability requirement %q: missing name", raw)
		}
		major = strings.TrimSpace(major)
		if major != "" {
			if _, err := parseMajorVersion(major); err != nil {
				return Requirement{}, fmt.Errorf("invalid capability requirement %q: %v", raw, err)
			}
		}
		return Requirement{Raw: raw, Kind: "capability", Name: name, Constraint: major}, nil
	}
	// package form: name@constraint (constraint optional)
	name, constraint, _ := strings.Cut(raw, "@")
	name = strings.TrimSpace(name)
	if name == "" {
		return Requirement{}, fmt.Errorf("invalid package requirement %q: missing name", raw)
	}
	if strings.ContainsAny(name, " \t") {
		return Requirement{}, fmt.Errorf("invalid package requirement %q: package name must not contain whitespace", raw)
	}
	constraint = strings.TrimSpace(constraint)
	if constraint != "" {
		if _, err := parseVersionConstraint(constraint); err != nil {
			return Requirement{}, fmt.Errorf("invalid package requirement %q: %v", raw, err)
		}
	}
	return Requirement{Raw: raw, Kind: "package", Name: name, Constraint: constraint}, nil
}

// satisfiedByVersion reports whether the requirement's constraint accepts the
// given installed version.
func (r Requirement) satisfiedByVersion(installed string) bool {
	if r.Kind != "package" {
		return false
	}
	if strings.TrimSpace(r.Constraint) == "" {
		return true
	}
	constraint, err := parseVersionConstraint(r.Constraint)
	if err != nil {
		return false
	}
	version, err := parseVersion(installed)
	if err != nil {
		return false
	}
	return constraint.matches(version)
}

// satisfiedByCapability reports whether the capability requirement accepts the
// given provided capability string (which may carry an @major suffix).
func (r Requirement) satisfiedByCapability(provided string) bool {
	if r.Kind != "capability" {
		return false
	}
	name, major, _ := strings.Cut(strings.TrimSpace(provided), "@")
	if name != r.Name {
		return false
	}
	if strings.TrimSpace(r.Constraint) == "" {
		return true
	}
	if strings.TrimSpace(major) == "" {
		return false
	}
	required, err := parseMajorVersion(r.Constraint)
	if err != nil {
		return false
	}
	got, err := parseMajorVersion(major)
	if err != nil {
		return false
	}
	return got == required
}

// parseMajorVersion parses a plain integer major version ("1", "2").
func parseMajorVersion(value string) (int, error) {
	value = strings.TrimSpace(value)
	n, err := strconv.Atoi(value)
	if err != nil || n < 0 {
		return 0, fmt.Errorf("invalid major version %q", value)
	}
	return n, nil
}

// version is a numeric semver-ish triple with an optional suffix that is
// ignored for ordering.
type version struct {
	major, minor, patch int
	raw                 string
}

func parseVersion(value string) (version, error) {
	value = strings.TrimSpace(value)
	core := value
	if idx := strings.IndexAny(core, "-+"); idx >= 0 {
		core = core[:idx]
	}
	parts := strings.Split(core, ".")
	nums := make([]int, 3)
	for i := 0; i < 3; i++ {
		if i >= len(parts) || strings.TrimSpace(parts[i]) == "" {
			continue
		}
		n, err := strconv.Atoi(strings.TrimSpace(parts[i]))
		if err != nil {
			return version{}, fmt.Errorf("invalid version %q", value)
		}
		nums[i] = n
	}
	return version{major: nums[0], minor: nums[1], patch: nums[2], raw: value}, nil
}

func (v version) atLeast(o version) bool {
	return v.major > o.major ||
		(v.major == o.major && (v.minor > o.minor || (v.minor == o.minor && v.patch >= o.patch)))
}

func (v version) above(o version) bool {
	return v.major > o.major ||
		(v.major == o.major && (v.minor > o.minor || (v.minor == o.minor && v.patch > o.patch)))
}

func (v version) below(o version) bool {
	return !v.atLeast(o)
}

func (v version) atMost(o version) bool {
	return !v.above(o)
}

func (v version) equal(o version) bool {
	return v.major == o.major && v.minor == o.minor && v.patch == o.patch
}

// versionConstraint is a parsed version constraint expression.
type versionConstraint struct {
	raw string
	// operator is one of "exact", "prefix", "gte", "gt", "lte", "lt",
	// "caret", "tilde", "any".
	operator string
	value    version
	// majorOnly is set for prefix constraints that only pin the major version
	// (e.g. "1" or "1.x").
	majorOnly bool
}

func parseVersionConstraint(value string) (versionConstraint, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "*" {
		return versionConstraint{raw: value, operator: "any"}, nil
	}
	// strip "v" prefix
	value = strings.TrimPrefix(value, "v")
	switch {
	case strings.HasPrefix(value, ">="):
		v, err := parseVersion(strings.TrimPrefix(value, ">="))
		if err != nil {
			return versionConstraint{}, err
		}
		return versionConstraint{raw: value, operator: "gte", value: v}, nil
	case strings.HasPrefix(value, "<="):
		v, err := parseVersion(strings.TrimPrefix(value, "<="))
		if err != nil {
			return versionConstraint{}, err
		}
		return versionConstraint{raw: value, operator: "lte", value: v}, nil
	case strings.HasPrefix(value, ">"):
		v, err := parseVersion(strings.TrimPrefix(value, ">"))
		if err != nil {
			return versionConstraint{}, err
		}
		return versionConstraint{raw: value, operator: "gt", value: v}, nil
	case strings.HasPrefix(value, "<"):
		v, err := parseVersion(strings.TrimPrefix(value, "<"))
		if err != nil {
			return versionConstraint{}, err
		}
		return versionConstraint{raw: value, operator: "lt", value: v}, nil
	case strings.HasPrefix(value, "^"):
		v, err := parseVersion(strings.TrimPrefix(value, "^"))
		if err != nil {
			return versionConstraint{}, err
		}
		return versionConstraint{raw: value, operator: "caret", value: v}, nil
	case strings.HasPrefix(value, "~"):
		v, err := parseVersion(strings.TrimPrefix(value, "~"))
		if err != nil {
			return versionConstraint{}, err
		}
		return versionConstraint{raw: value, operator: "tilde", value: v}, nil
	}
	// exact or prefix. "1.x" / "1.2.x" style prefixes are treated as prefix.
	if strings.Contains(value, "x") || strings.Contains(value, "X") {
		core := strings.ReplaceAll(strings.ReplaceAll(value, "x", ""), "X", "")
		core = strings.TrimSuffix(core, ".")
		if strings.TrimSpace(core) == "" {
			return versionConstraint{raw: value, operator: "any"}, nil
		}
		v, err := parseVersion(core)
		if err != nil {
			return versionConstraint{}, err
		}
		return versionConstraint{raw: value, operator: "prefix", value: v, majorOnly: !strings.Contains(core, ".")}, nil
	}
	v, err := parseVersion(value)
	if err != nil {
		return versionConstraint{}, err
	}
	parts := strings.Split(value, ".")
	switch len(parts) {
	case 1:
		return versionConstraint{raw: value, operator: "prefix", value: v, majorOnly: true}, nil
	case 2:
		return versionConstraint{raw: value, operator: "prefix", value: v}, nil
	default:
		return versionConstraint{raw: value, operator: "exact", value: v}, nil
	}
}

// matches reports whether the constraint accepts the given version.
func (c versionConstraint) matches(v version) bool {
	switch c.operator {
	case "any":
		return true
	case "exact":
		return v.equal(c.value)
	case "prefix":
		if c.majorOnly {
			return v.major == c.value.major
		}
		return v.major == c.value.major && v.minor == c.value.minor
	case "gte":
		return v.atLeast(c.value)
	case "gt":
		return v.above(c.value)
	case "lte":
		return v.atMost(c.value)
	case "lt":
		return v.below(c.value)
	case "caret":
		// ^M.m.p allows >= M.m.p and < (M+1).0.0
		upper := version{major: c.value.major + 1}
		return v.atLeast(c.value) && v.below(upper)
	case "tilde":
		// ~M.m.p allows >= M.m.p and < M.(m+1).0
		upper := version{major: c.value.major, minor: c.value.minor + 1}
		return v.atLeast(c.value) && v.below(upper)
	default:
		return false
	}
}
