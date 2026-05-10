package background

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestCompletedTaskCanBeQueriedAndDrainedOnce(t *testing.T) {
	manager := NewManager()
	cmd := exec.Command("sh", "-c", "printf done")

	task, err := manager.Start("task-complete", cmd, 0)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	<-task.Done

	if manager.IsRunning(task.ID) {
		t.Fatalf("expected task to be finished")
	}

	stored, err := manager.Get(task.ID)
	if err != nil {
		t.Fatalf("get completed task: %v", err)
	}
	if stored.Status != StatusCompleted {
		t.Fatalf("expected completed status, got %s", stored.Status)
	}
	if stored.Output != "done" {
		t.Fatalf("expected output %q, got %q", "done", stored.Output)
	}

	notifs := manager.Drain()
	if len(notifs) != 1 {
		t.Fatalf("expected 1 notification, got %d", len(notifs))
	}
	if notifs[0].Status != string(StatusCompleted) {
		t.Fatalf("expected completed notification, got %s", notifs[0].Status)
	}

	if notifs := manager.Drain(); len(notifs) != 0 {
		t.Fatalf("expected notifications to drain only once, got %d", len(notifs))
	}
}

func TestPeekNotificationsRequiresExplicitAck(t *testing.T) {
	manager := NewManager()
	cmd := exec.Command("sh", "-c", "printf done")

	task, err := manager.Start("task-peek", cmd, 0)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	<-task.Done

	notifs := manager.PeekNotifications()
	if len(notifs) != 1 {
		t.Fatalf("expected 1 preview notification, got %d", len(notifs))
	}
	if again := manager.PeekNotifications(); len(again) != 1 {
		t.Fatalf("expected preview to be non-destructive, got %d notifications", len(again))
	}

	manager.AckNotifications(notifs)
	if afterAck := manager.PeekNotifications(); len(afterAck) != 0 {
		t.Fatalf("expected acked notifications to disappear, got %d", len(afterAck))
	}
}

func TestCompletedTaskSpillsLargeOutput(t *testing.T) {
	manager := NewManager()
	spillDir := t.TempDir()
	task, err := manager.StartWithOptions(
		"task-large-output",
		exec.Command("sh", "-c", `printf '%70000s\n' x`),
		0,
		OutputOptions{SpillDir: spillDir},
	)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}
	<-task.Done

	stored, err := manager.Get(task.ID)
	if err != nil {
		t.Fatalf("get completed task: %v", err)
	}
	if !stored.OutputTruncated || stored.OutputPath == "" {
		t.Fatalf("expected truncated output with spill file, got %+v", stored)
	}
	if _, err := os.Stat(stored.OutputPath); err != nil {
		t.Fatalf("expected spill file to exist: %v", err)
	}
	if !strings.Contains(stored.Output, "captured output saved") {
		t.Fatalf("expected output preview to mention spill file, got %q", stored.Output)
	}
}

func TestStoredTaskCanBeReloadedAndOutputSearched(t *testing.T) {
	storeDir := t.TempDir()
	manager := NewManagerWithStore(storeDir)
	task, err := manager.StartWithOptions(
		"task-stored",
		exec.Command("sh", "-c", "printf 'alpha\\nbeta\\ngamma\\n'"),
		0,
		OutputOptions{Command: "printf lines"},
	)
	if err != nil {
		t.Fatalf("start stored task: %v", err)
	}
	<-task.Done

	if _, err := os.Stat(filepath.Join(storeDir, "task-stored", "summary.json")); err != nil {
		t.Fatalf("expected summary: %v", err)
	}
	if _, err := os.Stat(filepath.Join(storeDir, "task-stored", "output.log")); err != nil {
		t.Fatalf("expected output log: %v", err)
	}
	reloaded := NewManagerWithStore(storeDir)
	stored, err := reloaded.Get("task-stored")
	if err != nil {
		t.Fatalf("get reloaded task: %v", err)
	}
	if stored.Status != StatusCompleted || stored.Command != "printf lines" {
		t.Fatalf("unexpected reloaded task: %+v", stored)
	}
	tail, err := reloaded.ReadOutput("task-stored", OutputReadOptions{TailLines: 2})
	if err != nil {
		t.Fatalf("read tail: %v", err)
	}
	if !strings.Contains(tail.Output, "beta") || !strings.Contains(tail.Output, "gamma") {
		t.Fatalf("expected tail output, got %+v", tail)
	}
	found, err := reloaded.ReadOutput("task-stored", OutputReadOptions{Query: "beta"})
	if err != nil {
		t.Fatalf("search output: %v", err)
	}
	if found.MatchCount != 1 || !strings.Contains(found.Output, "beta") {
		t.Fatalf("expected search match, got %+v", found)
	}
}

