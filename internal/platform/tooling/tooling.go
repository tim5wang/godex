package tooling

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/platform/workspacefs"
)

type Definition struct {
	Name        string
	Description string
	InputSchema map[string]interface{}
}

func (d Definition) Schema() map[string]interface{} {
	return map[string]interface{}{
		"name":         d.Name,
		"description":  d.Description,
		"input_schema": cloneMap(d.InputSchema),
	}
}

func (d Definition) ToolSchema() protocol.ToolSchema {
	return protocol.ToolSchema{
		Name:        d.Name,
		Description: d.Description,
		InputSchema: cloneMap(d.InputSchema),
	}
}

func BashDefinition() Definition {
	return Definition{
		Name:        "bash",
		Description: "Run a shell command from the workspace root",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]string{"type": "string"},
			},
			"required": []string{"command"},
		},
	}
}

func ReadFileDefinition() Definition {
	return Definition{
		Name:        "read_file",
		Description: "Read UTF-8 text file contents from a workspace-relative path. Do not use for binary or large files such as PDFs, images, media, or archives.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path":       map[string]interface{}{"type": "string", "description": "Workspace-relative path such as agent/agent.go"},
				"limit":      map[string]interface{}{"type": "integer", "description": "Maximum bytes to return. Required for large files."},
				"offset":     map[string]interface{}{"type": "integer", "description": "Optional byte offset to start reading from. Use either offset or start_line, not both."},
				"start_line": map[string]interface{}{"type": "integer", "description": "Optional 1-based line number to start reading from. Use either start_line or offset, not both."},
			},
			"required": []string{"path"},
		},
	}
}

func WriteFileDefinition() Definition {
	return Definition{
		Name:        "write_file",
		Description: "Write content to a workspace-relative path",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path":    map[string]interface{}{"type": "string", "description": "Workspace-relative path such as notes/todo.txt"},
				"content": map[string]string{"type": "string"},
			},
			"required": []string{"path", "content"},
		},
	}
}

func EditFileDefinition() Definition {
	return Definition{
		Name:        "edit_file",
		Description: "Replace exact text in a file at a workspace-relative path",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path":     map[string]interface{}{"type": "string", "description": "Workspace-relative path such as skill/skill.go"},
				"old_text": map[string]string{"type": "string"},
				"new_text": map[string]string{"type": "string"},
			},
			"required": []string{"path", "old_text", "new_text"},
		},
	}
}

func AttachFileDefinition() Definition {
	return Definition{
		Name:        "attach_file",
		Description: "Attach a local workspace file to the current session reply without reading its contents. Use for screenshots, PDFs, downloads, and other files that should be sent as attachments.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Workspace-relative path such as .godex/.tmp/report.pdf",
				},
			},
			"required": []string{"path"},
		},
	}
}

func BackgroundRunDefinition() Definition {
	return Definition{
		Name:        "background_run",
		Description: "Run an executable with argv-style arguments in a background task. Quotes are supported; shell operators are not.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"command": map[string]string{"type": "string", "description": "Executable plus argv-style arguments, for example: go test ./..."},
				"timeout": map[string]string{"type": "integer", "description": "Optional timeout in seconds"},
			},
			"required": []string{"command"},
		},
	}
}

func SupportedToolSchemas(names ...string) []protocol.ToolSchema {
	result := make([]protocol.ToolSchema, 0, len(names))
	for _, name := range names {
		switch name {
		case "bash":
			result = append(result, BashDefinition().ToolSchema())
		case "read_file":
			result = append(result, ReadFileDefinition().ToolSchema())
		case "write_file":
			result = append(result, WriteFileDefinition().ToolSchema())
		case "edit_file":
			result = append(result, EditFileDefinition().ToolSchema())
		case "attach_file":
			result = append(result, AttachFileDefinition().ToolSchema())
		case "background_run":
			result = append(result, BackgroundRunDefinition().ToolSchema())
		}
	}
	return result
}

type WorkspaceExecutor struct {
	WorkspaceDir string
	TempDir      string
	Execution    ExecutionConfig
}

type ShellCommandOptions struct {
	AllowUnlistedCommands bool
	WorkspaceDir          string
	AllowPatterns         []string
	DenyPatterns          []string
	DenyHighRisk          bool
	AllowedCommands       []string
}

const (
	readFileBinarySampleBytes = 8192
	readFileDefaultMaxBytes   = 256 * 1024
)

const (
	ExecutionModeLocal  = "local"
	ExecutionModeDocker = "docker"
	ExecutionModeSSH    = "ssh"
)

type ExecutionConfig struct {
	Mode               string
	DockerImage        string
	DockerNetwork      string
	SSHTarget          string
	SSHWorkspace       string
	SSHOptions         []string
	ShellAllowPatterns []string
	ShellDenyPatterns  []string
}

func NewWorkspaceExecutor(workspaceDir string) *WorkspaceExecutor {
	return &WorkspaceExecutor{
		WorkspaceDir: workspaceDir,
		TempDir:      DefaultCommandOutputDir(workspaceDir),
		Execution:    normalizeExecutionConfig(ExecutionConfig{}),
	}
}

func NewWorkspaceExecutorWithTempDir(workspaceDir, tempDir string) *WorkspaceExecutor {
	if strings.TrimSpace(tempDir) == "" {
		tempDir = DefaultCommandOutputDir(workspaceDir)
	}
	return NewWorkspaceExecutorWithTempDirAndExecution(workspaceDir, tempDir, ExecutionConfig{})
}

func NewWorkspaceExecutorWithTempDirAndExecution(workspaceDir, tempDir string, execution ExecutionConfig) *WorkspaceExecutor {
	if strings.TrimSpace(tempDir) == "" {
		tempDir = DefaultCommandOutputDir(workspaceDir)
	}
	return &WorkspaceExecutor{
		WorkspaceDir: workspaceDir,
		TempDir:      tempDir,
		Execution:    normalizeExecutionConfig(execution),
	}
}

