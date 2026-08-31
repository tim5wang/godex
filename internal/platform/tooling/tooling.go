package tooling

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net/http"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/tim5wang/godex/internal/platform/workspacefs"
)

type WorkspaceExecutor struct {
	WorkspaceDir string
	TempDir      string
	Execution    ExecutionConfig
	fs           workspacefs.FS // optional pre-created FS; lazily created when nil
}

type ShellCommandOptions struct {
	AllowUnlistedCommands bool
	WorkspaceDir          string
	AllowPatterns         []string
	DenyPatterns          []string
	DenyHighRisk          bool
	AllowedCommands       []string

	// RelaxCommandSubstitution permits $(...) command substitution whose inner
	// command passes the normal safety chain (not high-risk, not a dangerous
	// base command, and not nested). Blocking command substitution is the
	// default so the hardened local/Docker/SSH paths stay strict when the
	// caller does not opt in.
	RelaxCommandSubstitution bool
	// RelaxSubstitutionAll permits any $(...) substitution without inspecting
	// the inner command. Used by yolo/trusted approval modes.
	RelaxSubstitutionAll bool
}

const (
	readFileBinarySampleBytes = 8192
	readFileDefaultMaxBytes   = 256 * 1024
	readFileDefaultMaxLines   = 2000
)

// ReadFileLinesOptions controls how ReadFileLines reads and formats output.
type ReadFileLinesOptions struct {
	Path               string
	Offset             int  // 1-based line number to start from (default: 1)
	Limit              int  // max lines to return (0 = use smart default)
	IncludeLineNumbers bool // prefix each line with line number (default: true)
}

// ReadFileLinesResult contains the read output and metadata.
type ReadFileLinesResult struct {
	Content    string
	Path       string
	TotalLines int
	Truncated  bool
	// Image detection fields.
	IsImage  bool   // true when file is a recognized image format
	MimeType string // e.g. "image/png" when IsImage is true
	Data     []byte // raw image bytes when IsImage is true (capped at 5MB)
}

// sourceCodeExts lists file extensions for which the default line limit is
// skipped so that source files are returned in full (the 256KB byte safety
// net still applies).
var sourceCodeExts = map[string]bool{
	".go": true, ".rs": true, ".py": true, ".ts": true, ".tsx": true,
	".js": true, ".jsx": true, ".java": true, ".c": true, ".cpp": true,
	".h": true, ".hpp": true, ".cs": true, ".rb": true, ".swift": true,
	".kt": true, ".scala": true, ".zig": true, ".lua": true, ".sh": true,
	".sql": true, ".yaml": true, ".yml": true, ".toml": true, ".json": true,
	".xml": true, ".proto": true, ".mod": true, ".sum": true, ".lock": true,
	".md": true, ".txt": true, ".css": true, ".scss": true, ".html": true,
}

// base64ImagePattern matches markdown image references with base64 data URIs.
var base64ImagePattern = regexp.MustCompile(`!\[([^\]]*)\]\(data:[^)]+\)`)

// detectImageMimeType checks the file header bytes for known image signatures.
// Returns the MIME type (e.g. "image/png") or empty string if not a recognized image.
func detectImageMimeType(sample []byte) string {
	if len(sample) < 8 {
		return ""
	}
	// JPEG: FF D8 FF (且第4字节不是F7，排除未知变体)
	if sample[0] == 0xFF && sample[1] == 0xD8 && sample[2] == 0xFF {
		if len(sample) > 3 && sample[3] == 0xF7 {
			return ""
		}
		return "image/jpeg"
	}
	// PNG: 89 50 4E 47 0D 0A 1A 0A (需验证 IHDR chunk，排除动画PNG)
	if sample[0] == 0x89 && sample[1] == 0x50 && sample[2] == 0x4E && sample[3] == 0x47 &&
		sample[4] == 0x0D && sample[5] == 0x0A && sample[6] == 0x1A && sample[7] == 0x0A {
		if isStaticPNG(sample) {
			return "image/png"
		}
		return ""
	}
	// GIF: 47 49 46 (ASCII "GIF")
	if sample[0] == 0x47 && sample[1] == 0x49 && sample[2] == 0x46 && sample[3] == 0x38 {
		return "image/gif"
	}
	// WebP: 52 49 46 46 (RIFF) + 57 45 42 50 (WEBP at offset 8)
	if sample[0] == 0x52 && sample[1] == 0x49 && sample[2] == 0x46 && sample[3] == 0x46 &&
		len(sample) >= 12 &&
		sample[8] == 0x57 && sample[9] == 0x45 && sample[10] == 0x42 && sample[11] == 0x50 {
		return "image/webp"
	}
	// BMP: 42 4D ("BM")
	if sample[0] == 0x42 && sample[1] == 0x4D {
		return "image/bmp"
	}
	return ""
}

