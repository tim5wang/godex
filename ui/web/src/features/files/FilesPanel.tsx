import React, { useEffect, useRef, useState } from "react";
import { Alert, Button, Input, Space, Spin, Typography, message, Modal } from "antd";
import { MenuFoldOutlined, MenuUnfoldOutlined, PlusOutlined, UploadOutlined, SearchOutlined, FolderOpenOutlined, SaveOutlined } from "@ant-design/icons";
import FileTree from "./FileTree";
import CodeEditor from "./CodeEditor";
import { useLayoutStore } from "../../store/layout";
import { useSettingsStore } from "../../store/settings";
import { useI18n } from "../../i18n";
import { listFiles, mkdirFile, readFile, writeFile, deleteFile, renameFile } from "../../lib/api";

// P2 / T6 (SPEC §4.3): the files panel is now mountable in two
// surfaces:
//
//   * mode="page" — a transparent host for the existing <FilesPage>
//     route. A2 does not wire the route itself; the page is unchanged
//     and continues to render its own header + body. We export the
//     container so a follow-up commit can wrap the page in <FilesPanel
//     mode="page"> without touching the page's internal layout.
//
//   * mode="dock" — a self-contained IDE-style panel:
//       - top toolbar (new folder / upload / search / breadcrumb)
//       - left tree (FileTree, collapsible to icon)
//       - right code preview (CodeEditor, read-only)
//
// Data flow:
//   * selectFilesLayoutState(state) → { collapsed, width, iconOnlyWidth }
//   * The store flag panels.files.width (default 320) drives the
//     dock column's expanded width; the collapsed strip is the fixed
//     40px bookmark per SPEC §4.5.
//   * Files list and tree come from features/files/FileTree.tsx via
//     the existing listFiles API; preview comes from CodeEditor.
//
// A2 deliberately does NOT mount this component into CenterGrid; that
// integration is P2-b and lives in its own commit.

export type FilesPanelProps = {
  mode: "dock" | "page";
  /** Initial directory to show in the tree. Defaults to "." */
  cwd?: string;
  /** Optional currently-selected file path (highlights in tree + shows in preview). */
  selectedPath?: string;
  /** Notify parent when the user picks a different file in the tree. */
  onSelect?: (path: string) => void;
  /** Attach a selected workspace file to the active chat composer. */
  onAttachFile?: (file: File) => void;
  /** Fill the parent grid cell and ignore the dock collapsed strip state. */
  fillContainer?: boolean;
  /** Transparent wrapper children for mode="page". */
  children?: React.ReactNode;
};

export function FilesPanel(props: FilesPanelProps) {
  const { mode } = props;
  if (mode === "page") {
    return <FilesPanelPageHost>{props.children}</FilesPanelPageHost>;
  }
  return <FilesPanelDock {...props} />;
}

function FilesPanelPageHost({ children }: { children?: React.ReactNode }) {
  // mode="page" is a transparent container. The <FilesPage> route
  // owns its own header / body and is mounted by the router.
  return <div data-testid="files-panel-page-host">{children}</div>;
}

