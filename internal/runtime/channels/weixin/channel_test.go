package weixin

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/tim5wang/godex/internal/agent"
	"github.com/tim5wang/godex/internal/core/config"
	"github.com/tim5wang/godex/internal/core/protocol"
	"github.com/tim5wang/godex/internal/runtime/channels"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
	"github.com/tim5wang/godex/internal/services/commands"
)

type stubCaller struct {
	mu        sync.Mutex
	responses []protocol.Response
	calls     int
	delay     time.Duration
}

func (c *stubCaller) Call(ctx context.Context, req protocol.Request) (*protocol.Response, error) {
	_ = ctx
	_ = req
	if c.delay > 0 {
		time.Sleep(c.delay)
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	resp := c.responses[c.calls]
	c.calls++
	return &resp, nil
}

type fakeTransport struct {
	mu sync.Mutex

	qr        qrCodeResponse
	statuses  []qrCodeStatus
	updates   []getUpdatesResponse
	updateErr error

	getUpdatesBufs []string
	getConfigCalls []string
	sendTyping     []sendTypingRequest
	sendMessages   []weixinMessage
	uploadRequests []getUploadURLRequest
	uploads        []uploadRecord
	downloads      map[string]downloadedPayload
	uploadResp     getUploadURLResponse
	uploadHeader   string
}

type uploadRecord struct {
	url  string
	data []byte
}

type downloadedPayload struct {
	data        []byte
	contentType string
}

func (f *fakeTransport) GetBotQRCode(context.Context) (qrCodeResponse, error) {
	return f.qr, nil
}

func (f *fakeTransport) GetQRCodeStatus(context.Context, string) (qrCodeStatus, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.statuses) == 0 {
		return qrCodeStatus{Status: "wait"}, nil
	}
	status := f.statuses[0]
	f.statuses = f.statuses[1:]
	return status, nil
}

func (f *fakeTransport) GetUpdates(ctx context.Context, cursor string, timeoutMs int) (getUpdatesResponse, error) {
	_ = timeoutMs
	f.mu.Lock()
	f.getUpdatesBufs = append(f.getUpdatesBufs, cursor)
	if f.updateErr != nil {
		err := f.updateErr
		f.updateErr = nil
		f.mu.Unlock()
		return getUpdatesResponse{}, err
	}
	if len(f.updates) > 0 {
		resp := f.updates[0]
		f.updates = f.updates[1:]
		f.mu.Unlock()
		return resp, nil
	}
	f.mu.Unlock()
	<-ctx.Done()
	return getUpdatesResponse{}, ctx.Err()
}

func (f *fakeTransport) SendMessage(ctx context.Context, msg weixinMessage) error {
	_ = ctx
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendMessages = append(f.sendMessages, msg)
	return nil
}

func (f *fakeTransport) GetConfig(ctx context.Context, userID, contextToken string) (getConfigResponse, error) {
	_ = ctx
	f.mu.Lock()
	defer f.mu.Unlock()
	f.getConfigCalls = append(f.getConfigCalls, userID+"|"+contextToken)
	return getConfigResponse{TypingTicket: "ticket-1"}, nil
}

func (f *fakeTransport) SendTyping(ctx context.Context, userID, typingTicket string, status int) error {
	_ = ctx
	f.mu.Lock()
	defer f.mu.Unlock()
	f.sendTyping = append(f.sendTyping, sendTypingRequest{
		ILinkUserID:  userID,
		TypingTicket: typingTicket,
		Status:       status,
	})
	return nil
}

func (f *fakeTransport) GetUploadURL(ctx context.Context, req getUploadURLRequest) (getUploadURLResponse, error) {
	_ = ctx
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploadRequests = append(f.uploadRequests, req)
	if f.uploadResp.UploadParam == "" && f.uploadResp.UploadFullURL == "" {
		return getUploadURLResponse{UploadParam: "upload-param"}, nil
	}
	return f.uploadResp, nil
}

func (f *fakeTransport) UploadCiphertext(ctx context.Context, fullURL string, ciphertext []byte) (string, error) {
	_ = ctx
	f.mu.Lock()
	defer f.mu.Unlock()
	f.uploads = append(f.uploads, uploadRecord{url: fullURL, data: append([]byte{}, ciphertext...)})
	return f.uploadHeader, nil
}

