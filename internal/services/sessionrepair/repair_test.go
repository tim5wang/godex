package sessionrepair

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestRepairRepointsMissingCheckpointPointer(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	session := filepath.Join(root, "session-1")
	writeValidCheckpoint(t, session, "20260507T115900.000000000Z-good")
	writeValidRoot(t, session)
	writeJSON(t, filepath.Join(session, checkpointPointer), checkpointPointerPayload{Current: "missing", CreatedAt: now})

	dry, err := Diagnose(Request{SessionsDir: root, Now: now})
	if err != nil {
		t.Fatalf("diagnose: %v", err)
	}
	if dry.Changed || len(dry.Actions) != 1 || dry.Actions[0].Status != "planned" {
		t.Fatalf("expected planned pointer repair, got %+v", dry)
	}

	report, err := Repair(Request{SessionsDir: root, Now: now})
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if !report.Changed || len(report.Actions) != 1 || report.Actions[0].Status != "applied" {
		t.Fatalf("expected applied pointer repair, got %+v", report)
	}
	var pointer checkpointPointerPayload
	readJSON(t, filepath.Join(session, checkpointPointer), &pointer)
	if pointer.Current != "20260507T115900.000000000Z-good" {
		t.Fatalf("unexpected pointer: %+v", pointer)
	}
	if _, err := os.Stat(filepath.Join(session, ".repair-backups")); err != nil {
		t.Fatalf("expected repair backup: %v", err)
	}
}

func TestRepairRestoresRootStateFromCheckpoint(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	session := filepath.Join(root, "session-1")
	cp := "20260507T115900.000000000Z-good"
	writeValidCheckpoint(t, session, cp)
	writeJSON(t, filepath.Join(session, checkpointPointer), checkpointPointerPayload{Current: cp, CreatedAt: now})
	if err := os.Remove(filepath.Join(session, stateFile)); err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}

	report, err := Repair(Request{SessionsDir: root, Now: now})
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if !hasAction(report, CodeRootStateRestored) {
		t.Fatalf("expected root restore action, got %+v", report)
	}
	if _, err := os.Stat(filepath.Join(session, stateFile)); err != nil {
		t.Fatalf("expected restored state: %v", err)
	}
}

func TestRepairRecomputesManifestDigest(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	session := filepath.Join(root, "session-1")
	state := []byte(`{"messages":[{"role":"user","content":[{"type":"text","text":"hello"}]}]}`)
	if err := os.MkdirAll(session, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session, stateFile), state, 0644); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(session, manifestFile), map[string]any{"session_id": "session-1", "state_digest": "bad"})

	report, err := Repair(Request{SessionsDir: root, Now: now})
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if !hasAction(report, CodeManifestDigestRecomputed) {
		t.Fatalf("expected digest action, got %+v", report)
	}
	var manifest map[string]any
	readJSON(t, filepath.Join(session, manifestFile), &manifest)
	if manifest["state_digest"] != digest(state) {
		t.Fatalf("expected recomputed digest, got %+v", manifest)
	}
}

func TestRepairMarksStaleRunningTurnInterruptedAndQueuesOrphanInjection(t *testing.T) {
	root := t.TempDir()
	now := time.Date(2026, 5, 7, 12, 0, 0, 0, time.UTC)
	session := filepath.Join(root, "session-1")
	if err := os.MkdirAll(session, 0755); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(session, turnsFile), []turnRecord{{ID: "turn-1", Status: "running", UpdatedAt: now.Add(-time.Hour)}})
	writeJSON(t, filepath.Join(session, turnQueueFile), []queuedTurn{{ID: "q-1", Status: "injected", Mode: "steering", Envelope: map[string]any{}}})

	report, err := Repair(Request{SessionsDir: root, Now: now})
	if err != nil {
		t.Fatalf("repair: %v", err)
	}
	if !hasAction(report, CodeStaleTurnInterrupted) || !hasAction(report, CodeOrphanInjectedQueued) {
		t.Fatalf("expected turn and queue actions, got %+v", report)
	}
	var turns []turnRecord
	readJSON(t, filepath.Join(session, turnsFile), &turns)
	if turns[0].Status != "interrupted" || !turns[0].CanResume || !turns[0].ResumeAvailable {
		t.Fatalf("unexpected repaired turn: %+v", turns[0])
	}
	var queue []queuedTurn
	readJSON(t, filepath.Join(session, turnQueueFile), &queue)
	if queue[0].Status != "queued" || queue[0].Mode != "follow_up" {
		t.Fatalf("unexpected repaired queue: %+v", queue[0])
	}
	if data, err := os.ReadFile(filepath.Join(session, eventJournalFile)); err != nil || len(data) == 0 {
		t.Fatalf("expected repair event journal, bytes=%d err=%v", len(data), err)
	}
}

func writeValidCheckpoint(t *testing.T, session, id string) {
	t.Helper()
	state := []byte(`{"messages":[]}`)
	cpDir := filepath.Join(session, checkpointsDir, id)
	if err := os.MkdirAll(cpDir, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(cpDir, stateFile), state, 0644); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(cpDir, manifestFile), map[string]any{"session_id": filepath.Base(session), "state_digest": digestForTest(state)})
	writeJSON(t, filepath.Join(cpDir, turnsFile), []turnRecord{{ID: "turn-0", Status: "completed"}})
	writeJSON(t, filepath.Join(cpDir, turnQueueFile), []queuedTurn{})
}

func writeValidRoot(t *testing.T, session string) {
	t.Helper()
	state := []byte(`{"messages":[]}`)
	if err := os.MkdirAll(session, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(session, stateFile), state, 0644); err != nil {
		t.Fatal(err)
	}
	writeJSON(t, filepath.Join(session, manifestFile), map[string]any{"session_id": filepath.Base(session), "state_digest": digestForTest(state)})
}

func writeJSON(t *testing.T, path string, value any) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	data, err := json.MarshalIndent(value, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, data, 0644); err != nil {
		t.Fatal(err)
	}
}

func readJSON(t *testing.T, path string, out any) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		t.Fatal(err)
	}
}

func digestForTest(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

func hasAction(report Report, code string) bool {
	for _, action := range report.Actions {
		if action.Code == code {
			return true
		}
	}
	return false
}
