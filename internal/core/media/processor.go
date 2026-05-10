package media

import (
	"archive/zip"
	"context"
	"encoding/base64"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	htmltomarkdown "github.com/JohannesKaufmann/html-to-markdown/v2"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/xuri/excelize/v2"
)

type BuildContext struct {
	SessionID     string
	SupportsImage bool
}

type Processor struct {
	cfg          config.MediaConfig
	workspaceDir string
	sessionsDir  string
	tempDir      string
	moonshot     *MoonshotClient
}

type DerivedMeta struct {
	Kind        string    `json:"kind"`
	Strategy    string    `json:"strategy"`
	Status      string    `json:"status"`
	Error       string    `json:"error,omitempty"`
	GeneratedAt time.Time `json:"generated_at"`
}

type mediaInfo struct {
	DurationSeconds float64
	Width           int
	Height          int
}

func NewProcessor(cfg config.MediaConfig, workspaceDir, sessionsDir, tempDir string) *Processor {
	return &Processor{
		cfg:          cfg,
		workspaceDir: strings.TrimSpace(workspaceDir),
		sessionsDir:  strings.TrimSpace(sessionsDir),
		tempDir:      strings.TrimSpace(tempDir),
		moonshot:     NewMoonshotClient(cfg.Moonshot),
	}
}

func (p *Processor) BuildBlocks(ctx context.Context, buildCtx BuildContext, attachment protocol.Attachment) ([]protocol.Block, error) {
	switch classifyAttachment(attachment) {
	case "image":
		return p.imageBlocks(ctx, buildCtx, attachment)
	case "text":
		return p.textAttachmentBlocks(ctx, buildCtx, attachment)
	case "document":
		return p.documentBlocks(ctx, buildCtx, attachment)
	case "audio":
		return p.audioBlocks(ctx, buildCtx, attachment)
	case "video":
		return p.videoBlocks(ctx, buildCtx, attachment)
	default:
		return []protocol.Block{protocol.TextBlock(p.unsupportedAttachmentText(attachment, "Parsing for this attached file type is not enabled in the current environment."))}, nil
	}
}

func (p *Processor) imageBlocks(ctx context.Context, buildCtx BuildContext, attachment protocol.Attachment) ([]protocol.Block, error) {
	path, err := p.attachmentPath(attachment)
	if err != nil {
		return []protocol.Block{protocol.TextBlock(p.unsupportedAttachmentText(attachment, err.Error()))}, nil
	}
	data, mediaType, err := readAttachmentBytes(path, attachment.MIMEType)
	if err != nil {
		return []protocol.Block{protocol.TextBlock(p.unsupportedAttachmentText(attachment, err.Error()))}, nil
	}

	blocks := make([]protocol.Block, 0, 3)
	summary := fmt.Sprintf(`Attached image "%s" (%s).`, attachmentLabel(attachment), mediaType)
	if buildCtx.SupportsImage {
		summary += " Analyze the accompanying image input for visual details."
	} else {
		summary += " Native image understanding is not enabled on the current request path."
	}
	blocks = append(blocks, protocol.TextBlock(summary))

	ocrText, truncatedPath, strategy, err := p.extractOCRText(ctx, buildCtx.SessionID, attachment, path)
	if err == nil && strings.TrimSpace(ocrText) != "" {
		body := fmt.Sprintf(`OCR text from image "%s":`+"\n%s", attachmentLabel(attachment), ocrText)
		if truncatedPath != "" {
			body += fmt.Sprintf("\n\n[Truncated. Full OCR text saved to %s]", truncatedPath)
		}
		if strategy != "" {
			body += fmt.Sprintf("\n\n[OCR source: %s]", strategy)
		}
		blocks = append(blocks, protocol.TextBlock(body))
	}
	if buildCtx.SupportsImage {
		blocks = append(blocks, protocol.ImageBlock(mediaType, base64.StdEncoding.EncodeToString(data)))
	}
	return blocks, nil
}

