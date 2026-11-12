import React, { useEffect, useMemo, useRef, useState } from "react";
import { Alert, Button, Input, Segmented, Space, Spin, Typography, message, Modal, Select } from "antd";
import { MenuFoldOutlined, MenuUnfoldOutlined, PlusOutlined, UploadOutlined, FolderOpenOutlined, SaveOutlined, ReloadOutlined } from "@ant-design/icons";
import FileTree from "./FileTree";
import CodeEditor from "./CodeEditor";
import BinaryPreview, { type BinaryPreviewKind } from "./BinaryPreview";
import { useNodeContextStore } from "../../store/nodeContext";
import { filesViewKey, readFilesView, useRememberFilesView } from "./filesViewState";
import { FilesGitChanges, GitDiffPane } from "./FilesGitChanges";
import { classifyGitChange, useGitStatus } from "./gitChanges";
import type { GitDiffBase } from "../../lib/api";
import { DiffView } from "../../components/DiffView";
import { computeLineDiff } from "./diffLines";
import { MarkdownContent } from "../../components/MarkdownContent";
import { useLayoutStore } from "../../store/layout";
import { useSettingsStore } from "../../store/settings";
import { useI18n } from "../../i18n";
import { mkdirFile, readFile, writeFile, deleteFile, renameFile, searchFiles, type FileSearchResult } from "../../lib/api";

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
  /** Session-scoped relay target (exec_mode=relay:<node>); file ops go
   * through the center proxy to that node. Empty = local center. */
  relayNode?: string;
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
  /** When set (changed), select + preview this file and auto-expand the tree chain. */
  focusPath?: string | null;
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
  const node = useNodeContextStore((s) => s.nodeID);
  const viewKey = filesViewKey("dock", props.relayNode ? "" : (props.cwd ?? "."), node, props.relayNode);
  return <FilesPanelDockWorkspace key={viewKey} {...props} viewKey={viewKey} />;
}

