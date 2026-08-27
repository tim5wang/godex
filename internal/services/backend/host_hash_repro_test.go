package backend

import (
	"testing"
)

// Repro for the taskboard jump-to-progress bug: does rebuilding the session
// identity from the stored HostRef (channel/key[/project_dir]) actually
// produce the host session id recorded in the ledger?
func TestReproTaskboardHostHash(t *testing.T) {
	host := struct{ id, channel, key string }{
		id: "web-56473c3f05d87357", channel: "web", key: "4c252225-b0ad-48cb-a96a-91e04b8dc969",
	}
	cases := []struct {
		name    string
		locator SessionLocator
	}{
		{"triple only", SessionLocator{Channel: host.channel, Key: host.key}},
		{"with default project_dir", SessionLocator{Channel: host.channel, Key: host.key, Metadata: map[string]string{"project_dir": "/Users/taiwu.wang/Documents/leader_agent/godex"}}},
		{"with trailing slash", SessionLocator{Channel: host.channel, Key: host.key, Metadata: map[string]string{"project_dir": "/Users/taiwu.wang/Documents/leader_agent/godex/"}}},
	}
	for _, tc := range cases {
		got := stableSessionID(tc.locator)
		match := "MISMATCH"
		if got == host.id {
			match = "MATCH"
		}
		t.Logf("%-28s -> %s (%s)", tc.name, got, match)
	}
}
