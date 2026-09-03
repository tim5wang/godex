package server

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

	"github.com/tim5wang/godex/internal/platform/idgen"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
)

// PromptHandler runs one ACP prompt turn for a session.
type PromptHandler func(ctx context.Context, turn PromptTurn) (PromptResult, error)

// PromptTurn is the adapter input handed to a PromptHandler.
type PromptTurn struct {
	SessionID           string
	CWD                 string
	Prompt              string
	Updater             SessionUpdater
	PermissionRequester PermissionRequester
	// McpServers are the MCP servers the client attached to this session via
	// session/new, session/load or session/resume. They are forwarded so the
	// backend prompt handler can record/audit them (godex does not spawn
	// client-proposed MCP servers itself).
	McpServers []acp.McpServer
}

// PromptResult is the adapter output returned from a PromptHandler.
type PromptResult struct {
	FinalText  string
	StopReason acp.StopReason
	Streamed   bool
}

// SessionUpdater streams session updates back to the ACP peer. A nil Updater
// silently discards updates so PromptHandlers work both with and without a
// live connection (tests vs. live serve).
type SessionUpdater interface {
	Update(ctx context.Context, update acp.SessionUpdate) error
}

// PermissionRequester requests an ACP-native permission decision from the peer.
type PermissionRequester interface {
	RequestPermission(context.Context, acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error)
}

// Agent implements acp.Agent for godex. The zero value is a usable skeleton that
// accepts prompts and echoes them back through EchoPromptHandler.
type Agent struct {
	// AgentInfo advertises godex to the peer during initialize.
	AgentInfo acp.Implementation
	// Handler processes one prompt turn. If nil, EchoPromptHandler is used.
	Handler PromptHandler
	// Features supplies optional session model/command metadata.
	Features SessionFeatureProvider

	mu       sync.Mutex
	conn     *acp.AgentSideConnection
	sessions map[string]*sessionState
}

type sessionState struct {
	cwd              string
	backendSessionID string
	commandsSent     bool
	cancel           context.CancelFunc
	// mode is the ACP session mode ("" = default, "minimal" = lean). It is
	// applied to the backend session on set_mode and used to build the Modes
	// state returned on session lifecycle responses.
	mode string
	// mcpServers records the MCP servers the client asked this session to
	// connect to (session/new, session/load, session/resume). godex does not
	// spawn them itself; they are forwarded to the backend prompt handler so
	// the session can record/audit them and optionally bridge them later.
	mcpServers []acp.McpServer
}

var _ acp.Agent = (*Agent)(nil)

// SetAgentConnection wires the agent-side connection after construction so
// Prompt handlers can stream updates. Called from Serve; tests may skip it.
func (a *Agent) SetAgentConnection(conn *acp.AgentSideConnection) {
	a.mu.Lock()
	a.conn = conn
	a.mu.Unlock()
}

func (a *Agent) info() acp.Implementation {
	if strings.TrimSpace(a.AgentInfo.Name) != "" {
		return a.AgentInfo
	}
	return acp.Implementation{Name: "godex", Version: "dev"}
}

func (a *Agent) handler() PromptHandler {
	a.mu.Lock()
	h := a.Handler
	a.mu.Unlock()
	if h == nil {
		return EchoPromptHandler
	}
	return h
}

// Authenticate always succeeds; godex does not require ACP-level authentication.
func (a *Agent) Authenticate(context.Context, acp.AuthenticateRequest) (acp.AuthenticateResponse, error) {
	return acp.AuthenticateResponse{}, nil
}

// Initialize advertises godex's baseline capabilities.
func (a *Agent) Initialize(context.Context, acp.InitializeRequest) (acp.InitializeResponse, error) {
	info := a.info()
	return acp.InitializeResponse{
		ProtocolVersion: acp.ProtocolVersion(acp.ProtocolVersionNumber),
		AgentInfo:       &info,
		AgentCapabilities: acp.AgentCapabilities{
			LoadSession: true,
			SessionCapabilities: acp.SessionCapabilities{
				Close:  &acp.SessionCloseCapabilities{},
				List:   &acp.SessionListCapabilities{},
				Resume: &acp.SessionResumeCapabilities{},
			},
			PromptCapabilities: acp.PromptCapabilities{
				EmbeddedContext: true,
			},
		},
	}, nil
}

