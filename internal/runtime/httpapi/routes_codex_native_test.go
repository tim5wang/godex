package httpapi

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/core/identity"
	"github.com/tim5wang/godex/internal/services/codexnative"
)

func TestCodexNativeActiveWriterReturnsStructuredConflict(t *testing.T) {
	for _, wrapped := range []bool{false, true} {
		var err error = &codexnative.RPCError{Code: -32600, Message: "thread thread already has an active writer"}
		if wrapped {
			err = fmt.Errorf("Codex resume: %w", err)
		}
		response := httptest.NewRecorder()
		codexNativeError(response, err)
		var body struct {
			Code           string `json:"code"`
			Retryable      bool   `json:"retryable"`
			OutcomeUnknown bool   `json:"outcomeUnknown"`
		}
		if json.Unmarshal(response.Body.Bytes(), &body) != nil || response.Code != http.StatusConflict || body.Code != "codex_active_writer" || body.Retryable || body.OutcomeUnknown {
			t.Fatalf("%d: %s", response.Code, response.Body)
		}
	}
	// Unrelated state conflicts must not be presented as writer ownership.
	response := httptest.NewRecorder()
	codexNativeError(response, fmt.Errorf("state: %w", codexnative.ErrConflict))
	if response.Code != http.StatusConflict || strings.Contains(response.Body.String(), "codex_active_writer") {
		t.Fatalf("%d: %s", response.Code, response.Body)
	}
	response = httptest.NewRecorder()
	codexNativeError(response, errors.New("unavailable"))
	if response.Code != http.StatusBadGateway {
		t.Fatal(response.Code)
	}
}

func TestCodexNativeEntireSurfaceRequiresNodeOwner(t *testing.T) {
	for _, principal := range []struct {
		name  string
		value *identity.Principal
		want  int
	}{
		{"missing", nil, http.StatusUnauthorized},
		{"tenant", &identity.Principal{TenantID: "tenant", UserID: "member"}, http.StatusForbidden},
	} {
		t.Run(principal.name, func(t *testing.T) {
			var dials atomic.Int32
			service := codexnative.New(codexnative.Options{WorkspaceRoots: []string{t.TempDir()}, Dial: func(context.Context) (io.ReadWriteCloser, error) {
				dials.Add(1)
				return nil, fmt.Errorf("must not connect")
			}})
			defer service.Close()
			protected := func(next http.Handler) http.Handler {
				return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if principal.value != nil {
						r = r.WithContext(WithPrincipal(r.Context(), *principal.value))
					}
					next.ServeHTTP(w, r)
				})
			}
			mux := http.NewServeMux()
			registerCodexNativeRoutes(mux, service, requireCenterLocalData(protected))
			for _, route := range []struct{ method, path string }{
				{"GET", "/external/codex/status"}, {"GET", "/external/codex/threads"}, {"GET", "/external/codex/threads?view=loaded"}, {"GET", "/external/codex/threads/thread/access"}, {"GET", "/external/codex/threads/thread/history"}, {"GET", "/external/codex/threads/thread/events"}, {"GET", "/external/codex/threads/thread/queue"},
				{"POST", "/external/codex/threads/thread/messages"}, {"POST", "/external/codex/threads/thread/queue/start"}, {"POST", "/external/codex/threads/thread/interrupt"}, {"POST", "/external/codex/threads/thread/approvals"},
				{"GET", "/external/codex/threads/thread/models"}, {"PATCH", "/external/codex/threads/thread/settings"},
			} {
				response := httptest.NewRecorder()
				mux.ServeHTTP(response, httptest.NewRequest(route.method, route.path, strings.NewReader("{}")))
				if response.Code != principal.want {
					t.Fatalf("%s %s: %d", route.method, route.path, response.Code)
				}
			}
			if dials.Load() != 0 {
				t.Fatal("unauthorized caller reached native runtime")
			}
		})
	}
}

func TestCodexModelSettingsRejectPermissionAndProviderInjection(t *testing.T) {
	var dials atomic.Int32
	service := codexnative.New(codexnative.Options{Dial: func(context.Context) (io.ReadWriteCloser, error) {
		dials.Add(1)
		return nil, fmt.Errorf("must not connect")
	}})
	defer service.Close()
	mux := http.NewServeMux()
	registerCodexNativeRoutes(mux, service, func(next http.Handler) http.Handler { return next })
	for _, body := range []string{`{"model":"model-a","effort":"high","approvalPolicy":"never"}`, `{"model":"model-a","effort":"high","modelProvider":"evil"}`, `{"model":"model-a","effort":"high","cwd":"/"}`, `null`, `{} {}`} {
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest("PATCH", "/external/codex/threads/thread/settings", strings.NewReader(body)))
		if response.Code != http.StatusBadRequest || dials.Load() != 0 {
			t.Fatalf("%s: %d dials=%d", body, response.Code, dials.Load())
		}
	}
}

