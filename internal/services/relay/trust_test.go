package relay

import (
	"strings"
	"testing"
)

func TestSignAndValidateRelayTrust(t *testing.T) {
	nodeID := "my-laptop"
	cred := "ck_0123456789abcdef"
	value := SignRelayTrust(nodeID, cred)
	if value == "" {
		t.Fatal("expected non-empty signature")
	}
	if !ValidateRelayTrust(value, nodeID, cred) {
		t.Fatal("expected valid signature to pass")
	}
}

func TestValidateRelayTrustRejectsWrongNodeOrCredential(t *testing.T) {
	nodeID := "my-laptop"
	cred := "ck_0123456789abcdef"
	value := SignRelayTrust(nodeID, cred)

	if ValidateRelayTrust(value, "other-node", cred) {
		t.Fatal("expected wrong node id to be rejected")
	}
	if ValidateRelayTrust(value, nodeID, "ck_wrong") {
		t.Fatal("expected wrong credential to be rejected")
	}
	if ValidateRelayTrust("garbage", nodeID, cred) {
		t.Fatal("expected garbage header to be rejected")
	}
}

func TestValidateRelayTrustRejectsEmptyInputs(t *testing.T) {
	if ValidateRelayTrust("", "node", "ck_x") {
		t.Fatal("expected empty header to be rejected")
	}
	if ValidateRelayTrust("abc", "", "ck_x") {
		t.Fatal("expected empty node id to be rejected")
	}
	if ValidateRelayTrust("abc", "node", "") {
		t.Fatal("expected empty credential to be rejected")
	}
}

func TestSignRelayTrustDeterministic(t *testing.T) {
	a := SignRelayTrust("node-a", "ck_x")
	b := SignRelayTrust("node-a", "ck_x")
	if a != b {
		t.Fatal("expected deterministic signature for same inputs")
	}
	if a == SignRelayTrust("node-a", "ck_y") {
		t.Fatal("expected different credential to change signature")
	}
}

func TestRelayTrustHeaderFormat(t *testing.T) {
	if strings.TrimSpace(RelayTrustHeader) != "X-Godex-Relay-Trusted" {
		t.Fatalf("unexpected header constant %q", RelayTrustHeader)
	}
}
