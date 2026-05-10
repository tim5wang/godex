package media

import (
	"archive/zip"
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/xuri/excelize/v2"
)

func TestProcessorDocumentMoonshotCachingAndFallback(t *testing.T) {
	dir := t.TempDir()
	uploadCount := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/files":
			uploadCount++
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"file-1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/files/file-1/content":
			_, _ = w.Write([]byte("Moonshot extracted document text"))
		default:
			http.NotFound(w, r)
		}
	}))

	attachmentPath := filepath.Join(dir, "report.pdf")
	if err := os.WriteFile(attachmentPath, []byte("fake pdf"), 0644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	processor := NewProcessor(config.MediaConfig{
		Moonshot: config.MoonshotMediaConfig{
			Enabled: true,
			BaseURL: server.URL,
			APIKey:  "moonshot-key",
		},
		Document: config.DocumentMediaConfig{MaxChars: 1000, PDFToTextPath: "missing-pdftotext"},
	}, dir, filepath.Join(dir, ".sessions"), filepath.Join(dir, ".tmp"))
	attachment := protocol.Attachment{ID: "att-doc", Name: "report.pdf", MIMEType: "application/pdf", Path: attachmentPath}

	blocks, err := processor.BuildBlocks(context.Background(), BuildContext{SessionID: "session-doc", SupportsImage: true}, attachment)
	if err != nil {
		t.Fatalf("build blocks: %v", err)
	}
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "Moonshot extracted document text") {
		t.Fatalf("unexpected blocks %#v", blocks)
	}
	if uploadCount != 1 {
		t.Fatalf("expected one moonshot upload, got %d", uploadCount)
	}

	server.Close()

	blocks, err = processor.BuildBlocks(context.Background(), BuildContext{SessionID: "session-doc", SupportsImage: true}, attachment)
	if err != nil {
		t.Fatalf("build blocks from cache: %v", err)
	}
	if len(blocks) != 1 || !strings.Contains(blocks[0].Text, "Moonshot extracted document text") {
		t.Fatalf("unexpected cached blocks %#v", blocks)
	}
	if uploadCount != 1 {
		t.Fatalf("expected cached read without new upload, got %d", uploadCount)
	}
}

func TestProcessorLocalDocumentExtractors(t *testing.T) {
	dir := t.TempDir()
	pdftotext := writeExecutable(t, dir, "pdftotext", "#!/bin/sh\nprintf 'pdf extracted text'\n")
	docxPath := filepath.Join(dir, "sample.docx")
	pptxPath := filepath.Join(dir, "deck.pptx")
	xlsxPath := filepath.Join(dir, "sheet.xlsx")
	pdfPath := filepath.Join(dir, "sample.pdf")
	if err := os.WriteFile(pdfPath, []byte("pdf"), 0644); err != nil {
		t.Fatalf("write pdf: %v", err)
	}
	if err := writeDOCX(docxPath, "第一段\n第二段"); err != nil {
		t.Fatalf("write docx: %v", err)
	}
	if err := writePPTX(pptxPath, []string{"第一页标题", "第二页内容"}); err != nil {
		t.Fatalf("write pptx: %v", err)
	}
	if err := writeXLSX(xlsxPath); err != nil {
		t.Fatalf("write xlsx: %v", err)
	}

	processor := NewProcessor(config.MediaConfig{
		Document: config.DocumentMediaConfig{MaxChars: 10000, PDFToTextPath: pdftotext},
	}, dir, filepath.Join(dir, ".sessions"), filepath.Join(dir, ".tmp"))

	cases := []struct {
		name     string
		path     string
		mimeType string
		want     string
	}{
		{name: "pdf", path: pdfPath, mimeType: "application/pdf", want: "pdf extracted text"},
		{name: "docx", path: docxPath, mimeType: "application/vnd.openxmlformats-officedocument.wordprocessingml.document", want: "第一段"},
		{name: "xlsx", path: xlsxPath, mimeType: "application/vnd.openxmlformats-officedocument.spreadsheetml.sheet", want: "## Sheet: Sheet1"},
		{name: "pptx", path: pptxPath, mimeType: "application/vnd.openxmlformats-officedocument.presentationml.presentation", want: "## Slide 1"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			blocks, err := processor.BuildBlocks(context.Background(), BuildContext{SessionID: "session-docs", SupportsImage: true}, protocol.Attachment{
				ID:       "att-" + tc.name,
				Name:     filepath.Base(tc.path),
				MIMEType: tc.mimeType,
				Path:     tc.path,
			})
			if err != nil {
				t.Fatalf("build blocks: %v", err)
			}
			if len(blocks) != 1 || !strings.Contains(blocks[0].Text, tc.want) {
				t.Fatalf("unexpected blocks %#v", blocks)
			}
		})
	}
}