func normalizeExecutionConfig(cfg ExecutionConfig) ExecutionConfig {
	mode := strings.ToLower(strings.TrimSpace(cfg.Mode))
	if mode == "" {
		mode = ExecutionModeLocal
	}
	image := strings.TrimSpace(cfg.DockerImage)
	if image == "" {
		image = "golang:1.26"
	}
	return ExecutionConfig{
		Mode:               mode,
		DockerImage:        image,
		DockerNetwork:      strings.TrimSpace(cfg.DockerNetwork),
		SSHTarget:          strings.TrimSpace(cfg.SSHTarget),
		SSHWorkspace:       strings.TrimSpace(cfg.SSHWorkspace),
		SSHOptions:         append([]string{}, cfg.SSHOptions...),
		ShellAllowPatterns: normalizeShellPatterns(cfg.ShellAllowPatterns),
		ShellDenyPatterns:  normalizeShellPatterns(cfg.ShellDenyPatterns),
	}
}

func (e *WorkspaceExecutor) ExecutionBackend() string {
	return normalizeExecutionConfig(e.Execution).Mode
}

func (e *WorkspaceExecutor) RunShell(ctx context.Context, command string) (string, error) {
	result, err := e.RunShellBudgeted(ctx, command)
	return result.ModelText(), err
}

func (e *WorkspaceExecutor) RunShellBudgeted(ctx context.Context, command string) (CommandOutputResult, error) {
	return e.RunShellBudgetedWithOptions(ctx, command, ShellCommandOptions{})
}

func (e *WorkspaceExecutor) RunShellBudgetedWithOptions(ctx context.Context, command string, options ShellCommandOptions) (CommandOutputResult, error) {
	options = e.shellOptions(options)
	if strings.TrimSpace(options.WorkspaceDir) == "" {
		options.WorkspaceDir = e.WorkspaceDir
	}
	if err := validateShellCommandWithOptions(command, options); err != nil {
		return CommandOutputResult{}, err
	}

	cmd, err := e.shellCommand(ctx, command)
	if err != nil {
		return CommandOutputResult{}, err
	}
	output := NewOutputCapture(CommandOutputOptions{
		SpillDir:    filepath.Join(e.TempDir, "shell"),
		SpillPrefix: "bash-",
	})
	cmd.Stdout = output
	cmd.Stderr = output
	err = cmd.Run()
	closeErr := output.Close()
	if err == nil {
		err = closeErr
	}
	result := output.Result()
	result.ExitCode = shellExitCode(err)
	return result, err
}

func (e *WorkspaceExecutor) BuildArgvCommand(command string) (*exec.Cmd, []string, error) {
	return e.BuildArgvCommandWithOptions(command, ShellCommandOptions{})
}

func (e *WorkspaceExecutor) BuildArgvCommandWithOptions(command string, options ShellCommandOptions) (*exec.Cmd, []string, error) {
	options = e.shellOptions(options)
	argv, err := SplitCommandLine(command)
	if err != nil {
		return nil, nil, err
	}
	if strings.TrimSpace(options.WorkspaceDir) == "" {
		options.WorkspaceDir = e.WorkspaceDir
	}
	if err := validateArgvCommandWithOptions(argv, options); err != nil {
		return nil, nil, err
	}
	argv, err = expandHomeArgs(argv)
	if err != nil {
		return nil, nil, err
	}
	cmd, err := e.argvCommand(argv)
	if err != nil {
		return nil, nil, err
	}
	return cmd, argv, nil
}

func (e *WorkspaceExecutor) shellOptions(options ShellCommandOptions) ShellCommandOptions {
	cfg := normalizeExecutionConfig(e.Execution)
	if len(options.AllowPatterns) == 0 {
		options.AllowPatterns = append([]string{}, cfg.ShellAllowPatterns...)
	}
	if len(options.DenyPatterns) == 0 {
		options.DenyPatterns = append([]string{}, cfg.ShellDenyPatterns...)
	}
	if strings.TrimSpace(options.WorkspaceDir) == "" {
		options.WorkspaceDir = e.WorkspaceDir
	}
	options.AllowPatterns = normalizeShellPatterns(options.AllowPatterns)
	options.DenyPatterns = normalizeShellPatterns(options.DenyPatterns)
	return options
}

func (e *WorkspaceExecutor) shellCommand(ctx context.Context, command string) (*exec.Cmd, error) {
	cfg := normalizeExecutionConfig(e.Execution)
	switch cfg.Mode {
	case ExecutionModeLocal:
		shell, shellArg := shellCommand()
		cmd := exec.CommandContext(ctx, shell, shellArg, command)
		cmd.Dir = e.WorkspaceDir
		cmd.Env = minimalCommandEnv(e.WorkspaceDir)
		return cmd, nil
	case ExecutionModeDocker:
		return e.dockerShellCommand(ctx, cfg, command)
	case ExecutionModeSSH:
		return e.sshShellCommand(ctx, cfg, command)
	default:
		return nil, fmt.Errorf("unsupported execution backend %q", cfg.Mode)
	}
}

func (e *WorkspaceExecutor) argvCommand(argv []string) (*exec.Cmd, error) {
	cfg := normalizeExecutionConfig(e.Execution)
	switch cfg.Mode {
	case ExecutionModeLocal:
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = e.WorkspaceDir
		cmd.Env = minimalCommandEnv(e.WorkspaceDir)
		return cmd, nil
	case ExecutionModeDocker:
		return e.dockerArgvCommand(cfg, argv)
	case ExecutionModeSSH:
		return e.sshArgvCommand(cfg, argv)
	default:
		return nil, fmt.Errorf("unsupported execution backend %q", cfg.Mode)
	}
}

