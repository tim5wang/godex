package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/idgen"
	"github.com/tim5wang/godex/internal/platform/logger"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
)

// selfJoinRequest is the node-side "join a center" form. The operator only
// supplies the center address and the center's web token; the node registers
// itself with the center, receives a per-node credential, and persists the
// control config (mirroring `godex node join`, but without running a CLI on
// the node — required for Android / container environments).
type selfJoinRequest struct {
	CenterURL  string `json:"center_url"`
	Token      string `json:"token"`
	NodeID     string `json:"node_id,omitempty"`
	Name       string `json:"name,omitempty"`
	TrustLevel string `json:"trust_level,omitempty"`

	// Credential is filled by registerWithCenter (center-issued ck_ value).
	Credential string `json:"-"`
}

// registerSelfJoinRoute wires the automatic join endpoint. Saving the control
// config goes through config.Manager.Update, which triggers the live-apply
// callback (relay agent / heartbeat / bridge hot reload) — no restart needed.
func registerSelfJoinRoute(mux *http.ServeMux, manager *config.Manager, protected func(http.Handler) http.Handler) {
	mux.Handle("POST /control/self/join", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req selfJoinRequest
		if err := decodeJSON(r, &req); err != nil {
			writeError(w, http.StatusBadRequest, err)
			return
		}
		req.CenterURL = strings.TrimRight(strings.TrimSpace(req.CenterURL), "/")
		req.Token = strings.TrimSpace(req.Token)
		req.NodeID = strings.TrimSpace(req.NodeID)
		req.Name = strings.TrimSpace(req.Name)
		req.TrustLevel = strings.TrimSpace(req.TrustLevel)
		if req.CenterURL == "" || req.Token == "" {
			writeError(w, http.StatusBadRequest, fmt.Errorf("center_url and token are required"))
			return
		}
		if req.TrustLevel == "" {
			req.TrustLevel = "trusted"
		}
		if req.NodeID == "" {
			req.NodeID = idgen.New("node-", 4)
		}
		if !validNodeIDToken(req.NodeID) {
			writeError(w, http.StatusBadRequest, fmt.Errorf("invalid node_id %q: use letters, digits, '_' or '-'", req.NodeID))
			return
		}
		// Audit: node self-registration attempt (P0-2).
		logger.Infof("audit self/join: caller=%s node=%s center=%s", r.RemoteAddr, req.NodeID, req.CenterURL)

		// 1. Register with the center (web token auth), 2. get the per-node
		// credential, 3. exchange the web token for the center's restricted node
		// proxy credential (nk_...) so the node never persists the full web token,
		// 4. persist local config, 5. sync node.json.
		if err := registerWithCenter(r.Context(), &req); err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		var proxyToken string
		proxyToken, err := exchangeSelfProxyToken(r.Context(), req.CenterURL, req.Token)
		if err != nil {
			writeError(w, http.StatusBadGateway, err)
			return
		}
		cfg := manager.Current()
		if err := manager.WriteHomeEnvVar("GODEX_CONTROL_CREDENTIAL", req.Credential); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("write credential env: %w", err))
			return
		}
		if err := manager.WriteHomeEnvVar("GODEX_CONTROL_CENTER_TOKEN", proxyToken); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("write center token env: %w", err))
			return
		}
		values := map[string]any{
			"control.center_url":  req.CenterURL,
			"control.node_id":     req.NodeID,
			"control.trust_level": req.TrustLevel,
		}
		if req.Name != "" {
			values["control.node_name"] = req.Name
		}
		if _, err := manager.Update(r.Context(), config.UpdateRequest{Values: values}); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("write control config: %w", err))
			return
		}
		if cfg != nil && strings.TrimSpace(cfg.StateDir) != "" {
			if _, err := noderegistry.EnsureNodeID(cfg.StateDir, req.NodeID); err != nil {
				writeError(w, http.StatusInternalServerError, fmt.Errorf("sync node id file: %w", err))
				return
			}
		}
		writeJSON(w, http.StatusOK, map[string]any{
			"node_id":    req.NodeID,
			"credential": maskSecret(req.Credential),
			"message":    fmt.Sprintf("node %q joined %s (trust=%s)", req.NodeID, req.CenterURL, req.TrustLevel),
		})
	})))
}