func TestProcessorImageOCRFallbackAddsTextAndImage(t *testing.T) {
	dir := t.TempDir()
	tesseract := writeExecutable(t, dir, "tesseract", "#!/bin/sh\nprintf 'OCR 提取文字'\n")
	imagePath := filepath.Join(dir, "photo.jpg")
	if err := os.WriteFile(imagePath, []byte{0xff, 0xd8, 0xff, 0xdb, 0x00, 0x43}, 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}
	processor := NewProcessor(config.MediaConfig{
		OCR: config.OCRMediaConfig{
			Mode:          "tesseract",
			TesseractPath: tesseract,
			MaxChars:      1000,
		},
	}, dir, filepath.Join(dir, ".sessions"), filepath.Join(dir, ".tmp"))
	blocks, err := processor.BuildBlocks(context.Background(), BuildContext{SessionID: "session-img", SupportsImage: true}, protocol.Attachment{
		ID:       "att-img",
		Name:     "photo.jpg",
		MIMEType: "image/jpeg",
		Path:     imagePath,
	})
	if err != nil {
		t.Fatalf("build blocks: %v", err)
	}
	if len(blocks) != 3 {
		t.Fatalf("expected summary + ocr + image, got %#v", blocks)
	}
	if !strings.Contains(blocks[1].Text, "OCR 提取文字") {
		t.Fatalf("expected OCR text block, got %#v", blocks[1])
	}
	if blocks[2].Type != protocol.BlockImage {
		t.Fatalf("expected image block, got %#v", blocks[2])
	}
}

