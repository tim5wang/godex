package logger

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLevelFiltering(t *testing.T) {
	restore := snapshotState(t)
	defer restore()

	var buf bytes.Buffer
	if err := InitWithConfig(Config{
		Level:  "warn",
		Output: &buf,
	}); err != nil {
		t.Fatalf("init logger: %v", err)
	}

	Debugf("debug message")
	Infof("info message")
	Warnf("warn message")
	Errorf("error message")

	output := buf.String()
	if strings.Contains(output, "debug message") {
		t.Fatal("expected debug message to be filtered out")
	}
	if strings.Contains(output, "info message") {
		t.Fatal("expected info message to be filtered out")
	}
	if !strings.Contains(output, "[WARN] warn message") {
		t.Fatalf("expected warn message, got %q", output)
	}
	if !strings.Contains(output, "[ERROR] error message") {
		t.Fatalf("expected error message, got %q", output)
	}
}

func TestNamedLoggerIncludesComponent(t *testing.T) {
	restore := snapshotState(t)
	defer restore()

	var buf bytes.Buffer
	if err := InitWithConfig(Config{
		Level:  "debug",
		Output: &buf,
	}); err != nil {
		t.Fatalf("init logger: %v", err)
	}

	New("agent").Debugf("step %d", 1)

	output := buf.String()
	if !strings.Contains(output, "[DEBUG] [agent] step 1") {
		t.Fatalf("expected component-prefixed debug log, got %q", output)
	}
}

func TestInitWithFileWritesLogs(t *testing.T) {
	restore := snapshotState(t)
	defer restore()

	logPath := filepath.Join(t.TempDir(), "app.log")
	if err := InitWithConfig(Config{
		Level:    "info",
		FilePath: logPath,
	}); err != nil {
		t.Fatalf("init logger with file: %v", err)
	}

	Infof("file output works")
	if err := Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(data), "[INFO] file output works") {
		t.Fatalf("expected info log in file, got %q", string(data))
	}
}

func TestInitWithFileCreatesParentDir(t *testing.T) {
	restore := snapshotState(t)
	defer restore()

	logPath := filepath.Join(t.TempDir(), "log", "godex.log")
	if err := InitWithConfig(Config{
		Level:    "info",
		FilePath: logPath,
	}); err != nil {
		t.Fatalf("init logger with nested file: %v", err)
	}

	Infof("nested file output works")
	if err := Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}
	if _, err := os.Stat(logPath); err != nil {
		t.Fatalf("expected nested log file to exist: %v", err)
	}
}

func TestInitWithFileDoesNotWriteToStderrByDefault(t *testing.T) {
	restore := snapshotState(t)
	defer restore()

	logPath := filepath.Join(t.TempDir(), "app.log")
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	defer stderrRead.Close()

	oldStderr := os.Stderr
	os.Stderr = stderrWrite
	defer func() { os.Stderr = oldStderr }()

	if err := InitWithConfig(Config{
		Level:    "info",
		FilePath: logPath,
	}); err != nil {
		t.Fatalf("init logger with file: %v", err)
	}

	Infof("file only output")
	if err := Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}
	_ = stderrWrite.Close()

	stderrData, err := io.ReadAll(stderrRead)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if len(stderrData) != 0 {
		t.Fatalf("expected no stderr output when file logging is enabled, got %q", string(stderrData))
	}

	fileData, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read log file: %v", err)
	}
	if !strings.Contains(string(fileData), "file only output") {
		t.Fatalf("expected file log output, got %q", string(fileData))
	}
}

func TestInitWithFileCanAlsoWriteToStderrWhenRequested(t *testing.T) {
	restore := snapshotState(t)
	defer restore()

	logPath := filepath.Join(t.TempDir(), "app.log")
	stderrRead, stderrWrite, err := os.Pipe()
	if err != nil {
		t.Fatalf("create stderr pipe: %v", err)
	}
	defer stderrRead.Close()

	oldStderr := os.Stderr
	os.Stderr = stderrWrite
	defer func() { os.Stderr = oldStderr }()

	if err := InitWithConfig(Config{
		Level:      "info",
		FilePath:   logPath,
		AlsoStderr: true,
	}); err != nil {
		t.Fatalf("init logger with stderr mirror: %v", err)
	}

	Infof("mirrored output")
	if err := Close(); err != nil {
		t.Fatalf("close logger: %v", err)
	}
	_ = stderrWrite.Close()

	stderrData, err := io.ReadAll(stderrRead)
	if err != nil {
		t.Fatalf("read stderr: %v", err)
	}
	if !strings.Contains(string(stderrData), "mirrored output") {
		t.Fatalf("expected stderr output when AlsoStderr is enabled, got %q", string(stderrData))
	}
}

func snapshotState(t *testing.T) func() {
	t.Helper()

	mu.Lock()
	oldLevel := currentLevel
	oldFlags := currentFlags
	oldLogger := baseLogger
	oldFile := logFile
	mu.Unlock()

	return func() {
		mu.Lock()
		defer mu.Unlock()

		if logFile != nil && logFile != oldFile {
			_ = logFile.Close()
		}
		currentLevel = oldLevel
		currentFlags = oldFlags
		baseLogger = oldLogger
		logFile = oldFile
	}
}
