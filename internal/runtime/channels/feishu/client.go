package feishu

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	lark "github.com/larksuite/oapi-sdk-go/v3"
	larkcore "github.com/larksuite/oapi-sdk-go/v3/core"
	larkim "github.com/larksuite/oapi-sdk-go/v3/service/im/v1"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/platform/logger"
)

const defaultRequestTimeout = 30 * time.Second

type sdkClient struct {
	baseURL   string
	appID     string
	appSecret string
	client    *http.Client
	sdk       *lark.Client
}

type downloadedResource struct {
	ContentType string
	Data        []byte
}

func newHTTPClient(cfg config.FeishuConfig) *sdkClient {
	baseURL := resolveBaseURL(cfg.Domain)
	httpClient := &http.Client{Timeout: defaultRequestTimeout}
	return &sdkClient{
		baseURL:   baseURL,
		appID:     cfg.AppID,
		appSecret: cfg.AppSecret,
		client:    httpClient,
		sdk: lark.NewClient(
			cfg.AppID,
			cfg.AppSecret,
			lark.WithOpenBaseUrl(baseURL),
			lark.WithReqTimeout(defaultRequestTimeout),
			lark.WithHttpClient(httpClient),
			lark.WithEnableTokenCache(true),
			lark.WithLogger(sdkLogger{base: logger.New("feishu-sdk")}),
		),
	}
}