function FilesPanelDock(props: FilesPanelProps) {
  const { t } = useI18n();
  const token = useSettingsStore((state) => state.token);
  const layoutCollapsed = useLayoutStore((state) => state.panels.files.collapsed);
  const layoutWidth = useLayoutStore((state) => state.panels.files.width ?? 320);
  const setWidth = useLayoutStore((state) => state.setWidth);
  const togglePanel = useLayoutStore((state) => state.toggle);
  const [search, setSearch] = useState("");
  const [selectedPath, setSelectedPath] = useState<string | undefined>(props.selectedPath);
  const [previewContent, setPreviewContent] = useState("");
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState("");
  const [treeCollapsed, setTreeCollapsed] = useState(false);
  const [treeWidth, setTreeWidth] = useState(() => Math.max(180, Math.round((layoutWidth ?? 320) * 0.28)));
  const [editedContent, setEditedContent] = useState<string | null>(null);
  const [saving, setSaving] = useState(false);
  const [refreshKey, setRefreshKey] = useState(0);
  const uploadRef = useRef<HTMLInputElement | null>(null);
  const resizeRef = useRef<{ startX: number; startWidth: number } | null>(null);

  const collapsed = !props.fillContainer && layoutCollapsed;
  const columnWidth = props.fillContainer ? "100%" : collapsed ? 40 : layoutWidth;
  const iconOnlyWidth = 40;

  const hasUnsavedChanges = editedContent !== null && editedContent !== previewContent;

  useEffect(() => {
    if (!selectedPath) {
      setPreviewContent("");
      setPreviewError("");
      setEditedContent(null);
      return;
    }
    let cancelled = false;
    setPreviewLoading(true);
    setPreviewError("");
    setEditedContent(null);
    readFile(token || null, selectedPath, props.cwd ?? ".")
      .then((result) => {
        if (cancelled) return;
        setPreviewContent(result.content);
      })
      .catch((error) => {
        if (cancelled) return;
        setPreviewContent("");
        setPreviewError(error instanceof Error ? error.message : "Failed to read file");
      })
      .finally(() => {
        if (!cancelled) setPreviewLoading(false);
      });
    return () => {
      cancelled = true;
    };
  }, [props.cwd, selectedPath, token]);

  const attachSelected = () => {
    if (!selectedPath || !props.onAttachFile) return;
    const fileName = selectedPath.split("/").pop() || selectedPath;
    props.onAttachFile(new File([previewContent], fileName, { type: "text/plain" }));
  };

  const handleNewFolder = (targetDir: string) => {
    const name = window.prompt(t("files.newFolderPrompt") || "Folder name:");
    if (!name) return;
    void (async () => {
      try {
        await mkdirFile(token || null, targetDir + "/" + name, props.cwd ?? ".");
        setRefreshKey((k) => k + 1);
        message.success(t("files.folderCreated") || "Folder created");
      } catch (e: any) {
        message.error(e?.message || "Failed to create folder");
      }
    })();
  };

  const handleNewFile = (targetDir: string) => {
    const name = window.prompt(t("files.newFilePrompt") || "File name:");
    if (!name) return;
    void (async () => {
      try {
        await writeFile(token || null, targetDir + "/" + name, "", props.cwd ?? ".");
        setRefreshKey((k) => k + 1);
        message.success(t("files.fileCreated") || "File created");
      } catch (e: any) {
        message.error(e?.message || "Failed to create file");
      }
    })();
  };

  const handleUploadTo = (targetDir: string) => {
    // Store target dir temporarily, then trigger the hidden file input.
    (window as any).__godex_upload_dir = targetDir;
    uploadRef.current?.click();
  };

  const handleUpload = () => {
    uploadRef.current?.click();
  };

  const handleFileSelected = async (e: React.ChangeEvent<HTMLInputElement>) => {
    const file = e.target.files?.[0];
    if (!file) return;
    const storedDir = (window as any).__godex_upload_dir as string | undefined;
    delete (window as any).__godex_upload_dir;
    try {
      const dir = storedDir ?? (selectedPath && !selectedPath.endsWith("/") ? parentDir(selectedPath) : (selectedPath || "."));
      const form = new FormData();
      form.append("file", file);
      const url = `/api/files/upload?token=${encodeURIComponent(token || "")}&path=${encodeURIComponent(dir)}&root=${encodeURIComponent(props.cwd ?? ".")}`;
      const resp = await fetch(url, { method: "POST", body: form });
      if (!resp.ok) throw new Error(await resp.text());
      setRefreshKey((k) => k + 1);
      message.success(t("files.uploaded") || "File uploaded");
    } catch (e: any) {
      message.error(e?.message || "Upload failed");
    }
    // Reset input so same file can be re-uploaded.
    e.target.value = "";
  };

  const handleSave = async () => {
    if (!selectedPath || editedContent === null) return;
    setSaving(true);
    try {
      await writeFile(token || null, selectedPath, editedContent, props.cwd ?? ".");
      setPreviewContent(editedContent);
      setEditedContent(null);
      message.success(t("files.saved") || "Saved");
    } catch (e: any) {
      message.error(e?.message || "Failed to save");
    } finally {
      setSaving(false);
    }
  };

  const handleDelete = async (path: string) => {
    Modal.confirm({
      title: t("files.confirmDelete") || "Delete file?",
      content: path,
      okText: t("files.delete") || "Delete",
      okType: "danger",
      cancelText: t("files.cancel") || "Cancel",
      onOk: async () => {
        try {
          await deleteFile(token || null, path, props.cwd ?? ".");
          if (selectedPath === path) { setSelectedPath(undefined); setPreviewContent(""); setEditedContent(null); }
          setRefreshKey((k) => k + 1);
        } catch (e: any) {
          message.error(e?.message || "Delete failed");
        }
      },
    });
  };

  const handleRename = async (path: string) => {
    const newName = window.prompt(t("files.renamePrompt") || "New name:", path.split("/").pop());
    if (!newName || newName === path.split("/").pop()) return;
    const to = parentDir(path) + "/" + newName;
    try {
      await renameFile(token || null, path, to, props.cwd ?? ".");
      if (selectedPath === path) setSelectedPath(to);
      setRefreshKey((k) => k + 1);
    } catch (e: any) {
      message.error(e?.message || "Rename failed");
    }
  };

  // The 40px collapsed strip shows the bare expand affordance.
  if (collapsed) {
    return (
      <div
        data-testid="files-panel-dock-collapsed"
        data-collapsed="true"
        style={{
          width: columnWidth,
          display: "flex",
          flexDirection: "column",
          alignItems: "center",
          paddingTop: 12,
          gap: 8,
        }}
      >
        <Button
          type="text"
          size="small"
          aria-label={t("files.expand") || "Expand files"}
          title={t("files.expand") || "Expand files"}
          onClick={() => {
            setWidth("files", Math.max(320, layoutWidth));
            togglePanel("files");
          }}
        >
          <FolderOpenOutlined />
        </Button>
      </div>
    );
  }

  return (
    <div
      data-testid="files-panel-dock"
      data-collapsed="false"
      style={{
        width: columnWidth,
        display: "flex",
        flexDirection: "column",
        height: "100%",
        background: "var(--panel)",
        borderRight: props.fillContainer ? 0 : "1px solid var(--border)",
      }}
    >
      <div
        style={{
          padding: "4px 8px",
          borderBottom: "1px solid var(--border)",
          display: "flex",
          alignItems: "center",
          gap: 4,
          flexWrap: "wrap",
        }}
      >
        <Button size="small" type="text" icon={<PlusOutlined />} aria-label={t("files.newFolder") || "New folder"} title={t("files.newFolder") || "New folder"} onClick={() => handleNewFolder(selectedPath ? parentDir(selectedPath) : ".")} />
        <Button size="small" type="text" icon={<UploadOutlined />} aria-label={t("files.upload") || "Upload"} title={t("files.upload") || "Upload"} onClick={handleUpload} />
        <input ref={uploadRef} type="file" style={{ display: "none" }} onChange={handleFileSelected} />
        <Button size="small" type="text" icon={treeCollapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
          aria-label={treeCollapsed ? "Expand file tree" : "Collapse file tree"}
          title={treeCollapsed ? "Expand file tree" : "Collapse file tree"}
          onClick={() => setTreeCollapsed((v) => !v)}
          data-testid={treeCollapsed ? "files-panel-tree-expand" : "files-panel-tree-collapse"}
        />
        <Input
          allowClear
          size="small"
          prefix={<SearchOutlined />}
          placeholder={t("files.searchPlaceholder") || "Search files"}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          style={{ flex: "1 1 120px", minWidth: 0 }}
        />
      </div>
      <div style={{ flex: 1, minHeight: 0, minWidth: 0 }}>
        {treeCollapsed ? (
          <div style={{ display: "grid", gridTemplateColumns: "40px minmax(0, 1fr)", height: "100%", minHeight: 0, minWidth: 0 }}>
            <div
              data-testid="files-panel-tree-collapsed"
              style={{ borderRight: "1px solid var(--border)", padding: 6, display: "flex", justifyContent: "center" }}
            >
              <Button
                type="text"
                size="small"
                icon={<MenuUnfoldOutlined />}
                aria-label="Expand file tree"
                title="Expand file tree"
                onClick={() => setTreeCollapsed(false)}
              />
            </div>
            <FilePreview
              selectedPath={selectedPath}
              previewContent={previewContent}
              previewLoading={previewLoading}
              previewError={previewError}
              onAttachFile={props.onAttachFile ? attachSelected : undefined}
              hasUnsavedChanges={hasUnsavedChanges}
              onSave={handleSave}
              saving={saving}
              onContentChange={setEditedContent}
              t={t}
            />
          </div>
        ) : (
          <div
            data-testid="files-panel-workbench"
            style={{
              display: "flex",
              height: "100%",
              minHeight: 0,
              minWidth: 0,
            }}
          >
            <div
              data-testid="files-panel-tree"
              style={{ height: "100%", width: treeWidth, minWidth: 80, overflow: "auto", borderRight: "1px solid var(--border)", flexShrink: 0 }}
            >
              <FileTree
                workspaceRoot={props.cwd ?? "."}
                selectedPath={selectedPath ?? null}
                searchQuery={search}
                refreshKey={refreshKey}
                onSelectFile={(path) => {
                  setSelectedPath(path);
                  props.onSelect?.(path);
                }}
                onUnsavedPrompt={async () => "discard" as const}
                onNewFile={handleNewFile}
                onNewFolder={handleNewFolder}
                onUploadTo={handleUploadTo}
                onDelete={handleDelete}
                onRename={handleRename}
              />
            </div>
            {/* Resize handle */}
            <div
              data-testid="files-panel-tree-resize"
              onMouseDown={(e) => {
                e.preventDefault();
                resizeRef.current = { startX: e.clientX, startWidth: treeWidth };
                const onMove = (ev: MouseEvent) => {
                  if (!resizeRef.current) return;
                  const delta = ev.clientX - resizeRef.current.startX;
                  setTreeWidth(Math.max(80, resizeRef.current.startWidth + delta));
                };
                const onUp = () => {
                  resizeRef.current = null;
                  window.removeEventListener("mousemove", onMove);
                  window.removeEventListener("mouseup", onUp);
                  document.body.style.cursor = "";
                  document.body.style.userSelect = "";
                };
                document.body.style.cursor = "col-resize";
                document.body.style.userSelect = "none";
                window.addEventListener("mousemove", onMove);
                window.addEventListener("mouseup", onUp);
              }}
              style={{
                width: 4,
                cursor: "col-resize",
                flexShrink: 0,
                background: "transparent",
                transition: "background 0.15s",
              }}
              onMouseEnter={(e) => { (e.target as HTMLElement).style.background = "var(--godex-accent)"; }}
              onMouseLeave={(e) => { (e.target as HTMLElement).style.background = "transparent"; }}
            />
            <div style={{ flex: "1 1 auto", minWidth: 0 }}>
            <FilePreview
              selectedPath={selectedPath}
              previewContent={previewContent}
              previewLoading={previewLoading}
              previewError={previewError}
              onAttachFile={props.onAttachFile ? attachSelected : undefined}
              hasUnsavedChanges={hasUnsavedChanges}
              onSave={handleSave}
              saving={saving}
              onContentChange={setEditedContent}
              t={t}
            />
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

function parentDir(path: string): string {
  const idx = path.lastIndexOf("/");
  return idx === -1 ? "." : path.substring(0, idx);
}

function FilePreview(props: {
  selectedPath?: string;
  previewContent: string;
  previewLoading: boolean;
  previewError: string;
  onAttachFile?: () => void;
  hasUnsavedChanges?: boolean;
  onSave?: () => void;
  saving?: boolean;
  onContentChange?: (content: string) => void;
  t: (key: string) => string;
}) {
  return (
    <div data-testid="files-panel-preview-pane" style={{ display: "flex", height: "100%", minWidth: 0, minHeight: 0, flexDirection: "column" }}>
      <div
        style={{
          display: "flex",
          alignItems: "center",
          justifyContent: "space-between",
          gap: 8,
          borderBottom: "1px solid var(--border)",
          padding: "4px 8px",
        }}
      >
        <Typography.Text data-testid="files-panel-preview-path" type="secondary" ellipsis>
          {props.selectedPath ?? (props.t("files.noFileSelected") || "Select a file")}
        </Typography.Text>
        <Space size={4}>
          {props.hasUnsavedChanges ? (
            <Button
              size="small" type="primary"
              icon={<SaveOutlined />}
              loading={props.saving}
              onClick={props.onSave}
              data-testid="files-panel-save"
            >
              {props.t("files.save") || "Save"}
            </Button>
          ) : null}
          {props.onAttachFile ? (
            <Button
              size="small"
              icon={<UploadOutlined />}
              disabled={!props.selectedPath || props.previewLoading || !!props.previewError}
              onClick={props.onAttachFile}
              data-testid="files-panel-attach-selected"
            >
              {props.t("files.attachToChat") || "Attach"}
            </Button>
          ) : null}
        </Space>
      </div>
      <div data-testid="files-panel-preview" style={{ flex: 1, minHeight: 0, overflow: "auto" }}>
        {props.previewLoading ? (
          <div style={{ display: "grid", height: "100%", placeItems: "center" }}>
            <Spin size="small" />
          </div>
        ) : props.previewError ? (
          <Alert type="error" showIcon message={props.previewError} />
        ) : (
          <CodeEditor
            content={props.previewContent}
            filePath={props.selectedPath ?? "(no file)"}
            readOnly={false}
            onChange={props.onContentChange}
          />
        )}
      </div>
    </div>
  );
}
