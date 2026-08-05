package relay

import (
	"strings"
	"testing"
)

func TestGenerateCredentialFormat(t *testing.T) {
	cred, err := GenerateCredential()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.HasPrefix(cred, "ck_") {
		t.Fatalf("expected ck_ prefix, got %q", cred)
	}
	if len(cred) < len("ck_")+32 {
		t.Fatalf("expected a reasonably long secret, got %q (len %d)", cred, len(cred))
	}
}

func TestGenerateCredentialUnique(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		cred, err := GenerateCredential()
		if err != nil {
			t.Fatalf("generate: %v", err)
		}
		if seen[cred] {
			t.Fatalf("duplicate credential %q", cred)
		}
		seen[cred] = true
	}
}

func TestCredentialHashRoundTrip(t *testing.T) {
	cred, err := GenerateCredential()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	hash := HashCredential(cred)
	if hash == "" {
		t.Fatal("expected non-empty hash")
	}
	if !ValidateCredential(cred, hash) {
		t.Fatal("expected valid credential to match its hash")
	}
	if ValidateCredential(cred+"x", hash) {
		t.Fatal("expected tampered credential to fail validation")
	}
	if ValidateCredential("", hash) {
		t.Fatal("expected empty credential to fail validation")
	}
}

func TestCredentialHashIsDeterministicButNotPlaintext(t *testing.T) {
	cred, err := GenerateCredential()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	hash1 := HashCredential(cred)
	hash2 := HashCredential(cred)
	if hash1 != hash2 {
		t.Fatal("expected deterministic hash for same credential")
	}
	if strings.Contains(hash1, cred) {
		t.Fatal("hash must not embed the plaintext credential")
	}
	if hash1 == cred {
		t.Fatal("hash must not equal the plaintext credential")
	}
}

func TestValidateCredentialEmptyHash(t *testing.T) {
	cred, err := GenerateCredential()
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if ValidateCredential(cred, "") {
		t.Fatal("expected validation to fail against empty stored hash")
	}
}