func TestCodexNativeReadinessAdmitsOperatorAndRelayNode(t *testing.T) {
	for _, principal := range []identity.Principal{{UserID: "operator", PlatformRole: identity.RolePlatformAdmin}, {Kind: identity.KindNode}} {
		service := codexnative.New(codexnative.Options{Socket: fmt.Sprintf("%s/no-socket", t.TempDir())})
		defer service.Close()
		protected := func(next http.Handler) http.Handler {
			return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				next.ServeHTTP(w, r.WithContext(WithPrincipal(r.Context(), principal)))
			})
		}
		mux := http.NewServeMux()
		registerCodexNativeRoutes(mux, service, requireCenterLocalData(protected))
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest("GET", "/external/codex/status", nil))
		if response.Code != 200 {
			t.Fatalf("%+v: %d %s", principal, response.Code, response.Body.String())
		}
		var status codexnative.Status
		if err := json.Unmarshal(response.Body.Bytes(), &status); err != nil || status.Ready || status.Reason == "" {
			t.Fatalf("%+v %v", status, err)
		}
	}
}

func TestCodexNativeRejectsConfigurationInjectionBeforeDial(t *testing.T) {
	service := codexnative.New(codexnative.Options{})
	defer service.Close()
	mux := http.NewServeMux()
	registerCodexNativeRoutes(mux, service, func(next http.Handler) http.Handler { return next })
	for _, body := range []string{`{"requestId":"request-1","mode":"queue","text":"hello","sandbox":"danger-full-access"}`, `{"turnId":"turn","threadId":"another"}`, `{} {}`} {
		path := "/external/codex/threads/thread/messages"
		if strings.Contains(body, "turnId") {
			path = "/external/codex/threads/thread/interrupt"
		}
		response := httptest.NewRecorder()
		mux.ServeHTTP(response, httptest.NewRequest("POST", path, strings.NewReader(body)))
		if response.Code != 400 {
			t.Fatalf("%s: %d", body, response.Code)
		}
	}
}

func TestCodexNativeRejectsUnknownThreadViewBeforeDial(t *testing.T) {
	var dials atomic.Int32
	service := codexnative.New(codexnative.Options{Dial: func(context.Context) (io.ReadWriteCloser, error) {
		dials.Add(1)
		return nil, fmt.Errorf("must not connect")
	}})
	defer service.Close()
	mux := http.NewServeMux()
	registerCodexNativeRoutes(mux, service, func(next http.Handler) http.Handler { return next })
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, httptest.NewRequest("GET", "/external/codex/threads?view=arbitrary-rpc", nil))
	if response.Code != http.StatusBadRequest || dials.Load() != 0 {
		t.Fatalf("%d dials=%d: %s", response.Code, dials.Load(), response.Body)
	}
}

func TestCodexNativeEventStreamSnapshotThenDeltaAndDetach(t *testing.T) {
	root := t.TempDir()
	unsubscribed := make(chan struct{}, 1)
	service := codexnative.New(codexnative.Options{WorkspaceRoots: []string{root}, Dial: func(context.Context) (io.ReadWriteCloser, error) {
		local, remote := net.Pipe()
		go func() {
			defer remote.Close()
			decoder := json.NewDecoder(remote)
			encoder := json.NewEncoder(remote)
			for {
				var request struct {
					ID     json.RawMessage `json:"id"`
					Method string          `json:"method"`
				}
				if decoder.Decode(&request) != nil {
					return
				}
				if len(request.ID) == 0 {
					continue
				}
				var result any = map[string]any{}
				thread := map[string]any{"id": "thread", "cwd": root, "status": map[string]string{"type": "idle"}}
				switch request.Method {
				case "thread/read":
					result = map[string]any{"thread": thread}
				case "thread/resume":
					result = map[string]any{"thread": thread, "initialTurnsPage": map[string]any{"data": []any{}, "nextCursor": nil}}
				case "thread/unsubscribe":
					unsubscribed <- struct{}{}
				}
				_ = encoder.Encode(map[string]any{"id": request.ID, "result": result})
				if request.Method == "thread/resume" {
					_ = encoder.Encode(map[string]any{"method": "item/agentMessage/delta", "params": map[string]string{"threadId": "thread", "turnId": "turn", "itemId": "message", "delta": "native delta"}})
				}
			}
		}()
		return local, nil
	}})
	defer service.Close()
	mux := http.NewServeMux()
	registerCodexNativeRoutes(mux, service, func(next http.Handler) http.Handler { return next })
	server := httptest.NewServer(mux)
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	request, _ := http.NewRequestWithContext(ctx, "GET", server.URL+"/external/codex/threads/thread/events", nil)
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	if response.StatusCode != 200 || !strings.HasPrefix(response.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d", response.StatusCode)
	}
	reader := bufio.NewReader(response.Body)
	for _, method := range []string{"codex/snapshot", "item/agentMessage/delta"} {
		line, err := reader.ReadBytes('\n')
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Contains(line, []byte(method)) {
			t.Fatalf("wanted %s: %s", method, line)
		}
		_, _ = reader.ReadBytes('\n')
	}
	cancel()
	_ = response.Body.Close()
	select {
	case <-unsubscribed:
	case <-time.After(4 * time.Second):
		t.Fatal("SSE disconnect did not detach native subscription")
	}
}
