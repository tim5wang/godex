package main

import (
	"context"
	"net/http"
	"slices"

	"github.com/tim5wang/godex/internal/app"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/runtime/httpapi"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/nodeobs"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
	"github.com/tim5wang/godex/internal/version"
)

// controlRuntime owns the node-side relay components whose lifecycle depends on
// the control section of the config (center_url / credential / center_token /
// node_id / forward_allow). It is built once inside Serve with the long-lived
// serve context, then reconciles on every config save so join / leave / center
// switch take effect WITHOUT restarting godex serve (required for Android /
// container environments where running the CLI is impractical).
type controlRuntime struct {
	ctx           context.Context // serve context (long-lived)
	apiHandler    http.Handler
	service       *backend.Service
	selfEndpoint  string
	bridge        *httpapi.CenterBridge
	forwardServer *relay.ForwardServer

	heartbeat app.LifecycleService
	agent     *relay.Agent
	observer  *relay.Observer
}

// Reconcile diffs the old and new control configs and stops/starts the relay
// components accordingly. It is safe to call with old == new (e.g. initial
// boot): the "changed" check also covers the wanted-but-missing case so the
// first join starts everything.
func (r *controlRuntime) Reconcile(oldCfg, newCfg *config.Config) {
	if r == nil {
		return
	}
	// forward_allow is a per-request data field on the agent: hot-reload it
	// directly, no restart needed.
	if r.agent != nil && !slices.Equal(oldCfg.Control.ForwardAllow, newCfg.Control.ForwardAllow) {
		r.agent.SetForwardAllow(newCfg.Control.ForwardAllow)
	}
	changed := controlConfigChanged(oldCfg, newCfg)
	wanted := newCfg.Control.CenterURL != "" && newCfg.Control.Credential != ""
	if !changed && !(wanted && r.agent == nil) {
		return
	}

	// Stop the old generation (if any).
	if r.observer != nil {
		_ = r.observer.Stop(r.ctx)
	}
	if r.agent != nil {
		_ = r.agent.Stop(r.ctx)
	}
	if r.heartbeat != nil {
		_ = r.heartbeat.Stop(r.ctx)
	}
	r.observer, r.agent, r.heartbeat = nil, nil, nil

	// Point the bridge / forward server at the (possibly new) center.
	if r.bridge != nil {
		r.bridge.SetEndpoint(newCfg.Control.CenterURL, newCfg.Control.CenterToken)
	}
	if r.forwardServer != nil {
		r.forwardServer.SetCenterBridge(newCfg.Control.CenterURL, newCfg.Control.CenterToken)
	}

	// Start the new generation per the new config.
	selfNode, err := noderegistry.SelfNodeWithVersion(newCfg, r.selfEndpoint, version.Current().Version)
	if err != nil {
		return
	}
	if remote := remoteControlHeartbeat(newCfg, selfNode, r.selfEndpoint); remote != nil {
		_ = remote.Start(r.ctx)
		r.heartbeat = remote
	}
	if agent := remoteRelayAgent(newCfg, selfNode, r.apiHandler); agent != nil {
		_ = agent.Start(r.ctx)
		r.agent = agent
		provider := nodeobs.NewProvider(r.service, selfNode.Version, selfNode.Capabilities)
		obs := relay.NewObserver(agent, provider, 0)
		_ = obs.Start(r.ctx)
		r.observer = obs
	}
}

// controlConfigChanged reports whether any join-relevant control field changed.
func controlConfigChanged(oldCfg, newCfg *config.Config) bool {
	if oldCfg == nil || newCfg == nil {
		return true
	}
	o, n := oldCfg.Control, newCfg.Control
	return o.CenterURL != n.CenterURL ||
		o.Credential != n.Credential ||
		o.CenterToken != n.CenterToken ||
		o.NodeID != n.NodeID ||
		o.NodeName != n.NodeName ||
		o.TrustLevel != n.TrustLevel
}