// isStaticPNG checks whether a PNG buffer is a static (non-animated) PNG.
// Animated PNGs have an acTL chunk before the first IDAT chunk.
func isStaticPNG(data []byte) bool {
	if len(data) < 41 { // 8(sig) + 4(len) + 4(IHDR) + 13(data) + 4(crc) + 4(len) + 4(type) = 41 minimum
		return true // too short to be animated, treat as static
	}
	offset := 8 // skip PNG signature
	for offset+8 <= len(data) {
		chunkLen := int(uint32(data[offset])<<24 | uint32(data[offset+1])<<16 | uint32(data[offset+2])<<8 | uint32(data[offset+3]))
		chunkType := string(data[offset+4 : offset+8])
		switch chunkType {
		case "acTL":
			return false // animated PNG
		case "IDAT":
			return true // reached image data without seeing acTL
		}
		offset += 12 + chunkLen // 4(len) + 4(type) + len(data) + 4(crc)
	}
	return true // truncated but likely static
}

const maxImageReadBytes = 5 * 1024 * 1024 // 5MB cap for inline image reading

// ReadFileLines reads a text file with line-numbered output and smart defaults.
func (e *WorkspaceExecutor) ReadFileLines(opts ReadFileLinesOptions) (ReadFileLinesResult, error) {
	path := strings.TrimSpace(opts.Path)
	if path == "" {
		return ReadFileLinesResult{}, fmt.Errorf("missing path argument")
	}
	offset := opts.Offset
	if offset < 1 {
		offset = 1
	}

	root := e.getFS()
	if root == nil {
		return ReadFileLinesResult{}, fmt.Errorf("workspace fs unavailable")
	}

	absPath, err := root.Abs(path)
	if err != nil {
		return ReadFileLinesResult{}, err
	}
	info, err := root.Stat(path)
	if err != nil {
		return ReadFileLinesResult{}, err
	}
	if info.IsDir() {
		return ReadFileLinesResult{}, fmt.Errorf("path is a directory: %s", path)
	}

	// Binary detection.
	file, err := root.Open(path)
	if err != nil {
		return ReadFileLinesResult{}, err
	}
	sampleLimit := readFileBinarySampleBytes
	if info.Size() > 0 && info.Size() < int64(sampleLimit) {
		sampleLimit = int(info.Size())
	}
	sample, err := io.ReadAll(io.LimitReader(file, int64(sampleLimit)))
	if err != nil {
		file.Close()
		return ReadFileLinesResult{}, err
	}
	// Check for recognized image first — don't treat as error.
	if imageMime := detectImageMimeType(sample); imageMime != "" {
		// Read full image data (capped at 5MB).
		if _, err := file.Seek(0, io.SeekStart); err != nil {
			file.Close()
			return ReadFileLinesResult{}, err
		}
		imageData, err := io.ReadAll(io.LimitReader(file, maxImageReadBytes+1))
		file.Close()
		if err != nil {
			return ReadFileLinesResult{}, err
		}
		if len(imageData) > maxImageReadBytes {
			imageData = imageData[:maxImageReadBytes]
		}
		return ReadFileLinesResult{
			Path:     filepath.ToSlash(path),
			IsImage:  true,
			MimeType: imageMime,
			Data:     imageData,
		}, nil
	}

	// Not a recognized image — apply full binary check.
	if looksLikeBinaryFile(absPath, sample) {
		file.Close()
		return ReadFileLinesResult{}, fmt.Errorf("read_file only supports text files; %s appears to be binary or unsupported (for example PDF, document, media, or archive). Treat it as a file to copy, attach, or send instead of reading its contents", path)
	}

	// Re-read full file.
	if _, err := file.Seek(0, io.SeekStart); err != nil {
		file.Close()
		return ReadFileLinesResult{}, err
	}
	data, err := io.ReadAll(io.LimitReader(file, readFileDefaultMaxBytes+1))
	file.Close()
	if err != nil {
		return ReadFileLinesResult{}, err
	}
	if len(data) > readFileDefaultMaxBytes {
		data = data[:readFileDefaultMaxBytes]
	}

	content := string(data)
	// Strip base64 images from markdown to reduce output size.
	content = base64ImagePattern.ReplaceAllString(content, "![$1](data:image/png;base64,...[stripped])")

	lines := strings.Split(content, "\n")
	totalLines := len(lines)

	// Clamp offset.
	if offset > totalLines {
		return ReadFileLinesResult{
			Content:    "",
			Path:       filepath.ToSlash(path),
			TotalLines: totalLines,
		}, nil
	}

	startIdx := offset - 1

	// Determine effective limit.
	// For source code files, skip the default line limit so the full file
	// is returned (the byte safety net still applies).
	limit := opts.Limit
	if limit <= 0 {
		ext := strings.ToLower(filepath.Ext(path))
		if sourceCodeExts[ext] {
			limit = totalLines // no line truncation for source code
		} else {
			limit = readFileDefaultMaxLines
		}
	}

	endIdx := startIdx + limit
	if endIdx > totalLines {
		endIdx = totalLines
	}
	truncated := endIdx < totalLines && opts.Limit <= 0

	// Format output.
	var out strings.Builder
	if opts.IncludeLineNumbers {
		for i := startIdx; i < endIdx; i++ {
			fmt.Fprintf(&out, "%6d\t%s\n", i+1, lines[i])
		}
	} else {
		for i := startIdx; i < endIdx; i++ {
			out.WriteString(lines[i])
			if i < endIdx-1 {
				out.WriteByte('\n')
			}
		}
	}

	output := out.String()
	if truncated {
		output += fmt.Sprintf("\n... (truncated: showing %d of %d lines, use offset/limit to read more)", endIdx-startIdx, totalLines)
	}

	return ReadFileLinesResult{
		Content:    output,
		Path:       filepath.ToSlash(path),
		TotalLines: totalLines,
		Truncated:  truncated,
	}, nil
}

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