func (p *Processor) textAttachmentBlocks(_ context.Context, buildCtx BuildContext, attachment protocol.Attachment) ([]protocol.Block, error) {
	extracted, extractedPath, err := p.extractTextLikeAttachment(buildCtx.SessionID, attachment)
	if err != nil || strings.TrimSpace(extracted) == "" {
		return []protocol.Block{protocol.TextBlock(p.unsupportedAttachmentText(attachment, "No readable text could be extracted from this attachment."))}, nil
	}
	body := fmt.Sprintf(`Extracted text from attachment "%s"`, attachmentLabel(attachment))
	if attachment.MIMEType != "" {
		body += fmt.Sprintf(" (%s)", attachment.MIMEType)
	}
	body += ":\n" + extracted
	if extractedPath != "" {
		body += fmt.Sprintf("\n\n[Truncated. Full extracted text saved to %s]", extractedPath)
	}
	return []protocol.Block{protocol.TextBlock(body)}, nil
}

func (p *Processor) documentBlocks(ctx context.Context, buildCtx BuildContext, attachment protocol.Attachment) ([]protocol.Block, error) {
	extracted, extractedPath, strategy, err := p.extractDocumentText(ctx, buildCtx.SessionID, attachment)
	if err != nil || strings.TrimSpace(extracted) == "" {
		return []protocol.Block{protocol.TextBlock(p.unsupportedAttachmentText(attachment, "Document parsing is not enabled or no readable text could be extracted."))}, nil
	}
	body := fmt.Sprintf(`Extracted document text from "%s":`+"\n%s", attachmentLabel(attachment), extracted)
	if extractedPath != "" {
		body += fmt.Sprintf("\n\n[Truncated. Full extracted text saved to %s]", extractedPath)
	}
	if strategy != "" {
		body += fmt.Sprintf("\n\n[Extraction source: %s]", strategy)
	}
	return []protocol.Block{protocol.TextBlock(body)}, nil
}

func (p *Processor) audioBlocks(ctx context.Context, buildCtx BuildContext, attachment protocol.Attachment) ([]protocol.Block, error) {
	if !p.cfg.Audio.Enabled {
		return []protocol.Block{protocol.TextBlock(p.unsupportedAttachmentText(attachment, "Audio transcription is not enabled in the current environment."))}, nil
	}
	path, err := p.attachmentPath(attachment)
	if err != nil {
		return []protocol.Block{protocol.TextBlock(p.unsupportedAttachmentText(attachment, err.Error()))}, nil
	}
	transcript, extractedPath, err := p.transcribeMedia(ctx, buildCtx.SessionID, attachment, path, false)
	if err != nil || strings.TrimSpace(transcript) == "" {
		return []protocol.Block{protocol.TextBlock(p.unsupportedAttachmentText(attachment, "Audio transcription is not available in the current environment."))}, nil
	}
	body := fmt.Sprintf(`Audio transcript for "%s":`+"\n%s", attachmentLabel(attachment), transcript)
	if extractedPath != "" {
		body += fmt.Sprintf("\n\n[Truncated. Full transcript saved to %s]", extractedPath)
	}
	return []protocol.Block{protocol.TextBlock(body)}, nil
}

func (p *Processor) videoBlocks(ctx context.Context, buildCtx BuildContext, attachment protocol.Attachment) ([]protocol.Block, error) {
	if !p.cfg.Video.Enabled {
		return []protocol.Block{protocol.TextBlock(p.unsupportedAttachmentText(attachment, "Video parsing is not enabled in the current environment."))}, nil
	}
	path, err := p.attachmentPath(attachment)
	if err != nil {
		return []protocol.Block{protocol.TextBlock(p.unsupportedAttachmentText(attachment, err.Error()))}, nil
	}
	info, _ := p.inspectMedia(ctx, path)
	transcript, extractedPath, err := p.transcribeMedia(ctx, buildCtx.SessionID, attachment, path, true)
	if err != nil {
		transcript = ""
	}
	framePaths, frameInterval, frameErr := p.extractVideoFrames(ctx, buildCtx.SessionID, attachment, path, info.DurationSeconds)

	if strings.TrimSpace(transcript) == "" && len(framePaths) == 0 {
		return []protocol.Block{protocol.TextBlock(p.unsupportedAttachmentText(attachment, "Video parsing is not enabled in the current environment."))}, nil
	}

	var details []string
	if info.DurationSeconds > 0 {
		details = append(details, fmt.Sprintf("duration=%.1fs", info.DurationSeconds))
	}
	if info.Width > 0 && info.Height > 0 {
		details = append(details, fmt.Sprintf("resolution=%dx%d", info.Width, info.Height))
	}
	body := fmt.Sprintf(`Video summary context for "%s"`, attachmentLabel(attachment))
	if len(details) > 0 {
		body += " (" + strings.Join(details, ", ") + ")"
	}
	body += ":"
	if strings.TrimSpace(transcript) != "" {
		body += "\n\nTranscript:\n" + transcript
		if extractedPath != "" {
			body += fmt.Sprintf("\n\n[Truncated. Full transcript saved to %s]", extractedPath)
		}
	}
	if len(framePaths) > 0 {
		body += fmt.Sprintf("\n\nKey frames follow as image inputs (interval %.0fs, %d frame(s)).", frameInterval, len(framePaths))
	} else if frameErr != nil {
		body += fmt.Sprintf("\n\n[Key frame extraction unavailable: %v]", frameErr)
	}

	blocks := []protocol.Block{protocol.TextBlock(body)}
	if buildCtx.SupportsImage {
		for _, framePath := range framePaths {
			frameData, frameType, err := readAttachmentBytes(framePath, "")
			if err != nil {
				continue
			}
			blocks = append(blocks, protocol.ImageBlock(frameType, base64.StdEncoding.EncodeToString(frameData)))
		}
	}
	return blocks, nil
}