// exchangeSelfProxyToken exchanges the center's full web token for its
// restricted node proxy credential (nk_...) via the center's
// POST /control/node-proxy-token endpoint. The returned nk_ is what the node
// persists as control.center_token, so the node never stores the full web token.
func exchangeSelfProxyToken(ctx context.Context, centerURL, webToken string) (string, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	var out struct {
		NodeProxyToken string `json:"node_proxy_token"`
	}
	if err := doCenterJSON(ctx, client, centerURL, "POST", "/control/node-proxy-token", webToken, nil, &out); err != nil {
		return "", err
	}
	if out.NodeProxyToken == "" {
		return "", fmt.Errorf("center returned empty node proxy token")
	}
	return out.NodeProxyToken, nil
}

// registerWithCenter registers the node and issues its per-node credential.
func registerWithCenter(ctx context.Context, req *selfJoinRequest) error {
	client := &http.Client{Timeout: 15 * time.Second}
	regBody, err := json.Marshal(noderegistry.NodeInput{
		ID:         req.NodeID,
		Name:       req.Name,
		TrustLevel: req.TrustLevel,
	})
	if err != nil {
		return err
	}
	if err := doCenterJSON(ctx, client, req.CenterURL, "POST", "/control/nodes/register", req.Token, regBody, nil); err != nil {
		return fmt.Errorf("center register: %w", err)
	}
	var credResp struct {
		NodeID     string `json:"node_id"`
		Credential string `json:"credential"`
	}
	if err := doCenterJSON(ctx, client, req.CenterURL, "POST", "/control/nodes/"+req.NodeID+"/credential", req.Token, nil, &credResp); err != nil {
		return fmt.Errorf("center credential: %w", err)
	}
	if credResp.Credential == "" {
		return fmt.Errorf("center returned empty credential")
	}
	req.Credential = credResp.Credential
	return nil
}

// registerNodeProxyTokenRoute wires the center-side restricted credential
// issuance endpoint. A joined node calls this with the full web token to obtain
// the restricted nk_ credential it then stores as control.center_token; the nk_
// only unlocks the node proxy/forward READ+PROXY surface, never the config /
// sessions / files management API.
func registerNodeProxyTokenRoute(mux *http.ServeMux, manager *config.Manager, protected func(http.Handler) http.Handler) {
	mux.Handle("POST /control/node-proxy-token", protected(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		current := strings.TrimSpace(manager.Current().Control.NodeProxyToken)
		if current != "" {
			writeJSON(w, http.StatusOK, map[string]string{"node_proxy_token": current})
			return
		}
		proxyToken, err := relay.GenerateNodeProxyToken()
		if err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("generate node proxy token: %w", err))
			return
		}
		if _, err := manager.Update(r.Context(), config.UpdateRequest{Values: map[string]any{
			"control.node_proxy_token": proxyToken,
		}}); err != nil {
			writeError(w, http.StatusInternalServerError, fmt.Errorf("persist node proxy token: %w", err))
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"node_proxy_token": proxyToken})
	})))
}

func doCenterJSON(ctx context.Context, client *http.Client, centerURL, method, path, token string, body []byte, out any) error {
	base := strings.TrimRight(strings.TrimSpace(centerURL), "/")
	if strings.HasSuffix(base, "/api") {
		base = strings.TrimSuffix(base, "/api")
	}
	req, err := http.NewRequestWithContext(ctx, method, base+"/api"+path, bytes.NewReader(body))
	if err != nil {
		return err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("%s: %s", resp.Status, strings.TrimSpace(string(data)))
	}
	if out != nil {
		return json.NewDecoder(resp.Body).Decode(out)
	}
	return nil
}

// validNodeIDToken mirrors the CLI's node id rule (letters/digits/_/-).
func validNodeIDToken(id string) bool {
	if id == "" {
		return false
	}
	for _, r := range id {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
		default:
			return false
		}
	}
	return true
}

// maskSecret shows only the credential prefix for display.
func maskSecret(secret string) string {
	if len(secret) <= 8 {
		return "****"
	}
	return secret[:4] + "…" + secret[len(secret)-4:]
}
