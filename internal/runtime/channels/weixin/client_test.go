package weixin

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHTTPTransportUploadCiphertextUsesPOSTAndReturnsEncryptedParam(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("unexpected method %s", r.Method)
		}
		if got := r.URL.Query().Get("encrypted_query_param"); got != "upload-param" {
			t.Fatalf("unexpected encrypted query param %q", got)
		}
		if got := r.URL.Query().Get("filekey"); got != "file-key" {
			t.Fatalf("unexpected filekey %q", got)
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read body: %v", err)
		}
		if string(body) != "ciphertext" {
			t.Fatalf("unexpected body %q", string(body))
		}
		w.Header().Set("X-Encrypted-Param", "download-param")
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	transport := &httpTransport{client: server.Client()}
	param, err := transport.UploadCiphertext(context.Background(), server.URL+"/upload?encrypted_query_param=upload-param&filekey=file-key", []byte("ciphertext"))
	if err != nil {
		t.Fatalf("upload ciphertext: %v", err)
	}
	if param != "download-param" {
		t.Fatalf("unexpected encrypted param %q", param)
	}
}

func TestBuildCDNUploadURLIncludesFileKey(t *testing.T) {
	url, err := buildCDNUploadURL(defaultCDNBaseURL, "", "upload-param", "file-key")
	if err != nil {
		t.Fatalf("build upload url: %v", err)
	}
	if !strings.Contains(url, "encrypted_query_param=upload-param") {
		t.Fatalf("unexpected upload url %q", url)
	}
	if !strings.Contains(url, "filekey=file-key") {
		t.Fatalf("unexpected upload url %q", url)
	}
}