func (p *Processor) extractTextLikeAttachment(sessionID string, attachment protocol.Attachment) (string, string, error) {
	if text, truncatedPath, ok := p.loadCachedText(sessionID, attachment); ok {
		return text, truncatedPath, nil
	}
	path, err := p.attachmentPath(attachment)
	if err != nil {
		return "", "", err
	}
	content, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	mimeType := strings.ToLower(strings.TrimSpace(attachment.MIMEType))
	ext := strings.ToLower(filepath.Ext(path))
	text := string(content)
	switch {
	case mimeType == "application/json" || ext == ".json":
		var pretty any
		if err := json.Unmarshal(content, &pretty); err == nil {
			if formatted, err := json.MarshalIndent(pretty, "", "  "); err == nil {
				text = string(formatted)
			}
		}
	case strings.HasSuffix(ext, ".html") || ext == ".htm" || mimeType == "text/html":
		if converted, err := htmltomarkdown.ConvertString(text); err == nil {
			text = converted
		}
	}
	text, truncatedPath, err := p.truncateAndPersist(sessionID, attachment, "text", "local-text", text, p.cfg.Document.MaxChars)
	return text, truncatedPath, err
}

func (p *Processor) extractDocumentText(ctx context.Context, sessionID string, attachment protocol.Attachment) (string, string, string, error) {
	if text, truncatedPath, ok := p.loadCachedText(sessionID, attachment); ok {
		meta := p.loadMeta(sessionID, attachment)
		return text, truncatedPath, meta.Strategy, nil
	}
	path, err := p.attachmentPath(attachment)
	if err != nil {
		return "", "", "", err
	}
	if p.moonshot != nil {
		if text, err := p.moonshot.ExtractText(ctx, path, attachmentLabel(attachment)); err == nil && strings.TrimSpace(text) != "" {
			truncated, truncatedPath, err := p.truncateAndPersist(sessionID, attachment, "document", "moonshot-file-extract", text, p.cfg.Document.MaxChars)
			return truncated, truncatedPath, "moonshot-file-extract", err
		}
	}
	text, strategy, err := p.extractDocumentLocally(ctx, attachment, path)
	if err != nil {
		p.writeMeta(sessionID, attachment, DerivedMeta{Kind: "document", Strategy: "local-fallback", Status: "error", Error: err.Error(), GeneratedAt: time.Now()})
		return "", "", "", err
	}
	truncated, truncatedPath, err := p.truncateAndPersist(sessionID, attachment, "document", strategy, text, p.cfg.Document.MaxChars)
	return truncated, truncatedPath, strategy, err
}

func (p *Processor) extractDocumentLocally(ctx context.Context, attachment protocol.Attachment, path string) (string, string, error) {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".pdf":
		text, err := p.extractPDFText(ctx, path)
		return text, "pdftotext", err
	case ".docx":
		text, err := extractDOCXText(path)
		return text, "local-docx", err
	case ".xlsx":
		text, err := extractXLSXText(path)
		return text, "local-xlsx", err
	case ".pptx":
		text, err := extractPPTXText(path)
		return text, "local-pptx", err
	default:
		return "", "", fmt.Errorf("unsupported document type %q", filepath.Ext(path))
	}
}