func (e *WorkspaceExecutor) dockerShellCommand(ctx context.Context, cfg ExecutionConfig, command string) (*exec.Cmd, error) {
	workspace, err := filepath.Abs(e.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	args := []string{
		"run",
		"--rm",
		"-v", workspace + ":/workspace",
		"-w", "/workspace",
	}
	args = appendDockerEnvArgs(args)
	if cfg.DockerNetwork != "" {
		args = append(args, "--network", cfg.DockerNetwork)
	}
	args = append(args, cfg.DockerImage, "sh", "-c", command)
	return exec.CommandContext(ctx, "docker", args...), nil
}

func (e *WorkspaceExecutor) dockerArgvCommand(cfg ExecutionConfig, argv []string) (*exec.Cmd, error) {
	workspace, err := filepath.Abs(e.WorkspaceDir)
	if err != nil {
		return nil, err
	}
	args := []string{
		"run",
		"--rm",
		"-v", workspace + ":/workspace",
		"-w", "/workspace",
	}
	args = appendDockerEnvArgs(args)
	if cfg.DockerNetwork != "" {
		args = append(args, "--network", cfg.DockerNetwork)
	}
	args = append(args, cfg.DockerImage)
	args = append(args, argv...)
	return exec.Command("docker", args...), nil
}

var (
	// shellCommand returns the OS-native shell and -c flag for RunShell.
	shellCommand = func() (shell, flag string) {
		return "sh", "-c"
	}
)

func init() {
	if runtime.GOOS == "windows" {
		shellCommand = func() (shell, flag string) {
			return "cmd", "/c"
		}
	}
}

func (e *WorkspaceExecutor) sshShellCommand(ctx context.Context, cfg ExecutionConfig, command string) (*exec.Cmd, error) {
	args, workspace, err := sshBaseArgs(cfg)
	if err != nil {
		return nil, err
	}
	args = append(args, "cd "+shellQuote(workspace)+" && sh -lc "+shellQuote(command))
	return exec.CommandContext(ctx, "ssh", args...), nil
}

func (e *WorkspaceExecutor) sshArgvCommand(cfg ExecutionConfig, argv []string) (*exec.Cmd, error) {
	args, workspace, err := sshBaseArgs(cfg)
	if err != nil {
		return nil, err
	}
	args = append(args, "cd "+shellQuote(workspace)+" && exec "+shellJoin(argv))
	return exec.Command("ssh", args...), nil
}

func sshBaseArgs(cfg ExecutionConfig) ([]string, string, error) {
	target := strings.TrimSpace(cfg.SSHTarget)
	if target == "" {
		return nil, "", fmt.Errorf("ssh execution backend requires tools.execution.ssh_target")
	}
	workspace := strings.TrimSpace(cfg.SSHWorkspace)
	if workspace == "" {
		return nil, "", fmt.Errorf("ssh execution backend requires tools.execution.ssh_workspace")
	}
	args := append([]string{}, cfg.SSHOptions...)
	args = append(args, target)
	return args, workspace, nil
}

func shellJoin(argv []string) string {
	parts := make([]string, 0, len(argv))
	for _, item := range argv {
		parts = append(parts, shellQuote(item))
	}
	return strings.Join(parts, " ")
}

func shellQuote(value string) string {
	if value == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (e *WorkspaceExecutor) ReadFile(path string, limit int) (string, error) {
	return e.ReadFileRange(path, limit, 0, 0)
}

func (e *WorkspaceExecutor) ReadFileRange(path string, limit, offset, startLine int) (string, error) {
	if offset < 0 {
		return "", fmt.Errorf("offset must be non-negative")
	}
	if startLine < 0 {
		return "", fmt.Errorf("start_line must be non-negative")
	}
	if offset > 0 && startLine > 0 {
		return "", fmt.Errorf("use either offset or start_line, not both")
	}

	root, err := workspacefs.New(e.WorkspaceDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	absPath, err := root.Abs(path)
	if err != nil {
		return "", err
	}
	info, err := root.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("path is a directory: %s", path)
	}

	file, err := root.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	sampleLimit := readFileBinarySampleBytes
	if info.Size() > 0 && info.Size() < int64(sampleLimit) {
		sampleLimit = int(info.Size())
	}
	sample, err := io.ReadAll(io.LimitReader(file, int64(sampleLimit)))
	if err != nil {
		return "", err
	}
	if looksLikeBinaryFile(absPath, sample) {
		return "", fmt.Errorf("read_file only supports text files; %s appears to be binary or unsupported (for example PDF, image, media, or archive). Treat it as a file to copy, attach, or send instead of reading its contents", path)
	}

	readLimit := limit
	if readLimit <= 0 {
		if info.Size() > readFileDefaultMaxBytes {
			return "", fmt.Errorf("file is too large to read without a limit (%d bytes). Provide a small limit or use shell tools such as rg, sed, or head", info.Size())
		}
		readLimit = readFileDefaultMaxBytes
	}
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	var reader io.Reader = file
	if startLine > 1 {
		lineReader := bufio.NewReader(file)
		for line := 1; line < startLine; line++ {
			if _, err := lineReader.ReadString('\n'); err != nil {
				if err == io.EOF {
					return "", nil
				}
				return "", err
			}
		}
		reader = lineReader
	} else if offset > 0 {
		if _, err := file.Seek(int64(offset), io.SeekStart); err != nil {
			return "", err
		}
	}
	data, err := io.ReadAll(io.LimitReader(reader, int64(readLimit)+1))
	if err != nil {
		return "", err
	}
	if len(data) > readLimit {
		data = data[:readLimit]
	}
	return string(data), nil
}

func (e *WorkspaceExecutor) WriteFile(path, content string) (string, error) {
	root, err := workspacefs.New(e.WorkspaceDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if err := root.WriteFile(path, []byte(content), 0644); err != nil {
		return "", err
	}
	return "OK", nil
}

func (e *WorkspaceExecutor) EditFile(path, oldText, newText string) (string, error) {
	root, err := workspacefs.New(e.WorkspaceDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
	if oldText == "" {
		return "", fmt.Errorf("missing old_text argument")
	}
	data, err := root.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(data)
	if !strings.Contains(content, oldText) {
		return "", fmt.Errorf("old_text not found in file: %s", preview(oldText))
	}
	newContent := strings.Replace(content, oldText, newText, 1)
	if err := root.WriteFile(path, []byte(newContent), 0644); err != nil {
		return "", err
	}
	return "OK", nil
}

func looksLikeBinaryFile(path string, sample []byte) bool {
	if isKnownBinaryExtension(strings.ToLower(filepath.Ext(path))) {
		return true
	}
	if len(sample) == 0 {
		return false
	}
	if bytesContainNUL(sample) {
		return true
	}

	contentType := http.DetectContentType(sample)
	if isKnownBinaryContentType(contentType) {
		return true
	}
	if !utf8.Valid(sample) {
		return true
	}

	controlCount := 0
	for _, r := range string(sample) {
		if r == '\n' || r == '\r' || r == '\t' {
			continue
		}
		if unicode.IsControl(r) {
			controlCount++
		}
	}
	return controlCount > 0 && controlCount*10 > len([]rune(string(sample)))
}

func bytesContainNUL(data []byte) bool {
	for _, b := range data {
		if b == 0 {
			return true
		}
	}
	return false
}

func isKnownBinaryExtension(ext string) bool {
	switch ext {
	case ".pdf", ".png", ".jpg", ".jpeg", ".gif", ".webp", ".bmp", ".ico", ".tiff", ".heic", ".avif",
		".mp3", ".wav", ".m4a", ".ogg", ".flac", ".mp4", ".mov", ".avi", ".mkv", ".webm",
		".zip", ".tar", ".gz", ".tgz", ".bz2", ".xz", ".7z", ".rar",
		".doc", ".docx", ".ppt", ".pptx", ".xls", ".xlsx",
		".sqlite", ".db", ".bin", ".exe", ".dll", ".so", ".dylib", ".jar", ".class", ".pyc":
		return true
	default:
		return false
	}
}

func isKnownBinaryContentType(contentType string) bool {
	contentType = strings.ToLower(strings.TrimSpace(contentType))
	switch {
	case strings.HasPrefix(contentType, "text/"):
		return false
	case strings.HasPrefix(contentType, "image/svg+xml"):
		return false
	case strings.HasPrefix(contentType, "application/json"),
		strings.HasPrefix(contentType, "application/xml"),
		strings.HasPrefix(contentType, "application/yaml"),
		strings.HasPrefix(contentType, "application/x-yaml"),
		strings.HasPrefix(contentType, "application/javascript"),
		strings.HasPrefix(contentType, "application/x-javascript"):
		return false
	case strings.HasPrefix(contentType, "application/pdf"),
		strings.HasPrefix(contentType, "image/"),
		strings.HasPrefix(contentType, "audio/"),
		strings.HasPrefix(contentType, "video/"),
		strings.HasPrefix(contentType, "application/zip"),
		strings.HasPrefix(contentType, "application/octet-stream"),
		strings.HasPrefix(contentType, "application/x-rar"),
		strings.HasPrefix(contentType, "application/vnd"),
		strings.HasPrefix(contentType, "font/"):
		return true
	default:
		return false
	}
}

func SplitCommandLine(input string) ([]string, error) {
	input = strings.TrimSpace(input)
	if input == "" {
		return nil, fmt.Errorf("empty command")
	}

	args := make([]string, 0)
	var current strings.Builder
	var quote rune
	runes := []rune(input)

	flush := func() {
		if current.Len() == 0 {
			return
		}
		args = append(args, current.String())
		current.Reset()
	}

	for i := 0; i < len(runes); i++ {
		r := runes[i]

		if quote != 0 {
			if r == quote {
				quote = 0
				continue
			}
			if r == '\\' {
				if next, ok := escapedRune(runes, i, quote); ok {
					current.WriteRune(next)
					i++
					continue
				}
			}
			current.WriteRune(r)
			continue
		}

		switch {
		case r == '\\':
			if next, ok := escapedRune(runes, i, 0); ok {
				current.WriteRune(next)
				i++
				continue
			}
			current.WriteRune(r)
		case r == '\'' || r == '"':
			quote = r
		case unicode.IsSpace(r):
			flush()
		default:
			current.WriteRune(r)
		}
	}
	if quote != 0 {
		return nil, fmt.Errorf("unterminated quote")
	}
	flush()
	if len(args) == 0 {
		return nil, fmt.Errorf("empty command")
	}
	return args, nil
}

func escapedRune(runes []rune, index int, quote rune) (rune, bool) {
	if index+1 >= len(runes) {
		return 0, false
	}
	next := runes[index+1]
	if next == '\\' || next == '\'' || next == '"' || unicode.IsSpace(next) {
		if quote == 0 || next == quote || next == '\\' || unicode.IsSpace(next) {
			return next, true
		}
	}
	return 0, false
}

func validateShellCommand(command string) error {
	return validateShellCommandWithOptions(command, ShellCommandOptions{})
}

// ValidateShellCommand applies the same shell safety checks used before local,
// Docker, or SSH execution. It is intended for declaration-time diagnostics
// that must not execute the command.
func ValidateShellCommand(command string) error {
	return validateShellCommand(command)
}

// ValidateShellCommandWithPolicy applies shell validation with explicit policy
// options. It is used by safety profiles and package smoke diagnostics.
func ValidateShellCommandWithPolicy(command string, options ShellCommandOptions) error {
	return validateShellCommandWithOptions(command, options)
}

func validateShellCommandWithOptions(command string, options ShellCommandOptions) error {
	if err := validateShellPatterns(command, options); err != nil {
		return err
	}
	if options.DenyHighRisk {
		if risk := ClassifyShellCommandRisk(command); risk.Level == ShellRiskHigh {
			return fmt.Errorf("high-risk shell command denied: %s", risk.Reason)
		}
	}
	segments, err := splitShellCommand(command)
	if err != nil {
		return err
	}
	if len(segments) == 0 {
		return fmt.Errorf("empty command")
	}

	for _, segment := range segments {
		name, err := firstShellCommandName(segment)
		if err != nil {
			return err
		}
		if name == "" {
			continue
		}
		argv, err := SplitCommandLine(segment)
		if err != nil {
			return err
		}
		if err := validateCommandSafety(name, argv, options); err != nil {
			return err
		}
		if !options.AllowUnlistedCommands && !allowedCommandWithOptions(name, options) {
			return fmt.Errorf("command not allowed: %s", name)
		}
	}
	return nil
}

func validateArgvCommandWithOptions(argv []string, options ShellCommandOptions) error {
	if len(argv) == 0 {
		return fmt.Errorf("empty command")
	}
	command := strings.Join(argv, " ")
	if err := validateShellPatterns(command, options); err != nil {
		return err
	}
	if options.DenyHighRisk {
		if risk := ClassifyShellCommandRisk(command); risk.Level == ShellRiskHigh {
			return fmt.Errorf("high-risk shell command denied: %s", risk.Reason)
		}
	}
	name := strings.TrimSpace(argv[0])
	if name == "" {
		return fmt.Errorf("empty command")
	}
	if err := validateCommandSafety(name, argv, options); err != nil {
		return err
	}
	if !options.AllowUnlistedCommands && !allowedCommandWithOptions(name, options) {
		return fmt.Errorf("command not allowed: %s", name)
	}
	return nil
}

func validateShellPatterns(command string, options ShellCommandOptions) error {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil
	}
	for _, pattern := range options.DenyPatterns {
		if shellPatternMatches(pattern, command) {
			return fmt.Errorf("shell command denied by policy pattern %q", pattern)
		}
	}
	if len(options.AllowPatterns) == 0 {
		return nil
	}
	for _, pattern := range options.AllowPatterns {
		if shellPatternMatches(pattern, command) {
			return nil
		}
	}
	return fmt.Errorf("shell command does not match any allowed policy pattern")
}

func shellPatternMatches(pattern, command string) bool {
	pattern = strings.TrimSpace(pattern)
	command = strings.TrimSpace(command)
	if pattern == "" {
		return false
	}
	if pattern == "*" {
		return true
	}
	if ok, err := filepath.Match(pattern, command); err == nil && ok {
		return true
	}
	if strings.HasSuffix(pattern, "*") && strings.HasPrefix(command, strings.TrimSuffix(pattern, "*")) {
		return true
	}
	return strings.EqualFold(pattern, command)
}

type ShellRiskLevel string

const (
	ShellRiskLow    ShellRiskLevel = "low"
	ShellRiskMedium ShellRiskLevel = "medium"
	ShellRiskHigh   ShellRiskLevel = "high"
)

type ShellRisk struct {
	Level  ShellRiskLevel
	Reason string
}

func ClassifyShellCommandRisk(command string) ShellRisk {
	command = strings.TrimSpace(command)
	if command == "" {
		return ShellRisk{Level: ShellRiskLow}
	}
	lower := strings.ToLower(command)
	switch {
	case strings.Contains(lower, "<(") || strings.Contains(lower, ">("):
		return ShellRisk{Level: ShellRiskHigh, Reason: "process substitution can hide executed input"}
	case downloadsPipedToShell(lower):
		return ShellRisk{Level: ShellRiskHigh, Reason: "downloaded content is piped directly into a shell"}
	case base64DecodedToShell(lower):
		return ShellRisk{Level: ShellRiskHigh, Reason: "base64-decoded content is piped directly into a shell"}
	}
	segments, err := splitShellCommand(command)
	if err != nil {
		return ShellRisk{Level: ShellRiskHigh, Reason: err.Error()}
	}
	for _, segment := range segments {
		argv, err := SplitCommandLine(segment)
		if err != nil || len(argv) == 0 {
			continue
		}
		name := filepath.Base(strings.ToLower(strings.TrimSpace(argv[0])))
		switch name {
		case "python", "python3", "node":
			if inlineExecFlag(argv[1:]) {
				return ShellRisk{Level: ShellRiskHigh, Reason: name + " inline code execution requires review"}
			}
		case "ruby", "perl", "php":
			if inlineExecFlag(argv[1:]) {
				return ShellRisk{Level: ShellRiskHigh, Reason: name + " inline code execution requires review"}
			}
		}
	}
	return ShellRisk{Level: ShellRiskLow}
}

func downloadsPipedToShell(command string) bool {
	parts := strings.Split(command, "|")
	if len(parts) < 2 {
		return false
	}
	for idx := 0; idx < len(parts)-1; idx++ {
		left := strings.TrimSpace(parts[idx])
		right := strings.TrimSpace(parts[idx+1])
		if (strings.HasPrefix(left, "curl ") || strings.HasPrefix(left, "wget ")) && startsShell(right) {
			return true
		}
	}
	return false
}

func base64DecodedToShell(command string) bool {
	parts := strings.Split(command, "|")
	if len(parts) < 2 {
		return false
	}
	for idx := 0; idx < len(parts)-1; idx++ {
		left := strings.TrimSpace(parts[idx])
		right := strings.TrimSpace(parts[idx+1])
		if strings.Contains(left, "base64") && (strings.Contains(left, "-d") || strings.Contains(left, "--decode")) && startsShell(right) {
			return true
		}
	}
	return false
}

func startsShell(command string) bool {
	command = strings.TrimSpace(command)
	return strings.HasPrefix(command, "sh") || strings.HasPrefix(command, "bash") || strings.HasPrefix(command, "zsh") ||
		strings.HasPrefix(command, "cmd") || strings.HasPrefix(command, "powershell") || strings.HasPrefix(command, "pwsh")
}

func inlineExecFlag(args []string) bool {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "-c" || arg == "-e" || arg == "/c" || arg == "/C" {
			return true
		}
		if strings.HasPrefix(arg, "-") && (strings.Contains(arg, "c") || strings.Contains(arg, "e")) {
			return true
		}
	}
	return false
}

func DisallowedShellCommands(command string) ([]string, error) {
	return DisallowedShellCommandsWithOptions(command, ShellCommandOptions{})
}

func DisallowedShellCommandsWithOptions(command string, options ShellCommandOptions) ([]string, error) {
	segments, err := splitShellCommand(command)
	if err != nil {
		return nil, err
	}
	seen := map[string]struct{}{}
	var names []string
	for _, segment := range segments {
		name, err := firstShellCommandName(segment)
		if err != nil {
			return nil, err
		}
		if name == "" || allowedCommandWithOptions(name, options) {
			continue
		}
		if _, ok := seen[name]; ok {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names, nil
}

func validateCommandSafety(name string, argv []string, options ShellCommandOptions) error {
	base := filepath.Base(strings.TrimSpace(name))
	switch base {
	case "sudo", "su", "shutdown", "reboot", "halt", "poweroff", "mkfs", "mount", "umount":
		return fmt.Errorf("dangerous shell command denied: %s", base)
	case "rm":
		if hasRecursiveForce(argv[1:]) && targetsRoot(argv[1:]) {
			return fmt.Errorf("dangerous shell command denied: rm -rf targeting root")
		}
	case "sh", "bash":
		if script := shellScriptArg(argv[1:]); script != "" {
			if err := validateShellCommandWithOptions(script, options); err != nil {
				return err
			}
		}
	case "cmd":
		if script := cmdScriptArg(argv[1:]); script != "" {
			if err := validateShellCommandWithOptions(script, options); err != nil {
				return err
			}
		}
	case "powershell", "pwsh":
		if script := powershellScriptArg(argv[1:]); script != "" {
			if err := validateShellCommandWithOptions(script, options); err != nil {
				return err
			}
		}
	}
	for _, arg := range argv[1:] {
		if err := validateShellURLArg(arg); err != nil {
			return err
		}
	}
	if isLocalPathSensitiveCommand(base, argv) {
		if err := validateWorkspacePathArgs(options.WorkspaceDir, base, argv[1:]); err != nil {
			return err
		}
	}
	return nil
}

func shellScriptArg(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "-c":
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		case strings.HasPrefix(arg, "-") && strings.Contains(arg, "c"):
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
	}
	return ""
}

func cmdScriptArg(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "/c" || arg == "/C":
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
	}
	return ""
}

func powershellScriptArg(args []string) string {
	for i := 0; i < len(args); i++ {
		arg := strings.TrimSpace(args[i])
		switch {
		case arg == "-Command" || arg == "-command" || arg == "-c":
			if i+1 < len(args) {
				return args[i+1]
			}
			return ""
		}
	}
	return ""
}

func hasRecursiveForce(args []string) bool {
	recursive := false
	force := false
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") || arg == "-" || arg == "--" {
			continue
		}
		if strings.Contains(arg, "r") || strings.Contains(arg, "R") {
			recursive = true
		}
		if strings.Contains(arg, "f") {
			force = true
		}
	}
	return recursive && force
}

