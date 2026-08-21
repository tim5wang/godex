package app

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/llm"
	"github.com/tim5wang/godex/internal/services/noderegistry"
	"github.com/tim5wang/godex/internal/services/relay"
	"github.com/tim5wang/godex/internal/services/usage"
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
		"  exec      Run a shell command on a node and stream its output",
		"  join      Configure this node to join a center (one-command onboarding)",
		"",
		"Examples:",
		"  godex node forward --node node_x --local 3306 --target 10.0.0.5:3306",
		"                                 Tunnel a local port to an internal database",
		"  godex node exec --node node_x 'cd ~/proj && go test ./...'",
		"                                 Run a command on a remote node",
		"  godex node join https://godex.claw.carc.top --id my-laptop --credential ck_xxx",
		"                                 Configure this node to join a center",
		"",
		"Flags (join):",
		"  --id <id>          Node id to register under (required)",
		"  --credential <ck>  Center-issued node credential, ck_... (required)",
		"  --trust <level>    trusted | guarded-remote (default trusted)",
		"  --name <name>      Human-readable node name",
		"  --llm-proxy [key]  Also write a local provider that routes LLM calls",
		"                     through this center's usage gateway. Pass an existing",
		"                     gdx_ key to use it, or omit the value (or pass 'auto')",
		"                     to create one via the center's usage API; the latter",
		"                     needs --token (center web token).",
		"  --llm-models <ids> Comma-separated model ids for the LLM proxy provider",
		"                     (default: fetch the center's /v1/models list)",
		"  --token <token>    Center web token (needed for --llm-proxy auto)",
		"",
		"Flags (forward):",
		"  --node <id>        Target node id (default: control.default_node)",
		"  --local <port>     Local listen port (default 3306)",
		"  --target <host:p>  TCP target to dial on the node's network (required)",
		"  --center <url>     Center base URL (default: control.center_url)",
		"  --token <token>    Center web token (default: config web token)",
		"",
		"Flags (exec):",
		"  --node <id>        Target node id (default: control.default_node)",
		"  --dir <path>       Node-side working directory (default: node workspace)",
		"  --center <url>     Center base URL (default: control.center_url)",
		"  --token <token>    Center web token (default: config web token)",
		"",
		"More help:",
		"  godex node forward --help",
		"  godex node exec --help",
	}, "\n")
}

