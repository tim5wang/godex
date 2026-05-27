package tooling

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestSplitCommandLineSupportsQuotesAndEscapes(t *testing.T) {
	argv, err := SplitCommandLine(`sh -c 'printf "%s" "$1"' sh "hello world"`)
	if err != nil {
		t.Fatalf("split command line: %v", err)
	}

	expected := []string{"sh", "-c", `printf "%s" "$1"`, "sh", "hello world"}
	if !reflect.DeepEqual(argv, expected) {
		t.Fatalf("expected argv %v, got %v", expected, argv)
	}
}

func TestSplitCommandLinePreservesWindowsPaths(t *testing.T) {
	argv, err := SplitCommandLine(`python C:\tmp\script.py "C:\Program Files\tool\config.json"`)
	if err != nil {
		t.Fatalf("split command line: %v", err)
	}

	expected := []string{"python", `C:\tmp\script.py`, `C:\Program Files\tool\config.json`}
	if !reflect.DeepEqual(argv, expected) {
		t.Fatalf("expected argv %v, got %v", expected, argv)
	}
}

func TestBuildArgvCommandExpandsHomeDirectory(t *testing.T) {
	workspace := t.TempDir()
	executor := NewWorkspaceExecutor(workspace)

	cmd, argv, err := executor.BuildArgvCommand(`bash ~/.example/script.sh`)
	if err != nil {
		t.Fatalf("build argv command: %v", err)
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		t.Fatalf("user home dir: %v", err)
	}

	expected := []string{"bash", filepath.Join(homeDir, ".example", "script.sh")}
	if !reflect.DeepEqual(argv, expected) {
		t.Fatalf("expected argv %v, got %v", expected, argv)
	}
	if filepath.Base(cmd.Path) != "bash" {
		t.Fatalf("expected command basename %q, got %q", "bash", cmd.Path)
	}
}

func TestBuildArgvCommandUsesDockerBackend(t *testing.T) {
	workspace := t.TempDir()
	executor := NewWorkspaceExecutorWithTempDirAndExecution(workspace, "", ExecutionConfig{
		Mode:          ExecutionModeDocker,
		DockerImage:   "golang:1.26",
		DockerNetwork: "none",
	})

	cmd, argv, err := executor.BuildArgvCommand(`go test ./...`)
	if err != nil {
		t.Fatalf("build docker argv command: %v", err)
	}
	if !reflect.DeepEqual(argv, []string{"go", "test", "./..."}) {
		t.Fatalf("expected original argv payload, got %v", argv)
	}
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		t.Fatalf("workspace abs: %v", err)
	}
	expectedPrefix := []string{"docker", "run", "--rm", "-v", workspaceAbs + ":/workspace", "-w", "/workspace"}
	if len(cmd.Args) < len(expectedPrefix) || !reflect.DeepEqual(cmd.Args[:len(expectedPrefix)], expectedPrefix) {
		t.Fatalf("expected docker args prefix %v, got %v", expectedPrefix, cmd.Args)
	}
	joined := strings.Join(cmd.Args, "\n")
	if !strings.Contains(joined, "\n--network\nnone\ngolang:1.26\ngo\ntest\n./...") {
		t.Fatalf("expected docker command tail with network/image/argv, got %v", cmd.Args)
	}
}

func TestRunShellRejectsIncompleteSSHBackendConfig(t *testing.T) {
	executor := NewWorkspaceExecutorWithTempDirAndExecution(t.TempDir(), "", ExecutionConfig{Mode: ExecutionModeSSH})

	_, err := executor.RunShell(context.Background(), `pwd`)
	if err == nil || !strings.Contains(err.Error(), "ssh_target") {
		t.Fatalf("expected ssh target config error, got %v", err)
	}
}

func TestBuildArgvCommandUsesSSHBackend(t *testing.T) {
	executor := NewWorkspaceExecutorWithTempDirAndExecution(t.TempDir(), "", ExecutionConfig{
		Mode:         ExecutionModeSSH,
		SSHTarget:    "builder.example",
		SSHWorkspace: "/srv/godex",
		SSHOptions:   []string{"-o", "BatchMode=yes"},
	})

	cmd, argv, err := executor.BuildArgvCommand(`go test ./...`)
	if err != nil {
		t.Fatalf("build ssh argv command: %v", err)
	}
	if !reflect.DeepEqual(argv, []string{"go", "test", "./..."}) {
		t.Fatalf("expected original argv payload, got %v", argv)
	}
	expected := []string{"ssh", "-o", "BatchMode=yes", "builder.example", "cd '/srv/godex' && exec 'go' 'test' './...'"}
	if !reflect.DeepEqual(cmd.Args, expected) {
		t.Fatalf("expected ssh command %v, got %v", expected, cmd.Args)
	}
}