// SetFS attaches a pre-created workspacefs.FS. When set, all file I/O
// methods (ReadFileLines, WriteFile, EditFile, etc.) use this FS instead
// of creating a local one from WorkspaceDir. Pass nil to revert to local.
func (e *WorkspaceExecutor) SetFS(fs workspacefs.FS) {
	if e != nil {
		e.fs = fs
	}
}

func (e *WorkspaceExecutor) getFS() workspacefs.FS {
	if e == nil {
		return nil
	}
	if e.fs != nil {
		return e.fs
	}
	// Lazy-create a local FS for backward compatibility.
	fs, err := workspacefs.New(e.WorkspaceDir)
	if err != nil {
		return nil
	}
	e.fs = fs // cache for reuse
	return fs
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
	err = runCommandContext(ctx, cmd)
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
		cmd.Env = InheritedCommandEnv(e.WorkspaceDir)
		return cmd, nil
	case ExecutionModeDocker:
		return e.dockerShellCommand(ctx, cfg, command)
	case ExecutionModeSSH:
		return e.sshShellCommand(ctx, cfg, command)
	default:
		return nil, fmt.Errorf("unsupported execution backend %q", cfg.Mode)
	}
}

// runCommandContext runs an *exec.Cmd and guarantees that on context
// cancellation the whole process tree is killed, not just the direct child.
// exec.CommandContext alone only SIGKILLs the immediate child; descendants
// that inherited the stdout/stderr pipe keep it open, so cmd.Wait() blocks
// forever and a "stuck bash call" can never be stopped by cancel or timeout.
// Starting the child in its own process group and killing the group closes
// every inherited pipe, so Wait() always returns promptly.
func runCommandContext(ctx context.Context, cmd *exec.Cmd) error {
	if cmd == nil {
		return fmt.Errorf("nil command")
	}
	if err := configureCommandProcessGroup(cmd); err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	waitDone := make(chan error, 1)
	go func() { waitDone <- cmd.Wait() }()
	select {
	case err := <-waitDone:
		return err
	case <-ctx.Done():
		killCommandProcessGroup(cmd)
		<-waitDone
		if ctxErr := ctx.Err(); ctxErr != nil {
			return ctxErr
		}
		return fmt.Errorf("command interrupted")
	}
}

