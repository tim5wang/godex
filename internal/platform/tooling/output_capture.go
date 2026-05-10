package tooling

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

const (
	defaultCommandOutputPreviewBytes int64 = 64 << 10
	defaultCommandOutputSpillBytes   int64 = 8 << 20
	defaultCommandOutputTailBytes    int64 = 64 << 10
)

// CommandOutputOptions controls how command output is retained.
type CommandOutputOptions struct {
	PreviewBytes int64
	SpillBytes   int64
	SpillDir     string
	SpillPrefix  string
	OutputPath   string
	TailBytes    int64
}

// CommandOutputResult is the bounded output retained from a command.
type CommandOutputResult struct {
	Text          string
	Tail          string
	Truncated     bool
	FilePath      string
	Bytes         int64
	StoredBytes   int64
	PreviewBytes  int64
	DiscardedTail bool
	ExitCode      int
}

// ModelText returns the preview plus a pointer to any spilled full output.
func (r CommandOutputResult) ModelText() string {
	text := strings.ToValidUTF8(r.Text, "?")
	if !r.Truncated {
		return appendExitCodeText(text, r.ExitCode)
	}
	tail := strings.ToValidUTF8(r.Tail, "?")
	var builder strings.Builder
	builder.WriteString(text)
	if tail != "" && tail != text {
		if !strings.HasSuffix(text, "\n") && text != "" {
			builder.WriteString("\n")
		}
		builder.WriteString("\n...[output middle truncated]...\n")
		builder.WriteString(tail)
	}
	if !strings.HasSuffix(text, "\n") && text != "" {
		builder.WriteString("\n")
	}
	builder.WriteString("\n[output truncated")
	if r.Bytes > 0 {
		builder.WriteString(fmt.Sprintf(": command produced %d bytes", r.Bytes))
	}
	if r.FilePath != "" {
		builder.WriteString(fmt.Sprintf("; captured output saved to %s", r.FilePath))
	}
	if r.DiscardedTail {
		builder.WriteString(fmt.Sprintf("; retained first %d bytes and discarded the rest", r.StoredBytes))
	}
	builder.WriteString("]")
	return appendExitCodeText(builder.String(), r.ExitCode)
}

func appendExitCodeText(text string, exitCode int) string {
	if exitCode == 0 {
		return text
	}
	if strings.TrimSpace(text) == "" {
		return fmt.Sprintf("[exit_code: %d]", exitCode)
	}
	if !strings.HasSuffix(text, "\n") {
		text += "\n"
	}
	return text + fmt.Sprintf("[exit_code: %d]", exitCode)
}

// OutputCapture is an io.Writer that keeps a small preview in memory and spills
// larger command output to disk with a hard retained-byte cap.
type OutputCapture struct {
	mu sync.Mutex

	preview bytes.Buffer
	tail    bytes.Buffer
	file    *os.File

	previewLimit int64
	tailLimit    int64
	spillLimit   int64
	spillDir     string
	spillPrefix  string
	outputPath   string

	filePath    string
	fileBytes   int64
	totalBytes  int64
	truncated   bool
	discardTail bool
}

// NewOutputCapture creates a bounded command output writer.
func NewOutputCapture(opts CommandOutputOptions) *OutputCapture {
	previewLimit := opts.PreviewBytes
	if previewLimit <= 0 {
		previewLimit = defaultCommandOutputPreviewBytes
	}
	spillLimit := opts.SpillBytes
	if spillLimit <= 0 {
		spillLimit = defaultCommandOutputSpillBytes
	}
	if spillLimit < previewLimit {
		spillLimit = previewLimit
	}
	tailLimit := opts.TailBytes
	if tailLimit <= 0 {
		tailLimit = defaultCommandOutputTailBytes
	}
	prefix := strings.TrimSpace(opts.SpillPrefix)
	if prefix == "" {
		prefix = "command-output-"
	}
	return &OutputCapture{
		previewLimit: previewLimit,
		tailLimit:    tailLimit,
		spillLimit:   spillLimit,
		spillDir:     strings.TrimSpace(opts.SpillDir),
		spillPrefix:  prefix,
		outputPath:   strings.TrimSpace(opts.OutputPath),
	}
}

