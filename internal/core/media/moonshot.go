package media

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/tim5wang/godex/internal/core/config"
)

type MoonshotClient struct {
	baseURL    string
	apiKey     string
	httpClient *http.Client
}

func NewMoonshotClient(cfg config.MoonshotMediaConfig) *MoonshotClient {
	if !cfg.Enabled || strings.TrimSpace(cfg.BaseURL) == "" || strings.TrimSpace(cfg.APIKey) == "" {
		return nil
	}
	return &MoonshotClient{
		baseURL:    strings.TrimRight(strings.TrimSpace(cfg.BaseURL), "/"),
		apiKey:     strings.TrimSpace(cfg.APIKey),
		httpClient: &http.Client{},
	}
}

func (c *MoonshotClient) ExtractText(ctx context.Context, path, name string) (string, error) {
	if c == nil {
		return "", fmt.Errorf("moonshot media client is not configured")
	}
	fileID, err := c.uploadForExtract(ctx, path, name)
	if err != nil {
		return "", err
	}
	return c.fetchContent(ctx, fileID)
}

func (c *MoonshotClient) uploadForExtract(ctx context.Context, path, name string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()

	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	part, err := writer.CreateFormFile("file", name)
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(part, file); err != nil {
		return "", err
	}
	if err := writer.WriteField("purpose", "file-extract"); err != nil {
		return "", err
	}
	if err := writer.Close(); err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/files", &body)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)
	req.Header.Set("Content-Type", writer.FormDataContentType())

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("moonshot upload failed: %s", strings.TrimSpace(string(data)))
	}
	var payload struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return "", err
	}
	if strings.TrimSpace(payload.ID) == "" {
		return "", fmt.Errorf("moonshot upload response missing file id")
	}
	return payload.ID, nil
}

func (c *MoonshotClient) fetchContent(ctx context.Context, fileID string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/files/"+fileID+"/content", nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("Authorization", "Bearer "+c.apiKey)

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	data, _ := io.ReadAll(resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("moonshot content fetch failed: %s", strings.TrimSpace(string(data)))
	}
	text := strings.TrimSpace(string(data))
	if text != "" && !looksLikeJSON(resp.Header.Get("Content-Type"), text) {
		return text, nil
	}
	var generic map[string]any
	if err := json.Unmarshal(data, &generic); err == nil {
		for _, key := range []string{"content", "text", "data"} {
			if value := strings.TrimSpace(asString(generic[key])); value != "" {
				return value, nil
			}
		}
	}
	return strings.TrimSpace(string(data)), nil
}

func looksLikeJSON(contentType, body string) bool {
	if strings.Contains(strings.ToLower(contentType), "json") {
		return true
	}
	body = strings.TrimSpace(body)
	return strings.HasPrefix(body, "{") || strings.HasPrefix(body, "[")
}

func asString(value any) string {
	switch typed := value.(type) {
	case string:
		return typed
	case fmt.Stringer:
		return typed.String()
	case nil:
		return ""
	default:
		return fmt.Sprintf("%v", typed)
	}
}

func baseNameOrDefault(path, fallback string) string {
	name := strings.TrimSpace(filepath.Base(path))
	if name == "" || name == "." {
		return fallback
	}
	return name
}