func targetsRoot(args []string) bool {
	for _, arg := range args {
		arg = strings.TrimSpace(arg)
		if arg == "" || strings.HasPrefix(arg, "-") {
			continue
		}
		if arg == "/" || arg == "/*" || arg == "/." || arg == "/.." {
			return true
		}
		// Windows root check (e.g., C:\, D:\)
		if len(arg) >= 2 && arg[1] == ':' && (len(arg) == 2 || arg[2] == '\\' || arg[2] == '/') {
			cleaned := filepath.Clean(arg)
			if len(cleaned) <= 3 { // e.g., "C:\" or "C:"
				return true
			}
		}
		cleaned := filepath.Clean(strings.TrimRight(arg, "/*\\"))
		if cleaned == string(os.PathSeparator) || cleaned == "~" {
			return true
		}
	}
	return false
}

func validateShellURLArg(arg string) error {
	raw := strings.Trim(strings.TrimSpace(arg), `"'`)
	if raw == "" {
		return nil
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return nil
	}
	switch strings.ToLower(parsed.Scheme) {
	case "http", "https":
	default:
		return nil
	}
	host := strings.ToLower(parsed.Hostname())
	if isMetadataHost(host) {
		return fmt.Errorf("shell command URL targets cloud metadata host: %s", host)
	}
	ip := net.ParseIP(host)
	if ip != nil && isPrivateOrLocalIP(ip) {
		return fmt.Errorf("shell command URL targets private or local network address: %s", host)
	}
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("shell command URL targets private or local network address: %s", host)
	}
	return nil
}