func (r *Runner) runNodeCommand(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("missing node subcommand\n\n%s", nodeHelpText())
	}
	switch args[0] {
	case "forward":
		return r.runNodeForward(ctx, args[1:])
	case "exec":
		return r.runNodeExec(ctx, args[1:])
	case "join":
		return r.runNodeJoin(ctx, args[1:])
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
	if nodeID == "" && r.Cfg != nil {
		nodeID = strings.TrimSpace(r.Cfg.Control.DefaultNode)
	}
	if nodeID == "" || target == "" {
		return fmt.Errorf("--node and --target are required (or set control.default_node)\n\n%s", nodeHelpText())
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

// runNodeExec runs a shell command on a remote node through the center's relay
// proxy. The command's output is streamed to stdout in real time (SSE), and a
// non-zero exit code is surfaced as an error.
func (r *Runner) runNodeExec(ctx context.Context, args []string) error {
	fs := flag.NewFlagSet("node exec", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var nodeID, centerURL, token, workspaceDir string
	fs.StringVar(&nodeID, "node", "", "target node id")
	fs.StringVar(&centerURL, "center", "", "center base URL (default: control.center_url)")
	fs.StringVar(&token, "token", "", "center web token (default: config web token)")
	fs.StringVar(&workspaceDir, "dir", "", "node-side working directory (default: node workspace)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if nodeID == "" && r.Cfg != nil {
		nodeID = strings.TrimSpace(r.Cfg.Control.DefaultNode)
	}
	if nodeID == "" {
		return fmt.Errorf("--node is required (or set control.default_node)\n\n%s", nodeHelpText())
	}
	command := strings.TrimSpace(strings.Join(fs.Args(), " "))
	if command == "" {
		return fmt.Errorf("missing command\n\n%s", nodeHelpText())
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

	target, err := execURL(centerURL, nodeID)
	if err != nil {
		return err
	}
	body, err := json.Marshal(map[string]string{
		"command":       command,
		"workspace_dir": workspaceDir,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("exec via center: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return fmt.Errorf("node exec failed (status %d): %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}

	// Stream SSE events: each data line is {output, final, exit_code}. The
	// node emits cumulative output snapshots, so only print the delta since
	// the previous event to avoid repeating already-shown text.
	exitCode := 0
	printed := 0
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev struct {
			Output   string `json:"output"`
			Final    bool   `json:"final"`
			ExitCode int    `json:"exit_code"`
		}
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			continue
		}
		if len(ev.Output) > printed {
			_, _ = io.WriteString(r.Stdout, ev.Output[printed:])
			printed = len(ev.Output)
		}
		if ev.Final {
			exitCode = ev.ExitCode
			break
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read exec stream: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("command exited with code %d", exitCode)
	}
	return nil
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

// execURL builds the center-side proxy URL that forwards POST /v1/exec to the
// target node (external path with the /api prefix that the webui strips).
func execURL(centerURL, nodeID string) (string, error) {
	raw := strings.TrimSpace(centerURL)
	if raw == "" || nodeID == "" {
		return "", fmt.Errorf("empty center URL or node id")
	}
	u, err := url.Parse(raw)
	if err != nil {
		return "", fmt.Errorf("invalid center URL %q: %w", raw, err)
	}
	switch u.Scheme {
	case "https", "http":
		// keep as-is
	default:
		return "", fmt.Errorf("unsupported center URL scheme %q", u.Scheme)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/control/nodes/" + nodeID + "/proxy/v1/exec"
	return u.String(), nil
}

// runNodeJoin configures this node to join a center: it validates the one-line
// onboarding arguments and writes center_url / credential / node_id (plus the
// optional trust level and name) into the control section of the config.
func (r *Runner) runNodeJoin(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("node join requires a center URL argument\n\n%s", nodeHelpText())
	}
	// The center URL is the first positional argument; Go's flag package stops
	// parsing at the first non-flag argument, so extract it before flag.Parse.
	centerURL := strings.TrimSpace(args[0])
	// --llm-proxy takes an optional value (a gdx_ key). A bare --llm-proxy
	// (followed by another flag or nothing) means "auto-create the key", so
	// normalize it to --llm-proxy=auto before flag.Parse.
	args = normalizeLLMProxyFlag(args)
	fs := flag.NewFlagSet("node join", flag.ContinueOnError)
	fs.SetOutput(r.Stderr)
	var nodeID, credential, trustLevel, name, llmProxy, llmModels, token string
	fs.StringVar(&nodeID, "id", "", "node id to register under (required)")
	fs.StringVar(&credential, "credential", "", "center-issued node credential, ck_... (required)")
	fs.StringVar(&trustLevel, "trust", "trusted", "trust level: trusted | guarded-remote")
	fs.StringVar(&name, "name", "", "human-readable node name")
	fs.StringVar(&llmProxy, "llm-proxy", "", "gdx_ key to use for the LLM proxy provider, or 'auto' to create one via the center (requires --token)")
	fs.StringVar(&llmModels, "llm-models", "", "comma-separated model ids for the LLM proxy provider (default: fetch the center's /v1/models)")
	fs.StringVar(&token, "token", "", "center web token (needed for --llm-proxy auto)")
	if err := fs.Parse(args[1:]); err != nil {
		return err
	}
	if fs.NArg() > 0 {
		return fmt.Errorf("unexpected arguments: %s\n\n%s", strings.Join(fs.Args(), " "), nodeHelpText())
	}
	nodeID = strings.TrimSpace(nodeID)
	credential = strings.TrimSpace(credential)
	trustLevel = strings.TrimSpace(trustLevel)
	name = strings.TrimSpace(name)
	llmProxy = strings.TrimSpace(llmProxy)
	llmModels = strings.TrimSpace(llmModels)
	token = strings.TrimSpace(token)

	if err := validateJoinArgs(centerURL, nodeID, credential, trustLevel); err != nil {
		return err
	}
	if r.ConfigManager == nil {
		return fmt.Errorf("config manager unavailable: cannot write control config")
	}
	// The credential is a secret: it is persisted to the home .env file
	// (GODEX_CONTROL_CREDENTIAL) rather than rendered into godex.yaml.
	if err := r.ConfigManager.WriteHomeEnvVar("GODEX_CONTROL_CREDENTIAL", credential); err != nil {
		return fmt.Errorf("write credential env: %w", err)
	}
	values := map[string]any{
		"control.center_url":  centerURL,
		"control.node_id":     nodeID,
		"control.trust_level": trustLevel,
	}
	if name != "" {
		values["control.node_name"] = name
	}
	if _, err := r.ConfigManager.Update(ctx, config.UpdateRequest{Values: values}); err != nil {
		return fmt.Errorf("write control config: %w", err)
	}
	// Sync state/node.json so the node registers under the operator-specified
	// id instead of a stale auto-generated one. EnsureNodeID prefers an
	// explicit id and persists it.
	if r.Cfg != nil && strings.TrimSpace(r.Cfg.StateDir) != "" {
		if _, err := noderegistry.EnsureNodeID(r.Cfg.StateDir, nodeID); err != nil {
			return fmt.Errorf("sync node id file: %w", err)
		}
	}
	if llmProxy != "" {
		if err := r.configureLLMProxy(ctx, centerURL, nodeID, llmProxy, llmModels, token); err != nil {
			return err
		}
	}
	fmt.Fprintf(r.Stdout, "node %q configured to join %s (trust=%s)\n", nodeID, centerURL, trustLevel)
	if llmProxy != "" {
		baseURL, _ := usageGatewayBaseURL(centerURL)
		fmt.Fprintf(r.Stdout, "llm proxy provider %q written (base_url %s, key in %s)\n", llmProxyProviderID, baseURL, llmProxyKeyEnv)
	}
	fmt.Fprintln(r.Stdout, "restart 'godex serve' to complete the join")
	return nil
}

// normalizeLLMProxyFlag rewrites a bare --llm-proxy (whose next token is
// another flag or absent) into --llm-proxy=auto so the flag package accepts
// the optional-value form.
func normalizeLLMProxyFlag(args []string) []string {
	out := make([]string, 0, len(args))
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--llm-proxy" {
			next := ""
			if i+1 < len(args) {
				next = args[i+1]
			}
			if next == "" || strings.HasPrefix(next, "-") {
				a = "--llm-proxy=auto"
			}
		}
		out = append(out, a)
	}
	return out
}

const (
	// llmProxyProviderID is the provider id written by node join --llm-proxy.
	llmProxyProviderID = "center-llm"
	// llmProxyKeyEnv is the home .env variable holding the gdx_ key. The key
	// itself never lands in godex.yaml (mirroring GODEX_CONTROL_CREDENTIAL).
	llmProxyKeyEnv = "GODEX_CENTER_LLM_KEY"
)

// configureLLMProxy writes a local openai_compatible provider that routes LLM
// calls through the center's usage gateway. keyOrAuto is either an existing
// gdx_ key or "auto", in which case a new key is created via the center's
// usage API using the web token.
func (r *Runner) configureLLMProxy(ctx context.Context, centerURL, nodeID, keyOrAuto, llmModels, token string) error {
	var gdxKey string
	if keyOrAuto == "auto" {
		if token == "" {
			return fmt.Errorf("--llm-proxy auto requires --token (center web token) to create a usage key")
		}
		var models []string
		for _, m := range splitCSV(llmModels) {
			models = append(models, strings.TrimSpace(m))
		}
		created, err := createCenterUsageKey(ctx, centerURL, token, "node-"+nodeID, models)
		if err != nil {
			return fmt.Errorf("create center usage key: %w", err)
		}
		gdxKey = created
		fmt.Fprintf(r.Stdout, "created usage key %s for node %q\n", maskSecret(gdxKey), nodeID)
	} else {
		if !strings.HasPrefix(keyOrAuto, usage.KeyPrefix) {
			return fmt.Errorf("invalid --llm-proxy value %q: expected a gdx_ key or 'auto'", keyOrAuto)
		}
		gdxKey = keyOrAuto
	}
	models := splitCSV(llmModels)
	if len(models) == 0 {
		fetched, err := fetchCenterModels(ctx, centerURL, gdxKey)
		if err != nil {
			return fmt.Errorf("fetch center models: %w (pass --llm-models to list them explicitly)", err)
		}
		models = fetched
	}
	if len(models) == 0 {
		return fmt.Errorf("no models available for the LLM proxy provider (pass --llm-models m1,m2)")
	}
	baseURL, err := usageGatewayBaseURL(centerURL)
	if err != nil {
		return err
	}
	modelMap := make(map[string]llm.ModelConfig, len(models))
	for _, m := range models {
		m = strings.TrimSpace(m)
		if m == "" {
			continue
		}
		modelMap[m] = llm.ModelConfig{ID: m, Model: m, Name: m, SupportsStreaming: true}
	}
	providers, err := r.existingProviders()
	if err != nil {
		return err
	}
	providers[llmProxyProviderID] = llm.ProviderConfig{
		Name:      "Center LLM Proxy",
		Type:      llm.ProviderOpenAICompatible,
		BaseURL:   baseURL,
		APIKeyEnv: llmProxyKeyEnv,
		Models:    modelMap,
	}
	if err := r.ConfigManager.UpdateProviders(providers); err != nil {
		return fmt.Errorf("write llm proxy provider: %w", err)
	}
	if err := r.ConfigManager.WriteHomeEnvVar(llmProxyKeyEnv, gdxKey); err != nil {
		return fmt.Errorf("write llm proxy key env: %w", err)
	}
	return nil
}

// existingProviders returns the current stored provider set (plaintext keys
// included) so the LLM proxy provider can be merged without clobbering the
// node's existing providers.
func (r *Runner) existingProviders() (map[string]llm.ProviderConfig, error) {
	raw, err := r.ConfigManager.Reveal("api.providers")
	if err != nil {
		return nil, fmt.Errorf("read existing providers: %w", err)
	}
	providers := map[string]llm.ProviderConfig{}
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return providers, nil
	}
	if err := json.Unmarshal([]byte(raw), &providers); err != nil {
		return nil, fmt.Errorf("parse existing providers: %w", err)
	}
	if providers == nil {
		providers = map[string]llm.ProviderConfig{}
	}
	return providers, nil
}

// splitCSV splits a comma-separated list, dropping empties.
func splitCSV(raw string) []string {
	var out []string
	for _, part := range strings.Split(raw, ",") {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

// usageGatewayBaseURL builds the center-side OpenAI-compatible base URL. The
// webui serves the API under /api (and strips the prefix), so the external
// path is <center>/api/v1.
func usageGatewayBaseURL(centerURL string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(centerURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("invalid center URL %q", centerURL)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1"
	u.RawQuery = ""
	u.Fragment = ""
	return u.String(), nil
}

// createCenterUsageKey creates a usage gateway key on the center via
// POST /api/usage/keys and returns the plaintext secret.
func createCenterUsageKey(ctx context.Context, centerURL, token, name string, models []string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(centerURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return "", fmt.Errorf("invalid center URL %q", centerURL)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/usage/keys"
	u.RawQuery = ""
	u.Fragment = ""
	body, err := json.Marshal(usage.KeyCreateRequest{Name: name, AllowedModels: models})
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, u.String(), bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusCreated && resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", fmt.Errorf("center returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var created usage.KeyCreateResponse
	if err := json.NewDecoder(resp.Body).Decode(&created); err != nil {
		return "", fmt.Errorf("decode center response: %w", err)
	}
	if !strings.HasPrefix(created.Secret, usage.KeyPrefix) {
		return "", fmt.Errorf("center returned an invalid key (missing %s prefix)", usage.KeyPrefix)
	}
	return created.Secret, nil
}

// fetchCenterModels lists the center's available public models via
// GET /api/v1/models (authenticated with the gdx_ key).
func fetchCenterModels(ctx context.Context, centerURL, gdxKey string) ([]string, error) {
	u, err := url.Parse(strings.TrimSpace(centerURL))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return nil, fmt.Errorf("invalid center URL %q", centerURL)
	}
	u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/models"
	u.RawQuery = ""
	u.Fragment = ""
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+gdxKey)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("center returned status %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&list); err != nil {
		return nil, fmt.Errorf("decode center models: %w", err)
	}
	var ids []string
	for _, m := range list.Data {
		if m.ID = strings.TrimSpace(m.ID); m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// maskSecret shows only the first few characters of a secret.
func maskSecret(secret string) string {
	if len(secret) <= 4 {
		return "****"
	}
	return secret[:4] + "..." + secret[len(secret)-4:]
}

func validateJoinArgs(centerURL, nodeID, credential, trustLevel string) error {
	if centerURL == "" {
		return fmt.Errorf("missing center URL\n\n%s", nodeHelpText())
	}
	u, err := url.Parse(centerURL)
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" {
		return fmt.Errorf("invalid center URL %q: expected http(s) URL", centerURL)
	}
	if nodeID == "" {
		return fmt.Errorf("--id is required\n\n%s", nodeHelpText())
	}
	if !validNodeID(nodeID) {
		return fmt.Errorf("invalid node id %q: only letters, digits, '_' and '-' are allowed", nodeID)
	}
	if credential == "" {
		return fmt.Errorf("--credential is required\n\n%s", nodeHelpText())
	}
	if !strings.HasPrefix(credential, "ck_") {
		return fmt.Errorf("invalid credential: expected ck_ prefix")
	}
	switch trustLevel {
	case "trusted", "guarded-remote":
	default:
		return fmt.Errorf("invalid trust level %q: expected trusted or guarded-remote", trustLevel)
	}
	return nil
}

func validNodeID(id string) bool {
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
