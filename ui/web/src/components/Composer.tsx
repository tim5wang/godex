import { useEffect, useImperativeHandle, useMemo, useRef, useState, type Ref } from "react";
import { Button, Progress, Space, Tag, Typography, type UploadFile } from "antd";
import { PaperClipOutlined } from "@ant-design/icons";
import { Attachments, Sender } from "@ant-design/x";
import type { AttachmentsRef } from "@ant-design/x/es/attachments";
import { useI18n } from "../i18n";
import { clearDraft, draftSignature, loadDraft, loadDraftFiles, saveDraft } from "../lib/composerDraft";
import type { CommandMetadata, PackageCommandEntry } from "../lib/types";

export interface ComposerSubmission {
  text: string;
  files: File[];
}

/** Composer 外部控制句柄：供语音识别等场景把文本注入输入框，由用户编辑后发送。 */
export interface ComposerHandle {
  setText: (text: string) => void;
}

interface ComposerProps {
  disabled?: boolean;
  uploading?: boolean;
  uploadProgress?: number | null;
  builtinCommands?: CommandMetadata[];
  packageCommands?: PackageCommandEntry[];
  queuedFiles?: File[];
  onQueuedFilesConsumed?: () => void;
  /** Stable per-session key used to persist the unsent draft (text + files).
   *  Empty disables draft persistence. */
  draftScope?: string;
  onSubmit: (submission: ComposerSubmission) => Promise<void>;
  /** React 19：ref 作为普通 prop 传入，暴露 ComposerHandle。 */
  ref?: Ref<ComposerHandle>;
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

export function Composer({ disabled, uploading = false, uploadProgress = null, builtinCommands = [], packageCommands = [], queuedFiles = [], onQueuedFilesConsumed, draftScope = "", onSubmit, ref }: ComposerProps) {
  const { t } = useI18n();
  const [value, setValue] = useState("");
  const [files, setFiles] = useState<File[]>([]);
  const [activeIndex, setActiveIndex] = useState(0);
  const attachmentsRef = useRef<AttachmentsRef>(null);
  const submitting = Boolean(disabled || uploading);
  const draftLoadedRef = useRef<string>("");
  const lastSavedRef = useRef<string>("");
  // True while an async draft restore is in flight. While restoring, the save
  // effect is suppressed so the intermediate empty state is never persisted.
  const [restoring, setRestoring] = useState(false);
  // Guards the async draft restore: once the user starts typing or attaching
  // files, a late-arriving restored draft must not clobber their input.
  const inputDirtyRef = useRef(false);

  // 外部注入文本（语音识别结果）：标记 dirty 防止晚到的草稿恢复覆盖用户输入。
  useImperativeHandle(
    ref,
    () => ({
      setText: (text: string) => {
        inputDirtyRef.current = true;
        setValue(text);
      },
    }),
    [],
  );
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

  // Load the persisted draft whenever the session scope changes. The ref guard
  // skips re-loading when the component re-renders for the same scope. After
  // restoring we set lastSavedRef to the restored content so the save effect
  // does not immediately rewrite the same draft.
  useEffect(() => {
    if (!draftScope) {
      setValue("");
      setFiles([]);
      draftLoadedRef.current = "";
      lastSavedRef.current = "";
      setRestoring(false);
      return;
    }
    if (draftLoadedRef.current === draftScope) {
      return;
    }
    draftLoadedRef.current = draftScope;
    inputDirtyRef.current = false;
    setRestoring(true);
    setValue("");
    setFiles([]);
    void loadDraft(draftScope)
      .then(async (draft) => {
        if (draftLoadedRef.current !== draftScope || inputDirtyRef.current) return;
        const text = draft?.text ?? "";
        setValue(text);
        let restored: File[] = [];
        if (draft && draft.files.length > 0) {
          try {
            restored = await loadDraftFiles(draft.files);
          } catch {
            restored = [];
          }
          if (draftLoadedRef.current !== draftScope || inputDirtyRef.current) return;
        }
        setFiles(restored);
        lastSavedRef.current = draftSignature(text, restored);
      })
      .catch(() => {
        // Ignore restore failures: fall back to an empty composer.
      })
      .finally(() => {
        if (draftLoadedRef.current === draftScope) {
          setRestoring(false);
        }
      });
  }, [draftScope]);

  // Debounced persistence: write the draft ~400ms after the user stops typing,
  // and on visibility loss (tab switch) so the very last keystroke survives.
  // Skipped while a restore is in flight and when nothing changed.
  useEffect(() => {
    if (!draftScope || draftLoadedRef.current !== draftScope || restoring) return;
    if (draftSignature(value, files) === lastSavedRef.current) return;
    if (!value && files.length === 0) {
      void clearDraft(draftScope);
      lastSavedRef.current = "";
      return;
    }
    const handle = window.setTimeout(() => {
      void saveDraft(draftScope, value, files).then(() => {
        lastSavedRef.current = draftSignature(value, files);
      });
    }, 400);
    const onVisibility = () => {
      if (document.visibilityState === "hidden" && draftLoadedRef.current === draftScope && !restoring) {
        void saveDraft(draftScope, value, files).then(() => {
          lastSavedRef.current = draftSignature(value, files);
        });
      }
    };
    document.addEventListener("visibilitychange", onVisibility);
    return () => {
      window.clearTimeout(handle);
      document.removeEventListener("visibilitychange", onVisibility);
    };
  }, [draftScope, files, restoring, value]);

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
    const payload = { text: trimmed, files };
    // Optimistic clear: empty the input immediately so the message does
    // not appear stuck while the network request is in flight.  OnSubmit
    // (ChatPage) shows the optimistic placeholder right away; on failure
    // we restore the text so the user can retry.
    setValue("");
    setFiles([]);
    inputDirtyRef.current = true;
    if (draftScope) {
      void clearDraft(draftScope);
    }
    try {
      await onSubmit(payload);
    } catch {
      setValue(trimmed);
      setFiles(payload.files);
    }
  };

  const pickCommand = (entry: PaletteEntry) => {
    inputDirtyRef.current = true;
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
          onChange={(next) => {
            inputDirtyRef.current = true;
            setValue(next);
          }}
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
          onPasteFile={(pastedFiles) => {
            inputDirtyRef.current = true;
            setFiles((current) => [...current, ...Array.from(pastedFiles)]);
          }}
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
                inputDirtyRef.current = true;
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