func (p *Processor) extractOCRText(ctx context.Context, sessionID string, attachment protocol.Attachment, path string) (string, string, string, error) {
	mode := strings.ToLower(strings.TrimSpace(p.cfg.OCR.Mode))
	if mode == "" {
		mode = "auto"
	}
	if mode == "disabled" {
		return "", "", "", fmt.Errorf("ocr disabled")
	}
	if text, truncatedPath, ok := p.loadCachedText(sessionID, attachment); ok {
		meta := p.loadMeta(sessionID, attachment)
		if meta.Kind == "ocr" {
			return text, truncatedPath, meta.Strategy, nil
		}
	}
	tryMoonshot := mode == "auto" || mode == "moonshot"
	if tryMoonshot && p.moonshot != nil {
		if text, err := p.moonshot.ExtractText(ctx, path, attachmentLabel(attachment)); err == nil && strings.TrimSpace(text) != "" {
			truncated, truncatedPath, err := p.truncateAndPersist(sessionID, attachment, "ocr", "moonshot-file-extract", text, p.cfg.OCR.MaxChars)
			return truncated, truncatedPath, "moonshot-file-extract", err
		}
	}
	tryTesseract := mode == "auto" || mode == "tesseract"
	if tryTesseract {
		text, err := p.extractWithTesseract(ctx, path)
		if err == nil && strings.TrimSpace(text) != "" {
			truncated, truncatedPath, err := p.truncateAndPersist(sessionID, attachment, "ocr", "tesseract", text, p.cfg.OCR.MaxChars)
			return truncated, truncatedPath, "tesseract", err
		}
	}
	return "", "", "", fmt.Errorf("ocr unavailable")
}

func (p *Processor) transcribeMedia(ctx context.Context, sessionID string, attachment protocol.Attachment, sourcePath string, forceAudioOnly bool) (string, string, error) {
	if text, truncatedPath, ok := p.loadCachedText(sessionID, attachment); ok {
		meta := p.loadMeta(sessionID, attachment)
		if meta.Kind == "audio" || meta.Kind == "video" {
			return text, truncatedPath, nil
		}
	}
	if strings.TrimSpace(p.cfg.Audio.FFmpegPath) == "" || strings.TrimSpace(p.cfg.Audio.FFprobePath) == "" || strings.TrimSpace(p.cfg.Audio.WhisperCPPPath) == "" || strings.TrimSpace(p.cfg.Audio.WhisperModelPath) == "" {
		return "", "", fmt.Errorf("audio transcription dependencies are not configured")
	}
	info, _ := p.inspectMedia(ctx, sourcePath)
	segmentLen := 600.0
	segments := 1
	if info.DurationSeconds > 20*60 {
		segments = int(math.Ceil(info.DurationSeconds / segmentLen))
	}
	artifactsDir := filepath.Join(p.derivedDir(sessionID, attachment), "artifacts")
	if err := os.MkdirAll(artifactsDir, 0755); err != nil {
		return "", "", err
	}
	texts := make([]string, 0, segments)
	for i := 0; i < segments; i++ {
		start := float64(i) * segmentLen
		duration := segmentLen
		if info.DurationSeconds > 0 && start+duration > info.DurationSeconds {
			duration = info.DurationSeconds - start
		}
		wavPath := filepath.Join(artifactsDir, fmt.Sprintf("segment-%02d.wav", i))
		if err := p.convertToWAV(ctx, sourcePath, wavPath, start, duration, forceAudioOnly); err != nil {
			return "", "", err
		}
		transcriptPath, err := p.runWhisperCPP(ctx, wavPath)
		if err != nil {
			return "", "", err
		}
		data, err := os.ReadFile(transcriptPath)
		if err != nil {
			return "", "", err
		}
		if text := strings.TrimSpace(string(data)); text != "" {
			texts = append(texts, text)
		}
	}
	combined := strings.Join(texts, "\n\n")
	kind := "audio"
	if forceAudioOnly {
		kind = "video"
	}
	return p.truncateAndPersist(sessionID, attachment, kind, "ffmpeg+whisper.cpp", combined, p.cfg.Audio.MaxChars)
}