func (f *fakeTransport) DownloadCiphertext(ctx context.Context, fullURL string) ([]byte, string, error) {
	_ = ctx
	f.mu.Lock()
	defer f.mu.Unlock()
	payload, ok := f.downloads[fullURL]
	if !ok {
		return nil, "", errors.New("download not found")
	}
	return append([]byte{}, payload.data...), payload.contentType, nil
}

func TestStateStoreRoundTrip(t *testing.T) {
	store := newStateStore(t.TempDir(), "primary")
	if err := store.SaveAccount(&accountState{BotToken: "token", BaseURL: "https://ilink.example.com"}); err != nil {
		t.Fatalf("save account: %v", err)
	}
	if err := store.SaveCursor("cursor-1"); err != nil {
		t.Fatalf("save cursor: %v", err)
	}
	if err := store.SaveContextToken("user-1", "ctx-1"); err != nil {
		t.Fatalf("save context token: %v", err)
	}

	account, err := store.LoadAccount()
	if err != nil {
		t.Fatalf("load account: %v", err)
	}
	if account.BotToken != "token" {
		t.Fatalf("unexpected account token: %#v", account)
	}
	cursor, err := store.LoadCursor()
	if err != nil {
		t.Fatalf("load cursor: %v", err)
	}
	if cursor != "cursor-1" {
		t.Fatalf("unexpected cursor %q", cursor)
	}
	token, err := store.LookupContextToken("user-1")
	if err != nil {
		t.Fatalf("lookup context token: %v", err)
	}
	if token != "ctx-1" {
		t.Fatalf("unexpected context token %q", token)
	}
}

func TestSetupAndLogoutManageStateFiles(t *testing.T) {
	cfg := newWeixinTestConfig(t)
	fake := &fakeTransport{
		qr: qrCodeResponse{QRCode: "qr-1", QRCodeImgURL: "https://example.com/qr.png"},
		statuses: []qrCodeStatus{
			{Status: "wait"},
			{Status: "confirmed", BotToken: "bot-token", BaseURL: "https://ilink.example.com", ILinkBotID: "bot", ILinkUserID: "user"},
		},
	}

	previous := newTransportFunc
	newTransportFunc = func(cfg config.WeixinConfig, botToken string) (transport, error) {
		_ = cfg
		_ = botToken
		return fake, nil
	}
	defer func() { newTransportFunc = previous }()

	var stdout bytes.Buffer
	if err := Setup(context.Background(), cfg, &stdout); err != nil {
		t.Fatalf("setup: %v", err)
	}
	store := newStateStore(cfg.StateDir, cfg.Weixin.AccountID)
	if _, err := os.Stat(store.AccountPath()); err != nil {
		t.Fatalf("expected account state file: %v", err)
	}
	if _, err := os.Stat(store.CursorPath()); err != nil {
		t.Fatalf("expected cursor state file: %v", err)
	}
	if _, err := os.Stat(store.ContextTokensPath()); err != nil {
		t.Fatalf("expected context token state file: %v", err)
	}
	if !strings.Contains(stdout.String(), "Weixin login completed") {
		t.Fatalf("unexpected setup output %q", stdout.String())
	}
	if err := Logout(context.Background(), cfg, &stdout); err != nil {
		t.Fatalf("logout: %v", err)
	}
	if _, err := os.Stat(store.Root()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected logout to remove state dir, got err=%v", err)
	}
}

