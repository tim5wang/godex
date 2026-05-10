package weixin

import "time"

const (
	channelName          = "weixin"
	defaultBaseURL       = "https://ilinkai.weixin.qq.com"
	defaultCDNBaseURL    = "https://novac2c.cdn.weixin.qq.com/c2c"
	defaultAccountID     = "default"
	defaultChannelVer    = "2.0.0"
	defaultPollTimeoutMs = 35000

	weixinMessageTypeUser = 1
	weixinMessageTypeBot  = 2

	weixinMessageStateGenerating = 1
	weixinMessageStateFinish     = 2

	weixinItemTypeText  = 1
	weixinItemTypeImage = 2
	weixinItemTypeVoice = 3
	weixinItemTypeFile  = 4
	weixinItemTypeVideo = 5

	weixinUploadMediaTypeImage = 1
	weixinUploadMediaTypeVideo = 2
	weixinUploadMediaTypeFile  = 3
	weixinUploadMediaTypeVoice = 4

	maxReplyChunkRunes = 1200
	qrPollInterval     = time.Second
	qrSetupTimeout     = 2 * time.Minute
	pollRetryDelay     = 2 * time.Second
	reauthRetryDelay   = 250 * time.Millisecond
)

type baseInfo struct {
	ChannelVersion string `json:"channel_version"`
}

type qrCodeResponse struct {
	QRCode           string `json:"qrcode"`
	QRCodeImgURL     string `json:"qrcode_img_url"`
	QRCodeImgContent string `json:"qrcode_img_content"`
}

type qrCodeStatus struct {
	Status       string `json:"status"`
	BotToken     string `json:"bot_token,omitempty"`
	BaseURL      string `json:"baseurl,omitempty"`
	ILinkBotID   string `json:"ilink_bot_id,omitempty"`
	ILinkUserID  string `json:"ilink_user_id,omitempty"`
	RedirectHost string `json:"redirect_host,omitempty"`
}

type apiStatus struct {
	Ret     int    `json:"ret"`
	ErrCode int    `json:"errcode,omitempty"`
	ErrMsg  string `json:"errmsg,omitempty"`
}

type getUpdatesRequest struct {
	BaseInfo      baseInfo `json:"base_info"`
	GetUpdatesBuf string   `json:"get_updates_buf"`
}

type getUpdatesResponse struct {
	apiStatus
	Msgs               []weixinMessage `json:"msgs"`
	GetUpdatesBuf      string          `json:"get_updates_buf"`
	LongPollingTimeout int             `json:"longpolling_timeout_ms"`
}

type weixinMessage struct {
	Seq          int64         `json:"seq,omitempty"`
	MessageID    int64         `json:"message_id,omitempty"`
	FromUserID   string        `json:"from_user_id,omitempty"`
	ToUserID     string        `json:"to_user_id,omitempty"`
	CreateTimeMS int64         `json:"create_time_ms,omitempty"`
	SessionID    string        `json:"session_id,omitempty"`
	MessageType  int           `json:"message_type,omitempty"`
	MessageState int           `json:"message_state,omitempty"`
	ContextToken string        `json:"context_token,omitempty"`
	ClientID     string        `json:"client_id,omitempty"`
	ItemList     []messageItem `json:"item_list,omitempty"`
}

type messageItem struct {
	Type      int        `json:"type"`
	TextItem  *textItem  `json:"text_item,omitempty"`
	ImageItem *imageItem `json:"image_item,omitempty"`
	VoiceItem *voiceItem `json:"voice_item,omitempty"`
	FileItem  *fileItem  `json:"file_item,omitempty"`
	VideoItem *videoItem `json:"video_item,omitempty"`
}

type textItem struct {
	Text string `json:"text,omitempty"`
}

type cdnMedia struct {
	EncryptQueryParam string `json:"encrypt_query_param,omitempty"`
	AESKey            string `json:"aes_key,omitempty"`
	EncryptType       int    `json:"encrypt_type,omitempty"`
	FullURL           string `json:"full_url,omitempty"`
}