func (p *Processor) extractVideoFrames(ctx context.Context, sessionID string, attachment protocol.Attachment, sourcePath string, duration float64) ([]string, float64, error) {
	framesDir := filepath.Join(p.derivedDir(sessionID, attachment), "frames")
	if existing, err := listFrameFiles(framesDir); err == nil && len(existing) > 0 {
		interval := float64(maxInt(1, p.cfg.Video.KeyframeIntervalSeconds))
		if duration > 0 {
			interval = maxFloat(interval, math.Ceil(duration/float64(maxInt(1, p.cfg.Video.MaxFrames))))
		}
		return existing, interval, nil
	}
	if strings.TrimSpace(p.cfg.Audio.FFmpegPath) == "" {
		return nil, 0, fmt.Errorf("ffmpeg is not configured")
	}
	if err := os.MkdirAll(framesDir, 0755); err != nil {
		return nil, 0, err
	}
	maxFrames := maxInt(1, p.cfg.Video.MaxFrames)
	interval := float64(maxInt(1, p.cfg.Video.KeyframeIntervalSeconds))
	if duration > 0 {
		interval = maxFloat(interval, math.Ceil(duration/float64(maxFrames)))
	}
	pattern := filepath.Join(framesDir, "frame-%03d.png")
	args := []string{"-y", "-i", sourcePath, "-vf", fmt.Sprintf("fps=1/%g", interval), "-frames:v", strconv.Itoa(maxFrames), pattern}
	if _, err := execCommand(ctx, p.cfg.Audio.FFmpegPath, args...); err != nil {
		return nil, interval, err
	}
	frames, err := listFrameFiles(framesDir)
	if err != nil {
		return nil, interval, err
	}
	meta := DerivedMeta{Kind: "video", Strategy: "ffmpeg-keyframes", Status: "ok", GeneratedAt: time.Now()}
	p.writeMeta(sessionID, attachment, meta)
	return frames, interval, nil
}

func (p *Processor) convertToWAV(ctx context.Context, sourcePath, outPath string, start, duration float64, forceAudioOnly bool) error {
	args := []string{"-y"}
	if start > 0 {
		args = append(args, "-ss", fmt.Sprintf("%.3f", start))
	}
	args = append(args, "-i", sourcePath)
	if duration > 0 {
		args = append(args, "-t", fmt.Sprintf("%.3f", duration))
	}
	if forceAudioOnly {
		args = append(args, "-vn")
	}
	args = append(args, "-ac", "1", "-ar", "16000", outPath)
	_, err := execCommand(ctx, p.cfg.Audio.FFmpegPath, args...)
	return err
}

func (p *Processor) runWhisperCPP(ctx context.Context, wavPath string) (string, error) {
	outBase := strings.TrimSuffix(wavPath, filepath.Ext(wavPath))
	args := []string{"-m", p.cfg.Audio.WhisperModelPath, "-f", wavPath, "-otxt", "-of", outBase}
	if _, err := execCommand(ctx, p.cfg.Audio.WhisperCPPPath, args...); err != nil {
		return "", err
	}
	return outBase + ".txt", nil
}

func (p *Processor) inspectMedia(ctx context.Context, path string) (mediaInfo, error) {
	type ffprobeStream struct {
		Width  int `json:"width"`
		Height int `json:"height"`
	}
	type ffprobeFormat struct {
		Duration string `json:"duration"`
	}
	type ffprobeResult struct {
		Streams []ffprobeStream `json:"streams"`
		Format  ffprobeFormat   `json:"format"`
	}
	out, err := execCommand(ctx, p.cfg.Audio.FFprobePath, "-v", "error", "-show_entries", "format=duration:stream=width,height", "-of", "json", path)
	if err != nil {
		return mediaInfo{}, err
	}
	var result ffprobeResult
	if err := json.Unmarshal(out, &result); err != nil {
		return mediaInfo{}, err
	}
	info := mediaInfo{}
	if result.Format.Duration != "" {
		if duration, err := strconv.ParseFloat(result.Format.Duration, 64); err == nil {
			info.DurationSeconds = duration
		}
	}
	for _, stream := range result.Streams {
		if stream.Width > 0 && stream.Height > 0 {
			info.Width = stream.Width
			info.Height = stream.Height
			break
		}
	}
	return info, nil
}