func TestWebAuthStartStatusAndLogout(t *testing.T) {
	cfg := newWeixinTestConfig(t)
	fake := &fakeTransport{
		qr: qrCodeResponse{QRCode: "qr-1", QRCodeImgURL: "https://example.com/qr.png"},
		statuses: []qrCodeStatus{
			{Status: "wait"},
			{Status: "confirmed", BotToken: "bot-token", BaseURL: "https://ilink.example.com", ILinkBotID: "bot", ILinkUserID: "user"},
		},
	}

	previous := newTransportFunc
	newTransportFunc = func(cfg config.WeixinConfig, botToken string) (transport, error) {
		_ = cfg
		_ = botToken
		return fake, nil
	}
	defer func() { newTransportFunc = previous }()

	reconciled := 0
	auth := NewWebAuth(func() *config.Config {
		return cfg.Clone()
	}, func(ctx context.Context, cfg *config.Config) error {
		_ = ctx
		_ = cfg
		reconciled++
		return nil
	})

	started, err := auth.Start(context.Background(), "")
	if err != nil {
		t.Fatalf("start web auth: %v", err)
	}
	if started.Login == nil || started.Login.QRCode != "qr-1" || !started.Login.Active {
		t.Fatalf("unexpected start status: %#v", started)
	}

	pending, err := auth.Status(context.Background(), "")
	if err != nil {
		t.Fatalf("status pending: %v", err)
	}
	if pending.Login == nil || pending.Login.State != "pending" {
		t.Fatalf("expected pending login state, got %#v", pending)
	}

	confirmed, err := auth.Status(context.Background(), "")
	if err != nil {
		t.Fatalf("status confirmed: %v", err)
	}
	if !confirmed.Configured || confirmed.Account == nil || confirmed.Account.ILinkBotID != "bot" {
		t.Fatalf("expected configured account after confirm, got %#v", confirmed)
	}
	if confirmed.Login == nil || confirmed.Login.State != "confirmed" {
		t.Fatalf("expected confirmed login state, got %#v", confirmed)
	}
	if reconciled != 1 {
		t.Fatalf("expected reconcile to run once, got %d", reconciled)
	}

	store := newStateStore(cfg.StateDir, cfg.Weixin.AccountID)
	if _, err := store.LoadAccount(); err != nil {
		t.Fatalf("load persisted account: %v", err)
	}

	loggedOut, err := auth.Logout(context.Background(), "")
	if err != nil {
		t.Fatalf("logout web auth: %v", err)
	}
	if loggedOut.Configured {
		t.Fatalf("expected logout to clear configured state, got %#v", loggedOut)
	}
	if _, err := os.Stat(store.Root()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("expected logout to remove state dir, got err=%v", err)
	}
}

func TestWebAuthRendersQRCodeURLAsDataImage(t *testing.T) {
	cfg := newWeixinTestConfig(t)
	fake := &fakeTransport{
		qr: qrCodeResponse{
			QRCode:           "qr-1",
			QRCodeImgContent: "https://liteapp.weixin.qq.com/q/demo?qrcode=qr-1&bot_type=3",
		},
	}

	previous := newTransportFunc
	newTransportFunc = func(cfg config.WeixinConfig, botToken string) (transport, error) {
		_ = cfg
		_ = botToken
		return fake, nil
	}
	defer func() { newTransportFunc = previous }()

	auth := NewWebAuth(func() *config.Config { return cfg.Clone() }, nil)
	status, err := auth.Start(context.Background(), "")
	if err != nil {
		t.Fatalf("start web auth: %v", err)
	}
	if status.Login == nil {
		t.Fatalf("expected login status, got %#v", status)
	}
	if !strings.HasPrefix(status.Login.QRCodeImgValue, "data:image/png;base64,") {
		t.Fatalf("expected rendered qr data url, got %q", status.Login.QRCodeImgValue)
	}
}

func TestChannelPollsTextMessagesAndUsesTypingAck(t *testing.T) {
	cfg := newWeixinTestConfig(t)
	service := newWeixinTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := channels.NewManager(cfg, service)
	manager.RegisterFactory(channelsStubFactory{name: channelName})
	manager.SetStatus(channelName, channels.ChannelStatusUpdate{
		Enabled: boolRef(true),
		Running: boolRef(false),
		State:   channels.StateStopped,
	})

	fake := &fakeTransport{
		updates: []getUpdatesResponse{{
			apiStatus:     apiStatus{Ret: 0},
			GetUpdatesBuf: "cursor-2",
			Msgs: []weixinMessage{{
				MessageID:    101,
				FromUserID:   "user-1@im.wechat",
				ToUserID:     "bot@im.bot",
				MessageType:  weixinMessageTypeUser,
				MessageState: weixinMessageStateFinish,
				ContextToken: "ctx-1",
				ItemList: []messageItem{{
					Type:     weixinItemTypeText,
					TextItem: &textItem{Text: "hello"},
				}},
			}},
		}},
	}
	channel, err := New(cfg, manager, WithTransport(fake))
	if err != nil {
		t.Fatalf("new channel: %v", err)
	}
	if err := channel.store.SaveAccount(&accountState{BotToken: "token", BaseURL: cfg.Weixin.BaseURL}); err != nil {
		t.Fatalf("save account state: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := channel.Start(ctx); err != nil {
		t.Fatalf("start channel: %v", err)
	}
	defer channel.Stop(context.Background())

	waitFor(t, time.Second, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.sendMessages) > 0
	})

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sendMessages) == 0 || fake.sendMessages[0].ItemList[0].TextItem.Text != "assistant reply" {
		t.Fatalf("unexpected sent messages: %#v", fake.sendMessages)
	}
}