// NewSession registers a new session and returns its identifier.
func (a *Agent) NewSession(ctx context.Context, params acp.NewSessionRequest) (acp.NewSessionResponse, error) {
	sid := randomSessionID()
	a.mu.Lock()
	if a.sessions == nil {
		a.sessions = make(map[string]*sessionState)
	}
	a.sessions[sid] = &sessionState{cwd: params.Cwd, mcpServers: cloneMcpServers(params.McpServers)}
	a.mu.Unlock()
	resp := acp.NewSessionResponse{SessionId: acp.SessionId(sid)}
	if view, ok := a.modelView(ctx, sid); ok {
		resp.ConfigOptions = acpModelConfigOptions(view)
	}
	resp.Modes = a.sessionModes(ctx, sid)
	a.scheduleAvailableCommands(acp.SessionId(sid))
	return resp, nil
}

// Prompt bridges a prompt request through the configured handler and, on
// success, streams the final reply as a single AgentMessageText update. The
// handler is invoked with a cancelable context registered on the session so
// an inbound ACP Cancel notification can abort the in-flight turn.
func (a *Agent) Prompt(ctx context.Context, params acp.PromptRequest) (acp.PromptResponse, error) {
	sid := string(params.SessionId)

	promptCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	a.mu.Lock()
	state, ok := a.sessions[sid]
	if ok {
		state.cancel = cancel
	}
	a.mu.Unlock()
	if !ok {
		return acp.PromptResponse{}, fmt.Errorf("session %s not found", sid)
	}
	_ = a.sendAvailableCommands(promptCtx, acp.SessionId(sid), true)

	defer func() {
		a.mu.Lock()
		state.cancel = nil
		a.mu.Unlock()
	}()

	updater := a.updater(acp.SessionId(sid))
	turn := PromptTurn{
		SessionID:           sid,
		CWD:                 state.cwd,
		Prompt:              extractPromptText(params.Prompt, state.cwd),
		Updater:             updater,
		PermissionRequester: a.permissionRequester(acp.SessionId(sid)),
		McpServers:          cloneMcpServers(state.mcpServers),
	}
	result, err := a.handler()(promptCtx, turn)
	if promptCtx.Err() != nil {
		return acp.PromptResponse{StopReason: acp.StopReasonCancelled}, nil
	}
	if err != nil {
		return acp.PromptResponse{}, err
	}
	if !result.Streamed && strings.TrimSpace(result.FinalText) != "" && updater != nil {
		if err := updater.Update(ctx, acp.UpdateAgentMessageText(result.FinalText)); err != nil {
			return acp.PromptResponse{}, fmt.Errorf("session update: %w", err)
		}
	}
	stop := result.StopReason
	if strings.TrimSpace(string(stop)) == "" {
		stop = acp.StopReasonEndTurn
	}
	return acp.PromptResponse{StopReason: stop}, nil
}

// ListSessions returns the ACP sessions tracked by this agent: the live
// in-memory sessions plus, when the backend supports listing, the persisted
// ACP-channel sessions (so a restarted agent still reports its sessions).
// In-memory entries win on session-id collision (they carry the live cwd).
func (a *Agent) ListSessions(ctx context.Context, _ acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	byID := make(map[string]acp.SessionInfo)
	a.mu.Lock()
	for sid, st := range a.sessions {
		byID[sid] = acp.SessionInfo{
			SessionId: acp.SessionId(sid),
			Cwd:       st.cwd,
		}
	}
	a.mu.Unlock()
	if provider, ok := a.Features.(sessionListFeatureProvider); ok {
		if persisted, err := provider.ListSessions(ctx); err == nil {
			for _, info := range persisted {
				sid := string(info.SessionId)
				if _, exists := byID[sid]; exists {
					continue
				}
				byID[sid] = info
			}
		}
	}
	sessions := make([]acp.SessionInfo, 0, len(byID))
	for _, info := range byID {
		sessions = append(sessions, info)
	}
	sort.Slice(sessions, func(i, j int) bool {
		return string(sessions[i].SessionId) < string(sessions[j].SessionId)
	})
	return acp.ListSessionsResponse{Sessions: sessions}, nil
}