func isMetadataHost(host string) bool {
	switch host {
	case "169.254.169.254", "169.254.170.2", "100.100.100.200", "metadata", "metadata.google.internal":
		return true
	default:
		return false
	}
}

func isPrivateOrLocalIP(ip net.IP) bool {
	return ip.IsPrivate() || ip.IsLoopback() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

func isLocalPathSensitiveCommand(name string, argv []string) bool {
	switch name {
	case "rm", "cp", "mv", "chmod", "chown", "mkdir", "touch":
		return true
	case "sed":
		for _, arg := range argv[1:] {
			if arg == "-i" || strings.HasPrefix(arg, "-i") {
				return true
			}
		}
	}
	return false
}

func validateWorkspacePathArgs(workspace, command string, args []string) error {
	workspace = strings.TrimSpace(workspace)
	if workspace == "" {
		return nil
	}
	workspaceAbs, err := filepath.Abs(workspace)
	if err != nil {
		return err
	}
	skipNext := false
	for _, arg := range args {
		if skipNext {
			skipNext = false
			continue
		}
		arg = strings.Trim(strings.TrimSpace(arg), `"'`)
		if arg == "" || arg == "--" {
			continue
		}
		if redirectionConsumesNextToken(arg) {
			skipNext = true
			continue
		}
		if strings.HasPrefix(arg, "-") || isShellRedirectionToken(arg) || looksLikeRemotePath(arg) || looksLikeURL(arg) {
			continue
		}
		if !isPotentialPath(arg) {
			continue
		}
		candidate := arg
		if !filepath.IsAbs(candidate) {
			candidate = filepath.Join(workspaceAbs, candidate)
		}
		cleaned, err := filepath.Abs(candidate)
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(workspaceAbs, cleaned)
		if err != nil {
			return err
		}
		if rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
			return fmt.Errorf("%s path escapes workspace: %s", command, arg)
		}
	}
	return nil
}

