// Package scope defines the ScopeId isolation model (roadmap 6.2): a
// kind:ref identifier used to bound memory, files and sandbox state to a
// session, a user, or an org. It mirrors the reference ScopeId helpers in
// temp/qm/src/types.ts and temp/qm/src/util/scope-storage-key.ts, trimmed to
// the kinds godex actually uses (session / personal / org).
package scope

import (
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Kind is the category of a scope. Only the kinds godex uses are enabled;
// QM also defines channel/team/group which need a directory service we do not
// have (see docs/scope-isolation-design.md §3.3).
type Kind string

const (
	// KindSession is one conversation/session: the default isolation granularity.
	KindSession Kind = "session"
	// KindPersonal is a single user, shared across that user's sessions.
	KindPersonal Kind = "personal"
	// KindOrg is workspace-wide (org-level) shared state; the default
	// compatibility layer when no explicit org id is configured.
	KindOrg Kind = "org"
)

var knownKinds = map[Kind]bool{
	KindSession:  true,
	KindPersonal: true,
	KindOrg:      true,
}

// Id is a scope identifier of the form "<kind>:<ref>", e.g.
// "session:web-abc123" or "org:godex". The zero value "" means "unspecified";
// callers decide the default (usually the session scope).
type Id string

// New builds a scope id. It returns "" when the kind is unknown or the ref is
// empty after trimming. Any ':' inside the ref is scrubbed to '-' so Parse
// stays unambiguous.
func New(kind Kind, ref string) Id {
	if !knownKinds[kind] {
		return ""
	}
	ref = strings.TrimSpace(ref)
	if ref == "" {
		return ""
	}
	ref = strings.ReplaceAll(ref, ":", "-")
	return Id(string(kind) + ":" + ref)
}

// Session builds a session scope id.
func Session(id string) Id { return New(KindSession, id) }

// Personal builds a per-user scope id.
func Personal(user string) Id { return New(KindPersonal, user) }

// Org builds an org-level scope id.
func Org(name string) Id { return New(KindOrg, name) }

// Parse splits a scope id into its kind and ref. It returns ok=false for an
// empty id, a missing separator, or an unknown kind.
func Parse(id Id) (Kind, string, bool) {
	raw := string(id)
	sep := strings.Index(raw, ":")
	if sep < 0 {
		return "", "", false
	}
	kind := Kind(raw[:sep])
	if !knownKinds[kind] {
		return "", "", false
	}
	return kind, raw[sep+1:], true
}

// IsShared reports whether the scope is shared beyond a single session.
// Session scopes are private; personal and org scopes are shared.
func (s Id) IsShared() bool {
	kind, _, ok := Parse(s)
	return ok && kind != KindSession
}

// String returns the canonical "<kind>:<ref>" form.
func (s Id) String() string { return string(s) }

// safeRef reports whether the ref survives StorageKey unchanged: it must be
// non-empty, contain only [a-zA-Z0-9_-] (no '/', '\', '.', space or '..'), so
// the scrubbed key cannot collide with another ref and cannot escape the
// memory directory.
func safeRef(ref string) bool {
	if ref == "" {
		return false
	}
	if strings.ContainsAny(ref, `/\ .`) {
		return false
	}
	for _, r := range ref {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_') {
			return false
		}
	}
	return true
}

// StorageKey maps a scope id to a safe, collision-resistant filesystem path
// segment. Safe chars ([a-zA-Z0-9_-]) are kept, everything else (including
// ':', '/', '.' and '..') becomes "__"; if the ref contains unsafe characters
// the result gets a "--<hash12>" suffix so distinct refs that scrub to the
// same form stay distinct and path traversal ("..", "/") is impossible.
func StorageKey(id Id) string {
	raw := string(id)
	legacy := scrub(raw)
	_, ref, ok := Parse(id)
	if !ok || safeRef(ref) {
		return legacy
	}
	return legacy + "--" + hash12(raw)
}

// scrub replaces every character outside [a-zA-Z0-9_-] with "__". The '-'
// and '_' are kept for readability; unsafe refs are disambiguated by the
// caller (StorageKey) via the hash suffix. '.' is NOT kept so ".." can never
// appear as a path segment in the resulting key.
func scrub(raw string) string {
	var b strings.Builder
	b.Grow(len(raw) + 8)
	for _, r := range raw {
		if isSafeScrubRune(r) {
			b.WriteRune(r)
		} else {
			b.WriteString("__")
		}
	}
	return b.String()
}

func isSafeScrubRune(r rune) bool {
	return r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_'
}

func hash12(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])[:12]
}
