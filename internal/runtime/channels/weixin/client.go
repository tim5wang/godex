package weixin

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/tim5wang/godex/internal/core/config"
)

var errSessionExpired = errors.New("weixin session expired")

type transport interface {
	GetBotQRCode(context.Context) (qrCodeResponse, error)
	GetQRCodeStatus(context.Context, string) (qrCodeStatus, error)
	GetUpdates(context.Context, string, int) (getUpdatesResponse, error)
	SendMessage(context.Context, weixinMessage) error
	GetConfig(context.Context, string, string) (getConfigResponse, error)
	SendTyping(context.Context, string, string, int) error
	GetUploadURL(context.Context, getUploadURLRequest) (getUploadURLResponse, error)
	UploadCiphertext(context.Context, string, []byte) (string, error)
	DownloadCiphertext(context.Context, string) ([]byte, string, error)
}

type httpTransport struct {
	baseURL    string
	cdnBaseURL string
	client     *http.Client
	botToken   string
}

var newTransportFunc = func(cfg config.WeixinConfig, botToken string) (transport, error) {
	return newHTTPTransport(cfg, botToken)
}

func newTransport(cfg config.WeixinConfig, botToken string) (transport, error) {
	return newTransportFunc(cfg, botToken)
}

func newHTTPTransport(cfg config.WeixinConfig, botToken string) (*httpTransport, error) {
	transportConfig := &http.Transport{}
	if proxy := strings.TrimSpace(cfg.Proxy); proxy != "" {
		parsed, err := url.Parse(proxy)
		if err != nil {
			return nil, fmt.Errorf("invalid weixin proxy: %w", err)
		}
		transportConfig.Proxy = http.ProxyURL(parsed)
	}
	baseURL := strings.TrimSpace(cfg.BaseURL)
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	timeoutMs := cfg.LongPollTimeoutMs
	if timeoutMs <= 0 {
		timeoutMs = defaultPollTimeoutMs
	}
	requestTimeout := time.Duration(timeoutMs)*time.Millisecond + 10*time.Second
	return &httpTransport{
		baseURL:    strings.TrimRight(baseURL, "/"),
		cdnBaseURL: strings.TrimRight(defaultIfEmpty(strings.TrimSpace(cfg.CDNBaseURL), defaultCDNBaseURL), "/"),
		client:     &http.Client{Timeout: requestTimeout, Transport: transportConfig},
		botToken:   strings.TrimSpace(botToken),
	}, nil
}

func (t *httpTransport) GetBotQRCode(ctx context.Context) (qrCodeResponse, error) {
	var out qrCodeResponse
	err := t.doJSON(ctx, http.MethodGet, "/ilink/bot/get_bot_qrcode?bot_type=3", nil, false, &out)
	return out, err
}

func (t *httpTransport) GetQRCodeStatus(ctx context.Context, qrcode string) (qrCodeStatus, error) {
	var out qrCodeStatus
	path := "/ilink/bot/get_qrcode_status?qrcode=" + url.QueryEscape(strings.TrimSpace(qrcode))
	err := t.doJSON(ctx, http.MethodGet, path, nil, false, &out)
	return out, err
}

func (t *httpTransport) GetUpdates(ctx context.Context, cursor string, timeoutMs int) (getUpdatesResponse, error) {
	if timeoutMs <= 0 {
		timeoutMs = defaultPollTimeoutMs
	}
	var out getUpdatesResponse
	err := t.doJSON(ctx, http.MethodPost, "/ilink/bot/getupdates", getUpdatesRequest{
		BaseInfo:           baseInfo{ChannelVersion: defaultChannelVer},
		GetUpdatesBuf:      strings.TrimSpace(cursor),
		LongPollingTimeout: timeoutMs,
	}, true, &out)
	if err != nil {
		return getUpdatesResponse{}, err
	}
	if out.Ret == -14 || out.ErrCode == -14 {
		return getUpdatesResponse{}, errSessionExpired
	}
	if out.Ret != 0 {
		return getUpdatesResponse{}, fmt.Errorf("weixin getupdates failed: ret=%d errcode=%d errmsg=%s", out.Ret, out.ErrCode, strings.TrimSpace(out.ErrMsg))
	}
	return out, nil
}

func (t *httpTransport) SendMessage(ctx context.Context, msg weixinMessage) error {
	var out sendMessageResponse
	err := t.doJSON(ctx, http.MethodPost, "/ilink/bot/sendmessage", sendMessageRequest{
		BaseInfo: baseInfo{ChannelVersion: defaultChannelVer},
		Msg:      msg,
	}, true, &out)
	if err != nil {
		return err
	}
	if out.Ret == -14 || out.ErrCode == -14 {
		return errSessionExpired
	}
	if out.Ret != 0 {
		return fmt.Errorf("weixin sendmessage failed: ret=%d errcode=%d errmsg=%s", out.Ret, out.ErrCode, strings.TrimSpace(out.ErrMsg))
	}
	return nil
}

