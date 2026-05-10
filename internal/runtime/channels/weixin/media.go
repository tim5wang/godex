package weixin

import (
	"bytes"
	"context"
	"crypto/aes"
	"crypto/md5"
	crand "crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tim5wang/godex/internal/domain/message"
	"github.com/tim5wang/godex/internal/runtime/channels"
	rtbackend "github.com/tim5wang/godex/internal/services/backend"
)

type inboundContent struct {
	text        string
	attachments []message.AttachmentRef
}

func (c *Channel) collectInboundContent(ctx context.Context, sessionID string, msg weixinMessage) (inboundContent, error) {
	textParts := make([]string, 0, len(msg.ItemList))
	attachments := make([]message.AttachmentRef, 0, len(msg.ItemList))

	for _, item := range msg.ItemList {
		switch item.Type {
		case weixinItemTypeText:
			if item.TextItem != nil {
				if text := strings.TrimSpace(item.TextItem.Text); text != "" {
					textParts = append(textParts, text)
				}
			}
		case weixinItemTypeImage, weixinItemTypeVoice, weixinItemTypeFile, weixinItemTypeVideo:
			attachment, err := c.downloadAttachment(ctx, sessionID, msg, item)
			if err != nil {
				return inboundContent{}, err
			}
			attachments = append(attachments, attachment)
		default:
			continue
		}
	}

	return inboundContent{
		text:        strings.TrimSpace(strings.Join(textParts, "\n")),
		attachments: attachments,
	}, nil
}

func (c *Channel) downloadAttachment(ctx context.Context, sessionID string, msg weixinMessage, item messageItem) (message.AttachmentRef, error) {
	spec, err := buildInboundMediaSpec(c.cfg.CDNBaseURL, item)
	if err != nil {
		return message.AttachmentRef{}, err
	}
	ciphertext, contentType, err := c.transport.DownloadCiphertext(ctx, spec.downloadURL)
	if err != nil {
		return message.AttachmentRef{}, err
	}
	plaintext, err := decryptAttachmentPayload(ciphertext, spec.aesKey)
	if err != nil {
		return message.AttachmentRef{}, err
	}
	mimeType := strings.TrimSpace(spec.mimeType)
	if mimeType == "" {
		mimeType = strings.TrimSpace(contentType)
	}
	if mimeType == "" {
		mimeType = http.DetectContentType(plaintext)
	}
	name := strings.TrimSpace(spec.name)
	if name == "" {
		name = defaultInboundAttachmentName(item.Type, mimeType, msg.MessageID)
	}
	return c.manager.StoreAttachment(ctx, sessionID, rtbackend.AttachmentUpload{
		Name:     name,
		MIMEType: mimeType,
		Reader:   bytes.NewReader(plaintext),
	})
}

type inboundMediaSpec struct {
	downloadURL string
	aesKey      []byte
	name        string
	mimeType    string
}

func buildInboundMediaSpec(cdnBaseURL string, item messageItem) (inboundMediaSpec, error) {
	switch item.Type {
	case weixinItemTypeImage:
		key, media := inboundImageKey(item.ImageItem)
		if len(key) == 0 || media == nil {
			return inboundMediaSpec{}, fmt.Errorf("missing weixin image media reference")
		}
		url, err := buildCDNDownloadURL(cdnBaseURL, media)
		if err != nil {
			return inboundMediaSpec{}, err
		}
		imageURL := ""
		if item.ImageItem != nil {
			imageURL = item.ImageItem.URL
		}
		return inboundMediaSpec{
			downloadURL: url,
			aesKey:      key,
			name:        defaultInboundName(item.Type, "image", imageURL),
			mimeType:    mime.TypeByExtension(strings.ToLower(filepath.Ext(imageURL))),
		}, nil
	case weixinItemTypeVoice:
		if item.VoiceItem == nil {
			return inboundMediaSpec{}, fmt.Errorf("missing weixin voice item")
		}
		key, err := decodeAESKey("", item.VoiceItem.Media)
		if err != nil {
			return inboundMediaSpec{}, err
		}
		url, err := buildCDNDownloadURL(cdnBaseURL, item.VoiceItem.Media)
		if err != nil {
			return inboundMediaSpec{}, err
		}
		return inboundMediaSpec{
			downloadURL: url,
			aesKey:      key,
			name:        defaultInboundVoiceName(item.VoiceItem),
			mimeType:    voiceMIMEType(item.VoiceItem.EncodeType),
		}, nil
	case weixinItemTypeFile:
		if item.FileItem == nil {
			return inboundMediaSpec{}, fmt.Errorf("missing weixin file item")
		}
		key, err := decodeAESKey("", item.FileItem.Media)
		if err != nil {
			return inboundMediaSpec{}, err
		}
		url, err := buildCDNDownloadURL(cdnBaseURL, item.FileItem.Media)
		if err != nil {
			return inboundMediaSpec{}, err
		}
		name := strings.TrimSpace(item.FileItem.FileName)
		return inboundMediaSpec{
			downloadURL: url,
			aesKey:      key,
			name:        defaultInboundName(item.Type, "file", name),
			mimeType:    mime.TypeByExtension(strings.ToLower(filepath.Ext(name))),
		}, nil
	case weixinItemTypeVideo:
		if item.VideoItem == nil {
			return inboundMediaSpec{}, fmt.Errorf("missing weixin video item")
		}
		key, err := decodeAESKey("", item.VideoItem.Media)
		if err != nil {
			return inboundMediaSpec{}, err
		}
		url, err := buildCDNDownloadURL(cdnBaseURL, item.VideoItem.Media)
		if err != nil {
			return inboundMediaSpec{}, err
		}
		return inboundMediaSpec{
			downloadURL: url,
			aesKey:      key,
			name:        defaultInboundName(item.Type, "video", ""),
			mimeType:    "video/mp4",
		}, nil
	default:
		return inboundMediaSpec{}, fmt.Errorf("unsupported weixin media item type=%d", item.Type)
	}
}