// SetSessionConfigOption supports the ACP-compatible model_profile select.
func (a *Agent) SetSessionConfigOption(ctx context.Context, req acp.SetSessionConfigOptionRequest) (acp.SetSessionConfigOptionResponse, error) {
	sid, profileID, ok := modelProfileIDFromConfigRequest(req)
	if !ok {
		return acp.SetSessionConfigOptionResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetConfigOption)
	}
	view, err := a.setSessionModelProfile(ctx, sid, profileID)
	if err != nil {
		return acp.SetSessionConfigOptionResponse{}, err
	}
	options := acpModelConfigOptions(view)
	if updater := a.updater(acp.SessionId(sid)); updater != nil && len(options) > 0 {
		_ = updater.Update(ctx, acp.SessionUpdate{ConfigOptionUpdate: &acp.SessionConfigOptionUpdate{ConfigOptions: options}})
	}
	return acp.SetSessionConfigOptionResponse{ConfigOptions: options}, nil
}

// SetSessionMode switches the session's creation mode ("default" or
// "minimal"). The mode is validated against the advertised set, applied to the
// backend session when a sessionModeFeatureProvider is configured, recorded on
// the session state, and announced to the client via a currentModeUpdate
// notification so its mode UI stays in sync.
func (a *Agent) SetSessionMode(ctx context.Context, req acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	sid := string(req.SessionId)
	mode := strings.TrimSpace(string(req.ModeId))
	if !validateAcpSessionMode(mode) {
		return acp.SetSessionModeResponse{}, fmt.Errorf("unsupported session mode %q (supported: %s, %s)", mode, acpSessionModeDefault, acpSessionModeMinimal)
	}
	a.mu.Lock()
	st, ok := a.sessions[sid]
	if ok {
		st.mode = mode
	}
	a.mu.Unlock()
	if !ok {
		return acp.SetSessionModeResponse{}, fmt.Errorf("session %s not found", sid)
	}
	if provider, ok := a.Features.(sessionModeFeatureProvider); ok {
		backendID, err := a.ensureBackendSession(ctx, sid)
		if err != nil {
			return acp.SetSessionModeResponse{}, err
		}
		if err := provider.SetSessionMode(ctx, backendID, mode); err != nil {
			return acp.SetSessionModeResponse{}, err
		}
	}
	if updater := a.updater(acp.SessionId(sid)); updater != nil {
		_ = updater.Update(ctx, acp.SessionUpdate{CurrentModeUpdate: &acp.SessionCurrentModeUpdate{
			CurrentModeId: acp.SessionModeId(mode),
		}})
	}
	return acp.SetSessionModeResponse{}, nil
}

// sessionModes builds the ACP mode state for a session: the advertised mode
// list with the current mode id. The current mode prefers the backend session
// (which owns the persisted mode) and falls back to the in-memory session
// state, then to the default mode.
func (a *Agent) sessionModes(ctx context.Context, sid string) *acp.SessionModeState {
	current := ""
	a.mu.Lock()
	if st, ok := a.sessions[sid]; ok {
		current = st.mode
	}
	a.mu.Unlock()
	if provider, ok := a.Features.(sessionModeFeatureProvider); ok {
		if backendID, err := a.ensureBackendSession(ctx, sid); err == nil {
			if mode, err := provider.SessionMode(ctx, backendID); err == nil && strings.TrimSpace(mode) != "" {
				current = strings.TrimSpace(mode)
			}
		}
	}
	return acpSessionModeState(current)
}

// Logout terminates the ACP connection. godex has no ACP-level authentication,
// so logout is a no-op success.
func (a *Agent) Logout(context.Context, acp.LogoutRequest) (acp.LogoutResponse, error) {
	return acp.LogoutResponse{}, nil
}

// CloseSession cancels any in-flight prompt for the session and drops its
// state. Unknown sessions are ignored (idempotent close).
func (a *Agent) CloseSession(_ context.Context, params acp.CloseSessionRequest) (acp.CloseSessionResponse, error) {
	sid := string(params.SessionId)
	a.mu.Lock()
	st, ok := a.sessions[sid]
	if ok {
		delete(a.sessions, sid)
		if st.cancel != nil {
			st.cancel()
		}
	}
	a.mu.Unlock()
	return acp.CloseSessionResponse{}, nil
}