func looksLikeURL(value string) bool {
	parsed, err := url.Parse(value)
	return err == nil && parsed.Scheme != "" && parsed.Host != ""
}

func looksLikeRemotePath(value string) bool {
	if strings.Contains(value, "://") {
		return true
	}
	idx := strings.IndexByte(value, ':')
	if idx <= 0 {
		return false
	}
	// Windows drive letter (e.g., C:\) is not a remote path
	if idx == 1 && len(value) > 2 && (value[2] == '\\' || value[2] == '/') {
		return false
	}
	return !strings.Contains(value[:idx], string(os.PathSeparator))
}

func isPotentialPath(value string) bool {
	return strings.HasPrefix(value, ".") || strings.HasPrefix(value, "~") || filepath.IsAbs(value) || strings.Contains(value, string(os.PathSeparator))
}

func splitShellCommand(command string) ([]string, error) {
	command = strings.TrimSpace(command)
	if command == "" {
		return nil, nil
	}

	segments := make([]string, 0, 4)
	current := make([]byte, 0, len(command))
	inSingle := false
	inDouble := false
	escaped := false

	appendSegment := func() {
		segment := strings.TrimSpace(string(current))
		current = current[:0]
		if segment != "" {
			segments = append(segments, segment)
		}
	}

	for i := 0; i < len(command); i++ {
		ch := command[i]

		if escaped {
			current = append(current, ch)
			escaped = false
			continue
		}

		if inSingle {
			current = append(current, ch)
			if ch == '\'' {
				inSingle = false
			}
			continue
		}

		if ch == '`' {
			return nil, fmt.Errorf("command substitution is not allowed")
		}
		if ch == '$' && i+1 < len(command) && command[i+1] == '(' {
			return nil, fmt.Errorf("command substitution is not allowed")
		}
		if (ch == '<' || ch == '>') && i+1 < len(command) && command[i+1] == '(' {
			return nil, fmt.Errorf("process substitution is not allowed")
		}

		if inDouble {
			current = append(current, ch)
			switch ch {
			case '\\':
				escaped = true
			case '"':
				inDouble = false
			}
			continue
		}

		switch ch {
		case '\\':
			current = append(current, ch)
			escaped = true
		case '\'':
			current = append(current, ch)
			inSingle = true
		case '"':
			current = append(current, ch)
			inDouble = true
		case '&':
			if i+1 < len(command) && command[i+1] == '&' {
				appendSegment()
				i++
				continue
			}
			if i+1 < len(command) && command[i+1] == '>' {
				current = append(current, ch)
				continue
			}
			if prev := lastNonSpaceByte(current); prev == '>' || prev == '<' {
				current = append(current, ch)
				continue
			}
			return nil, fmt.Errorf("background execution is not allowed")
		case '|':
			appendSegment()
			if i+1 < len(command) && command[i+1] == '|' {
				i++
			}
		case ';', '\n':
			appendSegment()
		default:
			current = append(current, ch)
		}
	}

	if escaped {
		return nil, fmt.Errorf("unfinished escape sequence")
	}
	if inSingle || inDouble {
		return nil, fmt.Errorf("unterminated quote")
	}

	appendSegment()
	return segments, nil
}