func TestRunShellAllowsBash(t *testing.T) {
	executor := NewWorkspaceExecutor(t.TempDir())

	output, err := executor.RunShell(context.Background(), `bash -lc 'printf ok'`)
	if err != nil {
		t.Fatalf("run shell with bash: %v", err)
	}
	if output != "ok" {
		t.Fatalf("expected output %q, got %q", "ok", output)
	}
}

func TestRunShellBudgetedSpillsLargeOutput(t *testing.T) {
	workspace := t.TempDir()
	tempDir := filepath.Join(workspace, ".godex", ".tmp")
	executor := NewWorkspaceExecutorWithTempDir(workspace, tempDir)

	result, err := executor.RunShellBudgeted(context.Background(), `printf '%70000s\n' x`)
	if err != nil {
		t.Fatalf("run shell with large output: %v", err)
	}
	if !result.Truncated || result.FilePath == "" {
		t.Fatalf("expected truncated spill result, got %+v", result)
	}
	if len(result.Text) > int(defaultCommandOutputPreviewBytes) {
		t.Fatalf("expected preview to be capped, got %d bytes", len(result.Text))
	}
	if _, err := os.Stat(result.FilePath); err != nil {
		t.Fatalf("expected spill file to exist: %v", err)
	}
	if !strings.Contains(result.ModelText(), "captured output saved") {
		t.Fatalf("expected model text to point at spill file, got %q", result.ModelText())
	}
}

func TestRunShellValidatesEveryShellSegment(t *testing.T) {
	executor := NewWorkspaceExecutor(t.TempDir())

	output, err := executor.RunShell(context.Background(), `pwd | cat`)
	if err != nil {
		t.Fatalf("run shell pipeline: %v", err)
	}
	if strings.TrimSpace(output) == "" {
		t.Fatal("expected non-empty pipeline output")
	}

	if _, err := executor.RunShell(context.Background(), `pwd; disallowed_cmd`); err == nil || !strings.Contains(err.Error(), "command not allowed: disallowed_cmd") {
		t.Fatalf("expected disallowed command error, got %v", err)
	}
}

func TestRunShellBudgetedCanBypassUnlistedCommandsAfterApproval(t *testing.T) {
	executor := NewWorkspaceExecutor(t.TempDir())

	names, err := DisallowedShellCommands(`command -v sh`)
	if err != nil {
		t.Fatalf("detect disallowed shell commands: %v", err)
	}
	if !reflect.DeepEqual(names, []string{"command"}) {
		t.Fatalf("expected command to be disallowed, got %v", names)
	}

	if _, err := executor.RunShellBudgeted(context.Background(), `command -v sh`); err == nil || !strings.Contains(err.Error(), "command not allowed: command") {
		t.Fatalf("expected command to be rejected by default, got %v", err)
	}

	result, err := executor.RunShellBudgetedWithOptions(context.Background(), `command -v sh`, ShellCommandOptions{AllowUnlistedCommands: true})
	if err != nil {
		t.Fatalf("expected approved unlisted command to run: %v", err)
	}
	if strings.TrimSpace(result.ModelText()) == "" {
		t.Fatalf("expected approved command output, got %q", result.ModelText())
	}
}

func TestRunShellBudgetedAllowsExplicitDiagnosticCommands(t *testing.T) {
	executor := NewWorkspaceExecutor(t.TempDir())

	if _, err := executor.RunShellBudgeted(context.Background(), `command -v sh`); err == nil || !strings.Contains(err.Error(), "command not allowed: command") {
		t.Fatalf("expected command to be rejected by default, got %v", err)
	}

	result, err := executor.RunShellBudgetedWithOptions(context.Background(), `command -v sh`, ShellCommandOptions{AllowedCommands: []string{"command"}})
	if err != nil {
		t.Fatalf("expected explicit diagnostic command to run: %v", err)
	}
	if strings.TrimSpace(result.ModelText()) == "" {
		t.Fatalf("expected diagnostic command output, got %q", result.ModelText())
	}
}