func (t *httpTransport) GetConfig(ctx context.Context, userID, contextToken string) (getConfigResponse, error) {
	var out getConfigResponse
	err := t.doJSON(ctx, http.MethodPost, "/ilink/bot/getconfig", getConfigRequest{
		BaseInfo:     baseInfo{ChannelVersion: defaultChannelVer},
		ILinkUserID:  strings.TrimSpace(userID),
		ContextToken: strings.TrimSpace(contextToken),
	}, true, &out)
	if err != nil {
		return getConfigResponse{}, err
	}
	if out.Ret == -14 || out.ErrCode == -14 {
		return getConfigResponse{}, errSessionExpired
	}
	if out.Ret != 0 {
		return getConfigResponse{}, fmt.Errorf("weixin getconfig failed: ret=%d errcode=%d errmsg=%s", out.Ret, out.ErrCode, strings.TrimSpace(out.ErrMsg))
	}
	return out, nil
}

func (t *httpTransport) SendTyping(ctx context.Context, userID, typingTicket string, status int) error {
	var out sendTypingResponse
	err := t.doJSON(ctx, http.MethodPost, "/ilink/bot/sendtyping", sendTypingRequest{
		BaseInfo:     baseInfo{ChannelVersion: defaultChannelVer},
		ILinkUserID:  strings.TrimSpace(userID),
		TypingTicket: strings.TrimSpace(typingTicket),
		Status:       status,
	}, true, &out)
	if err != nil {
		return err
	}
	if out.Ret == -14 || out.ErrCode == -14 {
		return errSessionExpired
	}
	if out.Ret != 0 {
		return fmt.Errorf("weixin sendtyping failed: ret=%d errcode=%d errmsg=%s", out.Ret, out.ErrCode, strings.TrimSpace(out.ErrMsg))
	}
	return nil
}

func (t *httpTransport) GetUploadURL(ctx context.Context, req getUploadURLRequest) (getUploadURLResponse, error) {
	req.BaseInfo = baseInfo{ChannelVersion: defaultChannelVer}
	var out getUploadURLResponse
	err := t.doJSON(ctx, http.MethodPost, "/ilink/bot/getuploadurl", req, true, &out)
	if err != nil {
		return getUploadURLResponse{}, err
	}
	if out.Ret == -14 || out.ErrCode == -14 {
		return getUploadURLResponse{}, errSessionExpired
	}
	if out.Ret != 0 {
		return getUploadURLResponse{}, fmt.Errorf("weixin getuploadurl failed: ret=%d errcode=%d errmsg=%s", out.Ret, out.ErrCode, strings.TrimSpace(out.ErrMsg))
	}
	return out, nil
}

func (t *httpTransport) UploadCiphertext(ctx context.Context, fullURL string, ciphertext []byte) (string, error) {
	if strings.TrimSpace(fullURL) == "" {
		return "", fmt.Errorf("missing weixin upload url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimSpace(fullURL), bytes.NewReader(ciphertext))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	resp, err := t.client.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return "", fmt.Errorf("weixin cdn upload http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	return strings.TrimSpace(resp.Header.Get("X-Encrypted-Param")), nil
}

func (t *httpTransport) DownloadCiphertext(ctx context.Context, fullURL string) ([]byte, string, error) {
	if strings.TrimSpace(fullURL) == "" {
		return nil, "", fmt.Errorf("missing weixin download url")
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, strings.TrimSpace(fullURL), nil)
	if err != nil {
		return nil, "", err
	}
	resp, err := t.client.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return nil, "", fmt.Errorf("weixin cdn download http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, "", err
	}
	return data, strings.TrimSpace(resp.Header.Get("Content-Type")), nil
}

func (t *httpTransport) doJSON(ctx context.Context, method, path string, body any, authenticated bool, out any) error {
	var reader io.Reader
	if body != nil {
		data, err := json.Marshal(body)
		if err != nil {
			return err
		}
		reader = bytes.NewReader(data)
	}
	req, err := http.NewRequestWithContext(ctx, method, t.baseURL+path, reader)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if authenticated {
		if strings.TrimSpace(t.botToken) == "" {
			return fmt.Errorf("missing weixin bot token")
		}
		req.Header.Set("AuthorizationType", "ilink_bot_token")
		req.Header.Set("Authorization", "Bearer "+t.botToken)
		req.Header.Set("X-WECHAT-UIN", randomWechatUIN())
		req.Header.Set("iLink-App-Id", "godex")
		req.Header.Set("iLink-App-ClientVersion", "1.0.0")
	}

	resp, err := t.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		data, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("weixin http %d: %s", resp.StatusCode, strings.TrimSpace(string(data)))
	}
	if out == nil {
		_, _ = io.Copy(io.Discard, resp.Body)
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func randomWechatUIN() string {
	var raw [4]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return ""
	}
	value := binary.BigEndian.Uint32(raw[:])
	return base64.StdEncoding.EncodeToString([]byte(fmt.Sprintf("%d", value)))
}
