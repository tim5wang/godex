package relay

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
)

// rotationCenter is a minimal stand-in for the center's rotation endpoint: it
// answers only with the credential it was given, so a test can assert what the
// node actually presented.
type rotationCenter struct {
	t            *testing.T
	accept       string
	status       int
	gotAuth      string
	gotPath      string
	nodeID       string
	proxyToken   string
	handlerCalls int
}

func (c *rotationCenter) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	c.handlerCalls++
	c.gotAuth = r.Header.Get("Authorization")
	c.gotPath = r.URL.Path
	if c.status != 0 && c.status != http.StatusOK {
		w.WriteHeader(c.status)
		return
	}
	if c.accept != "" && r.Header.Get("Authorization") != "Bearer "+c.accept {
		w.WriteHeader(http.StatusUnauthorized)
		return
	}
	_ = json.NewEncoder(w).Encode(map[string]string{
		"node_id":          c.nodeID,
		"node_proxy_token": c.proxyToken,
	})
}

func TestRotateProxyCredentialSendsCredentialAndParsesResult(t *testing.T) {
	center := &rotationCenter{t: t, accept: "ck_node_a", nodeID: "node-a", proxyToken: "nk_fresh"}
	srv := httptest.NewServer(center)
	t.Cleanup(srv.Close)

	got, err := RotateProxyCredential(context.Background(), srv.URL, "ck_node_a")
	if err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if got.NodeID != "node-a" || got.Token != "nk_fresh" {
		t.Fatalf("rotated = %+v, want node-a/nk_fresh", got)
	}
	// The credential travels as the Bearer value, and nothing else identifies the
	// node: that is what makes the call safe without the center web token.
	if center.gotAuth != "Bearer ck_node_a" {
		t.Errorf("Authorization = %q, want the node credential", center.gotAuth)
	}
	if center.gotPath != "/api/control/self/proxy-credential" {
		t.Errorf("path = %q, want the /api-prefixed rotation endpoint", center.gotPath)
	}
}

// A center URL that already carries /api must not end up with /api/api.
func TestRotateProxyCredentialToleratesAPIPrefixedCenterURL(t *testing.T) {
	center := &rotationCenter{t: t, accept: "ck_x", proxyToken: "nk_y"}
	srv := httptest.NewServer(center)
	t.Cleanup(srv.Close)

	if _, err := RotateProxyCredential(context.Background(), srv.URL+"/api", "ck_x"); err != nil {
		t.Fatalf("rotate: %v", err)
	}
	if center.gotPath != "/api/control/self/proxy-credential" {
		t.Errorf("path = %q, want a single /api prefix", center.gotPath)
	}
}

// Rejection and absence must be distinguishable, because they call for opposite
// operator action (re-check the credential vs upgrade the center).
func TestRotateProxyCredentialDistinguishesRejectionFromUnsupported(t *testing.T) {
	cases := []struct {
		name   string
		status int
		want   error
	}{
		{"unauthorized", http.StatusUnauthorized, ErrProxyRotationRejected},
		{"forbidden", http.StatusForbidden, ErrProxyRotationRejected},
		{"no such endpoint", http.StatusNotFound, ErrProxyRotationUnsupported},
		{"older center", http.StatusMethodNotAllowed, ErrProxyRotationUnsupported},
		{"endpoint disabled", http.StatusServiceUnavailable, ErrProxyRotationUnsupported},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(&rotationCenter{t: t, status: tc.status})
			t.Cleanup(srv.Close)
			_, err := RotateProxyCredential(context.Background(), srv.URL, "ck_x")
			if !errors.Is(err, tc.want) {
				t.Fatalf("err = %v, want %v", err, tc.want)
			}
		})
	}
}

func TestRotateProxyCredentialRejectsEmptyCredentialAndBadURL(t *testing.T) {
	if _, err := RotateProxyCredential(context.Background(), "https://center.example", "  "); err == nil {
		t.Error("an empty credential must not be sent")
	}
	for _, bad := range []string{"", "not-a-url", "ftp://center.example"} {
		if _, err := RotateProxyCredential(context.Background(), bad, "ck_x"); err == nil {
			t.Errorf("center URL %q should be refused", bad)
		}
	}
}

// An empty token in a 200 response is not a success: storing it would silently
// leave the node unable to authenticate.
func TestRotateProxyCredentialRejectsEmptyTokenInResponse(t *testing.T) {
	srv := httptest.NewServer(&rotationCenter{t: t, accept: "ck_x", nodeID: "node-a"})
	t.Cleanup(srv.Close)
	if _, err := RotateProxyCredential(context.Background(), srv.URL, "ck_x"); err == nil {
		t.Error("an empty node_proxy_token must be an error")
	}
}