func TestStoredRunningTaskReloadsAsInterrupted(t *testing.T) {
	storeDir := t.TempDir()
	taskDir := filepath.Join(storeDir, "task-running")
	if err := os.MkdirAll(taskDir, 0755); err != nil {
		t.Fatalf("mkdir task dir: %v", err)
	}
	summary := taskSummary{
		ID:            "task-running",
		Command:       "sleep 10",
		StartTime:     time.Now().Add(-time.Minute),
		Status:        StatusRunning,
		OutputLogPath: filepath.Join(taskDir, "output.log"),
	}
	data, err := json.Marshal(summary)
	if err != nil {
		t.Fatalf("marshal summary: %v", err)
	}
	if err := os.WriteFile(filepath.Join(taskDir, "summary.json"), data, 0644); err != nil {
		t.Fatalf("write summary: %v", err)
	}

	reloaded := NewManagerWithStore(storeDir)
	task, err := reloaded.Get("task-running")
	if err != nil {
		t.Fatalf("get task: %v", err)
	}
	if task.Status != StatusInterrupted {
		t.Fatalf("expected interrupted reload, got %+v", task)
	}
}

func TestCancelMarksTaskCanceled(t *testing.T) {
	manager := NewManager()
	cmd := exec.Command("sh", "-c", "sleep 5")

	task, err := manager.Start("task-cancel", cmd, 0)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}

	time.Sleep(100 * time.Millisecond)

	if err := manager.Cancel(task.ID); err != nil {
		t.Fatalf("cancel task: %v", err)
	}

	select {
	case <-task.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for canceled task")
	}

	stored, err := manager.Get(task.ID)
	if err != nil {
		t.Fatalf("get canceled task: %v", err)
	}
	if stored.Status != StatusCanceled {
		t.Fatalf("expected canceled status, got %s", stored.Status)
	}
}

func TestStartReturnsStartupError(t *testing.T) {
	manager := NewManager()
	if _, err := manager.Start("task-missing", exec.Command("/definitely/not/here"), 0); err == nil {
		t.Fatal("expected startup error")
	}
}

func TestTimeoutCancelsTask(t *testing.T) {
	manager := NewManager()
	task, err := manager.Start("task-timeout", exec.Command("sh", "-c", "sleep 5"), 100*time.Millisecond)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}

	select {
	case <-task.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for timeout cancellation")
	}

	stored, err := manager.Get(task.ID)
	if err != nil {
		t.Fatalf("get timed out task: %v", err)
	}
	if stored.Status != StatusCanceled {
		t.Fatalf("expected canceled status after timeout, got %s", stored.Status)
	}
}

func TestCancelKillsChildProcessesInTheSameProcessGroup(t *testing.T) {
	manager := NewManager()
	outputPath := filepath.Join(t.TempDir(), "late.txt")
	command := fmt.Sprintf("(sleep 1; printf late > %q) & wait", outputPath)

	task, err := manager.Start("task-child-cancel", exec.Command("sh", "-c", command), 0)
	if err != nil {
		t.Fatalf("start task: %v", err)
	}

	time.Sleep(100 * time.Millisecond)
	if err := manager.Cancel(task.ID); err != nil {
		t.Fatalf("cancel task: %v", err)
	}

	select {
	case <-task.Done:
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for canceled task")
	}

	time.Sleep(1200 * time.Millisecond)
	if _, err := exec.Command("sh", "-c", fmt.Sprintf("test ! -f %q", outputPath)).CombinedOutput(); err != nil {
		t.Fatalf("expected child process output file to stay absent: %v", err)
	}
}