func (c *OutputCapture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	written := len(p)
	start := c.totalBytes
	c.totalBytes += int64(len(p))
	c.appendTailLocked(p)
	if c.outputPath != "" {
		if err := c.ensureFileLocked(); err == nil {
			c.writeFileChunkLocked(p, start)
		}
	}

	if int64(c.preview.Len()) < c.previewLimit {
		remaining := c.previewLimit - int64(c.preview.Len())
		if remaining > int64(len(p)) {
			remaining = int64(len(p))
		}
		if remaining > 0 {
			_, _ = c.preview.Write(p[:remaining])
		}
	}

	if c.totalBytes <= c.previewLimit {
		return written, nil
	}
	c.truncated = c.totalBytes > c.previewLimit
	if c.outputPath != "" {
		return written, nil
	}
	if err := c.ensureFileLocked(); err != nil {
		return written, nil
	}

	if start < c.previewLimit {
		offset := int(c.previewLimit - start)
		if offset > len(p) {
			offset = len(p)
		}
		p = p[offset:]
		start = c.previewLimit
	}
	if start >= c.spillLimit || len(p) == 0 {
		c.discardTail = c.discardTail || start >= c.spillLimit
		return written, nil
	}
	remaining := c.spillLimit - c.fileBytes
	if remaining <= 0 {
		c.discardTail = true
		return written, nil
	}
	chunk := p
	if int64(len(chunk)) > remaining {
		chunk = chunk[:remaining]
		c.discardTail = true
	}
	if len(chunk) > 0 {
		if n, err := c.file.Write(chunk); err == nil {
			c.fileBytes += int64(n)
		}
	}
	return written, nil
}

func (c *OutputCapture) writeFileChunkLocked(p []byte, start int64) {
	if c.file == nil || len(p) == 0 {
		return
	}
	if start >= c.spillLimit {
		c.discardTail = true
		return
	}
	remaining := c.spillLimit - c.fileBytes
	if remaining <= 0 {
		c.discardTail = true
		return
	}
	chunk := p
	if int64(len(chunk)) > remaining {
		chunk = chunk[:remaining]
		c.discardTail = true
	}
	if n, err := c.file.Write(chunk); err == nil {
		c.fileBytes += int64(n)
	}
}

func (c *OutputCapture) appendTailLocked(p []byte) {
	if c.tailLimit <= 0 || len(p) == 0 {
		return
	}
	_, _ = c.tail.Write(p)
	if int64(c.tail.Len()) <= c.tailLimit {
		return
	}
	data := c.tail.Bytes()
	if int64(len(data)) > c.tailLimit {
		data = append([]byte{}, data[len(data)-int(c.tailLimit):]...)
		c.tail.Reset()
		_, _ = c.tail.Write(data)
	}
}

// Close closes the spill file, if one was created.
func (c *OutputCapture) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file == nil {
		return nil
	}
	err := c.file.Close()
	c.file = nil
	return err
}

// Result returns the retained output snapshot.
func (c *OutputCapture) Result() CommandOutputResult {
	c.mu.Lock()
	defer c.mu.Unlock()
	return CommandOutputResult{
		Text:          c.preview.String(),
		Tail:          c.tail.String(),
		Truncated:     c.truncated,
		FilePath:      c.filePath,
		Bytes:         c.totalBytes,
		StoredBytes:   c.fileBytes,
		PreviewBytes:  int64(c.preview.Len()),
		DiscardedTail: c.discardTail,
	}
}

func (c *OutputCapture) ensureFileLocked() error {
	if c.file != nil {
		return nil
	}
	dir := c.spillDir
	if dir == "" {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return err
	}
	var file *os.File
	var err error
	if c.outputPath != "" {
		if err := os.MkdirAll(filepath.Dir(c.outputPath), 0755); err != nil {
			return err
		}
		file, err = os.OpenFile(c.outputPath, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0644)
	} else {
		file, err = os.CreateTemp(dir, c.spillPrefix+"*.log")
	}
	if err != nil {
		return err
	}
	c.file = file
	c.filePath = file.Name()
	if c.preview.Len() > 0 {
		if n, err := c.file.Write(c.preview.Bytes()); err == nil {
			c.fileBytes += int64(n)
		}
	}
	if c.filePath != "" {
		c.filePath = filepath.Clean(c.filePath)
	}
	return nil
}

// DefaultCommandOutputDir returns the workspace-local spill directory used for
// command outputs when no explicit temp directory is configured.
func DefaultCommandOutputDir(workspace string) string {
	if strings.TrimSpace(workspace) == "" {
		return filepath.Join(os.TempDir(), "godex-command-output")
	}
	return filepath.Join(workspace, ".godex", ".tmp", "command-output")
}