// ResumeSession re-binds a persisted session id to this agent process. Like
// LoadSession it creates a state record for unknown ids so subsequent Prompt
// calls succeed, but it does not return prior messages (session/resume
// semantics). Model config options are refreshed from the backend session.
func (a *Agent) ResumeSession(ctx context.Context, params acp.ResumeSessionRequest) (acp.ResumeSessionResponse, error) {
	sid := string(params.SessionId)
	a.mu.Lock()
	if a.sessions == nil {
		a.sessions = make(map[string]*sessionState)
	}
	st, ok := a.sessions[sid]
	if !ok {
		st = &sessionState{}
		a.sessions[sid] = st
	}
	st.cwd = params.Cwd
	st.mcpServers = cloneMcpServers(params.McpServers)
	a.mu.Unlock()
	resp := acp.ResumeSessionResponse{}
	if view, ok := a.modelView(ctx, sid); ok {
		resp.ConfigOptions = acpModelConfigOptions(view)
	}
	resp.Modes = a.sessionModes(ctx, sid)
	a.scheduleAvailableCommands(acp.SessionId(sid))
	return resp, nil
}

// EchoPromptHandler is the default PromptHandler used when none is configured.
// It returns a deterministic "echo: <prompt>" reply so the skeleton is
// independently testable without the godex runtime.
func EchoPromptHandler(_ context.Context, turn PromptTurn) (PromptResult, error) {
	return PromptResult{
		FinalText:  "echo: " + turn.Prompt,
		StopReason: acp.StopReasonEndTurn,
	}, nil
}

func extractPromptText(blocks []acp.ContentBlock, workspaceOpt ...string) string {
	workspace := ""
	if len(workspaceOpt) > 0 {
		workspace = strings.TrimSpace(workspaceOpt[0])
	}
	var parts []string
	for _, b := range blocks {
		switch {
		case b.Text != nil && b.Text.Text != "":
			parts = append(parts, b.Text.Text)
		case b.ResourceLink != nil:
			parts = append(parts, formatResourceLink(b.ResourceLink, workspace))
		case b.Resource != nil:
			parts = append(parts, formatEmbeddedResource(b.Resource.Resource))
		case b.Image != nil:
			parts = append(parts, formatMediaPlaceholder("image", b.Image.MimeType, b.Image.Uri))
		case b.Audio != nil:
			parts = append(parts, formatMediaPlaceholder("audio", b.Audio.MimeType, nil))
		}
	}
	return strings.Join(parts, "")
}

func formatResourceLink(link *acp.ContentBlockResourceLink, workspace string) string {
	if link == nil {
		return ""
	}
	label := strings.TrimSpace(link.Name)
	if label == "" && link.Title != nil {
		label = strings.TrimSpace(*link.Title)
	}
	if label == "" {
		label = "resource"
	}
	uri := strings.TrimSpace(link.Uri)
	if uri == "" {
		return fmt.Sprintf("\n[resource: %s]\n", label)
	}
	header := fmt.Sprintf("\n[resource: %s <%s>]\n", label, uri)
	content, err := readWorkspaceFileResource(uri, workspace)
	if err != nil {
		return fmt.Sprintf("%s[resource unavailable: %v]\n", header, err)
	}
	if strings.TrimSpace(content) == "" {
		return header
	}
	return header + content + "\n"
}

func readWorkspaceFileResource(rawURI, workspace string) (string, error) {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return "", nil
	}
	parsed, err := url.Parse(strings.TrimSpace(rawURI))
	if err != nil {
		return "", err
	}
	if parsed.Scheme != "file" {
		return "", nil
	}
	path := strings.TrimSpace(parsed.Path)
	if path == "" {
		return "", fmt.Errorf("missing file path")
	}
	root, err := workspacefs.New(workspace)
	if err != nil {
		return "", err
	}
	defer root.Close()
	data, err := root.ReadFile(path)
	if err != nil {
		return "", err
	}
	return selectFileResourceLines(string(data), parsed.Fragment), nil
}

func selectFileResourceLines(content, fragment string) string {
	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return ""
	}
	start, end, ok := parseLineFragment(fragment)
	if !ok {
		start = 1
		end = min(len(lines), 200)
	} else if end-start+1 > 200 {
		end = start + 199
	}
	if start < 1 {
		start = 1
	}
	if end < start {
		end = start
	}
	if start > len(lines) {
		return fmt.Sprintf("[selected lines %d-%d are outside file; file has %d line(s)]", start, end, len(lines))
	}
	if end > len(lines) {
		end = len(lines)
	}
	selected := strings.Join(lines[start-1:end], "\n")
	if !ok && len(lines) > end {
		selected += fmt.Sprintf("\n[truncated: showing first %d of %d line(s)]", end, len(lines))
	}
	return selected
}

