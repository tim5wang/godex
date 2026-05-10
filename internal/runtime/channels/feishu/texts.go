package feishu

import (
	"fmt"
	"os"
	"strings"
)

type textKey string
type textLocale string

const defaultTextLocale textLocale = "zh-CN"

const (
	localeZhCN textLocale = "zh-CN"
	localeEnUS textLocale = "en-US"
)

const (
	textAckProcessing             textKey = "ack_processing"
	textCompletedFallback         textKey = "completed_fallback"
	textGroupMentionHint          textKey = "group_mention_hint"
	textNonTextUnsupported        textKey = "non_text_unsupported"
	textArtifactLabel             textKey = "artifact_label"
	textArtifactFallbackName      textKey = "artifact_fallback_name"
	textToolsSummary              textKey = "tools_summary"
	textNotes                     textKey = "notes"
	textStatusLabel               textKey = "status_label"
	textCommandLabel              textKey = "command_label"
	textReplyTitleBase            textKey = "reply_title_base"
	textReplyTitleCommand         textKey = "reply_title_command"
	textReplyTitleCompletedSuffix textKey = "reply_title_completed_suffix"
	textReplyTitleErrorSuffix     textKey = "reply_title_error_suffix"
	textReplyTitleRunningSuffix   textKey = "reply_title_running_suffix"
	textReplyTitleContinuedSuffix textKey = "reply_title_continued_suffix"
	textToolFallbackName          textKey = "tool_fallback_name"
	textStatusCompleted           textKey = "status_completed"
	textStatusFailed              textKey = "status_failed"
	textStatusRunning             textKey = "status_running"
	textArtifactUploadFailed      textKey = "artifact_upload_failed"
	textOpenSessionFailed         textKey = "open_session_failed"
	textDownloadAttachmentFailed  textKey = "download_attachment_failed"
	textProcessMessageFailed      textKey = "process_message_failed"
)

var textCatalog = map[textLocale]map[textKey]string{
	localeZhCN: {
		textAckProcessing:             "已收到消息，正在处理中，请稍候…",
		textCompletedFallback:         "已完成。",
		textGroupMentionHint:          "群聊里请先 @我 再发送问题内容，我会只在被 @ 时回复。",
		textNonTextUnsupported:        "当前只支持飞书文本、图片和文件消息。",
		textArtifactLabel:             "产物",
		textArtifactFallbackName:      "产物",
		textToolsSummary:              "工具摘要",
		textNotes:                     "备注",
		textStatusLabel:               "状态",
		textCommandLabel:              "命令",
		textReplyTitleBase:            "GoDex 回复",
		textReplyTitleCommand:         "GoDex /%s",
		textReplyTitleCompletedSuffix: " · 已完成",
		textReplyTitleErrorSuffix:     " · 出错",
		textReplyTitleRunningSuffix:   " · 处理中",
		textReplyTitleContinuedSuffix: "（续）",
		textToolFallbackName:          "工具",
		textStatusCompleted:           "已完成",
		textStatusFailed:              "失败",
		textStatusRunning:             "处理中",
		textArtifactUploadFailed:      "以下产物已生成，但回传飞书失败：\n- %s",
		textOpenSessionFailed:         "打开会话失败：%v",
		textDownloadAttachmentFailed:  "下载附件失败：%v",
		textProcessMessageFailed:      "处理消息失败：%v",
	},
	localeEnUS: {
		textAckProcessing:             "Message received. Processing now, please wait…",
		textCompletedFallback:         "Completed.",
		textGroupMentionHint:          "In group chats, please @mention me and include your question. I only reply when mentioned.",
		textNonTextUnsupported:        "Only Feishu text, image, and file messages are supported right now.",
		textArtifactLabel:             "Artifact",
		textArtifactFallbackName:      "artifact",
		textToolsSummary:              "Tool summary",
		textNotes:                     "Notes",
		textStatusLabel:               "Status",
		textCommandLabel:              "Command",
		textReplyTitleBase:            "GoDex reply",
		textReplyTitleCommand:         "GoDex /%s",
		textReplyTitleCompletedSuffix: " · Completed",
		textReplyTitleErrorSuffix:     " · Failed",
		textReplyTitleRunningSuffix:   " · Running",
		textReplyTitleContinuedSuffix: " (cont.)",
		textToolFallbackName:          "tool",
		textStatusCompleted:           "completed",
		textStatusFailed:              "failed",
		textStatusRunning:             "running",
		textArtifactUploadFailed:      "The following artifacts were generated, but Feishu upload failed:\n- %s",
		textOpenSessionFailed:         "Failed to open the session: %v",
		textDownloadAttachmentFailed:  "Failed to download the attachment: %v",
		textProcessMessageFailed:      "Failed to process the message: %v",
	},
}

func feishuText(key textKey, args ...any) string {
	template := textCatalog[resolveTextLocale()][key]
	if template == "" {
		template = textCatalog[defaultTextLocale][key]
	}
	if template == "" {
		template = string(key)
	}
	if len(args) == 0 {
		return template
	}
	return fmt.Sprintf(template, args...)
}

func resolveTextLocale() textLocale {
	value := strings.TrimSpace(os.Getenv("GODEX_TEXT_LOCALE"))
	if value == "" {
		return defaultTextLocale
	}
	switch strings.ToLower(value) {
	case "en", "en-us":
		return localeEnUS
	case "zh", "zh-cn":
		return localeZhCN
	default:
		return defaultTextLocale
	}
}

func localizeStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "", "completed":
		return feishuText(textStatusCompleted)
	case "failed", "error":
		return feishuText(textStatusFailed)
	case "running":
		return feishuText(textStatusRunning)
	default:
		return strings.TrimSpace(status)
	}
}