func TestRunShellBudgetedIncludesExitCodeInModelText(t *testing.T) {
	executor := NewWorkspaceExecutor(t.TempDir())

	result, err := executor.RunShellBudgeted(context.Background(), `grep nope missing-file`)
	if err == nil {
		t.Fatal("expected shell command to fail")
	}
	if result.ExitCode == 0 || !strings.Contains(result.ModelText(), "[exit_code:") {
		t.Fatalf("expected model-visible exit code, got result=%+v text=%q", result, result.ModelText())
	}
}

func TestValidateShellCommandDeniesDangerousPatterns(t *testing.T) {
	executor := NewWorkspaceExecutor(t.TempDir())

	for _, command := range []string{
		`sudo ls`,
		`rm -rf /`,
		`rm -fr /*`,
		`sh -c 'rm -rf /'`,
	} {
		if _, err := executor.RunShell(context.Background(), command); err == nil || !strings.Contains(err.Error(), "dangerous shell command denied") {
			t.Fatalf("expected dangerous command denial for %q, got %v", command, err)
		}
	}
}

func TestValidateShellCommandDeniesPrivateAndMetadataURLs(t *testing.T) {
	executor := NewWorkspaceExecutor(t.TempDir())

	for _, command := range []string{
		`curl http://169.254.169.254/latest/meta-data/`,
		`wget http://127.0.0.1:8080/secrets`,
		`curl http://metadata.google.internal/computeMetadata/v1/`,
		`bash -lc 'curl http://169.254.169.254/latest/meta-data/'`,
	} {
		if _, err := executor.RunShell(context.Background(), command); err == nil || !strings.Contains(err.Error(), "shell command URL targets") {
			t.Fatalf("expected private/metadata URL denial for %q, got %v", command, err)
		}
	}
}

func TestShellPathSensitiveCommandsCannotEscapeWorkspace(t *testing.T) {
	workspace := t.TempDir()
	executor := NewWorkspaceExecutor(workspace)

	if _, err := executor.RunShell(context.Background(), `rm ../outside.txt`); err == nil || !strings.Contains(err.Error(), "path escapes workspace") {
		t.Fatalf("expected workspace escape denial, got %v", err)
	}
	if _, _, err := executor.BuildArgvCommand(`rm ../outside.txt`); err == nil || !strings.Contains(err.Error(), "path escapes workspace") {
		t.Fatalf("expected background argv workspace escape denial, got %v", err)
	}
}

func TestShellPolicyPatternsRestrictCommands(t *testing.T) {
	executor := NewWorkspaceExecutorWithTempDirAndExecution(t.TempDir(), "", ExecutionConfig{
		ShellAllowPatterns: []string{"go test*"},
		ShellDenyPatterns:  []string{"go test ./danger*"},
	})

	if _, err := executor.RunShell(context.Background(), `echo ok`); err == nil || !strings.Contains(err.Error(), "does not match any allowed") {
		t.Fatalf("expected allow pattern denial, got %v", err)
	}
	if _, err := executor.RunShell(context.Background(), `go test ./danger/...`); err == nil || !strings.Contains(err.Error(), "denied by policy pattern") {
		t.Fatalf("expected deny pattern denial, got %v", err)
	}
}

func TestBuildDockerCommandIncludesMinimalEnvAndShellPolicy(t *testing.T) {
	t.Setenv("GODEX_SHOULD_NOT_LEAK", "secret")
	workspace := t.TempDir()
	executor := NewWorkspaceExecutorWithTempDirAndExecution(workspace, "", ExecutionConfig{
		Mode:               ExecutionModeDocker,
		DockerImage:        "golang:1.26",
		ShellAllowPatterns: []string{"go test*"},
	})

	if _, _, err := executor.BuildArgvCommand(`npm test`); err == nil || !strings.Contains(err.Error(), "does not match any allowed") {
		t.Fatalf("expected docker argv shell policy denial, got %v", err)
	}
	cmd, _, err := executor.BuildArgvCommand(`go test ./...`)
	if err != nil {
		t.Fatalf("build docker argv command: %v", err)
	}
	args := strings.Join(cmd.Args, "\n")
	if !strings.Contains(args, "-e\nPATH=") {
		t.Fatalf("expected docker env forwarding in args, got %v", cmd.Args)
	}
	if strings.Contains(args, "GODEX_SHOULD_NOT_LEAK") {
		t.Fatalf("expected minimal docker env to strip custom env, got %v", cmd.Args)
	}
}