func parseLineFragment(fragment string) (int, int, bool) {
	fragment = strings.TrimSpace(fragment)
	if fragment == "" {
		return 0, 0, false
	}
	if idx := strings.Index(fragment, "&"); idx >= 0 {
		fragment = fragment[:idx]
	}
	if idx := strings.Index(fragment, "?"); idx >= 0 {
		fragment = fragment[:idx]
	}
	parts := strings.SplitN(fragment, "-", 2)
	start, ok := parseLineToken(parts[0])
	if !ok {
		return 0, 0, false
	}
	end := start
	if len(parts) == 2 {
		if parsedEnd, endOK := parseLineToken(parts[1]); endOK {
			end = parsedEnd
		}
	}
	return start, end, true
}

func parseLineToken(token string) (int, bool) {
	token = strings.TrimSpace(token)
	token = strings.TrimPrefix(token, "L")
	token = strings.TrimPrefix(token, "l")
	if idx := strings.IndexAny(token, "Cc:"); idx >= 0 {
		token = token[:idx]
	}
	if token == "" {
		return 0, false
	}
	value := 0
	for _, r := range token {
		if r < '0' || r > '9' {
			return 0, false
		}
		value = value*10 + int(r-'0')
	}
	return value, value > 0
}

func formatEmbeddedResource(res acp.EmbeddedResourceResource) string {
	if res.TextResourceContents != nil {
		text := strings.TrimSpace(res.TextResourceContents.Text)
		uri := strings.TrimSpace(res.TextResourceContents.Uri)
		if uri != "" {
			if text == "" {
				return fmt.Sprintf("\n[resource: %s]\n", uri)
			}
			return fmt.Sprintf("\n[resource: %s]\n%s\n", uri, text)
		}
		return text
	}
	if res.BlobResourceContents != nil {
		mime := ""
		if res.BlobResourceContents.MimeType != nil {
			mime = strings.TrimSpace(*res.BlobResourceContents.MimeType)
		}
		return formatResourcePlaceholder(res.BlobResourceContents.Uri, mime)
	}
	return ""
}

func formatResourcePlaceholder(uri, mime string) string {
	uri = strings.TrimSpace(uri)
	mime = strings.TrimSpace(mime)
	switch {
	case uri != "" && mime != "":
		return fmt.Sprintf("\n[resource: %s; mime=%s]\n", uri, mime)
	case uri != "":
		return fmt.Sprintf("\n[resource: %s]\n", uri)
	case mime != "":
		return fmt.Sprintf("\n[resource: mime=%s]\n", mime)
	default:
		return "\n[resource]\n"
	}
}

func formatMediaPlaceholder(kind, mime string, uri *string) string {
	value := ""
	if uri != nil {
		value = strings.TrimSpace(*uri)
	}
	mime = strings.TrimSpace(mime)
	switch {
	case value != "" && mime != "":
		return fmt.Sprintf("\n[%s: %s; mime=%s]\n", kind, value, mime)
	case value != "":
		return fmt.Sprintf("\n[%s: %s]\n", kind, value)
	case mime != "":
		return fmt.Sprintf("\n[%s: mime=%s]\n", kind, mime)
	default:
		return fmt.Sprintf("\n[%s]\n", kind)
	}
}

func (a *Agent) updater(sid acp.SessionId) SessionUpdater {
	a.mu.Lock()
	conn := a.conn
	a.mu.Unlock()
	if conn == nil {
		return nil
	}
	return connectionUpdater{conn: conn, sessionID: sid}
}

func (a *Agent) permissionRequester(sid acp.SessionId) PermissionRequester {
	a.mu.Lock()
	conn := a.conn
	a.mu.Unlock()
	if conn == nil {
		return nil
	}
	return connectionRequester{conn: conn, sessionID: sid}
}

func (a *Agent) ensureBackendSession(ctx context.Context, sid string) (string, error) {
	if a.Features == nil {
		return "", nil
	}
	a.mu.Lock()
	if a.sessions == nil {
		a.sessions = make(map[string]*sessionState)
	}
	st, ok := a.sessions[sid]
	if !ok {
		st = &sessionState{}
		a.sessions[sid] = st
	}
	if strings.TrimSpace(st.backendSessionID) != "" {
		backendID := st.backendSessionID
		a.mu.Unlock()
		return backendID, nil
	}
	a.mu.Unlock()

	backendID, err := a.Features.EnsureSession(ctx, sid)
	if err != nil {
		return "", err
	}
	backendID = strings.TrimSpace(backendID)
	a.mu.Lock()
	if st, ok := a.sessions[sid]; ok {
		st.backendSessionID = backendID
	}
	a.mu.Unlock()
	return backendID, nil
}