function FilesPanelDockWorkspace(props: FilesPanelProps & { viewKey: string }) {
  const restored = useRef(readFilesView(props.viewKey)).current;
  const initialPath = props.focusPath || props.selectedPath || restored.selectedPath;
  const restoredDraft = useRef(initialPath === restored.selectedPath ? { path: initialPath, content: restored.draft } : null);
  const { t } = useI18n();
  const token = useSettingsStore((state) => state.token);
  const relayNode = props.relayNode;
  // Relay sessions run tools on the remote node: the A-side cwd (a local
  // path) does not exist there, so pass an empty root and let the node
  // resolve its own workspace (resolveFileRootFromParam falls back to the
  // node's workspace_dir). Non-relay sessions keep the caller's cwd.
  const cwd = relayNode ? "" : (props.cwd ?? ".");
  const layoutCollapsed = useLayoutStore((state) => state.panels.files.collapsed);
  const layoutWidth = useLayoutStore((state) => state.panels.files.width ?? 320);
  const setWidth = useLayoutStore((state) => state.setWidth);
  const togglePanel = useLayoutStore((state) => state.toggle);
  const [search, setSearch] = useState("");
  const [searchMode, setSearchMode] = useState<"name" | "content">("name");
  const [searchResults, setSearchResults] = useState<FileSearchResult[] | null>(null);
  const [searchLoading, setSearchLoading] = useState(false);
  const searchTimerRef = useRef<ReturnType<typeof setTimeout> | null>(null);
  const [selectedPath, setSelectedPath] = useState<string | undefined>(initialPath);
  const [previewContent, setPreviewContent] = useState("");
  const [previewLoading, setPreviewLoading] = useState(false);
  const [previewError, setPreviewError] = useState("");
  const [treeCollapsed, setTreeCollapsed] = useState(false);
  const [treeWidth, setTreeWidth] = useState(() => Math.max(180, Math.round((layoutWidth ?? 320) * 0.28)));
  const [editedContent, setEditedContent] = useState<string | null>(restoredDraft.current?.content ?? null);
  const [saving, setSaving] = useState(false);
  const [refreshKey, setRefreshKey] = useState(0);
  const [previewReloadKey, setPreviewReloadKey] = useState(0);
  // Sidebar surface: the file tree or the Git change list. Both dock and page
  // expose the same pair so the experience matches in either place.
  const [sidebarView, setSidebarView] = useState<"files" | "changes">(restored.sidebarView ?? "files");
  const [gitBase, setGitBase] = useState<GitDiffBase>(restored.gitBase ?? "head");
  useRememberFilesView(props.viewKey, {
    selectedPath, draft: editedContent ?? undefined, sidebarView, gitBase,
  });
  const uploadRef = useRef<HTMLInputElement | null>(null);
  const resizeRef = useRef<{ startX: number; startWidth: number } | null>(null);

  const collapsed = !props.fillContainer && layoutCollapsed;
  const columnWidth = props.fillContainer ? "100%" : collapsed ? 40 : layoutWidth;
  const iconOnlyWidth = 40;

  const hasUnsavedChanges = editedContent !== null && editedContent !== previewContent;

  // Read-only Git state for this workspace root. `cwd` is "" in relay sessions,
  // which is exactly what /git/status wants there (the node resolves its own
  // workspace), so the relay boundary needs no special case.
  const gitStatus = useGitStatus({ token, root: cwd, relayNode, refreshKey });
  const changedPaths = useMemo(() => {
    const map = new Map<string, ReturnType<typeof classifyGitChange>>();
    for (const file of gitStatus.data?.files ?? []) {
      map.set(file.path, classifyGitChange(file));
    }
    return map;
  }, [gitStatus.data]);
  const selectedChangeKind = selectedPath ? changedPaths.get(selectedPath) : undefined;
  // A deleted file cannot be read; the diff is the only thing left to show.
  const selectedIsDeleted = selectedChangeKind === "deleted";

  // Half-controlled selection: external focusPath (e.g. “open in Files” from a
  // Changes card) drives the internal selectedPath when it changes.
  useEffect(() => {
    if (props.focusPath && props.focusPath !== selectedPath) {
      setSelectedPath(props.focusPath);
    }
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [props.focusPath]);

  // Debounced backend search.
  useEffect(() => {
    if (searchTimerRef.current) clearTimeout(searchTimerRef.current);
    if (!search.trim()) {
      setSearchResults(null);
      return;
    }
    searchTimerRef.current = setTimeout(async () => {
      setSearchLoading(true);
      try {
        const res = await searchFiles(token || null, search, searchMode, cwd, relayNode);
        if (!cancelled) setSearchResults(res.items);
      } catch {
        if (!cancelled) setSearchResults([]);
      } finally {
        if (!cancelled) setSearchLoading(false);
      }
    }, 300);
    let cancelled = false;
    return () => { cancelled = true; if (searchTimerRef.current) { clearTimeout(searchTimerRef.current); searchTimerRef.current = null; } };
  }, [search, searchMode, token, cwd, relayNode]);

  useEffect(() => {
    if (!selectedPath) {
      setPreviewContent("");
      setPreviewError("");
      setEditedContent(null);
      return;
    }
    // A file git reports as deleted no longer exists on disk, so reading it
    // would only produce a 404. Its diff is the content left to show.
    if (selectedIsDeleted) {
      setPreviewLoading(false);
      setPreviewContent("");
      setPreviewError("");
      setEditedContent(null);
      return;
    }
    let cancelled = false;
    setPreviewLoading(true);
    setPreviewError("");
    if (restoredDraft.current?.path !== selectedPath) restoredDraft.current = null;
    setEditedContent(restoredDraft.current?.content ?? null);
    readFile(token || null, selectedPath, cwd, relayNode)
      .then((result) => {
        if (cancelled) return;
        setPreviewContent(result.content);
        restoredDraft.current = null;
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
  }, [cwd, selectedPath, token, previewReloadKey, relayNode, selectedIsDeleted]);

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
        await mkdirFile(token || null, targetDir + "/" + name, cwd, relayNode);
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
        await writeFile(token || null, targetDir + "/" + name, "", cwd, relayNode);
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
      const url = `${relayProxyPrefix(relayNode)}/files/upload?token=${encodeURIComponent(token || "")}&path=${encodeURIComponent(dir)}&root=${encodeURIComponent(cwd)}`;
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
      await writeFile(token || null, selectedPath, editedContent, cwd, relayNode);
      restoredDraft.current = null;
      setPreviewContent(editedContent);
      setEditedContent(null);
      message.success(t("files.saved") || "Saved");
      // The file's Git state just changed on disk, so the change list and any
      // open diff would otherwise keep showing the pre-save tree.
      setRefreshKey((k) => k + 1);
    } catch (e: any) {
      message.error(e?.message || "Failed to save");
    } finally {
      setSaving(false);
    }
  };

  /**
   * Ask before a file switch throws away unsaved edits.
   *
   * The dock previously passed `async () => "discard"`, so picking another file
   * in the tree silently dropped the buffer. Returns true when the caller may
   * proceed, false when the user cancelled.
   */
  const confirmDiscardIfDirty = async (): Promise<boolean> => {
    if (editedContent === null || editedContent === previewContent) return true;
    return new Promise((resolve) => {
      Modal.warning({
        title: t("files.unsavedSwitchTitle") || "Unsaved changes",
        content: t("files.unsavedSwitchBody", { path: selectedPath ?? "" }) ||
          `"${selectedPath}" has unsaved changes. Switching will discard them.`,
        okText: t("files.switchDiscard") || "Discard and switch",
        okButtonProps: { danger: true },
        cancelText: t("files.cancel") || "Cancel",
        onOk: () => resolve(true),
        onCancel: () => resolve(false),
        // Covers the X button and Esc, which fire neither onOk nor onCancel.
        afterClose: () => resolve(false),
      });
    });
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
          await deleteFile(token || null, path, cwd, relayNode);
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
      await renameFile(token || null, path, to, cwd, relayNode);
      if (selectedPath === path) setSelectedPath(to);
      setRefreshKey((k) => k + 1);
    } catch (e: any) {
      message.error(e?.message || "Rename failed");
    }
  };

  // Refresh control: reload the file tree and, when there are no unsaved
  // edits, re-read the currently selected file from disk. Unsaved edits are
  // never discarded by a refresh.
  const handleRefresh = () => {
    setRefreshKey((k) => k + 1);
    if (!hasUnsavedChanges) setPreviewReloadKey((k) => k + 1);
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
        <Space.Compact style={{ flex: "1 1 200px", minWidth: 0 }}>
          <Select
            size="small"
            value={searchMode}
            onChange={(v) => setSearchMode(v)}
            options={[
              { value: "name", label: "Name" },
              { value: "content", label: "Content" },
            ]}
            style={{ width: 100 }}
          />
          <Input
            allowClear
            size="small"
            placeholder={t("files.searchPlaceholder") || "Search files"}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
          />
        </Space.Compact>
        <Button
          size="small"
          type="text"
          icon={<ReloadOutlined />}
          aria-label={t("files.refresh") || "Refresh"}
          title={t("files.refresh") || "Refresh"}
          onClick={handleRefresh}
          data-testid="files-panel-refresh"
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
              editedContent={editedContent}
              previewLoading={previewLoading}
              previewError={previewError}
              onAttachFile={props.onAttachFile ? attachSelected : undefined}
              hasUnsavedChanges={hasUnsavedChanges}
              onSave={handleSave}
              saving={saving}
              onContentChange={setEditedContent}
              token={token}
              cwd={cwd}
              relayNode={relayNode}
              gitChangeKind={selectedChangeKind}
              gitBase={gitBase}
              onGitBaseChange={setGitBase}
              refreshKey={refreshKey}
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
              style={{ height: "100%", width: treeWidth, minWidth: 80, borderRight: "1px solid var(--border)", flexShrink: 0, display: "flex", flexDirection: "column", minHeight: 0 }}
            >
              {/* Files ↔ Changes, same pair as FilesPage so the two surfaces match. */}
              <Segmented
                size="small"
                block
                value={sidebarView}
                onChange={(value) => setSidebarView(value as "files" | "changes")}
                options={[
                  { value: "files", label: t("files.fileTree") || "Files" },
                  { value: "changes", label: `${t("files.gitChanges") || "Changes"}${changedPaths.size ? ` (${changedPaths.size})` : ""}` },
                ]}
                data-testid="files-panel-sidebar-mode"
                style={{ margin: 4 }}
              />
              <div style={{ flex: 1, minHeight: 0, overflow: "auto" }}>
                {sidebarView === "changes" ? (
                  <FilesGitChanges
                    token={token}
                    cwd={cwd}
                    relayNode={relayNode}
                    refreshKey={refreshKey}
                    selectedPath={selectedPath}
                    onSelectPath={async (path) => {
                      // Same protection as the tree: a switch must not silently
                      // drop the edited buffer.
                      if (!(await confirmDiscardIfDirty())) return;
                      setEditedContent(null);
                      setSelectedPath(path);
                      props.onSelect?.(path);
                    }}
                    onRefresh={handleRefresh}
                    t={t}
                  />
                ) : (
                  <FileTree
                    workspaceRoot={cwd}
                    relayNode={relayNode}
                    selectedPath={selectedPath ?? null}
                    searchQuery={search}
                    searchResults={searchResults}
                    searchLoading={searchLoading}
                    refreshKey={refreshKey}
                    focusPath={selectedPath ?? null}
                    onSelectFile={(path) => {
                      // No prompt here: FileTree already asked through
                      // onUnsavedPrompt just above, and prompting again would
                      // show the dialog twice for one switch.
                      setEditedContent(null);
                      setSelectedPath(path);
                      props.onSelect?.(path);
                      // Clear search when selecting from results.
                      if (searchResults) { setSearch(""); setSearchResults(null); }
                    }}
                    onUnsavedPrompt={async () => ((await confirmDiscardIfDirty()) ? ("discard" as const) : ("cancel" as const))}
                    onNewFile={handleNewFile}
                    onNewFolder={handleNewFolder}
                    onUploadTo={handleUploadTo}
                    onDelete={handleDelete}
                    onRename={handleRename}
                  />
                )}
              </div>
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
              editedContent={editedContent}
              previewLoading={previewLoading}
              previewError={previewError}
              onAttachFile={props.onAttachFile ? attachSelected : undefined}
              hasUnsavedChanges={hasUnsavedChanges}
              onSave={handleSave}
              saving={saving}
              onContentChange={setEditedContent}
              token={token}
              cwd={cwd}
              relayNode={relayNode}
              gitChangeKind={selectedChangeKind}
              gitBase={gitBase}
              onGitBaseChange={setGitBase}
              refreshKey={refreshKey}
              t={t}
            />
            </div>
          </div>
        )}
      </div>
    </div>
  );
}

