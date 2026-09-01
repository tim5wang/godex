package tools

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"time"

	"github.com/go-rod/rod"
	"github.com/go-rod/rod/lib/cdp"
	"github.com/gorilla/websocket"
)

// CDPDialer establishes a raw TCP connection to a CDP endpoint on a registered
// node over the relay channel. The center injects an implementation backed by
// the relay hub (OpenTCPStream); nodes that launch their own browser do not
// need one.
type CDPDialer func(ctx context.Context, nodeID, target string) (net.Conn, error)

// relayNetConn adapts an io.ReadWriteCloser (a relay TCP stream) into a
// net.Conn so gorilla/websocket can run the CDP WebSocket handshake over it.
// Deadlines are intentionally no-ops: the relay channel is governed by the
// hub's own liveness handling, not per-connection deadlines.
type relayNetConn struct {
	io.ReadWriteCloser
	local  net.Addr
	remote net.Addr
}

func (c *relayNetConn) LocalAddr() net.Addr              { return c.local }
func (c *relayNetConn) RemoteAddr() net.Addr             { return c.remote }
func (c *relayNetConn) SetDeadline(time.Time) error      { return nil }
func (c *relayNetConn) SetReadDeadline(time.Time) error  { return nil }
func (c *relayNetConn) SetWriteDeadline(time.Time) error { return nil }

// gorillaWSAdapter wraps a gorilla/websocket connection as the transport rod's
// cdp.Client expects (text messages only).
type gorillaWSAdapter struct {
	conn *websocket.Conn
}

func (a *gorillaWSAdapter) Send(data []byte) error {
	return a.conn.WriteMessage(websocket.TextMessage, data)
}

func (a *gorillaWSAdapter) Read() ([]byte, error) {
	_, data, err := a.conn.ReadMessage()
	return data, err
}

// startRelayCDP connects this browser service to the Chromium CDP endpoint
// exposed on a remote node over the relay channel (distributed browser
// runtime). The node must run its own browser with tools.browser.cdp_listen
// set so its CDP port is fixed and reachable via the relay.
func (s *BrowserService) startRelayCDP(ctx context.Context) (*rod.Browser, error) {
	s.mu.Lock()
	cfg := s.cfg
	dialer := s.cdpDialer
	s.mu.Unlock()
	if dialer == nil {
		return nil, fmt.Errorf("cdp_relay_node is set but no relay CDP dialer is installed (center relay wiring missing)")
	}
	nodeID := strings.TrimSpace(cfg.CDPRelayNode)
	target := strings.TrimSpace(cfg.CDPRelayTarget)
	if target == "" {
		target = "127.0.0.1:9222"
	}
	// A ws:// scheme prefix is tolerated so cdp_relay_target can mirror the
	// usual CDP URL shape; the host:port is what we dial.
	target = strings.TrimPrefix(target, "ws://")
	target = strings.TrimPrefix(target, "wss://")

	relayCtx, cancel := context.WithTimeout(ctx, cfgTimeout(cfg.ActionTimeoutSeconds))
	defer cancel()
	conn, err := dialer(relayCtx, nodeID, target)
	if err != nil {
		return nil, fmt.Errorf("dial relay CDP node %q target %s: %w", nodeID, target, err)
	}

	wsURL, err := url.Parse("ws://" + target)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("invalid relay CDP target %q: %w", target, err)
	}
	wsConn, _, err := websocket.NewClient(conn, wsURL, nil, 0, 0)
	if err != nil {
		_ = conn.Close()
		return nil, fmt.Errorf("cdp websocket handshake to node %q: %w", nodeID, err)
	}

	cdpClient := cdp.New().Start(&gorillaWSAdapter{conn: wsConn})
	return rod.New().Client(cdpClient), nil
}