func firstShellCommandName(segment string) (string, error) {
	argv, err := SplitCommandLine(segment)
	if err != nil {
		return "", err
	}

	skipNext := false
	for _, token := range argv {
		if skipNext {
			skipNext = false
			continue
		}
		switch {
		case token == "":
		case isShellEnvAssignment(token):
		case redirectionConsumesNextToken(token):
			skipNext = true
		case isShellRedirectionToken(token):
		default:
			return token, nil
		}
	}
	return "", nil
}

func isShellEnvAssignment(token string) bool {
	idx := strings.IndexByte(token, '=')
	if idx <= 0 {
		return false
	}
	name := token[:idx]
	for i, r := range name {
		switch {
		case r == '_':
		case unicode.IsLetter(r):
		case unicode.IsDigit(r) && i > 0:
		default:
			return false
		}
	}
	return true
}

func redirectionConsumesNextToken(token string) bool {
	switch token {
	case "<", ">", ">>", "<<", "<<<", "<>", "1>", "1>>", "1<", "2>", "2>>", "2<", "&>", "&>>", ">&", "<&":
		return true
	default:
		return false
	}
}

func isShellRedirectionToken(token string) bool {
	if token == "" {
		return false
	}
	if redirectionConsumesNextToken(token) {
		return true
	}
	if strings.HasPrefix(token, "&>") {
		return true
	}
	i := 0
	for i < len(token) && token[i] >= '0' && token[i] <= '9' {
		i++
	}
	if i < len(token) && (token[i] == '<' || token[i] == '>') {
		return true
	}
	return strings.HasPrefix(token, "<") || strings.HasPrefix(token, ">")
}