func inboundImageKey(item *imageItem) ([]byte, *cdnMedia) {
	if item == nil {
		return nil, nil
	}
	if media := item.Media; media != nil {
		if key, err := decodeAESKey(item.AESKey, media); err == nil {
			return key, media
		}
	}
	if media := item.ThumbMedia; media != nil {
		if key, err := decodeAESKey(item.AESKey, media); err == nil {
			return key, media
		}
	}
	return nil, nil
}

func buildCDNDownloadURL(cdnBaseURL string, media *cdnMedia) (string, error) {
	if media == nil {
		return "", fmt.Errorf("missing weixin media reference")
	}
	if full := strings.TrimSpace(media.FullURL); full != "" {
		return full, nil
	}
	param := strings.TrimSpace(media.EncryptQueryParam)
	if param == "" {
		return "", fmt.Errorf("missing weixin media encrypt_query_param")
	}
	base := strings.TrimRight(defaultIfEmpty(strings.TrimSpace(cdnBaseURL), defaultCDNBaseURL), "/")
	return base + "/download?encrypted_query_param=" + url.QueryEscape(param), nil
}

func buildCDNUploadURL(cdnBaseURL, fullURL, uploadParam, fileKey string) (string, error) {
	if full := strings.TrimSpace(fullURL); full != "" {
		return full, nil
	}
	if strings.TrimSpace(uploadParam) == "" {
		return "", fmt.Errorf("missing weixin upload_param")
	}
	if strings.TrimSpace(fileKey) == "" {
		return "", fmt.Errorf("missing weixin filekey")
	}
	base := strings.TrimRight(defaultIfEmpty(strings.TrimSpace(cdnBaseURL), defaultCDNBaseURL), "/")
	return base + "/upload?encrypted_query_param=" + url.QueryEscape(strings.TrimSpace(uploadParam)) + "&filekey=" + url.QueryEscape(strings.TrimSpace(fileKey)), nil
}

func decodeAESKey(direct string, media *cdnMedia) ([]byte, error) {
	if key := strings.TrimSpace(direct); key != "" {
		if decoded, err := decodeAESKeyString(key); err == nil {
			return decoded, nil
		}
	}
	if media == nil {
		return nil, fmt.Errorf("missing weixin media aes key")
	}
	return decodeAESKeyString(media.AESKey)
}

func decodeAESKeyString(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return nil, fmt.Errorf("missing weixin media aes key")
	}
	if raw, err := hex.DecodeString(value); err == nil && len(raw) == 16 {
		return raw, nil
	}
	raw, err := base64.StdEncoding.DecodeString(value)
	if err != nil {
		return nil, fmt.Errorf("decode weixin media aes key: %w", err)
	}
	switch len(raw) {
	case 16:
		return raw, nil
	case 32:
		decoded, err := hex.DecodeString(string(raw))
		if err == nil && len(decoded) == 16 {
			return decoded, nil
		}
	}
	return nil, fmt.Errorf("unsupported weixin media aes key length=%d", len(raw))
}

func encryptAttachmentPayload(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	padded := pkcs7Pad(plaintext, aes.BlockSize)
	ciphertext := make([]byte, len(padded))
	for offset := 0; offset < len(padded); offset += aes.BlockSize {
		block.Encrypt(ciphertext[offset:offset+aes.BlockSize], padded[offset:offset+aes.BlockSize])
	}
	return ciphertext, nil
}

