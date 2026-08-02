import { useEffect, useMemo, useRef, useState } from "react";
import { Button, Progress, Space, Tag, Typography, type UploadFile } from "antd";
import { PaperClipOutlined } from "@ant-design/icons";
import { Attachments, Sender } from "@ant-design/x";
import type { AttachmentsRef } from "@ant-design/x/es/attachments";
import { useI18n } from "../i18n";
import type { CommandMetadata, PackageCommandEntry } from "../lib/types";

export interface ComposerSubmission {
  text: string;
  files: File[];
}

interface ComposerProps {
  disabled?: boolean;
  uploading?: boolean;
  uploadProgress?: number | null;
  builtinCommands?: CommandMetadata[];
  packageCommands?: PackageCommandEntry[];
  queuedFiles?: File[];
  onQueuedFilesConsumed?: () => void;
  onSubmit: (submission: ComposerSubmission) => Promise<void>;
}

/** A slash-palette entry: either a built-in command (/clear, /model …)
 *  or a package command (/namespace name …) — unified so keyboard
 *  navigation and filtering work across both sources. */
interface PaletteEntry {
  key: string;
  invocation: string;
  description?: string;
  inputHint?: string;
  mode?: string;
  roles?: string[];
  bundles?: string[];
}

export function Composer({ disabled, uploading = false, uploadProgress = null, builtinCommands = [], packageCommands = [], queuedFiles = [], onQueuedFilesConsumed, onSubmit }: ComposerProps) {
  const { t } = useI18n();
  const [value, setValue] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const [activeIndex, setActiveIndex] = useState(0);
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
  const paletteEntries = useMemo(() => matchSlashCommands(value, builtinCommands, packageCommands), [builtinCommands, packageCommands, value]);
  const showCommandPalette = value.trimStart().startsWith("/") && !value.endsWith(" ") && files.length === 0 && paletteEntries.length > 0;

  useEffect(() => {
    setActiveIndex(0);
  }, [value]);

  useEffect(() => {
    if (queuedFiles.length === 0) return;
    setFiles((current) => [...current, ...queuedFiles]);
    onQueuedFilesConsumed?.();
  }, [onQueuedFilesConsumed, queuedFiles]);

  const submit = async (text: string) => {
    const trimmed = text.trim();
    if ((!trimmed && files.length === 0) || submitting) {
      return;
    }
    await onSubmit({ text: trimmed, files });
    setValue("");
    setFiles([]);
  };

  const pickCommand = (entry: PaletteEntry) => {
    setValue(`${entry.invocation} `);
    setActiveIndex(0);
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
          <div className="command-palette" role="listbox">
            {paletteEntries.map((entry, index) => (
              <button
                type="button"
                className={index === activeIndex ? "command-palette-item command-palette-item-active" : "command-palette-item"}
                key={entry.key}
                role="option"
                aria-selected={index === activeIndex}
                onMouseEnter={() => setActiveIndex(index)}
                onClick={() => pickCommand(entry)}
              >
                <Space direction="vertical" size={2} style={{ width: "100%" }}>
                  <Space size={6} wrap>
                    <Typography.Text strong>{entry.invocation}</Typography.Text>
                    {entry.inputHint ? <Typography.Text type="secondary">{entry.inputHint}</Typography.Text> : null}
                    {entry.mode ? <Tag>{entry.mode}</Tag> : null}
                    {entry.roles?.slice(0, 2).map((role) => <Tag key={role}>{role}</Tag>)}
                  </Space>
                  {entry.description ? <Typography.Text type="secondary">{entry.description}</Typography.Text> : null}
                  {entry.bundles?.length ? (
                    <Space size={4} wrap>
                      {entry.bundles.slice(0, 4).map((bundle) => <Tag key={bundle}>{bundle}</Tag>)}
                    </Space>
                  ) : null}
                </Space>
              </button>
            ))}
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
          onSubmit={(message) => {
            if (showCommandPalette && paletteEntries[activeIndex]) {
              pickCommand(paletteEntries[activeIndex]);
              return;
            }
            void submit(message);
          }}
          onKeyDown={(event) => {
            if (!showCommandPalette) return;
            if (event.key === "ArrowDown" || (event.key === "Tab" && !event.shiftKey)) {
              event.preventDefault();
              setActiveIndex((current) => (current + 1) % paletteEntries.length);
            } else if (event.key === "ArrowUp" || (event.key === "Tab" && event.shiftKey)) {
              event.preventDefault();
              setActiveIndex((current) => (current - 1 + paletteEntries.length) % paletteEntries.length);
            } else if (event.key === "Escape") {
              event.preventDefault();
              setValue("");
            }
          }}
          onPasteFile={(pastedFiles) => setFiles((current) => [...current, ...Array.from(pastedFiles)])}
          prefix={
            <Button
              type="text"
              size="small"
              icon={<PaperClipOutlined />}
              aria-label={t("chat.addFiles")}
              title={t("chat.addFiles")}
              disabled={submitting}
              onClick={() => attachmentsRef.current?.select({ multiple: true })}
            />
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

function matchSlashCommands(value: string, builtinCommands: CommandMetadata[], packageCommands: PackageCommandEntry[]): PaletteEntry[] {
  const trimmed = value.trimStart();
  if (!trimmed.startsWith("/")) {
    return [];
  }
  const query = normalizeCommandQuery(trimmed.slice(1));
  const builtins: PaletteEntry[] = builtinCommands.map((command) => ({
    key: `builtin:${command.name}`,
    invocation: `/${command.name}`,
    description: command.description,
    inputHint: command.input_hint,
  }));
  const packages: PaletteEntry[] = packageCommands.map((command) => ({
    key: `pkg:${command.package_name}:${command.namespace || ""}:${command.name}:${command.path}`,
    invocation: `/${command.namespace || command.package_name} ${command.name}`,
    description: command.description,
    mode: command.mode,
    roles: command.roles,
    bundles: command.recommended_bundles,
  }));
  const all = [...builtins, ...packages];
  if (!query) {
    return all.slice(0, 8);
  }
  return all
    .filter((entry) =>
      normalizeCommandQuery(
        [entry.invocation, entry.description, ...(entry.roles ?? []), ...(entry.bundles ?? [])].filter(Boolean).join(" "),
      ).includes(query),
    )
    .slice(0, 8);
}

function normalizeCommandQuery(value: string) {
  return value.toLowerCase().replace(/\s+/g, " ").trim();
}