func (p *Processor) extractPDFText(ctx context.Context, path string) (string, error) {
	if strings.TrimSpace(p.cfg.Document.PDFToTextPath) == "" {
		return "", fmt.Errorf("pdftotext is not configured")
	}
	out, err := execCommand(ctx, p.cfg.Document.PDFToTextPath, "-layout", "-enc", "UTF-8", path, "-")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (p *Processor) extractWithTesseract(ctx context.Context, path string) (string, error) {
	if strings.TrimSpace(p.cfg.OCR.TesseractPath) == "" {
		return "", fmt.Errorf("tesseract is not configured")
	}
	out, err := execCommand(ctx, p.cfg.OCR.TesseractPath, path, "stdout", "-l", "chi_sim+eng", "--psm", "6")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (p *Processor) truncateAndPersist(sessionID string, attachment protocol.Attachment, kind, strategy, content string, maxChars int) (string, string, error) {
	content = strings.TrimSpace(content)
	if content == "" {
		return "", "", nil
	}
	dir := p.derivedDir(sessionID, attachment)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", "", err
	}
	fullPath := filepath.Join(dir, "extract.txt")
	if err := os.WriteFile(fullPath, []byte(content), 0644); err != nil {
		return "", "", err
	}
	relFullPath := p.relativeToWorkspace(fullPath)
	meta := DerivedMeta{Kind: kind, Strategy: strategy, Status: "ok", GeneratedAt: time.Now()}
	p.writeMeta(sessionID, attachment, meta)

	if maxChars <= 0 {
		return content, "", nil
	}
	runes := []rune(content)
	if len(runes) <= maxChars {
		return content, "", nil
	}
	return string(runes[:maxChars]), relFullPath, nil
}

func (p *Processor) loadCachedText(sessionID string, attachment protocol.Attachment) (string, string, bool) {
	dir := p.derivedDir(sessionID, attachment)
	path := filepath.Join(dir, "extract.txt")
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", false
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return "", "", false
	}
	relPath := p.relativeToWorkspace(path)
	maxChars := p.cacheMaxCharsForAttachment(attachment)
	runes := []rune(text)
	if maxChars > 0 && len(runes) > maxChars {
		return string(runes[:maxChars]), relPath, true
	}
	return text, "", true
}

func (p *Processor) cacheMaxCharsForAttachment(attachment protocol.Attachment) int {
	switch classifyAttachment(attachment) {
	case "image":
		return p.cfg.OCR.MaxChars
	case "audio", "video":
		return p.cfg.Audio.MaxChars
	default:
		return p.cfg.Document.MaxChars
	}
}

func (p *Processor) loadMeta(sessionID string, attachment protocol.Attachment) DerivedMeta {
	data, err := os.ReadFile(filepath.Join(p.derivedDir(sessionID, attachment), "meta.json"))
	if err != nil {
		return DerivedMeta{}
	}
	var meta DerivedMeta
	if err := json.Unmarshal(data, &meta); err != nil {
		return DerivedMeta{}
	}
	return meta
}

func (p *Processor) writeMeta(sessionID string, attachment protocol.Attachment, meta DerivedMeta) {
	dir := p.derivedDir(sessionID, attachment)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return
	}
	data, err := json.MarshalIndent(meta, "", "  ")
	if err != nil {
		return
	}
	_ = os.WriteFile(filepath.Join(dir, "meta.json"), data, 0644)
}

func (p *Processor) derivedDir(sessionID string, attachment protocol.Attachment) string {
	id := strings.TrimSpace(attachment.ID)
	if id == "" {
		id = safeAttachmentName(attachmentLabel(attachment))
	}
	if sessionID == "" {
		return filepath.Join(p.tempDir, "derived", id)
	}
	return filepath.Join(p.sessionsDir, sessionID, "derived", id)
}

func (p *Processor) attachmentPath(attachment protocol.Attachment) (string, error) {
	path := strings.TrimSpace(attachment.Path)
	if path == "" {
		return "", fmt.Errorf("missing attachment path")
	}
	if filepath.IsAbs(path) {
		return path, nil
	}
	if p.workspaceDir == "" {
		return filepath.Clean(path), nil
	}
	return filepath.Join(p.workspaceDir, path), nil
}

