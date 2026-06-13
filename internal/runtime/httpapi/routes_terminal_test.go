package httpapi

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestTerminalCreate(t *testing.T) {
	mgr := newTerminalManager()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/terminal/create", mgr.handleCreateTerminal)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	t.Run("creates terminal with valid id", func(t *testing.T) {
		resp, err := http.Post(srv.URL+"/v1/terminal/create", "application/json", strings.NewReader(`{"workspaceDir":"/tmp"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201, got %d", resp.StatusCode)
		}
		var out createTerminalResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(out.TerminalID, "term-") {
			t.Errorf("expected term- prefix, got %s", out.TerminalID)
		}
		if out.InitialCursor != 0 {
			t.Errorf("expected initialCursor 0, got %d", out.InitialCursor)
		}
		if _, ok := mgr.terminals[out.TerminalID]; !ok {
			t.Error("terminal not registered in manager")
		}
	})

	t.Run("creates without body (empty workspaceDir)", func(t *testing.T) {
		resp, err := http.Post(srv.URL+"/v1/terminal/create", "application/json", nil)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusCreated {
			t.Fatalf("expected 201, got %d", resp.StatusCode)
		}
	})

	t.Run("two creates produce unique ids", func(t *testing.T) {
		var ids []string
		for i := 0; i < 3; i++ {
			resp, err := http.Post(srv.URL+"/v1/terminal/create", "application/json", nil)
			if err != nil {
				t.Fatal(err)
			}
			var out createTerminalResponse
			json.NewDecoder(resp.Body).Decode(&out)
			resp.Body.Close()
			ids = append(ids, out.TerminalID)
		}
		seen := make(map[string]bool)
		for _, id := range ids {
			if seen[id] {
				t.Errorf("duplicate terminal id: %s", id)
			}
			seen[id] = true
		}
	})
}

func TestTerminalOutput(t *testing.T) {
	mgr := newTerminalManager()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/terminal/create", mgr.handleCreateTerminal)
	mux.HandleFunc("GET /v1/terminal/{id}/output", mgr.handleTerminalOutput)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	// Helper to create a terminal and return its id.
	create := func() string {
		resp, err := http.Post(srv.URL+"/v1/terminal/create", "application/json", strings.NewReader(`{}`))
		if err != nil {
			t.Fatal(err)
		}
		var out createTerminalResponse
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		return out.TerminalID
	}

	t.Run("output for non-existent terminal returns 404", func(t *testing.T) {
		resp, err := http.Get(srv.URL + "/v1/terminal/nonexistent/output")
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", resp.StatusCode)
		}
	})

	t.Run("output for real terminal at cursor 0 returns data chunk", func(t *testing.T) {
		id := create()
		time.Sleep(300 * time.Millisecond)

		resp, err := http.Get(fmt.Sprintf("%s/v1/terminal/%s/output?cursor=0", srv.URL, id))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("expected 200, got %d", resp.StatusCode)
		}
		var chunk outputChunk
		if err := json.NewDecoder(resp.Body).Decode(&chunk); err != nil {
			t.Fatal(err)
		}
		if chunk.TerminalID != id {
			t.Errorf("terminalId mismatch: %s vs %s", chunk.TerminalID, id)
		}
		if chunk.Cursor < 0 {
			t.Errorf("expected cursor >= 0, got %d", chunk.Cursor)
		}
	})

	t.Run("output with invalid cursor returns 400", func(t *testing.T) {
		id := create()
		resp, err := http.Get(fmt.Sprintf("%s/v1/terminal/%s/output?cursor=abc", srv.URL, id))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid cursor, got %d", resp.StatusCode)
		}
	})

	t.Run("output with cursor beyond buffer returns empty data", func(t *testing.T) {
		id := create()
		resp, err := http.Get(fmt.Sprintf("%s/v1/terminal/%s/output?cursor=999999", srv.URL, id))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
		var chunk outputChunk
		json.NewDecoder(resp.Body).Decode(&chunk)
		if chunk.Data != "" {
			t.Errorf("expected empty data for far-ahead cursor, got %q", chunk.Data[:min(len(chunk.Data), 20)])
		}
	})
}

func TestTerminalInput(t *testing.T) {
	mgr := newTerminalManager()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/terminal/create", mgr.handleCreateTerminal)
	mux.HandleFunc("POST /v1/terminal/{id}/input", mgr.handleTerminalInput)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	create := func() string {
		resp, _ := http.Post(srv.URL+"/v1/terminal/create", "application/json", nil)
		var out createTerminalResponse
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		return out.TerminalID
	}

	t.Run("input to non-existent terminal returns 404", func(t *testing.T) {
		resp, err := http.Post(srv.URL+"/v1/terminal/nonexistent/input", "application/json", strings.NewReader(`{"data":"ls\n"}`))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", resp.StatusCode)
		}
	})

	t.Run("input to real terminal returns accepted", func(t *testing.T) {
		id := create()
		resp, err := http.Post(
			fmt.Sprintf("%s/v1/terminal/%s/input", srv.URL, id),
			"application/json",
			strings.NewReader(`{"data":"echo hello\n"}`),
		)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		if accepted, _ := out["accepted"].(bool); !accepted {
			t.Error("expected accepted=true")
		}
	})

	t.Run("input with invalid json body returns 400", func(t *testing.T) {
		id := create()
		resp, err := http.Post(
			fmt.Sprintf("%s/v1/terminal/%s/input", srv.URL, id),
			"application/json",
			strings.NewReader("not-json"),
		)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusBadRequest {
			t.Errorf("expected 400 for invalid json, got %d", resp.StatusCode)
		}
	})
}

func TestTerminalDelete(t *testing.T) {
	mgr := newTerminalManager()
	mux := http.NewServeMux()
	mux.HandleFunc("POST /v1/terminal/create", mgr.handleCreateTerminal)
	mux.HandleFunc("DELETE /v1/terminal/{id}", mgr.handleTerminalDelete)
	srv := httptest.NewServer(mux)
	defer srv.Close()

	create := func() string {
		resp, _ := http.Post(srv.URL+"/v1/terminal/create", "application/json", nil)
		var out createTerminalResponse
		json.NewDecoder(resp.Body).Decode(&out)
		resp.Body.Close()
		return out.TerminalID
	}

	t.Run("delete non-existent terminal returns 404", func(t *testing.T) {
		req, _ := http.NewRequest(http.MethodDelete, srv.URL+"/v1/terminal/nonexistent", nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("expected 404, got %d", resp.StatusCode)
		}
	})

	t.Run("delete real terminal returns ok and removes from manager", func(t *testing.T) {
		id := create()
		req, _ := http.NewRequest(http.MethodDelete, fmt.Sprintf("%s/v1/terminal/%s", srv.URL, id), nil)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("expected 200, got %d", resp.StatusCode)
		}
		var out map[string]any
		json.NewDecoder(resp.Body).Decode(&out)
		if exited, _ := out["exited"].(bool); !exited {
			t.Error("expected exited=true")
		}

		mgr.mu.Lock()
		_, ok := mgr.terminals[id]
		mgr.mu.Unlock()
		if ok {
			t.Error("terminal should be removed after delete")
		}
	})
}

func TestTerminalManager_Lifecycle(t *testing.T) {
	mgr := newTerminalManager()
	ctx := t.Context()

	t.Run("create → output → input → delete", func(t *testing.T) {
		session, err := mgr.create(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		id := session.id

		// Give the shell a moment to start and maybe output a banner.
		time.Sleep(300 * time.Millisecond)

		// Read output from cursor 0.
		chunk, err := mgr.output(id, 0)
		if err != nil {
			t.Fatal(err)
		}
		if chunk.TerminalID != id {
			t.Errorf("id mismatch: %s vs %s", chunk.TerminalID, id)
		}

		// Write input.
		if err := mgr.writeInput(id, "echo test123\n"); err != nil {
			t.Fatal(err)
		}

		// Wait for the echo to appear.
		time.Sleep(500 * time.Millisecond)

		// Read output again.
		chunk2, err := mgr.output(id, chunk.Cursor)
		if err != nil {
			t.Fatal(err)
		}
		if chunk2.Cursor <= chunk.Cursor {
			t.Logf("no new output after input (cursor %d → %d)", chunk.Cursor, chunk2.Cursor)
		}

		// Kill the terminal.
		if err := mgr.kill(id); err != nil {
			t.Fatal(err)
		}

		// Verify it's gone.
		_, ok := mgr.terminals[id]
		if ok {
			t.Error("terminal should be removed after kill")
		}
	})

	t.Run("output for killed terminal returns 404", func(t *testing.T) {
		session, err := mgr.create(ctx, "")
		if err != nil {
			t.Fatal(err)
		}
		mgr.kill(session.id)
		_, err = mgr.output(session.id, 0)
		if err == nil {
			t.Error("expected error for killed terminal")
		}
	})
}