func allowedCommand(name string) bool {
	name = filepath.Base(strings.TrimSpace(name))
	allowed := map[string]bool{
		"ls": true, "cd": true, "pwd": true, "cat": true, "head": true,
		"tail": true, "grep": true, "rg": true, "find": true, "mkdir": true, "rm": true,
		"cp": true, "mv": true, "touch": true, "chmod": true, "git": true, "sed": true,
		"go": true, "python": true, "pip": true, "npm": true, "npx": true, "node": true,
		"pnpm": true, "yarn": true, "bun": true, "playwright-cli": true,
		"curl": true, "wget": true, "docker": true, "make": true, "sh": true,
		"bash": true, "jq": true, "diff": true, "echo": true, "printf": true,
		"ssh": true, "scp": true, "rsync": true,
		// Windows-specific:
		"cmd": true, "powershell": true, "pwsh": true, "type": true,
		"where": true, "attrib": true, "xcopy": true, "robocopy": true,
		"tasklist": true, "taskkill": true, "findstr": true, "more": true,
		"sort": true, "fc": true, "comp": true, "systeminfo": true,
		"ver": true, "ipconfig": true, "ping": true, "tracert": true,
		"netstat": true, "nslookup": true, "whoami": true, "cscript": true,
	}
	return allowed[name]
}

func allowedCommandWithOptions(name string, options ShellCommandOptions) bool {
	if allowedCommand(name) {
		return true
	}
	name = filepath.Base(strings.TrimSpace(name))
	for _, candidate := range options.AllowedCommands {
		if strings.EqualFold(name, filepath.Base(strings.TrimSpace(candidate))) {
			return true
		}
	}
	return false
}

func normalizeShellPatterns(items []string) []string {
	out := make([]string, 0, len(items))
	seen := map[string]struct{}{}
	for _, item := range items {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if _, ok := seen[item]; ok {
			continue
		}
		seen[item] = struct{}{}
		out = append(out, item)
	}
	return out
}

func appendDockerEnvArgs(args []string) []string {
	for _, item := range minimalCommandEnv("") {
		if strings.TrimSpace(item) == "" {
			continue
		}
		args = append(args, "-e", item)
	}
	return args
}

func minimalCommandEnv(workingDir string) []string {
	keepExact := map[string]bool{
		"PATH": true, "HOME": true, "USER": true, "LOGNAME": true, "SHELL": true,
		"TMPDIR": true, "TEMP": true, "TMP": true, "LANG": true, "TERM": true,
		"GOCACHE": true, "GOMODCACHE": true, "GOPATH": true, "GOROOT": true,
		"NODE_OPTIONS": true, "NPM_CONFIG_CACHE": true, "PNPM_HOME": true,
		// Windows-specific:
		"USERPROFILE": true, "APPDATA": true, "LOCALAPPDATA": true,
		"COMPUTERNAME": true, "COMSPEC": true, "PATHEXT": true,
		"SYSTEMROOT": true, "WINDIR": true, "ALLUSERSPROFILE": true,
		"PROCESSOR_ARCHITECTURE": true, "NUMBER_OF_PROCESSORS": true,
		"OS": true,
	}
	env := make([]string, 0, len(keepExact)+4)
	seen := map[string]struct{}{}
	for _, item := range os.Environ() {
		key, _, ok := strings.Cut(item, "=")
		if !ok || key == "" {
			continue
		}
		if keepExact[key] || strings.HasPrefix(key, "LC_") {
			env = append(env, item)
			seen[key] = struct{}{}
		}
	}
	if _, ok := seen["PATH"]; !ok {
		if runtime.GOOS == "windows" {
			env = append(env, "PATH=C:\\Windows\\system32;C:\\Windows;C:\\Windows\\System32\\Wbem")
		} else {
			env = append(env, "PATH=/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin")
		}
	}
	if strings.TrimSpace(workingDir) != "" {
		if runtime.GOOS == "windows" {
			env = append(env, "CD="+workingDir)
		} else {
			env = append(env, "PWD="+workingDir)
		}
	}
	return env
}

func expandHomeArgs(args []string) ([]string, error) {
	if len(args) == 0 {
		return args, nil
	}

	homeDir, err := os.UserHomeDir()
	if err != nil {
		return nil, fmt.Errorf("resolve home directory: %w", err)
	}

	expanded := make([]string, len(args))
	for i, arg := range args {
		switch {
		case arg == "~":
			expanded[i] = homeDir
		case strings.HasPrefix(arg, "~/"):
			expanded[i] = filepath.Join(homeDir, strings.TrimPrefix(arg, "~/"))
		default:
			expanded[i] = arg
		}
	}
	return expanded, nil
}

func preview(text string) string {
	if len(text) <= 50 {
		return text
	}
	return text[:50]
}

func shellExitCode(err error) int {
	if err == nil {
		return 0
	}
	if exitErr, ok := err.(*exec.ExitError); ok && exitErr.ProcessState != nil {
		return exitErr.ProcessState.ExitCode()
	}
	return -1
}

func lastNonSpaceByte(data []byte) byte {
	for i := len(data) - 1; i >= 0; i-- {
		if !unicode.IsSpace(rune(data[i])) {
			return data[i]
		}
	}
	return 0
}

func cloneMap(input map[string]interface{}) map[string]interface{} {
	if len(input) == 0 {
		return map[string]interface{}{}
	}
	result := make(map[string]interface{}, len(input))
	for key, value := range input {
		result[key] = value
	}
	return result
}
