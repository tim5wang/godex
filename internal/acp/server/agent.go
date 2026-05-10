package server

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net/url"
	"strings"
	"sync"
	"time"

	acp "github.com/coder/acp-go-sdk"

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
	a.sessions[sid] = &sessionState{cwd: params.Cwd}
	a.mu.Unlock()
	resp := acp.NewSessionResponse{SessionId: acp.SessionId(sid)}
	if view, ok := a.modelView(ctx, sid); ok {
		resp.Models = acpModelState(view)
		resp.ConfigOptions = acpModelConfigOptions(view)
	}
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

// ListSessions returns the in-memory ACP sessions tracked by this agent.
func (a *Agent) ListSessions(_ context.Context, _ acp.ListSessionsRequest) (acp.ListSessionsResponse, error) {
	a.mu.Lock()
	sessions := make([]acp.SessionInfo, 0, len(a.sessions))
	for sid, st := range a.sessions {
		info := acp.SessionInfo{
			SessionId: acp.SessionId(sid),
			Cwd:       st.cwd,
		}
		sessions = append(sessions, info)
	}
	a.mu.Unlock()
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

// SetSessionMode is not yet supported.
func (a *Agent) SetSessionMode(context.Context, acp.SetSessionModeRequest) (acp.SetSessionModeResponse, error) {
	return acp.SetSessionModeResponse{}, acp.NewMethodNotFound(acp.AgentMethodSessionSetMode)
}

// UnstableSetSessionModel supports ACP clients that expose model selection as
// the protocol's model state instead of a config option.
func (a *Agent) UnstableSetSessionModel(ctx context.Context, req acp.UnstableSetSessionModelRequest) (acp.UnstableSetSessionModelResponse, error) {
	_, err := a.setSessionModelProfile(ctx, string(req.SessionId), string(req.ModelId))
	if err != nil {
		return acp.UnstableSetSessionModelResponse{}, err
	}
	return acp.UnstableSetSessionModelResponse{}, nil
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
	var b [12]byte
	if _, err := io.ReadFull(rand.Reader, b[:]); err != nil {
		return fmt.Sprintf("sess_%d", time.Now().UnixNano())
	}
	return "sess_" + hex.EncodeToString(b[:])
}