func TestReplySenderUsesTypingAckLifecycle(t *testing.T) {
	store := newStateStore(t.TempDir(), "default")
	if err := store.SaveContextToken("user-1@im.wechat", "ctx-1"); err != nil {
		t.Fatalf("save context token: %v", err)
	}
	fake := &fakeTransport{}
	reply := &replySender{
		transport:    fake,
		store:        store,
		cdnBaseURL:   defaultCDNBaseURL,
		toUserID:     "user-1@im.wechat",
		contextToken: "ctx-1",
	}
	if err := reply.SendAck(context.Background()); err != nil {
		t.Fatalf("send ack: %v", err)
	}
	if err := reply.SendReplyPlan(context.Background(), channels.ReplyPlan{Text: "assistant reply"}); err != nil {
		t.Fatalf("send reply plan: %v", err)
	}
	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.getConfigCalls) == 0 {
		t.Fatal("expected GetConfig to be called")
	}
	if len(fake.sendTyping) < 2 || fake.sendTyping[0].Status != 1 || fake.sendTyping[len(fake.sendTyping)-1].Status != 2 {
		t.Fatalf("expected typing start/stop, got %#v", fake.sendTyping)
	}
}

func TestChannelRoutesImageAttachmentsIntoBackendSession(t *testing.T) {
	cfg := newWeixinTestConfig(t)
	service := newWeixinTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("image received")}}}})
	manager := channels.NewManager(cfg, service)

	key := bytes.Repeat([]byte{0x11}, 16)
	plaintext := []byte("fake image bytes")
	ciphertext, err := encryptAttachmentPayload(plaintext, key)
	if err != nil {
		t.Fatalf("encrypt attachment payload: %v", err)
	}

	fake := &fakeTransport{
		downloads: map[string]downloadedPayload{
			"https://cdn.example.com/download": {data: ciphertext, contentType: "image/png"},
		},
		updates: []getUpdatesResponse{{
			apiStatus:     apiStatus{Ret: 0},
			GetUpdatesBuf: "cursor-2",
			Msgs: []weixinMessage{{
				MessageID:    202,
				FromUserID:   "user-1@im.wechat",
				ToUserID:     "bot@im.bot",
				MessageType:  weixinMessageTypeUser,
				MessageState: weixinMessageStateFinish,
				ContextToken: "ctx-1",
				ItemList: []messageItem{{
					Type: weixinItemTypeImage,
					ImageItem: &imageItem{
						Media: &cdnMedia{
							FullURL: "https://cdn.example.com/download",
							AESKey:  base64.StdEncoding.EncodeToString(key),
						},
					},
				}},
			}},
		}},
	}
	channel, err := New(cfg, manager, WithTransport(fake))
	if err != nil {
		t.Fatalf("new channel: %v", err)
	}
	if err := channel.store.SaveAccount(&accountState{BotToken: "token", BaseURL: cfg.Weixin.BaseURL, CDNBaseURL: cfg.Weixin.CDNBaseURL}); err != nil {
		t.Fatalf("save account state: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := channel.Start(ctx); err != nil {
		t.Fatalf("start channel: %v", err)
	}
	defer channel.Stop(context.Background())

	waitFor(t, time.Second, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.sendMessages) > 0
	})

	opened, err := service.OpenSession(context.Background(), rtbackend.SessionLocator{
		Channel: channelName,
		Key:     "default:user-1@im.wechat",
		UserID:  "user-1@im.wechat",
	})
	if err != nil {
		t.Fatalf("open session: %v", err)
	}
	snapshot, err := service.Snapshot(context.Background(), opened.SessionID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if len(snapshot.Messages) == 0 || snapshot.Messages[0].Metadata == nil || len(snapshot.Messages[0].Metadata.Attachments) != 1 {
		t.Fatalf("expected one stored attachment, got %#v", snapshot.Messages)
	}
	if got := snapshot.Messages[0].Metadata.Attachments[0].MIMEType; got != "image/png" {
		t.Fatalf("unexpected attachment mime type %q", got)
	}
}