func decryptAttachmentPayload(ciphertext, key []byte) ([]byte, error) {
	if len(ciphertext)%aes.BlockSize != 0 {
		return nil, fmt.Errorf("invalid weixin ciphertext size=%d", len(ciphertext))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	plaintext := make([]byte, len(ciphertext))
	for offset := 0; offset < len(ciphertext); offset += aes.BlockSize {
		block.Decrypt(plaintext[offset:offset+aes.BlockSize], ciphertext[offset:offset+aes.BlockSize])
	}
	if unpadded, err := pkcs7Unpad(plaintext, aes.BlockSize); err == nil {
		return unpadded, nil
	}
	return plaintext, nil
}

func pkcs7Pad(input []byte, blockSize int) []byte {
	padding := blockSize - len(input)%blockSize
	if padding == 0 {
		padding = blockSize
	}
	out := make([]byte, len(input)+padding)
	copy(out, input)
	for i := len(input); i < len(out); i++ {
		out[i] = byte(padding)
	}
	return out
}

func pkcs7Unpad(input []byte, blockSize int) ([]byte, error) {
	if len(input) == 0 || len(input)%blockSize != 0 {
		return nil, fmt.Errorf("invalid pkcs7 payload")
	}
	padding := int(input[len(input)-1])
	if padding <= 0 || padding > blockSize || padding > len(input) {
		return nil, fmt.Errorf("invalid pkcs7 padding")
	}
	for _, b := range input[len(input)-padding:] {
		if int(b) != padding {
			return nil, fmt.Errorf("invalid pkcs7 tail")
		}
	}
	return input[:len(input)-padding], nil
}

func (r *replySender) sendArtifact(ctx context.Context, artifact channels.ReplyArtifact) error {
	path := strings.TrimSpace(artifact.Path)
	if path == "" {
		return fmt.Errorf("missing artifact path")
	}
	absolutePath, err := filepath.Abs(path)
	if err != nil {
		return err
	}
	kind, err := detectWeixinArtifactKind(absolutePath)
	if err != nil {
		return err
	}
	if kind != "image" && kind != "file" {
		return fmt.Errorf("unsupported weixin artifact kind=%s", kind)
	}
	spec, err := prepareOutboundMedia(absolutePath, kind)
	if err != nil {
		return err
	}
	uploadResp, err := r.transport.GetUploadURL(ctx, getUploadURLRequest{
		FileKey:     spec.fileKey,
		MediaType:   spec.mediaType,
		ToUserID:    r.toUserID,
		RawSize:     int64(len(spec.plaintext)),
		RawFileMD5:  spec.rawMD5,
		FileSize:    int64(len(spec.ciphertext)),
		NoNeedThumb: true,
		AESKey:      spec.aesKeyHex,
	})
	if err != nil {
		return err
	}
	uploadURL, err := buildCDNUploadURL(r.cdnBaseURL, uploadResp.UploadFullURL, uploadResp.UploadParam, spec.fileKey)
	if err != nil {
		return err
	}
	encryptQueryParam, err := r.transport.UploadCiphertext(ctx, uploadURL, spec.ciphertext)
	if err != nil {
		return err
	}
	encryptQueryParam = strings.TrimSpace(encryptQueryParam)
	if encryptQueryParam == "" {
		return fmt.Errorf("missing weixin upload encrypt_query_param")
	}
	item := buildOutboundMediaItem(kind, spec.name, encryptQueryParam, spec.aesKeyWire, spec.cipherSize, spec.fileSize)
	contextToken := r.resolveContextToken()
	if strings.TrimSpace(contextToken) == "" {
		return fmt.Errorf("missing weixin context token")
	}
	return r.transport.SendMessage(ctx, weixinMessage{
		ClientID:     newClientID(),
		ToUserID:     r.toUserID,
		MessageType:  weixinMessageTypeBot,
		MessageState: weixinMessageStateFinish,
		ContextToken: contextToken,
		ItemList:     []messageItem{item},
	})
}

type outboundMediaSpec struct {
	fileKey    string
	name       string
	mediaType  int
	plaintext  []byte
	ciphertext []byte
	rawMD5     string
	aesKeyRaw  []byte
	aesKeyHex  string
	aesKeyWire string
	fileSize   int64
	cipherSize int64
}

func prepareOutboundMedia(path, kind string) (outboundMediaSpec, error) {
	plaintext, err := os.ReadFile(path)
	if err != nil {
		return outboundMediaSpec{}, err
	}
	aesKey := make([]byte, 16)
	if _, err := io.ReadFull(crand.Reader, aesKey); err != nil {
		return outboundMediaSpec{}, err
	}
	fileKeyBytes := make([]byte, 16)
	if _, err := io.ReadFull(crand.Reader, fileKeyBytes); err != nil {
		return outboundMediaSpec{}, err
	}
	ciphertext, err := encryptAttachmentPayload(plaintext, aesKey)
	if err != nil {
		return outboundMediaSpec{}, err
	}
	aesKeyHex := hex.EncodeToString(aesKey)
	spec := outboundMediaSpec{
		fileKey:    hex.EncodeToString(fileKeyBytes),
		name:       filepath.Base(path),
		plaintext:  plaintext,
		ciphertext: ciphertext,
		aesKeyRaw:  aesKey,
		aesKeyHex:  aesKeyHex,
		aesKeyWire: base64.StdEncoding.EncodeToString([]byte(aesKeyHex)),
		fileSize:   int64(len(plaintext)),
		cipherSize: int64(len(ciphertext)),
	}
	sum := md5.Sum(plaintext)
	spec.rawMD5 = hex.EncodeToString(sum[:])
	switch kind {
	case "image":
		spec.mediaType = weixinUploadMediaTypeImage
	case "file":
		spec.mediaType = weixinUploadMediaTypeFile
	default:
		return outboundMediaSpec{}, fmt.Errorf("unsupported outbound weixin media kind=%s", kind)
	}
	return spec, nil
}

func buildOutboundMediaItem(kind, name, encryptQueryParam, aesKeyWire string, cipherSize, fileSize int64) messageItem {
	media := &cdnMedia{
		EncryptQueryParam: strings.TrimSpace(encryptQueryParam),
		AESKey:            strings.TrimSpace(aesKeyWire),
		EncryptType:       1,
	}
	switch kind {
	case "image":
		return messageItem{
			Type: weixinItemTypeImage,
			ImageItem: &imageItem{
				Media:   media,
				MidSize: cipherSize,
			},
		}
	default:
		return messageItem{
			Type: weixinItemTypeFile,
			FileItem: &fileItem{
				Media:    media,
				FileName: name,
				Len:      strconv.FormatInt(fileSize, 10),
			},
		}
	}
}

func detectWeixinArtifactKind(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "", fmt.Errorf("artifact path is a directory")
	}
	if mimeType := mime.TypeByExtension(strings.ToLower(filepath.Ext(path))); strings.HasPrefix(mimeType, "image/") {
		return "image", nil
	}
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	var sniff [512]byte
	n, err := file.Read(sniff[:])
	if err != nil && err != io.EOF {
		return "", err
	}
	if strings.HasPrefix(http.DetectContentType(sniff[:n]), "image/") {
		return "image", nil
	}
	return "file", nil
}

