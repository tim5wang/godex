package scope

import (
	"strings"
	"testing"
)

func TestNewAndString(t *testing.T) {
	cases := []struct {
		kind Kind
		ref  string
		want string
	}{
		{KindSession, "web-abc123", "session:web-abc123"},
		{KindPersonal, "user-42", "personal:user-42"},
		{KindOrg, "godex", "org:godex"},
	}
	for _, tc := range cases {
		id := New(tc.kind, tc.ref)
		if got := id.String(); got != tc.want {
			t.Errorf("New(%q, %q).String() = %q, want %q", tc.kind, tc.ref, got, tc.want)
		}
	}
}

func TestNewRejectsUnknownKind(t *testing.T) {
	if got := New(Kind("team"), "x"); got != "" {
		t.Errorf("New(unknown kind) = %q, want empty", got)
	}
}

func TestNewTrimsAndCleansRef(t *testing.T) {
	if got := New(KindSession, "  abc  "); got.String() != "session:abc" {
		t.Errorf("trim: got %q", got.String())
	}
	// Colon in the ref must be scrubbed so parse stays unambiguous.
	if got := New(KindOrg, "a:b"); got.String() != "org:a-b" {
		t.Errorf("colon scrub: got %q, want org:a-b", got.String())
	}
	if got := New(KindSession, ""); got != "" {
		t.Errorf("empty ref: got %q, want empty", got)
	}
}

func TestSessionPersonalOrgHelpers(t *testing.T) {
	if got := Session("s1").String(); got != "session:s1" {
		t.Errorf("Session = %q", got)
	}
	if got := Personal("u1").String(); got != "personal:u1" {
		t.Errorf("Personal = %q", got)
	}
	if got := Org("o1").String(); got != "org:o1" {
		t.Errorf("Org = %q", got)
	}
}

func TestParse(t *testing.T) {
	kind, ref, ok := Parse(Session("s1"))
	if !ok || kind != KindSession || ref != "s1" {
		t.Errorf("Parse(session:s1) = %q, %q, %v", kind, ref, ok)
	}
	kind, ref, ok = Parse(Org("o1"))
	if !ok || kind != KindOrg || ref != "o1" {
		t.Errorf("Parse(org:o1) = %q, %q, %v", kind, ref, ok)
	}
	if _, _, ok := Parse(""); ok {
		t.Errorf("Parse(empty) should fail")
	}
	if _, _, ok := Parse("noseparator"); ok {
		t.Errorf("Parse(noseparator) should fail")
	}
	if _, _, ok := Parse("team:x"); ok {
		t.Errorf("Parse(unknown kind) should fail")
	}
}

func TestIsShared(t *testing.T) {
	if Session("s1").IsShared() {
		t.Errorf("session scope should not be shared")
	}
	if !Personal("u1").IsShared() {
		t.Errorf("personal scope should be shared")
	}
	if !Org("o1").IsShared() {
		t.Errorf("org scope should be shared")
	}
}

func TestStorageKeySimpleRefs(t *testing.T) {
	cases := []struct {
		id   Id
		want string
	}{
		{Session("web-abc123"), "session__web-abc123"},
		{Org("godex"), "org__godex"},
		{Personal("user-42"), "personal__user-42"},
	}
	for _, tc := range cases {
		if got := StorageKey(tc.id); got != tc.want {
			t.Errorf("StorageKey(%q) = %q, want %q", tc.id, got, tc.want)
		}
	}
}

func TestStorageKeyScrubsUnsafeRef(t *testing.T) {
	// Ref containing path separators must be scrubbed AND suffixed with a
	// hash so collisions between scrubbed forms are avoided.
	key := StorageKey(Org("a/b"))
	if strings.Contains(key, "/") {
		t.Errorf("StorageKey(%q) contains '/': %q", Org("a/b"), key)
	}
	if !strings.HasPrefix(key, "org__a__b--") {
		t.Errorf("StorageKey(%q) = %q, want org__a__b--<hash> prefix", Org("a/b"), key)
	}
	if key == StorageKey(Org("a_b")) {
		t.Errorf("scrubbed key collides between a/b and a_b: %q", key)
	}
}

func TestStorageKeyPathTraversalSafe(t *testing.T) {
	for _, ref := range []string{"..", "../..", "a/../../b", "..\\evil", "a b"} {
		id := Org(ref)
		key := StorageKey(id)
		if strings.Contains(key, "/") || strings.Contains(key, "\\") {
			t.Errorf("StorageKey(%q) = %q contains a path separator", id, key)
		}
		for _, seg := range strings.Split(key, "__") {
			if seg == ".." {
				t.Errorf("StorageKey(%q) = %q contains '..' segment", id, key)
			}
		}
	}
}

func TestStorageKeyDistinctPerRef(t *testing.T) {
	a := StorageKey(Org("repo-a"))
	b := StorageKey(Org("repo-b"))
	if a == b {
		t.Errorf("distinct refs must not collide: %q", a)
	}
	if StorageKey(Session("s1")) == StorageKey(Org("s1")) {
		t.Errorf("different kinds must not collide")
	}
}