func TestReplySenderUploadsArtifactMedia(t *testing.T) {
	dir := t.TempDir()
	imagePath := filepath.Join(dir, "result.png")
	if err := os.WriteFile(imagePath, []byte("fake image bytes"), 0644); err != nil {
		t.Fatalf("write image: %v", err)
	}

	store := newStateStore(t.TempDir(), "default")
	if err := store.SaveContextToken("user-1@im.wechat", "ctx-1"); err != nil {
		t.Fatalf("save context token: %v", err)
	}
	fake := &fakeTransport{
		uploadResp: getUploadURLResponse{
			UploadParam: "upload-param",
		},
		uploadHeader: "download-param",
	}
	reply := &replySender{
		transport:    fake,
		store:        store,
		cdnBaseURL:   defaultCDNBaseURL,
		toUserID:     "user-1@im.wechat",
		contextToken: "ctx-1",
	}
	plan := channels.ReplyPlan{
		Text: "done",
		Artifacts: []channels.ReplyArtifact{{
			Path: imagePath,
			Name: "result.png",
		}},
	}
	if err := reply.SendReplyPlan(context.Background(), plan); err != nil {
		t.Fatalf("send reply plan: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.uploadRequests) != 1 {
		t.Fatalf("expected one upload request, got %#v", fake.uploadRequests)
	}
	if got := fake.uploadRequests[0].FileKey; len(got) != 32 || strings.Contains(got, ".") {
		t.Fatalf("expected random hex filekey, got %q", got)
	}
	if len(fake.uploads) != 1 {
		t.Fatalf("expected one CDN upload, got %#v", fake.uploads)
	}
	expectedUploadURL := "https://novac2c.cdn.weixin.qq.com/c2c/upload?encrypted_query_param=upload-param&filekey=" + fake.uploadRequests[0].FileKey
	if got := fake.uploads[0].url; got != expectedUploadURL {
		t.Fatalf("unexpected upload url %q", got)
	}
	if len(fake.sendMessages) < 2 {
		t.Fatalf("expected text and media send messages, got %#v", fake.sendMessages)
	}
	last := fake.sendMessages[len(fake.sendMessages)-1]
	if len(last.ItemList) != 1 || last.ItemList[0].Type != weixinItemTypeImage || last.ItemList[0].ImageItem == nil {
		t.Fatalf("expected final outbound image message, got %#v", last)
	}
	if got := last.ItemList[0].ImageItem.Media.EncryptQueryParam; got != "download-param" {
		t.Fatalf("unexpected media encrypt query param %q", got)
	}
	if want := base64.StdEncoding.EncodeToString([]byte(fake.uploadRequests[0].AESKey)); last.ItemList[0].ImageItem.Media.AESKey != want {
		t.Fatalf("unexpected media aes_key %q want %q", last.ItemList[0].ImageItem.Media.AESKey, want)
	}
	if got := last.ItemList[0].ImageItem.Media.EncryptType; got != 1 {
		t.Fatalf("unexpected media encrypt_type %d", got)
	}
	if got := last.ItemList[0].ImageItem.Media.FullURL; got != "" {
		t.Fatalf("unexpected media full_url %q", got)
	}
	if got := last.ItemList[0].ImageItem.AESKey; got != "" {
		t.Fatalf("unexpected top-level image aeskey %q", got)
	}
	if got := last.ItemList[0].ImageItem.HDSize; got != 0 {
		t.Fatalf("unexpected image hd_size %d", got)
	}
	if got := last.ItemList[0].ImageItem.MidSize; got == 0 {
		t.Fatalf("expected image mid_size to be populated")
	}
}

func TestReplySenderUploadsFileArtifactAsFileMessage(t *testing.T) {
	dir := t.TempDir()
	filePath := filepath.Join(dir, "manual.pdf")
	if err := os.WriteFile(filePath, []byte("%PDF-1.7 fake bytes"), 0644); err != nil {
		t.Fatalf("write file: %v", err)
	}

	store := newStateStore(t.TempDir(), "default")
	if err := store.SaveContextToken("user-1@im.wechat", "ctx-1"); err != nil {
		t.Fatalf("save context token: %v", err)
	}
	fake := &fakeTransport{
		uploadResp: getUploadURLResponse{
			UploadParam: "upload-param",
		},
		uploadHeader: "download-param",
	}
	reply := &replySender{
		transport:    fake,
		store:        store,
		cdnBaseURL:   defaultCDNBaseURL,
		toUserID:     "user-1@im.wechat",
		contextToken: "ctx-1",
	}
	plan := channels.ReplyPlan{
		Text: "done",
		Artifacts: []channels.ReplyArtifact{{
			Path: filePath,
			Name: "manual.pdf",
		}},
	}
	if err := reply.SendReplyPlan(context.Background(), plan); err != nil {
		t.Fatalf("send reply plan: %v", err)
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.uploadRequests) != 1 {
		t.Fatalf("expected one upload request, got %#v", fake.uploadRequests)
	}
	if fake.uploadRequests[0].MediaType != weixinUploadMediaTypeFile {
		t.Fatalf("expected file media type, got %d", fake.uploadRequests[0].MediaType)
	}
	last := fake.sendMessages[len(fake.sendMessages)-1]
	if len(last.ItemList) != 1 || last.ItemList[0].Type != weixinItemTypeFile || last.ItemList[0].FileItem == nil {
		t.Fatalf("expected outbound file message, got %#v", last)
	}
	if got := last.ItemList[0].FileItem.FileName; got != "manual.pdf" {
		t.Fatalf("unexpected file name %q", got)
	}
	if got := last.ItemList[0].FileItem.Media.EncryptQueryParam; got != "download-param" {
		t.Fatalf("unexpected file media encrypt query param %q", got)
	}
}

func TestChannelSessionExpiryClearsStateAndReauths(t *testing.T) {
	cfg := newWeixinTestConfig(t)
	service := newWeixinTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := channels.NewManager(cfg, service)

	fake := &fakeTransport{updateErr: errSessionExpired}
	channel, err := New(cfg, manager, WithTransport(fake))
	if err != nil {
		t.Fatalf("new channel: %v", err)
	}
	if err := channel.store.SaveAccount(&accountState{BotToken: "token", BaseURL: cfg.Weixin.BaseURL}); err != nil {
		t.Fatalf("save account state: %v", err)
	}
	if err := channel.store.SaveCursor("cursor-1"); err != nil {
		t.Fatalf("save cursor: %v", err)
	}
	if err := channel.store.SaveContextToken("user-1", "ctx-1"); err != nil {
		t.Fatalf("save context token: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := channel.Start(ctx); err != nil {
		t.Fatalf("start channel: %v", err)
	}
	waitFor(t, time.Second, func() bool {
		report := manager.StatusReport()
		for _, status := range report.Channels {
			if status.Name == channelName && status.State == channels.StateError {
				return true
			}
		}
		return false
	})

	if account, err := channel.store.LoadAccount(); err != nil {
		t.Fatalf("load account: %v", err)
	} else if account != nil {
		t.Fatalf("expected account state to be cleared after session expiry")
	}
	if cursor, err := channel.store.LoadCursor(); err != nil {
		t.Fatalf("load cursor: %v", err)
	} else if cursor != "" {
		t.Fatalf("expected cursor to be cleared, got %q", cursor)
	}
	tokens, err := channel.store.LoadContextTokens()
	if err != nil {
		t.Fatalf("load context tokens: %v", err)
	}
	if len(tokens) != 0 {
		t.Fatalf("expected context tokens to be cleared, got %#v", tokens)
	}
}

func TestChannelAutoResumesAfterExternalSetup(t *testing.T) {
	cfg := newWeixinTestConfig(t)
	service := newWeixinTestService(cfg, &stubCaller{responses: []protocol.Response{{Content: []protocol.Block{protocol.TextBlock("assistant reply")}}}})
	manager := channels.NewManager(cfg, service)

	fake := &fakeTransport{
		updates: []getUpdatesResponse{{
			apiStatus:     apiStatus{Ret: 0},
			GetUpdatesBuf: "cursor-2",
			Msgs: []weixinMessage{{
				MessageID:    303,
				FromUserID:   "user-1@im.wechat",
				ToUserID:     "bot@im.bot",
				MessageType:  weixinMessageTypeUser,
				MessageState: weixinMessageStateFinish,
				ContextToken: "ctx-1",
				ItemList: []messageItem{{
					Type:     weixinItemTypeText,
					TextItem: &textItem{Text: "hello after setup"},
				}},
			}},
		}},
	}
	channel, err := New(cfg, manager, WithTransport(fake))
	if err != nil {
		t.Fatalf("new channel: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := channel.Start(ctx); err != nil {
		t.Fatalf("start channel: %v", err)
	}
	defer channel.Stop(context.Background())

	waitFor(t, time.Second, func() bool {
		report := manager.StatusReport()
		for _, status := range report.Channels {
			if status.Name == channelName && status.State == channels.StateError {
				return true
			}
		}
		return false
	})

	if err := channel.store.SaveAccount(&accountState{BotToken: "token", BaseURL: cfg.Weixin.BaseURL}); err != nil {
		t.Fatalf("save account state: %v", err)
	}

	waitFor(t, 2*time.Second, func() bool {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		return len(fake.sendMessages) > 0
	})

	fake.mu.Lock()
	defer fake.mu.Unlock()
	if len(fake.sendMessages) == 0 || fake.sendMessages[0].ItemList[0].TextItem.Text != "assistant reply" {
		t.Fatalf("unexpected sent messages after external setup: %#v", fake.sendMessages)
	}
}

func waitFor(t *testing.T, timeout time.Duration, fn func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if fn() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("timed out waiting for condition")
}

type channelsStubFactory struct{ name string }

func (f channelsStubFactory) Name() string                { return f.name }
func (f channelsStubFactory) Enabled(*config.Config) bool { return true }
func (f channelsStubFactory) Build(*config.Config, *channels.Manager) (channels.Channel, error) {
	return nil, nil
}

func newWeixinTestService(cfg *config.Config, caller *stubCaller) *rtbackend.Service {
	shared := agent.NewSharedDependenciesWithCaller(cfg, caller)
	return rtbackend.NewService(cfg, shared, commands.NewService(cfg))
}

func newWeixinTestConfig(t *testing.T) *config.Config {
	t.Helper()
	workspace := t.TempDir()
	cfg := &config.Config{
		Model:             "test-model",
		BaseURL:           "http://127.0.0.1",
		MaxTokens:         1024,
		WorkspaceDir:      workspace,
		StateDir:          filepath.Join(workspace, ".godex"),
		TeamDir:           filepath.Join(workspace, ".godex", ".team"),
		TasksDir:          filepath.Join(workspace, ".godex", ".tasks"),
		TodosDir:          filepath.Join(workspace, ".godex", ".todos"),
		MemoryDir:         filepath.Join(workspace, ".godex", "memory"),
		RulesDir:          filepath.Join(workspace, ".godex", "rules"),
		SkillsDir:         filepath.Join(workspace, ".godex", "skills"),
		MCPConfigPath:     filepath.Join(workspace, ".godex", "mcp.json"),
		TempDir:           filepath.Join(workspace, ".godex", ".tmp"),
		TranscriptsDir:    filepath.Join(workspace, ".godex", ".transcripts"),
		SessionsDir:       filepath.Join(workspace, ".godex", ".sessions"),
		CompressThreshold: 100000,
		LeadName:          "lead",
		TeamName:          "default",
		Weixin: config.WeixinConfig{
			Enabled:           true,
			BaseURL:           defaultBaseURL,
			CDNBaseURL:        defaultCDNBaseURL,
			AccountID:         "default",
			LongPollTimeoutMs: defaultPollTimeoutMs,
		},
	}
	for _, dir := range []string{
		filepath.Join(cfg.TeamDir, "inbox"),
		cfg.TasksDir,
		cfg.TodosDir,
		cfg.MemoryDir,
		cfg.RulesDir,
		cfg.SkillsDir,
		cfg.TempDir,
		cfg.TranscriptsDir,
		cfg.SessionsDir,
	} {
		if err := os.MkdirAll(dir, 0755); err != nil {
			t.Fatalf("mkdir %s: %v", dir, err)
		}
	}
	return cfg
}
