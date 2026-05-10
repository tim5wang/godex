package tools

import (
	"context"
	"fmt"
	"io"
	"net/http"
	urlpkg "net/url"
	"os"
	"path/filepath"
	"strings"
)

func downloadFileViaHTTP(ctx context.Context, dir, rawURL, fileName string) (string, string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return "", "", err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", "", fmt.Errorf("http download failed: %s", resp.Status)
	}
	resolvedName := sanitizeDownloadFileName(strings.TrimSpace(fileName))
	if resolvedName == "" {
		if parsed, err := urlpkg.Parse(rawURL); err == nil {
			resolvedName = sanitizeDownloadFileName(filepath.Base(parsed.Path))
		}
	}
	if resolvedName == "" || resolvedName == "." || resolvedName == "/" {
		resolvedName = "download.bin"
	}
	path := filepath.Join(dir, resolvedName)
	file, err := os.Create(path)
	if err != nil {
		return "", "", err
	}
	defer file.Close()
	if _, err := io.Copy(file, resp.Body); err != nil {
		return "", "", err
	}
	return path, resolvedName, nil
}