/** Center-proxy prefix for node-scoped file ops (exec_mode=relay:<node>).
 * The center strips /control/nodes/{id}/proxy before forwarding, so the
 * remainder must match the node's own httpapi routes (no /api prefix). */
function relayProxyPrefix(relayNode?: string): string {
  return relayNode ? `/api/control/nodes/${encodeURIComponent(relayNode)}/proxy` : "";
}

function parentDir(path: string): string {
  const idx = path.lastIndexOf("/");
  return idx === -1 ? "." : path.substring(0, idx);
}

function FilePreview(props: {
  selectedPath?: string;
  previewContent: string;
  editedContent?: string | null;
  previewLoading: boolean;
  previewError: string;
  onAttachFile?: () => void;
  hasUnsavedChanges?: boolean;
  onSave?: () => void;
  saving?: boolean;
  onContentChange?: (content: string) => void;
  token?: string | null;
  cwd?: string;
  relayNode?: string;
  /** Git status of the selected path, when it has one. */
  gitChangeKind?: string;
  gitBase: GitDiffBase;
  onGitBaseChange: (base: GitDiffBase) => void;
  refreshKey: number;
  t: (key: string, vars?: Record<string, string | number>) => string;
}) {
  const isMarkdown = isMarkdownPath(props.selectedPath);
  // "diff" is the UNSAVED-edits comparison; "git" is the real Git diff. They
  // are separate modes on purpose and each states its baseline, so an unsaved
  // buffer can never be read as repository history.
  const [viewMode, setViewMode] = useState<"source" | "render" | "diff" | "git">("source");
  const isDeleted = props.gitChangeKind === "deleted";
  const hasGitChange = !!props.gitChangeKind;

  // Opening a deleted file has nothing to source-render: jump to its diff.
  useEffect(() => {
    setViewMode(isDeleted ? "git" : "source");
  }, [props.selectedPath, isDeleted]);
  // Rewrite relative image srcs to the preview static route so workspace
  // images render with auth (img tags cannot attach an Authorization header,
  // so the token is passed as a query param). Relay sessions route through
  // the center proxy to the remote node.
  const resolveImageUrl = useMemo(() => {
    if (!props.token || !props.selectedPath) return undefined;
    const dir = parentDir(props.selectedPath);
    const root = props.cwd || ".";
    return (src: string) => {
      if (/^(https?:|data:|mailto:|#|\/\/)/i.test(src)) return src;
      const rel = src.startsWith("/") ? src.slice(1) : dir === "." ? src : `${dir}/${src}`;
      const params = new URLSearchParams({ token: props.token ?? "", root });
      const encoded = rel.split("/").map(encodeURIComponent).join("/");
      return `${relayProxyPrefix(props.relayNode)}/preview/static/${encoded}${params.toString() ? `?${params.toString()}` : ""}`;
    };
  }, [props.token, props.selectedPath, props.cwd, props.relayNode]);
  const diffText =
    props.editedContent != null && props.editedContent !== props.previewContent
      ? computeLineDiff(props.previewContent, props.editedContent)
      : "";

  // Non-text previews (image/video/audio/pdf) are served by the preview
  // static route with the token forwarded as a query param (same auth model
  // as the markdown image resolver above).
  const binaryKind = getBinaryPreviewKind(props.selectedPath);
  const binaryUrl = useMemo(() => {
    if (!binaryKind || !props.selectedPath) return "";
    return buildPreviewStaticUrl(props.selectedPath, props.cwd ?? ".", props.token, props.relayNode);
  }, [binaryKind, props.selectedPath, props.cwd, props.token, props.relayNode]);
  const [binaryFailed, setBinaryFailed] = useState(false);
  useEffect(() => {
    setBinaryFailed(false);
  }, [binaryUrl]);

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
          {isMarkdown ? (
            <Segmented
              size="small"
              value={viewMode}
              onChange={(value) => setViewMode(value as "source" | "render" | "diff" | "git")}
              options={[
                { value: "source", label: props.t("files.viewSource") || "Source" },
                { value: "render", label: props.t("files.viewRender") || "Render" },
                ...(hasGitChange ? [{ value: "git", label: props.t("files.viewGitDiff") || "Git diff" }] : []),
                ...(props.hasUnsavedChanges
                  ? [{ value: "diff", label: props.t("files.unsavedDiff") || "Unsaved" }]
                  : []),
              ]}
              data-testid="files-panel-preview-mode"
            />
          ) : hasGitChange || props.hasUnsavedChanges || isDeleted ? (
            <Segmented
              size="small"
              value={viewMode}
              onChange={(value) => setViewMode(value as "source" | "render" | "diff" | "git")}
              options={[
                ...(isDeleted ? [] : [{ value: "source", label: props.t("files.viewSource") || "Source" }]),
                ...(hasGitChange ? [{ value: "git", label: props.t("files.viewGitDiff") || "Git diff" }] : []),
                ...(props.hasUnsavedChanges
                  ? [{ value: "diff", label: props.t("files.unsavedDiff") || "Unsaved" }]
                  : []),
              ]}
              data-testid="files-panel-preview-mode"
            />
          ) : null}
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
        {viewMode === "git" ? (
          // The real Git diff: fetched from /git/diff, baseline stated in the pane.
          <GitDiffPane
            token={props.token ?? null}
            path={props.selectedPath ?? ""}
            cwd={props.cwd ?? "."}
            relayNode={props.relayNode}
            base={props.gitBase}
            onBaseChange={props.onGitBaseChange}
            refreshKey={props.refreshKey}
            t={props.t}
          />
        ) : props.previewLoading ? (
          <div style={{ display: "grid", height: "100%", placeItems: "center" }}>
            <Spin size="small" />
          </div>
        ) : props.previewError ? (
          <Alert type="error" showIcon message={props.previewError} />
        ) : viewMode === "diff" ? (
          // Unsaved-edits comparison against the on-disk file. Labelled so it is
          // never mistaken for repository history.
          <div data-testid="files-panel-unsaved-diff">
            <div className="diff-view-baseline">
              {props.t("files.unsavedDiffBaseline") || "Unsaved edits vs file on disk (not a Git diff)"}
            </div>
            <DiffView diff={diffText} />
          </div>
        ) : isMarkdown && viewMode === "render" ? (
          <div className="files-markdown-render">
            <MarkdownContent content={props.previewContent} forceMarkdown resolveImageUrl={resolveImageUrl} />
          </div>
        ) : binaryKind && binaryUrl ? (
          <BinaryPreview
            kind={binaryKind}
            url={binaryUrl}
            fileName={props.selectedPath?.split("/").pop() ?? ""}
            failed={binaryFailed}
            onFailed={() => setBinaryFailed(true)}
            t={props.t}
          />
        ) : (
          <CodeEditor
            content={props.editedContent ?? props.previewContent}
            filePath={props.selectedPath ?? "(no file)"}
            readOnly={false}
            onChange={props.onContentChange}
          />
        )}
      </div>
    </div>
  );
}

function isMarkdownPath(path?: string): boolean {
  if (!path) return false;
  const ext = path.split(".").pop()?.toLowerCase() ?? "";
  return ext === "md" || ext === "mdx" || ext === "markdown";
}

// File extensions that map to a browser-native preview element. Text-like
// formats are intentionally absent: they keep using the CodeEditor path.
const BINARY_PREVIEW_EXTENSIONS: Record<string, BinaryPreviewKind> = {
  png: "image",
  jpg: "image",
  jpeg: "image",
  gif: "image",
  webp: "image",
  svg: "image",
  bmp: "image",
  ico: "image",
  avif: "image",
  mp4: "video",
  webm: "video",
  mov: "video",
  m4v: "video",
  mp3: "audio",
  wav: "audio",
  m4a: "audio",
  aac: "audio",
  flac: "audio",
  opus: "audio",
  pdf: "pdf",
};

function getBinaryPreviewKind(path?: string): BinaryPreviewKind | null {
  if (!path) return null;
  const ext = path.split(".").pop()?.toLowerCase() ?? "";
  return BINARY_PREVIEW_EXTENSIONS[ext] ?? null;
}

/** Build a token-authenticated URL for the /api/preview/static route. */
function buildPreviewStaticUrl(path: string, cwd: string, token?: string | null, relayNode?: string): string {
  const rel = path.split("/").map(encodeURIComponent).join("/");
  const params = new URLSearchParams();
  if (token) params.set("token", token);
  if (cwd && cwd !== ".") params.set("root", cwd);
  const qs = params.toString();
  return `${relayProxyPrefix(relayNode)}/preview/static/${rel}${qs ? `?${qs}` : ""}`;
}