func (a *Agent) modelView(ctx context.Context, sid string) (backend.ModelsView, bool) {
	if a.Features == nil {
		return backend.ModelsView{}, false
	}
	backendID, err := a.ensureBackendSession(ctx, sid)
	if err != nil {
		return backend.ModelsView{}, false
	}
	view, err := a.Features.Models(ctx, backendID)
	if err != nil {
		return backend.ModelsView{}, false
	}
	return view, true
}

func (a *Agent) setSessionModelProfile(ctx context.Context, sid, profileID string) (backend.ModelsView, error) {
	if a.Features == nil {
		return backend.ModelsView{}, fmt.Errorf("session model features are unavailable")
	}
	backendID, err := a.ensureBackendSession(ctx, sid)
	if err != nil {
		return backend.ModelsView{}, err
	}
	current, err := a.Features.Models(ctx, backendID)
	if err != nil {
		return backend.ModelsView{}, err
	}
	if err := validateModelProfile(current, profileID); err != nil {
		return backend.ModelsView{}, err
	}
	return a.Features.SetSessionModelProfile(ctx, backendID, profileID)
}

func (a *Agent) scheduleAvailableCommands(sid acp.SessionId) {
	go func() {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		// Let the RPC response that created/loaded the session be written first.
		// Some ACP clients register session-local command UI only after receiving
		// that response, so an in-handler notification can be dropped.
		time.Sleep(25 * time.Millisecond)
		_ = a.sendAvailableCommands(ctx, sid, true)
	}()
}

func (a *Agent) sendAvailableCommands(ctx context.Context, sid acp.SessionId, force bool) error {
	updater := a.updater(sid)
	if updater == nil {
		return nil
	}
	a.mu.Lock()
	st, ok := a.sessions[string(sid)]
	if ok && st.commandsSent && !force {
		a.mu.Unlock()
		return nil
	}
	backendID := ""
	if ok {
		backendID = st.backendSessionID
	}
	a.mu.Unlock()

	items := commands.AvailableMetadata()
	if provider, ok := a.Features.(commandFeatureProvider); ok {
		items = provider.AvailableCommands(ctx, backendID)
	}
	if err := updater.Update(ctx, acp.SessionUpdate{AvailableCommandsUpdate: &acp.SessionAvailableCommandsUpdate{AvailableCommands: acpCommands(items)}}); err != nil {
		return err
	}
	a.mu.Lock()
	if st, ok := a.sessions[string(sid)]; ok {
		st.commandsSent = true
	}
	a.mu.Unlock()
	return nil
}

type connectionUpdater struct {
	conn      *acp.AgentSideConnection
	sessionID acp.SessionId
}

func (u connectionUpdater) Update(ctx context.Context, update acp.SessionUpdate) error {
	return u.conn.SessionUpdate(ctx, acp.SessionNotification{
		SessionId: u.sessionID,
		Update:    update,
	})
}

type connectionRequester struct {
	conn      *acp.AgentSideConnection
	sessionID acp.SessionId
}

func (r connectionRequester) RequestPermission(ctx context.Context, req acp.RequestPermissionRequest) (acp.RequestPermissionResponse, error) {
	req.SessionId = r.sessionID
	return r.conn.RequestPermission(ctx, req)
}

func randomSessionID() string {
	return idgen.New("sess_", 12)
}

// cloneMcpServers deep-copies the slice so session state never aliases the
// request params (which the SDK may reuse). Stdio server args/env slices are
// copied per entry; nil input yields nil.
func cloneMcpServers(servers []acp.McpServer) []acp.McpServer {
	if len(servers) == 0 {
		return nil
	}
	out := make([]acp.McpServer, len(servers))
	copy(out, servers)
	for i := range out {
		s := out[i]
		if s.Stdio != nil {
			clone := *s.Stdio
			if len(clone.Args) > 0 {
				clone.Args = append([]string{}, clone.Args...)
			}
			if len(clone.Env) > 0 {
				env := make([]acp.EnvVariable, len(clone.Env))
				copy(env, clone.Env)
				clone.Env = env
			}
			s.Stdio = &clone
		}
		out[i] = s
	}
	return out
}
