package relay

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// ProxyCredential is the result of rotating a node's own proxy credential: the
// id the center resolved from the presented ck_ (the request body carries no id,
// so this is what proves the rotation was scoped to the caller), and the fresh
// nk_ credential to store as control.center_token.
type ProxyCredential struct {
	NodeID string
	Token  string
}

// ErrProxyRotationUnsupported reports that the center exposes no per-node
// rotation endpoint (an older build). Distinguished from a rejected credential
// because the two call for opposite behaviour: an unsupported center must not be
// retried on a timer, while a rejected credential is worth re-authenticating.
var ErrProxyRotationUnsupported = errors.New("center does not support per-node proxy credential rotation")

// ErrProxyRotationRejected reports that the center refused the node credential
// we presented (unknown, or rotated away by another party).
var ErrProxyRotationRejected = errors.New("center rejected the node credential")

// RotateProxyCredential exchanges a node's per-node ck_ credential for a fresh
// per-node nk_ credential, via the center's POST /control/self/proxy-credential.
//
// The credential travels as the Bearer value and is the ONLY identity in the
// request: there is no node id to supply, so a node cannot rotate anyone else's
// token even if it tries. That is what makes this safe to expose without the
// center's web token.
func RotateProxyCredential(ctx context.Context, centerURL, credential string) (ProxyCredential, error) {
	credential = strings.TrimSpace(credential)
	if credential == "" {
		return ProxyCredential{}, fmt.Errorf("missing node credential")
	}
	endpoint, err := proxyCredentialURL(centerURL)
	if err != nil {
		return ProxyCredential{}, err
	}
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, nil)
	if err != nil {
		return ProxyCredential{}, err
	}
	req.Header.Set("Authorization", "Bearer "+credential)

	resp, err := client.Do(req)
	if err != nil {
		return ProxyCredential{}, err
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK, http.StatusCreated:
	case http.StatusUnauthorized, http.StatusForbidden:
		// One error for unknown/revoked: the caller learns nothing about which
		// node ids exist on the center.
		return ProxyCredential{}, ErrProxyRotationRejected
	case http.StatusServiceUnavailable, http.StatusNotFound, http.StatusMethodNotAllowed:
		// An older center without the endpoint. Reported distinctly so a caller
		// can stop trying instead of hammering it.
		return ProxyCredential{}, ErrProxyRotationUnsupported
	default:
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return ProxyCredential{}, fmt.Errorf("center returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	var out struct {
		NodeID         string `json:"node_id"`
		NodeProxyToken string `json:"node_proxy_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return ProxyCredential{}, err
	}
	if strings.TrimSpace(out.NodeProxyToken) == "" {
		return ProxyCredential{}, fmt.Errorf("center returned empty node proxy token")
	}
	return ProxyCredential{NodeID: strings.TrimSpace(out.NodeID), Token: strings.TrimSpace(out.NodeProxyToken)}, nil
}

// proxyCredentialURL builds the rotation endpoint URL, tolerating a center URL
// that already carries the /api prefix the Web UI uses.
func proxyCredentialURL(centerURL string) (string, error) {
	raw := strings.TrimSpace(centerURL)
	if raw == "" {
		return "", fmt.Errorf("empty center URL")
	}
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("invalid center URL %q", centerURL)
	}
	base := strings.TrimRight(u.Path, "/")
	// The bridge and CLI are configured with either form; accept both rather
	// than making the caller guess, and never double up the prefix.
	if !strings.HasSuffix(base, "/api") {
		base += "/api"
	}
	u.Path = base + "/control/self/proxy-credential"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}
