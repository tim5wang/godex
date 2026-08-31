// Package toolfilter implements the shared allow/deny list semantics used by
// Agent Step tool narrowing.
package toolfilter

import "strings"

// Allows reports whether list permits item. Entries may be exact names,
// "*", "name/*", or the same forms prefixed with "!". Exclusions take
// precedence over inclusions, and an empty list permits nothing.
func Allows(list []string, item string) bool {
	allowAll := false
	for _, entry := range list {
		if entry == "*" {
			allowAll = true
		}
	}
	for _, entry := range list {
		if !strings.HasPrefix(entry, "!") {
			continue
		}
		exclude := strings.TrimPrefix(entry, "!")
		exclude = strings.TrimSuffix(exclude, "/*")
		if exclude == item || exclude == "*" {
			return false
		}
	}
	if allowAll {
		return true
	}
	for _, entry := range list {
		if entry == item || entry == item+"/*" {
			return true
		}
	}
	return false
}