func (p *Processor) relativeToWorkspace(path string) string {
	if path == "" || p.workspaceDir == "" {
		return path
	}
	if rel, err := filepath.Rel(p.workspaceDir, path); err == nil {
		return filepath.ToSlash(rel)
	}
	return path
}

func (p *Processor) unsupportedAttachmentText(attachment protocol.Attachment, reason string) string {
	line := attachmentLabel(attachment)
	if attachment.MIMEType != "" {
		line += " (" + attachment.MIMEType + ")"
	}
	if strings.TrimSpace(reason) == "" {
		reason = "Parsing for this attached file type is not enabled in the current environment."
	}
	return reason + "\n- " + line
}

func classifyAttachment(attachment protocol.Attachment) string {
	mimeType := strings.ToLower(strings.TrimSpace(attachment.MIMEType))
	ext := strings.ToLower(filepath.Ext(strings.TrimSpace(attachment.Name)))
	if ext == "" {
		ext = strings.ToLower(filepath.Ext(strings.TrimSpace(attachment.Path)))
	}
	switch {
	case strings.HasPrefix(mimeType, "image/") || isExt(ext, ".png", ".jpg", ".jpeg", ".webp", ".gif", ".jfif"):
		return "image"
	case strings.HasPrefix(mimeType, "text/") || mimeType == "application/json" || isExt(ext, ".txt", ".md", ".markdown", ".csv", ".json", ".html", ".htm"):
		return "text"
	case mimeType == "application/pdf" || isExt(ext, ".pdf", ".docx", ".xlsx", ".pptx"):
		return "document"
	case strings.HasPrefix(mimeType, "audio/") || isExt(ext, ".mp3", ".wav", ".m4a", ".aac", ".ogg", ".flac"):
		return "audio"
	case strings.HasPrefix(mimeType, "video/") || isExt(ext, ".mp4", ".mov", ".mkv", ".avi", ".webm"):
		return "video"
	default:
		return "unsupported"
	}
}

func isExt(ext string, allowed ...string) bool {
	for _, candidate := range allowed {
		if ext == candidate {
			return true
		}
	}
	return false
}

func readAttachmentBytes(path, explicitType string) ([]byte, string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, "", err
	}
	mediaType := strings.TrimSpace(explicitType)
	if mediaType == "" {
		switch strings.ToLower(filepath.Ext(path)) {
		case ".png":
			mediaType = "image/png"
		case ".jpg", ".jpeg", ".jfif":
			mediaType = "image/jpeg"
		case ".webp":
			mediaType = "image/webp"
		case ".gif":
			mediaType = "image/gif"
		default:
			mediaType = "application/octet-stream"
		}
	}
	return data, mediaType, nil
}

func attachmentLabel(attachment protocol.Attachment) string {
	for _, candidate := range []string{attachment.Name, filepath.Base(strings.TrimSpace(attachment.Path)), attachment.URL, attachment.ID, "attachment"} {
		trimmed := strings.TrimSpace(candidate)
		if trimmed != "" && trimmed != "." {
			return trimmed
		}
	}
	return "attachment"
}

func safeAttachmentName(name string) string {
	name = strings.TrimSpace(name)
	if name == "" {
		return "attachment"
	}
	name = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z':
			return r
		case r >= 'A' && r <= 'Z':
			return r
		case r >= '0' && r <= '9':
			return r
		case r == '.', r == '-', r == '_':
			return r
		default:
			return '_'
		}
	}, name)
	return strings.Trim(name, "._")
}

func execCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return nil, fmt.Errorf("%s %v: %w: %s", name, args, err, strings.TrimSpace(string(out)))
	}
	return out, nil
}

func extractDOCXText(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	for _, file := range r.File {
		if file.Name != "word/document.xml" {
			continue
		}
		rc, err := file.Open()
		if err != nil {
			return "", err
		}
		defer rc.Close()
		return collectWordprocessingMLText(rc)
	}
	return "", fmt.Errorf("word/document.xml not found")
}

func collectWordprocessingMLText(r io.Reader) (string, error) {
	decoder := xml.NewDecoder(r)
	var b strings.Builder
	inText := false
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch typed := tok.(type) {
		case xml.StartElement:
			switch typed.Name.Local {
			case "t":
				inText = true
			case "tab":
				b.WriteString("\t")
			case "br", "cr":
				b.WriteString("\n")
			}
		case xml.EndElement:
			switch typed.Name.Local {
			case "t":
				inText = false
			case "p", "tr":
				b.WriteString("\n")
			case "tc":
				b.WriteString("\t")
			}
		case xml.CharData:
			if inText {
				b.Write([]byte(typed))
			}
		}
	}
	return strings.TrimSpace(b.String()), nil
}