type imageItem struct {
	Media       *cdnMedia `json:"media,omitempty"`
	ThumbMedia  *cdnMedia `json:"thumb_media,omitempty"`
	AESKey      string    `json:"aeskey,omitempty"`
	URL         string    `json:"url,omitempty"`
	MidSize     int64     `json:"mid_size,omitempty"`
	ThumbSize   int64     `json:"thumb_size,omitempty"`
	ThumbHeight int       `json:"thumb_height,omitempty"`
	ThumbWidth  int       `json:"thumb_width,omitempty"`
	HDSize      int64     `json:"hd_size,omitempty"`
}

type voiceItem struct {
	Media         *cdnMedia `json:"media,omitempty"`
	EncodeType    int       `json:"encode_type,omitempty"`
	BitsPerSample int       `json:"bits_per_sample,omitempty"`
	SampleRate    int       `json:"sample_rate,omitempty"`
	Playtime      int       `json:"playtime,omitempty"`
	Text          string    `json:"text,omitempty"`
}

type fileItem struct {
	Media    *cdnMedia `json:"media,omitempty"`
	FileName string    `json:"file_name,omitempty"`
	MD5      string    `json:"md5,omitempty"`
	Len      string    `json:"len,omitempty"`
}

type videoItem struct {
	Media       *cdnMedia `json:"media,omitempty"`
	VideoSize   int64     `json:"video_size,omitempty"`
	PlayLength  int       `json:"play_length,omitempty"`
	VideoMD5    string    `json:"video_md5,omitempty"`
	ThumbMedia  *cdnMedia `json:"thumb_media,omitempty"`
	ThumbSize   int64     `json:"thumb_size,omitempty"`
	ThumbHeight int       `json:"thumb_height,omitempty"`
	ThumbWidth  int       `json:"thumb_width,omitempty"`
}

type sendMessageRequest struct {
	BaseInfo baseInfo      `json:"base_info"`
	Msg      weixinMessage `json:"msg"`
}

type sendMessageResponse struct {
	apiStatus
}

type getConfigRequest struct {
	BaseInfo     baseInfo `json:"base_info"`
	ILinkUserID  string   `json:"ilink_user_id"`
	ContextToken string   `json:"context_token,omitempty"`
}

type getConfigResponse struct {
	apiStatus
	TypingTicket string `json:"typing_ticket,omitempty"`
}

type sendTypingRequest struct {
	BaseInfo     baseInfo `json:"base_info"`
	ILinkUserID  string   `json:"ilink_user_id"`
	TypingTicket string   `json:"typing_ticket"`
	Status       int      `json:"status"`
}

type sendTypingResponse struct {
	apiStatus
}

type getUploadURLRequest struct {
	BaseInfo        baseInfo `json:"base_info"`
	FileKey         string   `json:"filekey,omitempty"`
	MediaType       int      `json:"media_type,omitempty"`
	ToUserID        string   `json:"to_user_id,omitempty"`
	RawSize         int64    `json:"rawsize,omitempty"`
	RawFileMD5      string   `json:"rawfilemd5,omitempty"`
	FileSize        int64    `json:"filesize,omitempty"`
	ThumbRawSize    int64    `json:"thumb_rawsize,omitempty"`
	ThumbRawFileMD5 string   `json:"thumb_rawfilemd5,omitempty"`
	ThumbFileSize   int64    `json:"thumb_filesize,omitempty"`
	NoNeedThumb     bool     `json:"no_need_thumb,omitempty"`
	AESKey          string   `json:"aeskey,omitempty"`
}

type getUploadURLResponse struct {
	apiStatus
	UploadParam      string `json:"upload_param,omitempty"`
	ThumbUploadParam string `json:"thumb_upload_param,omitempty"`
	UploadFullURL    string `json:"upload_full_url,omitempty"`
}

type accountState struct {
	BotToken    string    `json:"bot_token"`
	BaseURL     string    `json:"base_url"`
	CDNBaseURL  string    `json:"cdn_base_url"`
	ILinkBotID  string    `json:"ilink_bot_id,omitempty"`
	ILinkUserID string    `json:"ilink_user_id,omitempty"`
	UpdatedAt   time.Time `json:"updated_at"`
}

type cursorState struct {
	GetUpdatesBuf string    `json:"get_updates_buf"`
	UpdatedAt     time.Time `json:"updated_at"`
}

type contextTokensState struct {
	Tokens    map[string]string `json:"tokens"`
	UpdatedAt time.Time         `json:"updated_at"`
}