func (c *sdkClient) FetchWSEndpoint(ctx context.Context) (string, error) {
	body, err := json.Marshal(map[string]string{
		"AppID":     c.appID,
		"AppSecret": c.appSecret,
	})
	if err != nil {
		return "", err
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/callback/ws/endpoint", bytes.NewReader(body))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.client.Do(req)
	if err != nil {
		return "", fmt.Errorf("fetch feishu websocket endpoint: %w", err)
	}
	defer resp.Body.Close()

	var payload struct {
		Code int    `json:"code"`
		Msg  string `json:"msg"`
		Data struct {
			URL string `json:"URL"`
		} `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&payload); err != nil {
		return "", fmt.Errorf("decode feishu websocket endpoint: %w", err)
	}
	if payload.Code != 0 || strings.TrimSpace(payload.Data.URL) == "" {
		return "", fmt.Errorf("feishu websocket endpoint failed: code=%d msg=%s", payload.Code, payload.Msg)
	}
	return payload.Data.URL, nil
}

func (c *sdkClient) SendText(ctx context.Context, chatID, text string) error {
	content, err := json.Marshal(map[string]string{"text": text})
	if err != nil {
		return err
	}

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(
			larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType("text").
				Content(string(content)).
				Uuid(newMessageUUID()).
				Build(),
		).
		Build()

	resp, err := c.sdk.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("send feishu message: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("send feishu message: empty response")
	}
	if !resp.Success() {
		return fmt.Errorf("send feishu message failed: %s", resp.CodeError.ErrorResp())
	}
	return nil
}

func (c *sdkClient) SendFile(ctx context.Context, chatID, fileKey string) error {
	content, err := json.Marshal(map[string]string{"file_key": fileKey})
	if err != nil {
		return err
	}

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(
			larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType("file").
				Content(string(content)).
				Uuid(newMessageUUID()).
				Build(),
		).
		Build()

	resp, err := c.sdk.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("send feishu file message: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("send feishu file message: empty response")
	}
	if !resp.Success() {
		return fmt.Errorf("send feishu file message failed: %s", resp.CodeError.ErrorResp())
	}
	return nil
}

func (c *sdkClient) SendImage(ctx context.Context, chatID, imageKey string) error {
	content, err := json.Marshal(map[string]string{"image_key": imageKey})
	if err != nil {
		return err
	}

	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(
			larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType("image").
				Content(string(content)).
				Uuid(newMessageUUID()).
				Build(),
		).
		Build()

	resp, err := c.sdk.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("send feishu image message: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("send feishu image message: empty response")
	}
	if !resp.Success() {
		return fmt.Errorf("send feishu image message failed: %s", resp.CodeError.ErrorResp())
	}
	return nil
}

func (c *sdkClient) SendPost(ctx context.Context, chatID, content string) error {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(
			larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType(larkim.MsgTypePost).
				Content(content).
				Uuid(newMessageUUID()).
				Build(),
		).
		Build()

	resp, err := c.sdk.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return fmt.Errorf("send feishu post message: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("send feishu post message: empty response")
	}
	if !resp.Success() {
		return fmt.Errorf("send feishu post message failed: %s", resp.CodeError.ErrorResp())
	}
	return nil
}

func (c *sdkClient) CreateCard(ctx context.Context, chatID, content string) (string, error) {
	req := larkim.NewCreateMessageReqBuilder().
		ReceiveIdType(larkim.ReceiveIdTypeChatId).
		Body(
			larkim.NewCreateMessageReqBodyBuilder().
				ReceiveId(chatID).
				MsgType(larkim.MsgTypeInteractive).
				Content(content).
				Uuid(newMessageUUID()).
				Build(),
		).
		Build()

	resp, err := c.sdk.Im.V1.Message.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("send feishu interactive card: %w", err)
	}
	if resp == nil {
		return "", fmt.Errorf("send feishu interactive card: empty response")
	}
	if !resp.Success() {
		return "", fmt.Errorf("send feishu interactive card failed: %s", resp.CodeError.ErrorResp())
	}
	messageID := strings.TrimSpace(valueOrEmpty(resp.Data.MessageId))
	if messageID == "" {
		return "", fmt.Errorf("send feishu interactive card failed: missing message id")
	}
	return messageID, nil
}

func (c *sdkClient) PatchCard(ctx context.Context, messageID, content string) error {
	req := larkim.NewPatchMessageReqBuilder().
		MessageId(messageID).
		Body(
			larkim.NewPatchMessageReqBodyBuilder().
				Content(content).
				Build(),
		).
		Build()

	resp, err := c.sdk.Im.V1.Message.Patch(ctx, req)
	if err != nil {
		return fmt.Errorf("patch feishu interactive card: %w", err)
	}
	if resp == nil {
		return fmt.Errorf("patch feishu interactive card: empty response")
	}
	if !resp.Success() {
		return fmt.Errorf("patch feishu interactive card failed: %s", resp.CodeError.ErrorResp())
	}
	return nil
}

func (c *sdkClient) UploadFile(ctx context.Context, path, name string) (string, error) {
	absolutePath, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	file, err := os.Open(absolutePath)
	if err != nil {
		return "", fmt.Errorf("open feishu file upload source: %w", err)
	}
	defer file.Close()

	displayName := strings.TrimSpace(name)
	if displayName == "" {
		displayName = filepath.Base(absolutePath)
	}
	fileType := strings.TrimPrefix(strings.ToLower(filepath.Ext(displayName)), ".")
	if fileType == "" {
		fileType = "stream"
	}

	req := larkim.NewCreateFileReqBuilder().
		Body(
			larkim.NewCreateFileReqBodyBuilder().
				FileType(fileType).
				FileName(displayName).
				File(file).
				Build(),
		).
		Build()

	resp, err := c.sdk.Im.V1.File.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("upload feishu file: %w", err)
	}
	if resp == nil {
		return "", fmt.Errorf("upload feishu file: empty response")
	}
	if !resp.Success() {
		return "", fmt.Errorf("upload feishu file failed: %s", resp.CodeError.ErrorResp())
	}
	fileKey := strings.TrimSpace(valueOrEmpty(resp.Data.FileKey))
	if fileKey == "" {
		return "", fmt.Errorf("upload feishu file failed: missing file key")
	}
	return fileKey, nil
}

func (c *sdkClient) UploadImage(ctx context.Context, path string) (string, error) {
	absolutePath, err := filepath.Abs(strings.TrimSpace(path))
	if err != nil {
		return "", err
	}
	file, err := os.Open(absolutePath)
	if err != nil {
		return "", fmt.Errorf("open feishu image upload source: %w", err)
	}
	defer file.Close()

	req := larkim.NewCreateImageReqBuilder().
		Body(
			larkim.NewCreateImageReqBodyBuilder().
				ImageType("message").
				Image(file).
				Build(),
		).
		Build()

	resp, err := c.sdk.Im.V1.Image.Create(ctx, req)
	if err != nil {
		return "", fmt.Errorf("upload feishu image: %w", err)
	}
	if resp == nil {
		return "", fmt.Errorf("upload feishu image: empty response")
	}
	if !resp.Success() {
		return "", fmt.Errorf("upload feishu image failed: %s", resp.CodeError.ErrorResp())
	}
	imageKey := strings.TrimSpace(valueOrEmpty(resp.Data.ImageKey))
	if imageKey == "" {
		return "", fmt.Errorf("upload feishu image failed: missing image key")
	}
	return imageKey, nil
}

func (c *sdkClient) DownloadMessageResource(ctx context.Context, messageID, fileKey, resourceType string) (downloadedResource, error) {
	req := larkim.NewGetMessageResourceReqBuilder().
		MessageId(messageID).
		FileKey(fileKey).
		Type(resourceType).
		Build()

	resp, err := c.sdk.Im.V1.MessageResource.Get(ctx, req)
	if err != nil {
		return downloadedResource{}, fmt.Errorf("download feishu message resource: %w", err)
	}
	if resp == nil {
		return downloadedResource{}, fmt.Errorf("download feishu message resource failed: empty response")
	}
	if !resp.Success() {
		return downloadedResource{}, fmt.Errorf("download feishu message resource failed: %s", resp.CodeError.ErrorResp())
	}

	data, err := io.ReadAll(resp.File)
	if err != nil {
		return downloadedResource{}, fmt.Errorf("read feishu message resource: %w", err)
	}
	return downloadedResource{
		ContentType: strings.TrimSpace(resp.Header.Get("Content-Type")),
		Data:        data,
	}, nil
}

type sdkLogger struct {
	base *logger.Logger
}

func (l sdkLogger) Debug(ctx context.Context, args ...interface{}) {
	_ = ctx
	if l.base != nil {
		l.base.Debugf("%s", fmt.Sprint(args...))
	}
}

func (l sdkLogger) Info(ctx context.Context, args ...interface{}) {
	_ = ctx
	if l.base != nil {
		l.base.Infof("%s", fmt.Sprint(args...))
	}
}

func (l sdkLogger) Warn(ctx context.Context, args ...interface{}) {
	_ = ctx
	if l.base != nil {
		l.base.Warnf("%s", fmt.Sprint(args...))
	}
}

func (l sdkLogger) Error(ctx context.Context, args ...interface{}) {
	_ = ctx
	if l.base != nil {
		l.base.Errorf("%s", fmt.Sprint(args...))
	}
}

var _ larkcore.Logger = sdkLogger{}

func newMessageUUID() string {
	var buf [16]byte
	if _, err := rand.Read(buf[:]); err != nil {
		return fmt.Sprintf("godex-%d", time.Now().UnixNano())
	}
	return fmt.Sprintf(
		"%s-%s-%s-%s-%s",
		hex.EncodeToString(buf[0:4]),
		hex.EncodeToString(buf[4:6]),
		hex.EncodeToString(buf[6:8]),
		hex.EncodeToString(buf[8:10]),
		hex.EncodeToString(buf[10:16]),
	)
}

func resolveBaseURL(domain string) string {
	switch strings.TrimSpace(strings.ToLower(domain)) {
	case "", "lark":
		return "https://open.larksuite.com"
	case "feishu":
		return "https://open.feishu.cn"
	default:
		if strings.HasPrefix(domain, "http://") || strings.HasPrefix(domain, "https://") {
			return strings.TrimRight(domain, "/")
		}
		return (&url.URL{Scheme: "https", Host: domain}).String()
	}
}