func extractPPTXText(path string) (string, error) {
	r, err := zip.OpenReader(path)
	if err != nil {
		return "", err
	}
	defer r.Close()
	type slideFile struct {
		number int
		file   *zip.File
	}
	slides := make([]slideFile, 0)
	for _, file := range r.File {
		if !strings.HasPrefix(file.Name, "ppt/slides/slide") || !strings.HasSuffix(file.Name, ".xml") {
			continue
		}
		number := trailingNumber(file.Name)
		slides = append(slides, slideFile{number: number, file: file})
	}
	sort.Slice(slides, func(i, j int) bool { return slides[i].number < slides[j].number })
	var sections []string
	for idx, slide := range slides {
		rc, err := slide.file.Open()
		if err != nil {
			return "", err
		}
		text, err := collectPresentationText(rc)
		rc.Close()
		if err != nil {
			return "", err
		}
		if strings.TrimSpace(text) == "" {
			continue
		}
		number := slide.number
		if number <= 0 {
			number = idx + 1
		}
		sections = append(sections, fmt.Sprintf("## Slide %d\n%s", number, text))
	}
	if len(sections) == 0 {
		return "", fmt.Errorf("no slide text found")
	}
	return strings.Join(sections, "\n\n"), nil
}

func collectPresentationText(r io.Reader) (string, error) {
	decoder := xml.NewDecoder(r)
	var lines []string
	var current strings.Builder
	inText := false
	flush := func(force bool) {
		line := strings.TrimSpace(current.String())
		if line != "" || force {
			if line != "" {
				lines = append(lines, line)
			}
		}
		current.Reset()
	}
	for {
		tok, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			return "", err
		}
		switch typed := tok.(type) {
		case xml.StartElement:
			if typed.Name.Local == "t" {
				inText = true
			}
		case xml.EndElement:
			switch typed.Name.Local {
			case "t":
				inText = false
			case "p":
				flush(false)
			}
		case xml.CharData:
			if inText {
				current.Write([]byte(typed))
			}
		}
	}
	flush(false)
	return strings.Join(lines, "\n"), nil
}

func extractXLSXText(path string) (string, error) {
	file, err := excelize.OpenFile(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	sheets := file.GetSheetList()
	sections := make([]string, 0, len(sheets))
	for _, sheet := range sheets {
		rows, err := file.GetRows(sheet)
		if err != nil {
			return "", err
		}
		lines := make([]string, 0, len(rows))
		for _, row := range rows {
			last := len(row) - 1
			for last >= 0 && strings.TrimSpace(row[last]) == "" {
				last--
			}
			if last < 0 {
				continue
			}
			lines = append(lines, strings.Join(row[:last+1], "\t"))
		}
		if len(lines) == 0 {
			continue
		}
		sections = append(sections, fmt.Sprintf("## Sheet: %s\n%s", sheet, strings.Join(lines, "\n")))
	}
	if len(sections) == 0 {
		return "", fmt.Errorf("no spreadsheet text found")
	}
	return strings.Join(sections, "\n\n"), nil
}

func listFrameFiles(dir string) ([]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := strings.ToLower(entry.Name())
		if strings.HasSuffix(name, ".png") || strings.HasSuffix(name, ".jpg") || strings.HasSuffix(name, ".jpeg") {
			paths = append(paths, filepath.Join(dir, entry.Name()))
		}
	}
	sort.Strings(paths)
	return paths, nil
}

func trailingNumber(path string) int {
	base := filepath.Base(path)
	base = strings.TrimSuffix(base, filepath.Ext(base))
	for i := len(base) - 1; i >= 0; i-- {
		if base[i] < '0' || base[i] > '9' {
			if i == len(base)-1 {
				return 0
			}
			n, _ := strconv.Atoi(base[i+1:])
			return n
		}
	}
	n, _ := strconv.Atoi(base)
	return n
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func maxFloat(a, b float64) float64 {
	if a > b {
		return a
	}
	return b
}