func defaultInboundAttachmentName(itemType int, mimeType string, messageID int64) string {
	ext, _ := mime.ExtensionsByType(mimeType)
	suffix := ""
	if len(ext) > 0 {
		suffix = ext[0]
	}
	switch itemType {
	case weixinItemTypeImage:
		return fmt.Sprintf("image-%d%s", messageID, suffixOr(suffix, ".bin"))
	case weixinItemTypeVoice:
		return fmt.Sprintf("voice-%d%s", messageID, suffixOr(suffix, ".bin"))
	case weixinItemTypeVideo:
		return fmt.Sprintf("video-%d%s", messageID, suffixOr(suffix, ".bin"))
	default:
		return fmt.Sprintf("file-%d%s", messageID, suffixOr(suffix, ".bin"))
	}
}

func defaultInboundName(itemType int, fallback, candidate string) string {
	candidate = strings.TrimSpace(candidate)
	if candidate != "" {
		return filepath.Base(candidate)
	}
	switch itemType {
	case weixinItemTypeImage:
		return fallback + ".jpg"
	case weixinItemTypeVideo:
		return fallback + ".mp4"
	default:
		return fallback
	}
}

func defaultInboundVoiceName(item *voiceItem) string {
	ext := ".bin"
	switch item.EncodeType {
	case 5:
		ext = ".amr"
	case 6:
		ext = ".silk"
	case 7:
		ext = ".mp3"
	case 8:
		ext = ".ogg"
	case 1:
		ext = ".wav"
	}
	return "voice" + ext
}

func voiceMIMEType(encodeType int) string {
	switch encodeType {
	case 5:
		return "audio/amr"
	case 6:
		return "audio/silk"
	case 7:
		return "audio/mpeg"
	case 8:
		return "audio/ogg"
	default:
		return "audio/wav"
	}
}

func suffixOr(value, fallback string) string {
	if strings.TrimSpace(value) != "" {
		return value
	}
	return fallback
}