func TestRunShellUsesMinimalEnvironment(t *testing.T) {
	t.Setenv("GODEX_SHOULD_NOT_LEAK", "secret")
	executor := NewWorkspaceExecutor(t.TempDir())

	output, err := executor.RunShell(context.Background(), `sh -c 'printf "%s" "${GODEX_SHOULD_NOT_LEAK:-}"'`)
	if err != nil {
		t.Fatalf("run shell with minimal env: %v", err)
	}
	if strings.TrimSpace(output) != "" {
		t.Fatalf("expected custom environment variable to be stripped, got %q", output)
	}
}

func TestValidateShellCommandAllowsPlaywrightSkillCommands(t *testing.T) {
	for _, command := range []string{
		`playwright-cli open https://example.com`,
		`playwright-cli --raw snapshot | jq .`,
		`npx playwright-cli snapshot`,
		`npm exec playwright-cli -- snapshot`,
		`diff before.yml after.yml`,
	} {
		if err := validateShellCommand(command); err != nil {
			t.Fatalf("expected Playwright skill command to be allowed for %q, got %v", command, err)
		}
	}
}

func TestValidateShellCommandAllowsDeploymentCommands(t *testing.T) {
	for _, command := range []string{
		`ssh mycloud 'ls /root'`,
		`scp godex mycloud:/tmp/godex`,
		`rsync -av ./ mycloud:/srv/godex`,
		`sed -n '1,20p' godex.yaml`,
		`echo ok`,
	} {
		if err := validateShellCommand(command); err != nil {
			t.Fatalf("expected deployment command to be allowed for %q, got %v", command, err)
		}
	}
}

func TestRunShellRejectsUnsupportedShellConstructs(t *testing.T) {
	executor := NewWorkspaceExecutor(t.TempDir())

	for _, command := range []string{
		`pwd $(pwd)`,
		"pwd `pwd`",
		`pwd & cat`,
		`cat <(pwd)`,
	} {
		if _, err := executor.RunShell(context.Background(), command); err == nil {
			t.Fatalf("expected unsupported shell construct to be rejected for %q", command)
		}
	}
}

func TestReadFileRejectsBinaryFiles(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "book.pdf")
	data := append([]byte("%PDF-1.7\n"), 0x00, 0x01, 0x02, 0x03)
	if err := os.WriteFile(target, data, 0644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}

	executor := NewWorkspaceExecutor(workspace)
	_, err := executor.ReadFile("book.pdf", 0)
	if err == nil || !strings.Contains(err.Error(), "binary or unsupported") {
		t.Fatalf("expected binary file rejection, got %v", err)
	}
}

func TestReadFileRequiresLimitForLargeTextFiles(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "large.txt")
	large := strings.Repeat("a", readFileDefaultMaxBytes+64)
	if err := os.WriteFile(target, []byte(large), 0644); err != nil {
		t.Fatalf("write large text: %v", err)
	}

	executor := NewWorkspaceExecutor(workspace)
	_, err := executor.ReadFile("large.txt", 0)
	if err == nil || !strings.Contains(err.Error(), "too large to read without a limit") {
		t.Fatalf("expected large file rejection, got %v", err)
	}

	content, err := executor.ReadFile("large.txt", 32)
	if err != nil {
		t.Fatalf("read large file with limit: %v", err)
	}
	if got := len(content); got != 32 {
		t.Fatalf("expected truncated content length 32, got %d", got)
	}
}

func TestReadFileRangeSupportsOffsetAndStartLine(t *testing.T) {
	workspace := t.TempDir()
	target := filepath.Join(workspace, "notes.txt")
	if err := os.WriteFile(target, []byte("line1\nline2\nline3\n"), 0644); err != nil {
		t.Fatalf("write text: %v", err)
	}

	executor := NewWorkspaceExecutor(workspace)
	content, err := executor.ReadFileRange("notes.txt", 100, 0, 2, 0)
	if err != nil {
		t.Fatalf("read with start line: %v", err)
	}
	if content != "line2\nline3\n" {
		t.Fatalf("unexpected start line content: %q", content)
	}

	content, err = executor.ReadFileRange("notes.txt", 5, 6, 0, 0)
	if err != nil {
		t.Fatalf("read with offset: %v", err)
	}
	if content != "line2" {
		t.Fatalf("unexpected offset content: %q", content)
	}

	if _, err := executor.ReadFileRange("notes.txt", 5, 1, 2, 0); err == nil || !strings.Contains(err.Error(), "either offset or start_line") {
		t.Fatalf("expected mutually exclusive range error, got %v", err)
	}
}
