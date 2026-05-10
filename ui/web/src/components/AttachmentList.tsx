import { useEffect, useMemo, useState } from "react";
import { Button, Card, Image, List, Space, Typography } from "antd";
import { DownloadOutlined, EyeOutlined, FileOutlined } from "@ant-design/icons";
import { fetchAttachmentBlob } from "../lib/api";
import type { AttachmentRef } from "../lib/types";
import { useSettingsStore } from "../store/settings";

interface AttachmentListProps {
  attachments: AttachmentRef[];
}

export function AttachmentList({ attachments }: AttachmentListProps) {
  const token = useSettingsStore((state) => state.token);
  const [previewURLs, setPreviewURLs] = useState<Record<string, string>>({});
  const imageAttachments = useMemo(
    () => attachments.filter((attachment) => isImage(attachment) && !!attachment.url),
    [attachments],
  );

  useEffect(() => {
    let active = true;
    const created: string[] = [];
    async function loadPreviews() {
      const next: Record<string, string> = {};
      for (const attachment of imageAttachments) {
        const key = attachmentKey(attachment);
        if (!attachment.url) {
          continue;
        }
        try {
          const blob = await fetchAttachmentBlob(token || null, attachment.url);
          const objectURL = URL.createObjectURL(blob);
          created.push(objectURL);
          next[key] = objectURL;
        } catch {
          continue;
        }
      }
      if (active) {
        setPreviewURLs(next);
      } else {
        created.forEach((url) => URL.revokeObjectURL(url));
      }
    }
    void loadPreviews();
    return () => {
      active = false;
      created.forEach((url) => URL.revokeObjectURL(url));
    };
  }, [imageAttachments, token]);

  if (attachments.length === 0) {
    return null;
  }

  return (
    <List
      grid={{ gutter: 12, xs: 1, sm: 2 }}
      dataSource={attachments}
      renderItem={(attachment, index) => {
        const key = attachmentKey(attachment, index);
        const label = attachment.name || attachment.path || attachment.url || `Attachment ${index + 1}`;
        const details = [attachment.mime_type, formatSize(attachment.size_bytes)].filter(Boolean).join(" · ");
        const previewURL = previewURLs[key];
        return (
          <List.Item key={key}>
            <Card
              size="small"
              cover={previewURL ? <Image src={previewURL} alt={label} height={150} style={{ objectFit: "cover" }} /> : undefined}
              actions={
                attachment.url
                  ? [
                      <Button
                        key="preview"
                        type="text"
                        size="small"
                        icon={<EyeOutlined />}
                        aria-label={`Preview ${label}`}
                        onClick={() => void openPreview(token, attachment)}
                      >
                        Preview
                      </Button>,
                      <Button
                        key="download"
                        type="text"
                        size="small"
                        icon={<DownloadOutlined />}
                        aria-label={`Download ${label}`}
                        onClick={() => void downloadAttachment(token, attachment)}
                      >
                        Download
                      </Button>,
                    ]
                  : []
              }
            >
              <Space align="start">
                <FileOutlined />
                <Space direction="vertical" size={2}>
                  <Typography.Text strong ellipsis style={{ maxWidth: 280 }}>
                    {label}
                  </Typography.Text>
                  {details ? <Typography.Text type="secondary">{details}</Typography.Text> : null}
                  {attachment.path ? <Typography.Text type="secondary">{attachment.path}</Typography.Text> : null}
                </Space>
              </Space>
            </Card>
          </List.Item>
        );
      }}
    />
  );
}

function attachmentKey(attachment: AttachmentRef, index = 0) {
  return attachment.id || attachment.url || attachment.path || attachment.name || `attachment:${index}`;
}

function isImage(attachment: AttachmentRef) {
  return !!attachment.mime_type && attachment.mime_type.startsWith("image/");
}

function formatSize(sizeBytes: number | undefined) {
  if (!sizeBytes || sizeBytes <= 0) {
    return "";
  }
  if (sizeBytes < 1024) {
    return `${sizeBytes} B`;
  }
  if (sizeBytes < 1024 * 1024) {
    return `${(sizeBytes / 1024).toFixed(1)} KB`;
  }
  return `${(sizeBytes / (1024 * 1024)).toFixed(1)} MB`;
}

async function openPreview(token: string | null, attachment: AttachmentRef) {
  if (!attachment.url) {
    return;
  }
  const blob = await fetchAttachmentBlob(token || null, attachment.url);
  const objectURL = URL.createObjectURL(blob);
  window.open(objectURL, "_blank", "noopener,noreferrer");
  window.setTimeout(() => URL.revokeObjectURL(objectURL), 60_000);
}

async function downloadAttachment(token: string | null, attachment: AttachmentRef) {
  if (!attachment.url) {
    return;
  }
  const blob = await fetchAttachmentBlob(token || null, attachment.url);
  const objectURL = URL.createObjectURL(blob);
  const anchor = document.createElement("a");
  anchor.href = objectURL;
  anchor.download = attachment.name || "attachment";
  document.body.appendChild(anchor);
  anchor.click();
  anchor.remove();
  window.setTimeout(() => URL.revokeObjectURL(objectURL), 60_000);
}
