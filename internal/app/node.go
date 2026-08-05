package app

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"

	"github.com/tim5wang/godex/internal/services/relay"
)

func nodeHelpText() string {
	return strings.Join([]string{
		"Usage:",
		"  godex node <command> [flags]",
		"",
		"Jump-host commands: use the center server as a relay to reach a node's",
		"local network (equivalent to ssh -L).",
		"",
		"Commands:",
		"  forward   Forward a local TCP port to a target on a node's network",
		"",
		"Examples:",
		"  godex node forward --node node_x --local 3306 --target 10.0.0.5:3306",
		"                                 Tunnel a local port to an internal database",
		"",
		"Flags:",
		"  --node <id>        Target node id (required)",
		"  --local <port>     Local listen port (default 3306)",
		"  --target <host:p>  TCP target to dial on the node's network (required)",
		"  --center <url>     Center base URL (default: control.center_url)",
		"  --token <token>    Center web token (default: config web token)",
		"",
		"More help:",
		"  godex node forward --help",
	}, "\n")
}

func (r *Runner) runNodeCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing node subcommand\n\n%s", nodeHelpText())
	}
	switch args[0] {
	case "forward":
		return r.runNodeForward(ctx, args[1:])
	default:
		return fmt.Errorf("unknown node subcommand %q\n\n%s", args[0], nodeHelpText())
	}
}

func (r *Runner) runNodeForward(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("node forward", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var nodeID, localPort, target, centerURL, token string
	fs.StringVar(&nodeID, "node", "", "target node id")
	fs.StringVar(&localPort, "local", "3306", "local listen port")
	fs.StringVar(&target, "target", "", "TCP target host:port on the node's network")
	fs.StringVar(&centerURL, "center", "", "center base URL (default: control.center_url)")
	fs.StringVar(&token, "token", "", "center web token (default: config web token)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if nodeID == "" || target == "" {
		return fmt.Errorf("--node and --target are required\n\n%s", nodeHelpText())
	}
	if centerURL == "" && r.Cfg != nil {
		centerURL = strings.TrimSpace(r.Cfg.Control.CenterURL)
	}
	if centerURL == "" {
		return fmt.Errorf("missing center URL: pass --center or set control.center_url\n\n%s", nodeHelpText())
	}
	if token == "" && r.Cfg != nil {
		token = strings.TrimSpace(r.Cfg.WebToken)
	}

	wsURL, err := forwardWSURL(centerURL, nodeID)
	if err != nil {
		return err
	}
	client, err := relay.DialForward(ctx, wsURL, token)
	if err != nil {
		return fmt.Errorf("connect center forward endpoint: %w", err)
	}
	defer client.Close()

	listenAddr := "127.0.0.1:" + localPort
	ln, err := net.Listen("tcp", listenAddr)
	if err != nil {
		return fmt.Errorf("listen %s: %w", listenAddr, err)
	}
	defer ln.Close()
	fmt.Fprintf(r.Stdout, "forwarding %s -> node %s -> %s (ctrl-c to stop)\n", listenAddr, nodeID, target)

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		go bridgeForwardConn(client, conn, target)
	}
}

// bridgeForwardConn opens one node-side TCP stream per accepted local
// connection and copies bytes in both directions until either side closes.
func bridgeForwardConn(client *relay.ForwardClient, localConn net.Conn, target string) {
	defer localConn.Close()
	stream, err := client.Open(target)
	if err != nil {
		return
	}
	defer stream.Close()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_, _ = io.Copy(stream, localConn)
	}()
	go func() {
		defer wg.Done()
		_, _ = io.Copy(localConn, stream)
	}()
	wg.Wait()
}

// forwardWSURL converts a center base URL (http(s)://host or ws(s)://host)
// into the forward session WebSocket URL for a node. The center serves relay
// endpoints under /api (the webui strips the prefix), so the path mirrors the
// external proxy URL: /api/control/nodes/{id}/forward.
func forwardWSURL(centerURL, nodeID string) (string, error) {
	raw := strings.TrimSpace(centerURL)
	if raw == "" || nodeID == "" {
		return "", fmt.Errorf("empty center URL or node id")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid center URL %q: %w", raw, err)
	}
	switch u.Scheme {
	case "https":
		u.Scheme = "wss"
	case "http":
		u.Scheme = "ws"
	case "ws", "wss":
		// keep as-is
	default:
		return "", fmt.Errorf("unsupported center URL scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/control/nodes/" + nodeID + "/forward"
	return u.String(), nil
}
