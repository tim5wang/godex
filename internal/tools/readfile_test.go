package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/platform/tooling"
)

func TestReadFileStructuredMetadata(t *testing.T) {
	workspace := t.TempDir()
	// No trailing newline to get exactly 3 lines when splitting by \n.
	content := "line1\nline2\nline3"
	if err := os.WriteFile(filepath.Join(workspace, "data.txt"), []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	handler := NewToolHandler()
	handler.RegisterWithMeta(NewReadFileTool(workspace), ToolMeta{AlwaysActive: true})

	result, err := handler.HandleResult(context.Background(), "read_file", map[string]interface{}{
		"path": "data.txt",
	})
	if err != nil {
		t.Fatalf("handle result: %v", err)
	}

	if result.Structured == nil {
		t.Fatal("expected structured metadata")
	}
	meta, ok := result.Structured.(map[string]interface{})
	if !ok {
		t.Fatalf("expected map, got %T", result.Structured)
	}
	if meta["type"] != "text" {
		t.Fatalf("expected type=text, got %v", meta["type"])
	}
	tl, ok := meta["total_lines"].(int)
	if !ok || tl != 3 {
		t.Fatalf("expected total_lines=3, got %v (type %T)", meta["total_lines"], meta["total_lines"])
	}
	if truncated, ok := meta["truncated"].(bool); !ok || truncated {
		t.Fatalf("expected truncated=false, got %v (type %T)", meta["truncated"], meta["truncated"])
	}
}

func TestReadFileSourceCodeNoTruncation(t *testing.T) {
	workspace := t.TempDir()
	// Create a .go file with more than 2000 lines but less than 256KB.
	// No trailing newline to avoid counting empty line.
	var content strings.Builder
	for i := 1; i <= 2500; i++ {
		if i > 1 {
			content.WriteString("\n")
		}
		content.WriteString("// line ")
		content.WriteString(strings.Repeat("x", 30))
	}
	if err := os.WriteFile(filepath.Join(workspace, "big.go"), []byte(content.String()), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := tooling.NewWorkspaceExecutor(workspace).ReadFileLines(tooling.ReadFileLinesOptions{
		Path:               "big.go",
		IncludeLineNumbers: false,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if result.TotalLines != 2500 {
		t.Fatalf("expected 2500 lines, got %d", result.TotalLines)
	}
	if result.Truncated {
		t.Fatal("source code file should not be truncated")
	}
}

func TestReadFileNonSourceTruncation(t *testing.T) {
	workspace := t.TempDir()
	// Create a .log file with more than 2000 lines. No trailing newline.
	var content strings.Builder
	for i := 1; i <= 2500; i++ {
		if i > 1 {
			content.WriteString("\n")
		}
		content.WriteString("log entry ")
		content.WriteString(strings.Repeat("x", 30))
	}
	if err := os.WriteFile(filepath.Join(workspace, "output.log"), []byte(content.String()), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := tooling.NewWorkspaceExecutor(workspace).ReadFileLines(tooling.ReadFileLinesOptions{
		Path:               "output.log",
		IncludeLineNumbers: false,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if result.TotalLines != 2500 {
		t.Fatalf("expected 2500 total lines, got %d", result.TotalLines)
	}
	if !result.Truncated {
		t.Fatal("non-source file should be truncated at 2000 lines")
	}
}

func TestReadFileDetectsImageAndReturnsStructuredResult(t *testing.T) {
	workspace := t.TempDir()
	// Minimal valid PNG (1x1 red pixel)
	pngData := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
		0x00, 0x00, 0x00, 0x0D, // IHDR length (13)
		0x49, 0x48, 0x44, 0x52, // "IHDR"
		0x00, 0x00, 0x00, 0x01, // width=1
		0x00, 0x00, 0x00, 0x01, // height=1
		0x08, 0x02, 0x00, 0x00, 0x00, // bit depth, color type, etc.
		0x90, 0x77, 0x53, 0xDE, // IHDR CRC
		0x00, 0x00, 0x00, 0x0E, // IDAT length (14)
		0x49, 0x44, 0x41, 0x54, // "IDAT"
		0x78, 0x9C, 0x62, 0x60, 0x60, 0x60, 0x00, 0x00, 0x00, 0x04, 0x00, 0x01, 0x27, 0x34, 0x03, 0x6A,
		0x00, 0x00, 0x00, 0x00, // IEND
		0x49, 0x45, 0x4E, 0x44,
		0xAE, 0x42, 0x60, 0x82,
	}
	if err := os.WriteFile(filepath.Join(workspace, "icon.png"), pngData, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewReadFileTool(workspace)

	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "icon.png",
	})
	if err != nil {
		t.Fatalf("read image: %v", err)
	}
	if !strings.Contains(result, "Image file detected") {
		t.Fatalf("expected image detection message, got: %s", result)
	}
	if !strings.Contains(result, "image/png") {
		t.Fatalf("expected mime type image/png, got: %s", result)
	}
	if !strings.Contains(result, "attach_file") {
		t.Fatalf("expected attach_file suggestion, got: %s", result)
	}

	// Check structured metadata via handler
	handler := NewToolHandler()
	handler.RegisterWithMeta(NewReadFileTool(workspace), ToolMeta{AlwaysActive: true})
	handlerResult, err := handler.HandleResult(context.Background(), "read_file", map[string]interface{}{
		"path": "icon.png",
	})
	if err != nil {
		t.Fatalf("handle result: %v", err)
	}
	if handlerResult.Structured == nil {
		t.Fatal("expected structured metadata for image")
	}
	meta := handlerResult.Structured.(map[string]interface{})
	if meta["type"] != "image" {
		t.Fatalf("expected type=image, got %v", meta["type"])
	}
	if meta["mime_type"] != "image/png" {
		t.Fatalf("expected mime_type=image/png, got %v", meta["mime_type"])
	}
}

func TestReadFileRejectsAnimatedPNG(t *testing.T) {
	workspace := t.TempDir()
	// Build animated PNG with acTL chunk, padded past 41 bytes so isStaticPNG scans chunks.
	apngData := []byte{
		0x89, 0x50, 0x4E, 0x47, 0x0D, 0x0A, 0x1A, 0x0A, // PNG signature
		0x00, 0x00, 0x00, 0x08, // acTL length
		0x61, 0x63, 0x54, 0x4C, // "acTL"
		0x00, 0x00, 0x00, 0x03, // num_frames=3
		0x00, 0x00, 0x00, 0x01, // num_plays=1
		0xAA, 0xBB, 0xCC, 0xDD, // CRC (dummy)
	}
	// Pad well past the 41-byte minimum for chunk parsing.
	padding := make([]byte, 100)
	apngData = append(apngData, padding...)
	if err := os.WriteFile(filepath.Join(workspace, "anim.png"), apngData, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	// Since isStaticPNG detects acTL, detectImageMimeType returns "" and the
	// file hits looksLikeBinaryFile → rejected with binary error.
	tool := NewReadFileTool(workspace)

	_, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "anim.png",
	})
	if err == nil {
		t.Fatal("expected error for animated PNG")
	}
	if !strings.Contains(err.Error(), "binary") {
		t.Fatalf("expected binary error for animated PNG, got: %v", err)
	}
}

func TestReadFileJPEGDetection(t *testing.T) {
	workspace := t.TempDir()
	jpegData := []byte{
		0xFF, 0xD8, 0xFF, 0xE0, // SOI + APP0 marker
		0x00, 0x10, 0x4A, 0x46, 0x49, 0x46, 0x00, // "JFIF\0"
		0x01, 0x01, 0x00, 0x00, 0x01, 0x00, 0x01, 0x00, 0x00,
		0xFF, 0xDB, // DQT marker (dummy)
		0x00, 0x43, 0x00,
	}
	if err := os.WriteFile(filepath.Join(workspace, "photo.jpeg"), jpegData, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewReadFileTool(workspace)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "photo.jpeg",
	})
	if err != nil {
		t.Fatalf("read jpeg: %v", err)
	}
	if !strings.Contains(result, "image/jpeg") {
		t.Fatalf("expected image/jpeg, got: %s", result)
	}
}

func TestReadFileGIFDetection(t *testing.T) {
	workspace := t.TempDir()
	gifData := []byte{
		0x47, 0x49, 0x46, 0x38, 0x39, 0x61, // "GIF89a"
		0x01, 0x00, 0x01, 0x00, 0x80, 0x00, 0x00,
	}
	if err := os.WriteFile(filepath.Join(workspace, "anim.gif"), gifData, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewReadFileTool(workspace)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "anim.gif",
	})
	if err != nil {
		t.Fatalf("read gif: %v", err)
	}
	if !strings.Contains(result, "image/gif") {
		t.Fatalf("expected image/gif, got: %s", result)
	}
}

func TestReadFileWebPDetection(t *testing.T) {
	workspace := t.TempDir()
	webpData := []byte{
		0x52, 0x49, 0x46, 0x46, // "RIFF"
		0x14, 0x00, 0x00, 0x00, // file size - 8 = 20
		0x57, 0x45, 0x42, 0x50, // "WEBP"
		0x56, 0x50, 0x38, 0x20, // "VP8 "
	}
	if err := os.WriteFile(filepath.Join(workspace, "image.webp"), webpData, 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	tool := NewReadFileTool(workspace)
	result, err := tool.Execute(context.Background(), map[string]interface{}{
		"path": "image.webp",
	})
	if err != nil {
		t.Fatalf("read webp: %v", err)
	}
	if !strings.Contains(result, "image/webp") {
		t.Fatalf("expected image/webp, got: %s", result)
	}
}

func TestReadFileBase64Stripping(t *testing.T) {
	workspace := t.TempDir()
	// Markdown with a base64 image
	content := "# Report\n\n![Screenshot](data:image/png;base64,iVBORw0KGgoAAAANSUhEUgAAAAEAAAA)\n\nSome text\n"
	if err := os.WriteFile(filepath.Join(workspace, "report.md"), []byte(content), 0644); err != nil {
		t.Fatalf("write: %v", err)
	}

	result, err := tooling.NewWorkspaceExecutor(workspace).ReadFileLines(tooling.ReadFileLinesOptions{
		Path:               "report.md",
		IncludeLineNumbers: false,
	})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if strings.Contains(result.Content, "iVBORw0KGgo") {
		t.Fatalf("base64 image should be stripped, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "[stripped]") {
		t.Fatalf("expected stripped placeholder, got: %s", result.Content)
	}
	if !strings.Contains(result.Content, "Screenshot") {
		t.Fatalf("expected alt text preserved, got: %s", result.Content)
	}
}
