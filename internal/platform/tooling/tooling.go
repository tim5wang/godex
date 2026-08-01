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
	"regexp"
	"runtime"
	"sort"
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
		Description: "Run a shell command from the workspace root. Sandbox limits: no command substitution $() or backticks (precompute values instead); heredocs are supported but avoid embedding unbalanced quotes inside them; file writes are restricted to the workspace (use .godex/tmp for scratch scripts). Prefer one compound command (cmd1 && cmd2) over several sequential calls when the steps are dependent.",
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
		Description: "Read UTF-8 text file contents from a workspace-relative path. Returns content with line numbers. Source code files are returned in full; other files default to 2000 lines. Do not use for binary files such as PDFs, images, media, or archives — use attach_file instead.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string", "description": "Workspace-relative path such as agent/agent.go"},
				"offset": map[string]interface{}{
					"type":        "integer",
					"description": "1-based line number to start reading from. Default: 1.",
				},
				"limit": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum number of lines to return. Source code files default to all lines; other files default to 2000.",
				},
				"include_line_numbers": map[string]interface{}{
					"type":        "boolean",
					"description": "Include line numbers in output. Default: true.",
				},
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
		Description: "Make precise text replacements in workspace-relative files. Three modes, tried in order: (1) files[] — batch edits across MULTIPLE files in ONE call (up to 20 files); all files are validated first and if any old_text fails to match, NOTHING is written to ANY file, making it the safest way to do coordinated cross-file changes in a single round-trip. (2) path + edits[] — multiple independent changes to the same file in one call (up to 50 edits), applied atomically. (3) path + old_text/new_text — single replacement. Prefer batching: many sequential edit_file calls waste round-trips. Every old_text must appear exactly once in its file and must match the ORIGINAL file content verbatim (whitespace included) — re-read the region first if you have not read it in this session. On a failed multi-line old_text, the error pinpoints the first diverging line.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{"type": "string", "description": "Workspace-relative path such as skill/skill.go. Required for edits[] and old_text/new_text modes; omit when using files[]."},
				"old_text": map[string]interface{}{
					"type":        "string",
					"description": "Exact text to find and replace. Must be unique in the file. Use with new_text for single edit.",
				},
				"new_text": map[string]interface{}{
					"type":        "string",
					"description": "Replacement text. Omit or use empty string to delete old_text.",
				},
				"edits": map[string]interface{}{
					"type":        "array",
					"description": "Multiple non-overlapping edits to the file at path. Every old_text must match the ORIGINAL file (before any edits are applied). Edits must not overlap and each old_text must appear exactly once.",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"old_text": map[string]string{"type": "string"},
							"new_text": map[string]string{"type": "string"},
						},
						"required": []string{"old_text", "new_text"},
					},
				},
				"files": map[string]interface{}{
					"type":        "array",
					"description": "Batch edits across multiple files in one call. Each entry has a path and its own edits[] (same rules as above). All files are validated before any write; one bad old_text aborts the whole batch with no file modified.",
					"items": map[string]interface{}{
						"type": "object",
						"properties": map[string]interface{}{
							"path": map[string]string{"type": "string"},
							"edits": map[string]interface{}{
								"type": "array",
								"items": map[string]interface{}{
									"type": "object",
									"properties": map[string]interface{}{
										"old_text": map[string]string{"type": "string"},
										"new_text": map[string]string{"type": "string"},
									},
									"required": []string{"old_text", "new_text"},
								},
							},
						},
						"required": []string{"path", "edits"},
					},
				},
			},
		},
	}
}

// FileEdit represents a single find-and-replace operation.
type FileEdit struct {
	OldText string `json:"old_text"`
	NewText string `json:"new_text"`
}

func GrepDefinition() Definition {
	return Definition{
		Name:        "grep",
		Description: "Search file contents using a regex pattern. Returns matching lines with file paths and line numbers. Supports glob filtering and case-insensitive search.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]interface{}{
					"type":        "string",
					"description": "Regex pattern to search for, e.g. 'func.*Handler' or 'TODO'.",
				},
				"path": map[string]interface{}{
					"type":        "string",
					"description": "File or directory to search in. Defaults to workspace root.",
				},
				"glob": map[string]interface{}{
					"type":        "string",
					"description": "Glob pattern to filter files, e.g. '*.go', '*.{ts,tsx}'.",
				},
				"case_insensitive": map[string]interface{}{
					"type":        "boolean",
					"description": "Perform case-insensitive matching. Default: false.",
				},
				"max_results": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum matches to return. Default: 100, max: 500.",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

func FindDefinition() Definition {
	return Definition{
		Name:        "find",
		Description: "Find files matching a glob pattern. Searches recursively through directories. Supports patterns like '*.go', '**/*_test.go', 'cmd/**/main.go'.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"pattern": map[string]interface{}{
					"type":        "string",
					"description": "Glob pattern to match files, e.g. '**/*.go' or '*.md'.",
				},
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Directory to search in. Defaults to workspace root.",
				},
				"max_results": map[string]interface{}{
					"type":        "integer",
					"description": "Maximum files to return. Default: 200, max: 1000.",
				},
			},
			"required": []string{"pattern"},
		},
	}
}

func LsDefinition() Definition {
	return Definition{
		Name:        "ls",
		Description: "List the contents of a directory. Returns file names, types (file/directory), and sizes.",
		InputSchema: map[string]interface{}{
			"type": "object",
			"properties": map[string]interface{}{
				"path": map[string]interface{}{
					"type":        "string",
					"description": "Directory path to list. Defaults to workspace root.",
				},
			},
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
		case "grep":
			result = append(result, GrepDefinition().ToolSchema())
		case "find":
			result = append(result, FindDefinition().ToolSchema())
		case "ls":
			result = append(result, LsDefinition().ToolSchema())
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

	root, err := workspacefs.New(e.WorkspaceDir)
	if err != nil {
		return ReadFileLinesResult{}, err
	}
	defer root.Close()

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
	root, err := workspacefs.New(e.WorkspaceDir)
	if err != nil {
		return "", err
	}
	defer root.Close()
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

	root, err := workspacefs.New(e.WorkspaceDir)
	if err != nil {
		return "", err
	}
	defer root.Close()

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
