import { useMemo, useRef, useState } from "react";
import { Button, Progress, Space, Tag, Typography, type UploadFile } from "antd";
import { PaperClipOutlined } from "@ant-design/icons";
import { Attachments, Sender } from "@ant-design/x";
import type { AttachmentsRef } from "@ant-design/x/es/attachments";
import { useI18n } from "../i18n";
import type { PackageCommandEntry } from "../lib/types";

export interface ComposerSubmission {
  text: string;
  files: File[];
}

interface ComposerProps {
  disabled?: boolean;
  uploading?: boolean;
  uploadProgress?: number | null;
  packageCommands?: PackageCommandEntry[];
  onSubmit: (submission: ComposerSubmission) => Promise<void>;
}

export function Composer({ disabled, uploading = false, uploadProgress = null, packageCommands = [], onSubmit }: ComposerProps) {
  const { t } = useI18n();
  const [value, setValue] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const attachmentsRef = useRef<AttachmentsRef>(null);
  const submitting = Boolean(disabled || uploading);
  const uploadItems = useMemo<UploadFile[]>(
    () =>
      files.map((file, index) => ({
        uid: `${file.name}:${file.size}:${file.lastModified}:${index}`,
        name: file.name,
        size: file.size,
        type: file.type,
        status: "done" as const,
        originFileObj: file as UploadFile["originFileObj"],
    })),
    [files],
  );
  const commandMatches = useMemo(() => matchPackageCommands(value, packageCommands), [packageCommands, value]);
  const showCommandPalette = value.trimStart().startsWith("/") && !value.endsWith(" ") && files.length === 0 && commandMatches.length > 0;

  const submit = async (text: string) => {
    const trimmed = text.trim();
    if ((!trimmed && files.length === 0) || submitting) {
      return;
    }
    await onSubmit({ text: trimmed, files });
    setValue("");
    setFiles([]);
  };

  return (
    <div className="chat-composer">
      <Space direction="vertical" size={8} style={{ width: "100%" }}>
        {uploading ? (
          <Space direction="vertical" size={4} style={{ width: "100%" }}>
            <Typography.Text type="secondary">
              {t("chat.uploadingAttachments")} {uploadProgress ?? 0}%
            </Typography.Text>
            <Progress percent={uploadProgress ?? 0} size="small" />
          </Space>
        ) : null}
        {showCommandPalette ? (
          <div className="command-palette">
            {commandMatches.map((command) => {
              const invocation = packageCommandInvocation(command);
              return (
                <button
                  type="button"
                  className="command-palette-item"
                  key={`${command.package_name}:${command.namespace || ""}:${command.name}:${command.path}`}
                  onClick={() => setValue(`${invocation} `)}
                >
                  <Space direction="vertical" size={2} style={{ width: "100%" }}>
                    <Space size={6} wrap>
                      <Typography.Text strong>{invocation}</Typography.Text>
                      {command.mode ? <Tag>{command.mode}</Tag> : null}
                      {command.roles?.slice(0, 2).map((role) => <Tag key={role}>{role}</Tag>)}
                    </Space>
                    {command.description ? <Typography.Text type="secondary">{command.description}</Typography.Text> : null}
                    {command.recommended_bundles?.length ? (
                      <Space size={4} wrap>
                        {command.recommended_bundles.slice(0, 4).map((bundle) => <Tag key={bundle}>{bundle}</Tag>)}
                      </Space>
                    ) : null}
                  </Space>
                </button>
              );
            })}
          </div>
        ) : null}
        <Sender
          value={value}
          loading={uploading}
          disabled={submitting}
          placeholder={t("chat.composerPlaceholder")}
          submitType="enter"
          autoSize={{ minRows: 2, maxRows: 8 }}
          onChange={(next) => setValue(next)}
          onSubmit={(message) => void submit(message)}
          onPasteFile={(pastedFiles) => setFiles((current) => [...current, ...Array.from(pastedFiles)])}
          prefix={
            <Button
              type="text"
              icon={<PaperClipOutlined />}
              aria-label={t("chat.addFiles")}
              disabled={submitting}
              onClick={() => attachmentsRef.current?.select({ multiple: true })}
            >
              {t("chat.addFiles")}
            </Button>
          }
          header={
            <Attachments
              ref={attachmentsRef}
              items={uploadItems}
              overflow="wrap"
              beforeUpload={(file) => {
                setFiles((current) => [...current, file]);
                return false;
              }}
              onRemove={(file) => {
                setFiles((current) => current.filter((_, index) => uploadItems[index]?.uid !== file.uid));
                return true;
              }}
              placeholder={{
                title: t("chat.addFiles"),
                description: t("chat.selectedFilesCount", { count: files.length }),
              }}
              style={{ display: files.length > 0 ? undefined : "none" }}
            />
          }
        />
      </Space>
    </div>
  );
}

function matchPackageCommands(value: string, commands: PackageCommandEntry[]) {
  const trimmed = value.trimStart();
  if (!trimmed.startsWith("/")) {
    return [];
  }
  const query = normalizeCommandQuery(trimmed.slice(1));
  if (!query) {
    return commands.slice(0, 8);
  }
  return commands
    .filter((command) => packageCommandHaystack(command).includes(query))
    .slice(0, 8);
}

function packageCommandInvocation(command: PackageCommandEntry) {
  return `/${command.namespace || command.package_name} ${command.name}`;
}

function packageCommandHaystack(command: PackageCommandEntry) {
  return normalizeCommandQuery(
    [
      command.namespace,
      command.package_name,
      command.name,
      command.description,
      ...(command.aliases ?? []),
      ...(command.roles ?? []),
      ...(command.recommended_bundles ?? []),
    ].filter(Boolean).join(" "),
  );
}

function normalizeCommandQuery(value: string) {
  return value.toLowerCase().replace(/\s+/g, " ").trim();
}
