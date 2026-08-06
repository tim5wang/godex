package httpapi

import (
	"bufio"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
)

func newExecTestServer(t *testing.T) *httptest.Server {
	t.Helper()
	cfg := newTestConfig(t)
	manager := newTestManager(t, cfg)
	caller := &stubCaller{}
	service := backend.NewService(cfg, agent.NewSharedDependenciesWithCaller(cfg, caller), commands.NewService(cfg))
	return httptest.NewServer(NewHandler(manager, service, nil, nil, nil, nil, nil))
}

// execEvent mirrors the SSE payload emitted by POST /v1/exec.
type execEvent struct {
	Output   string `json:"output"`
	Final    bool   `json:"final"`
	ExitCode int    `json:"exit_code"`
}

func readExecEvents(t *testing.T, resp *http.Response) []execEvent {
	t.Helper()
	defer resp.Body.Close()
	var events []execEvent
	scanner := bufio.NewScanner(resp.Body)
	for scanner.Scan() {
		line := scanner.Text()
		if !strings.HasPrefix(line, "data: ") {
			continue
		}
		var ev execEvent
		if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &ev); err != nil {
			t.Fatalf("unmarshal exec event: %v (line %q)", err, line)
		}
		events = append(events, ev)
	}
	return events
}

func TestV1ExecStreamsCommandOutput(t *testing.T) {
	server := newExecTestServer(t)
	defer server.Close()

	body, _ := json.Marshal(map[string]string{"command": "echo hello-from-exec"})
	resp, err := http.Post(server.URL+"/v1/exec", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post exec: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/event-stream") {
		t.Fatalf("expected SSE content type, got %q", ct)
	}

	events := readExecEvents(t, resp)
	if len(events) == 0 {
		t.Fatal("expected at least one exec event")
	}
	var output strings.Builder
	last := events[len(events)-1]
	for _, ev := range events {
		output.WriteString(ev.Output)
	}
	if !strings.Contains(output.String(), "hello-from-exec") {
		t.Fatalf("output missing command result: %q", output.String())
	}
	if !last.Final {
		t.Fatal("expected final event")
	}
	if last.ExitCode != 0 {
		t.Fatalf("expected exit code 0, got %d", last.ExitCode)
	}
}

func TestV1ExecReportsNonZeroExitCode(t *testing.T) {
	server := newExecTestServer(t)
	defer server.Close()

	body, _ := json.Marshal(map[string]string{"command": "exit 3"})
	resp, err := http.Post(server.URL+"/v1/exec", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post exec: %v", err)
	}
	events := readExecEvents(t, resp)
	if len(events) == 0 || !events[len(events)-1].Final {
		t.Fatal("expected final event")
	}
	if events[len(events)-1].ExitCode != 3 {
		t.Fatalf("expected exit code 3, got %d", events[len(events)-1].ExitCode)
	}
}

func TestV1ExecRejectsMissingCommand(t *testing.T) {
	server := newExecTestServer(t)
	defer server.Close()

	body, _ := json.Marshal(map[string]string{})
	resp, err := http.Post(server.URL+"/v1/exec", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("post exec: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing command, got %d", resp.StatusCode)
	}
}