func (e *WorkspaceExecutor) argvCommand(argv []string) (*exec.Cmd, error) {
	cfg := normalizeExecutionConfig(e.Execution)
	switch cfg.Mode {
	case ExecutionModeLocal:
		cmd := exec.Command(argv[0], argv[1:]...)
		cmd.Dir = e.WorkspaceDir
		cmd.Env = InheritedCommandEnv(e.WorkspaceDir)
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
	return e.ReadFileRange(path, limit, 0, 0, 0)
}

func (e *WorkspaceExecutor) ReadFileRange(path string, limit, offset, startLine, maxLines int) (string, error) {
	if offset < 0 {
		return "", fmt.Errorf("offset must be non-negative")
	}
	if startLine < 0 {
		return "", fmt.Errorf("start_line must be non-negative")
	}
	if offset > 0 && startLine > 0 {
		return "", fmt.Errorf("use either offset or start_line, not both")
	}

	root := e.getFS()
	if root == nil {
		return "", fmt.Errorf("workspace fs unavailable")
	}
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
	// Check for recognized images before binary rejection.
	if imageMime := detectImageMimeType(sample); imageMime != "" {
		return fmt.Sprintf("[Image: %s, %s, %d bytes. Use read_file with the main agent or attach_file for OCR/vision analysis.]",
			filepath.Base(path), imageMime, info.Size()), nil
	}
	if looksLikeBinaryFile(absPath, sample) {
		return "", fmt.Errorf("read_file only supports text files; %s appears to be binary or unsupported (for example PDF, document, media, or archive). Treat it as a file to copy, attach, or send instead of reading its contents", path)
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

	// When max_lines is set, read line-by-line and cap at maxLines.
	if maxLines > 0 {
		var buf strings.Builder
		lineReader := bufio.NewReader(reader)
		for i := 0; i < maxLines; i++ {
			line, err := lineReader.ReadString('\n')
			if len(line) > 0 {
				buf.WriteString(line)
			}
			if err != nil {
				if err == io.EOF {
					break
				}
				return "", err
			}
			// Byte limit still acts as a safety cap.
			if limit > 0 && buf.Len() >= limit {
				break
			}
		}
		result := buf.String()
		if limit > 0 && len(result) > limit {
			result = result[:limit]
		}
		return result, nil
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
	root := e.getFS()
	if root == nil {
		return "", fmt.Errorf("workspace fs unavailable")
	}
	if err := root.WriteFile(path, []byte(content), 0644); err != nil {
		return "", err
	}
	return "OK", nil
}

func (e *WorkspaceExecutor) EditFile(path, oldText, newText string) (string, error) {
	root := e.getFS()
	if root == nil {
		return "", fmt.Errorf("workspace fs unavailable")
	}
	if oldText == "" {
		return "", fmt.Errorf("missing old_text argument: edit_file needs old_text to locate text to replace.\n" +
			"Minimal usage: {\"path\":\"file.go\",\"old_text\":\"<exact existing text>\",\"new_text\":\"<replacement>\"}\n" +
			"- To APPEND to an existing file: pass the last line(s) of the current file verbatim as old_text, and set new_text to that anchor + your new content.\n" +
			"- To create a NEW file: use write_file instead of edit_file.")
	}
	if oldText == newText {
		return "", fmt.Errorf("old_text and new_text must be different")
	}
	data, err := root.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(data)
	count := strings.Count(content, oldText)
	if count == 0 {
		return "", buildEditNotFoundError(oldText, content)
	}
	if count > 1 {
		locations := findEditLocations(content, oldText)
		return "", fmt.Errorf("old_text found %d times in file. Provide more context to make it unique, or use edits[] for multiple replacements.\nLocations: %s", count, strings.Join(locations, ", "))
	}
	newContent := strings.Replace(content, oldText, newText, 1)
	if err := root.WriteFile(path, []byte(newContent), 0644); err != nil {
		return "", err
	}
	return "OK", nil
}

type editRange struct {
	start int
	end   int
	index int
}

// FileEditBatch groups the edits for one file in a multi-file EditFilesMulti call.
type FileEditBatch struct {
	Path  string
	Edits []FileEdit
}

const (
	maxEditsPerFile  = 50
	maxFilesPerBatch = 20
)

// validateEdits checks every edit against content: non-empty old_text,
// old_text != new_text, exactly one occurrence. Error messages are prefixed
// with label (e.g. "edits[2]" or "files[1] (b.txt) edits[0]").
func validateEdits(content string, edits []FileEdit, label func(i int) string) error {
	for i, edit := range edits {
		if edit.OldText == "" {
			return fmt.Errorf("%s: old_text must not be empty", label(i))
		}
		if edit.OldText == edit.NewText {
			return fmt.Errorf("%s: old_text and new_text must be different", label(i))
		}
		count := strings.Count(content, edit.OldText)
		if count == 0 {
			return fmt.Errorf("%s: %w", label(i), buildEditNotFoundError(edit.OldText, content))
		}
		if count > 1 {
			locations := findEditLocations(content, edit.OldText)
			return fmt.Errorf("%s: old_text found %d times in file. Provide more context to make it unique.\nLocations: %s", label(i), count, strings.Join(locations, ", "))
		}
	}
	return nil
}

// applyEdits renders the post-edit content. Caller must have run validateEdits.
func applyEdits(content string, edits []FileEdit) (string, error) {
	ranges := make([]editRange, len(edits))
	for i, edit := range edits {
		idx := strings.Index(content, edit.OldText)
		ranges[i] = editRange{start: idx, end: idx + len(edit.OldText), index: i}
	}
	for i := 0; i < len(ranges); i++ {
		for j := i + 1; j < len(ranges); j++ {
			if rangesOverlap(ranges[i], ranges[j]) {
				return "", fmt.Errorf("edits[%d] and edits[%d] overlap in the file. Merge them into a single edit or adjust the ranges.", ranges[i].index, ranges[j].index)
			}
		}
	}
	// Apply edits in ascending position order. Because we always slice
	// from the original content (not a mutable copy), we never need
	// to adjust positions for size changes introduced by earlier edits.
	sort.SliceStable(ranges, func(i, j int) bool { return ranges[i].start < ranges[j].start })

	var buf strings.Builder
	pos := 0
	for _, r := range ranges {
		edit := edits[r.index]
		buf.WriteString(content[pos:r.start])
		buf.WriteString(edit.NewText)
		pos = r.start + len(edit.OldText)
	}
	buf.WriteString(content[pos:])
	return buf.String(), nil
}

// EditFileMulti applies multiple non-overlapping edits to a file.
// All edits are validated for uniqueness and non-overlap before any writes occur.
func (e *WorkspaceExecutor) EditFileMulti(path string, edits []FileEdit) (string, error) {
	if len(edits) == 0 {
		return "", fmt.Errorf("edits must not be empty")
	}
	if len(edits) > maxEditsPerFile {
		return "", fmt.Errorf("too many edits (%d); maximum is %d per call", len(edits), maxEditsPerFile)
	}
	root := e.getFS()
	if root == nil {
		return "", fmt.Errorf("workspace fs unavailable")
	}
	data, err := root.ReadFile(path)
	if err != nil {
		return "", err
	}
	content := string(data)

	if err := validateEdits(content, edits, func(i int) string { return fmt.Sprintf("edits[%d]", i) }); err != nil {
		return "", err
	}
	newContent, err := applyEdits(content, edits)
	if err != nil {
		return "", err
	}
	if err := root.WriteFile(path, []byte(newContent), 0644); err != nil {
		return "", err
	}
	return fmt.Sprintf("Applied %d edit(s) to %s", len(edits), path), nil
}

// EditFilesMulti applies edits across MULTIPLE files in one call. Every file
// is read and validated first; if ANY file fails validation, nothing is
// written to ANY file. Writes then proceed per file (an I/O error mid-write
// can still leave earlier files written — validation errors cannot).
func (e *WorkspaceExecutor) EditFilesMulti(batches []FileEditBatch) (string, error) {
	if len(batches) == 0 {
		return "", fmt.Errorf("files must not be empty")
	}
	if len(batches) > maxFilesPerBatch {
		return "", fmt.Errorf("too many files (%d); maximum is %d per call", len(batches), maxFilesPerBatch)
	}
	seen := make(map[string]bool, len(batches))
	for i, batch := range batches {
		if strings.TrimSpace(batch.Path) == "" {
			return "", fmt.Errorf("files[%d]: path must not be empty", i)
		}
		if seen[batch.Path] {
			return "", fmt.Errorf("files[%d]: duplicate path %q; merge its edits into a single files[] entry", i, batch.Path)
		}
		seen[batch.Path] = true
		if len(batch.Edits) == 0 {
			return "", fmt.Errorf("files[%d] (%s): edits must not be empty", i, batch.Path)
		}
		if len(batch.Edits) > maxEditsPerFile {
			return "", fmt.Errorf("files[%d] (%s): too many edits (%d); maximum is %d per file", i, batch.Path, len(batch.Edits), maxEditsPerFile)
		}
	}

	root := e.getFS()
	if root == nil {
		return "", fmt.Errorf("workspace fs unavailable")
	}

	// Phase 1: read + validate + render all files. Nothing is written here.
	type renderedFile struct {
		path       string
		newContent string
	}
	rendered := make([]renderedFile, len(batches))
	totalEdits := 0
	for i, batch := range batches {
		data, err := root.ReadFile(batch.Path)
		if err != nil {
			return "", fmt.Errorf("files[%d] (%s): %w", i, batch.Path, err)
		}
		content := string(data)
		label := func(j int) string { return fmt.Sprintf("files[%d] (%s) edits[%d]", i, batch.Path, j) }
		if err := validateEdits(content, batch.Edits, label); err != nil {
			return "", err
		}
		newContent, err := applyEdits(content, batch.Edits)
		if err != nil {
			return "", fmt.Errorf("files[%d] (%s): %w", i, batch.Path, err)
		}
		rendered[i] = renderedFile{path: batch.Path, newContent: newContent}
		totalEdits += len(batch.Edits)
	}

	// Phase 2: write all files.
	for _, f := range rendered {
		if err := root.WriteFile(f.path, []byte(f.newContent), 0644); err != nil {
			return "", fmt.Errorf("writing %s: %w", f.path, err)
		}
	}
	return fmt.Sprintf("Applied %d edit(s) across %d file(s)", totalEdits, len(rendered)), nil
}

func rangesOverlap(a, b editRange) bool {
	return a.start < b.end && b.start < a.end
}

func buildEditNotFoundError(oldText, content string) error {
	lines := strings.Split(content, "\n")
	// Multi-line old_text: locate the file position where the longest line-wise
	// prefix of old_text matches exactly, then report the FIRST diverging line
	// with expected vs actual content. This is far more actionable than the
	// generic fallback (which only previews the first line of the file).
	if oldLines := strings.Split(oldText, "\n"); len(oldLines) > 1 {
		bestStart, bestMatched := -1, 0
		for start := 0; start < len(lines); start++ {
			matched := 0
			for matched < len(oldLines) && start+matched < len(lines) && lines[start+matched] == oldLines[matched] {
				matched++
			}
			if matched > bestMatched {
				bestMatched, bestStart = matched, start
			}
		}
		// bestMatched == len(oldLines) is impossible here (exact match would
		// have been found by the caller), so the diverging line always exists.
		if bestMatched > 0 && bestStart >= 0 {
			mismatchLine := bestStart + bestMatched + 1 // 1-based file line
			expected := oldLines[bestMatched]
			actual := ""
			if bestStart+bestMatched < len(lines) {
				actual = lines[bestStart+bestMatched]
			}
			return fmt.Errorf("old_text not found in file\n\nClosest match starts at line %d (%d of %d lines matched).\nfirst mismatch at line %d:\n  expected: %q\n  actual:   %q\n\n- Re-read the region around line %d (read_file offset=%d) and copy old_text verbatim", bestStart+1, bestMatched, len(oldLines), mismatchLine, expected, actual, mismatchLine, mismatchLine)
		}
	}
	var preview string
	if len(lines) > 0 {
		preview = truncateLine(lines[0], 500)
	}
	var suggestions []string
	for i, line := range lines {
		if len(line) > 0 && len(oldText) > 0 {
			diff := len(line) - len(oldText)
			if diff < 0 {
				diff = -diff
			}
			if diff <= 5 {
				sim := stringSimilarity(line, oldText)
				if sim > 0.5 {
					suggestions = append(suggestions, fmt.Sprintf("line %d: %q", i+1, truncateLine(line, 60)))
				}
			}
		}
	}
	suggestionStr := ""
	if len(suggestions) > 0 {
		suggestionStr = fmt.Sprintf("\nSimilar lines found:\n  %s\n\nSuggestions:", strings.Join(suggestions, "\n  "))
	}
	return fmt.Errorf("old_text not found in file\n\nExpected:\n%q\n\nFile preview (first 500 chars of first line):\n%s\n%s\n- Verify exact text matches including whitespace and indentation\n- Use read_file to see current file content\n- Try a smaller, unique portion of the old_text", oldText, preview, suggestionStr)
}

func findEditLocations(content, oldText string) []string {
	lines := strings.Split(content, "\n")
	var locations []string
	for lineNum, line := range lines {
		idx := strings.Index(line, oldText)
		if idx >= 0 {
			locations = append(locations, fmt.Sprintf("line %d (col %d)", lineNum+1, idx+1))
		}
	}
	return locations
}

func stringSimilarity(a, b string) float64 {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	setA := make(map[rune]bool)
	for _, r := range a {
		setA[r] = true
	}
	intersection := 0
	for _, r := range b {
		if setA[r] {
			intersection++
		}
	}
	union := len(setA) + len(b) - intersection
	if union == 0 {
		return 0
	}
	return float64(intersection) / float64(union)
}

func truncateLine(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "..."
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
	// The sample window can end in the middle of a multi-byte UTF-8
	// sequence; trim up to utf8.UTFMax-1 trailing bytes so a truncated
	// tail rune doesn't mark an otherwise valid text file as binary.
	// Invalid sequences in the middle of the sample still count as binary.
	utf8Sample := sample
	for i := 0; i < utf8.UTFMax-1 && len(utf8Sample) > 0 && !utf8.Valid(utf8Sample); i++ {
		utf8Sample = utf8Sample[:len(utf8Sample)-1]
	}
	if !utf8.Valid(utf8Sample) {
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