func TestProcessorAudioAndVideoLocalPipeline(t *testing.T) {
	dir := t.TempDir()
	ffmpeg := writeExecutable(t, dir, "ffmpeg", `#!/bin/sh
last=""
for arg in "$@"; do last="$arg"; done
case "$last" in
  *.wav)
    printf 'wav' > "$last"
    ;;
  *%03d.png)
    outdir=$(dirname "$last")
    mkdir -p "$outdir"
    printf 'frame' > "$outdir/frame-001.png"
    printf 'frame' > "$outdir/frame-002.png"
    ;;
esac
`)
	ffprobe := writeExecutable(t, dir, "ffprobe", "#!/bin/sh\nprintf '{\"streams\":[{\"width\":1280,\"height\":720}],\"format\":{\"duration\":\"24\"}}'\n")
	whisper := writeExecutable(t, dir, "whisper-cli", `#!/bin/sh
out=""
prev=""
for arg in "$@"; do
  if [ "$prev" = "-of" ]; then out="$arg"; fi
  prev="$arg"
done
printf 'transcribed media text' > "${out}.txt"
`)
	modelPath := filepath.Join(dir, "ggml-base.bin")
	if err := os.WriteFile(modelPath, []byte("model"), 0644); err != nil {
		t.Fatalf("write model: %v", err)
	}
	audioPath := filepath.Join(dir, "sample.mp3")
	videoPath := filepath.Join(dir, "clip.mp4")
	if err := os.WriteFile(audioPath, []byte("audio"), 0644); err != nil {
		t.Fatalf("write audio: %v", err)
	}
	if err := os.WriteFile(videoPath, []byte("video"), 0644); err != nil {
		t.Fatalf("write video: %v", err)
	}

	processor := NewProcessor(config.MediaConfig{
		Audio: config.AudioMediaConfig{
			Enabled:          true,
			FFmpegPath:       ffmpeg,
			FFprobePath:      ffprobe,
			WhisperCPPPath:   whisper,
			WhisperModelPath: modelPath,
			MaxChars:         1000,
		},
		Video: config.VideoMediaConfig{
			Enabled:                 true,
			KeyframeIntervalSeconds: 8,
			MaxFrames:               12,
		},
	}, dir, filepath.Join(dir, ".sessions"), filepath.Join(dir, ".tmp"))

	audioBlocks, err := processor.BuildBlocks(context.Background(), BuildContext{SessionID: "session-audio", SupportsImage: true}, protocol.Attachment{
		ID:       "att-audio",
		Name:     "sample.mp3",
		MIMEType: "audio/mpeg",
		Path:     audioPath,
	})
	if err != nil {
		t.Fatalf("audio build blocks: %v", err)
	}
	if len(audioBlocks) != 1 || !strings.Contains(audioBlocks[0].Text, "transcribed media text") {
		t.Fatalf("unexpected audio blocks %#v", audioBlocks)
	}

	videoBlocks, err := processor.BuildBlocks(context.Background(), BuildContext{SessionID: "session-video", SupportsImage: true}, protocol.Attachment{
		ID:       "att-video",
		Name:     "clip.mp4",
		MIMEType: "video/mp4",
		Path:     videoPath,
	})
	if err != nil {
		t.Fatalf("video build blocks: %v", err)
	}
	if len(videoBlocks) < 3 {
		t.Fatalf("expected summary + keyframes, got %#v", videoBlocks)
	}
	if !strings.Contains(videoBlocks[0].Text, "Transcript:") || !strings.Contains(videoBlocks[0].Text, "transcribed media text") {
		t.Fatalf("unexpected video summary %#v", videoBlocks[0])
	}
	if videoBlocks[1].Type != protocol.BlockImage {
		t.Fatalf("expected keyframe image block, got %#v", videoBlocks[1])
	}
}

func writeExecutable(t *testing.T, dir, name, script string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(script), 0755); err != nil {
		t.Fatalf("write executable %s: %v", name, err)
	}
	return path
}

func writeDOCX(path, text string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	zipWriter := zip.NewWriter(file)
	defer zipWriter.Close()
	entry, err := zipWriter.Create("word/document.xml")
	if err != nil {
		return err
	}
	_, err = entry.Write([]byte(fmt.Sprintf(`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body><w:p><w:r><w:t>%s</w:t></w:r></w:p></w:body></w:document>`, text)))
	return err
}

func writePPTX(path string, slides []string) error {
	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer file.Close()
	zipWriter := zip.NewWriter(file)
	defer zipWriter.Close()
	for idx, slide := range slides {
		entry, err := zipWriter.Create(fmt.Sprintf("ppt/slides/slide%d.xml", idx+1))
		if err != nil {
			return err
		}
		if _, err := entry.Write([]byte(fmt.Sprintf(`<p:sld xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main"><p:cSld><p:spTree><p:sp><p:txBody><a:p><a:r><a:t>%s</a:t></a:r></a:p></p:txBody></p:sp></p:spTree></p:cSld></p:sld>`, slide))); err != nil {
			return err
		}
	}
	return nil
}

func writeXLSX(path string) error {
	file := excelize.NewFile()
	defer file.Close()
	file.SetCellValue("Sheet1", "A1", "姓名")
	file.SetCellValue("Sheet1", "B1", "分数")
	file.SetCellValue("Sheet1", "A2", "Alice")
	file.SetCellValue("Sheet1", "B2", 99)
	return file.SaveAs(path)
}
