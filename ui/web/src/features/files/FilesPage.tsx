import { useCallback, useState, useRef, useEffect, useMemo } from "react";
import { Input, Button, Modal, message, Tag, Select, Segmented, Spin } from "antd";
import { SaveOutlined, FolderOpenOutlined, MenuFoldOutlined, MenuUnfoldOutlined, ReloadOutlined } from "@ant-design/icons";
import { useSettingsStore } from "../../store/settings";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { readFile, writeFile, deleteFile, mkdirFile, renameFile } from "../../lib/api";
import FileTree from "./FileTree";
import CodeEditor from "./CodeEditor";
import { FilesGitChanges, GitDiffPane } from "./FilesGitChanges";
import { classifyGitChange, useGitStatus } from "./gitChanges";
import { useI18n } from "../../i18n";
import type { GitDiffBase } from "../../lib/api";
import type { MetaResponse } from "../../lib/types";
import { getMeta } from "../../lib/api";
import { useNodeContextStore } from "../../store/nodeContext";
import { filesViewKey, readFilesView, useRememberFilesView } from "./filesViewState";

const MAX_FILE_SIZE = 500 * 1024;
const DEFAULT_SIDEBAR_WIDTH = 280;
const MIN_SIDEBAR_WIDTH = 150;
const MAX_SIDEBAR_WIDTH = 600;

function useWorkspaceMeta(node: string | null) {
  return useQuery({
    queryKey: ["workspaceMeta", node],
    queryFn: () => getMeta(),
    staleTime: Infinity,
  });
}

function getRecentWorkspaces(): string[] {
  try {
    const raw = localStorage.getItem("godex-recent-workspaces");
    return raw ? JSON.parse(raw) : [];
  } catch {
    return [];
  }
}

function saveRecentWorkspace(root: string) {
  const recents = getRecentWorkspaces().filter((r) => r !== root);
  recents.unshift(root);
  localStorage.setItem("godex-recent-workspaces", JSON.stringify(recents.slice(0, 10)));
}

export default function FilesPage() {
  const node = useNodeContextStore((s) => s.nodeID);
  return <FilesPageHost key={node ?? "local"} node={node} />;
}

function FilesPageHost({ node }: { node: string | null }) {
  const { data: meta } = useWorkspaceMeta(node);
  const workspaceKey = filesViewKey("workspace", "", node);
  const [workspaceOverride, setWorkspaceRoot] = useState<string | null>(() => readFilesView(workspaceKey).workspaceRoot ?? null);
  // Async metadata remains the default until a workspace is explicitly chosen.
  const workspaceRoot = workspaceOverride ?? meta?.workspace_dir ?? "";
  useRememberFilesView(workspaceKey, { workspaceRoot: workspaceOverride ?? undefined });
  const viewKey = filesViewKey("page", workspaceRoot, node);
  // Do not expose a selectable tree for an unresolved root; its selection would
  // otherwise be lost when metadata remounts the actual workspace.
  if (!workspaceRoot) return <Spin />;
  return <FilesWorkspace key={viewKey} viewKey={viewKey} workspaceRoot={workspaceRoot} setWorkspaceRoot={setWorkspaceRoot} />;
}

function FilesWorkspace({ viewKey, workspaceRoot, setWorkspaceRoot }: {
  viewKey: string;
  workspaceRoot: string;
  setWorkspaceRoot: (root: string) => void;
}) {
  const token = useSettingsStore((s) => s.token);
  const queryClient = useQueryClient();
  const restored = useRef(readFilesView(viewKey)).current;
  const [rootInput, setRootInput] = useState("");
  const [selectedFile, setSelectedFile] = useState<string | null>(restored.selectedPath ?? null);
  const [editorContent, setEditorContent] = useState(restored.draft ?? "");
  const [originalContent, setOriginalContent] = useState("");
  const [isDirty, setIsDirty] = useState(restored.draft !== undefined);
  const [refreshKey, setRefreshKey] = useState(0);
  const [isLargeFile, setIsLargeFile] = useState(false);

  // Sidebar shows either the file tree or the Git change list; the editor area
  // shows either the editor or the changed file's Git diff. Both are explicit
  // so a Git diff is never confused with the unsaved-edits comparison.
  const { t } = useI18n();
  const [sidebarView, setSidebarView] = useState<"files" | "changes">(restored.sidebarView ?? "files");
  const [editorView, setEditorView] = useState<"editor" | "git">(restored.editorView ?? "editor");
  const [gitBase, setGitBase] = useState<GitDiffBase>(restored.gitBase ?? "head");
  useRememberFilesView(viewKey, {
    selectedPath: selectedFile ?? undefined,
    draft: isDirty ? editorContent : undefined,
    sidebarView, editorView, gitBase,
  });
  // FilesPage has no relay target, so an empty root only means meta has not
  // loaded yet; skip the request rather than reading the default workspace's
  // status and showing its changes for a path the user has not opened.
  const gitStatus = useGitStatus({ token, root: workspaceRoot, refreshKey, enabled: !!workspaceRoot });
  const changedPaths = useMemo(() => {
    const map = new Map<string, ReturnType<typeof classifyGitChange>>();
    for (const file of gitStatus.data?.files ?? []) {
      map.set(file.path, classifyGitChange(file));
    }
    return map;
  }, [gitStatus.data]);
  const selectedChangeKind = selectedFile ? changedPaths.get(selectedFile) : undefined;
  // A deleted file has no content to read: opening it must show its Git diff
  // instead of a 404 from /files/read.
  const isDeletedSelection = selectedChangeKind === "deleted";

  // Sidebar layout state
  const [sidebarWidth, setSidebarWidth] = useState(DEFAULT_SIDEBAR_WIDTH);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const dragRef = useRef<{ startX: number; startWidth: number } | null>(null);
  const prevWidthRef = useRef(DEFAULT_SIDEBAR_WIDTH);

  const readFileQuery = useQuery({
    queryKey: ["readFile", viewKey, selectedFile],
    queryFn: async () => {
      if (!selectedFile) return null;
      const res = await readFile(token, selectedFile, workspaceRoot);
      const isLarge = res.size > MAX_FILE_SIZE;
      setIsLargeFile(isLarge);
      return res;
    },
    enabled: !!selectedFile && !!workspaceRoot && !isDeletedSelection,
  });

  // Sync editor content when file is loaded via useEffect instead of during-render setState
  const loadedContent = readFileQuery.data?.content;
  useEffect(() => {
    if (loadedContent !== undefined && selectedFile && !isDirty) {
      setEditorContent(loadedContent);
      setOriginalContent(loadedContent);
    }
  }, [loadedContent, selectedFile, isDirty]);

  const writeMutation = useMutation({
    mutationFn: async (content: string) => {
      if (!selectedFile) return;
      await writeFile(token, selectedFile, content, workspaceRoot);
    },
    onSuccess: () => {
      setOriginalContent(editorContent);
      setIsDirty(false);
      message.success("File saved");
      setRefreshKey((k) => k + 1);
    },
    onError: (err: Error) => {
      message.error(`Save failed: ${err.message}`);
    },
  });

  const deleteMutation = useMutation({
    mutationFn: async (path: string) => {
      await deleteFile(token, path, workspaceRoot);
    },
    onSuccess: () => {
      message.success("Deleted");
      setSelectedFile(null);
      setRefreshKey((k) => k + 1);
    },
    onError: (err: Error) => {
      message.error(`Delete failed: ${err.message}`);
    },
  });

  const mkdirMutation = useMutation({
    mutationFn: async (path: string) => {
      await mkdirFile(token, path, workspaceRoot);
    },
    onSuccess: () => {
      message.success("Directory created");
      setRefreshKey((k) => k + 1);
    },
    onError: (err: Error) => {
      message.error(`Create directory failed: ${err.message}`);
    },
  });

  const renameMutation = useMutation({
    mutationFn: async ({ from, to }: { from: string; to: string }) => {
      await renameFile(token, from, to, workspaceRoot);
    },
    onSuccess: () => {
      message.success("Renamed");
      setRefreshKey((k) => k + 1);
    },
    onError: (err: Error) => {
      message.error(`Rename failed: ${err.message}`);
    },
  });

  const handleSelectFile = useCallback((path: string) => {
    // Tree selection means source, even if this same file was open as a diff.
    setEditorView("editor");
    if (path === selectedFile) return; // Reselecting must not discard this buffer.
    setSelectedFile(path);
    setIsDirty(false);
  }, [selectedFile]);

  const handleUnsavedPrompt = useCallback(
    async (nextPath: string): Promise<"save" | "discard" | "cancel"> => {
      if (!isDirty) return "discard";
      return new Promise((resolve) => {
        const modal = Modal.warning({
          title: "Unsaved changes",
          content: `You have unsaved changes in "${selectedFile}". What would you like to do?`,
          footer: (
            <div style={{ display: "flex", justifyContent: "flex-end", gap: 8 }}>
              <Button onClick={() => { modal.destroy(); resolve("cancel"); }}>
                Cancel
              </Button>
              <Button danger onClick={() => { modal.destroy(); resolve("discard"); }}>
                Don't Save
              </Button>
              <Button type="primary" onClick={() => { writeMutation.mutate(editorContent); modal.destroy(); resolve("save"); }}>
                Save
              </Button>
            </div>
          ),
          closable: true,
          maskClosable: true,
          onCancel: () => resolve("cancel"),
        });
      });
    },
    [isDirty, selectedFile, editorContent, writeMutation],
  );

  const handleSave = useCallback(() => {
    if (isDirty && selectedFile) {
      writeMutation.mutate(editorContent);
    }
  }, [isDirty, selectedFile, editorContent, writeMutation]);

  const handleEditorChange = useCallback((content: string) => {
    setEditorContent(content);
    setIsDirty(true);
  }, []);

  const handleNewFile = useCallback(
    (parentPath: string) => {
      let name = "";
      Modal.confirm({
        title: "New File",
        content: (
          <Input
            placeholder="filename.txt"
            onChange={(e) => { name = e.target.value; }}
          />
        ),
        onOk: async () => {
          if (!name.trim()) return;
          const fullPath = parentPath === "." ? name.trim() : `${parentPath}/${name.trim()}`;
          await writeFile(token, fullPath, "", workspaceRoot);
          message.success("File created");
          setRefreshKey((k) => k + 1);
        },
      });
    },
    [token, workspaceRoot],
  );

  const handleNewFolder = useCallback(
    (parentPath: string) => {
      let name = "";
      Modal.confirm({
        title: "New Folder",
        content: (
          <Input
            placeholder="folder-name"
            onChange={(e) => { name = e.target.value; }}
          />
        ),
        onOk: async () => {
          if (!name.trim()) return;
          const fullPath = parentPath === "." ? name.trim() : `${parentPath}/${name.trim()}`;
          mkdirMutation.mutate(fullPath);
        },
      });
    },
    [mkdirMutation],
  );

  const handleDelete = useCallback(
    (path: string) => {
      Modal.confirm({
        title: "Delete",
        content: `Are you sure you want to delete "${path}"?`,
        okText: "Delete",
        okType: "danger",
        onOk: () => { deleteMutation.mutate(path); },
      });
    },
    [deleteMutation],
  );

  const handleRename = useCallback(
    (path: string) => {
      let newName = "";
      const parentDir = path.includes("/") ? path.substring(0, path.lastIndexOf("/")) : ".";
      Modal.confirm({
        title: "Rename",
        content: (
          <Input
            placeholder="new-name"
            defaultValue={path.split("/").pop()}
            onChange={(e) => { newName = e.target.value; }}
          />
        ),
        onOk: () => {
          if (!newName.trim()) return;
          const to = parentDir === "." ? newName.trim() : `${parentDir}/${newName.trim()}`;
          renameMutation.mutate({ from: path, to });
        },
      });
    },
    [renameMutation],
  );

  const handleSwitchWorkspace = useCallback(() => {
    const trimmed = rootInput.trim();
    if (!trimmed) return;
    setWorkspaceRoot(trimmed);
    saveRecentWorkspace(trimmed);
    setRootInput("");
  }, [rootInput, setWorkspaceRoot]);

  // Sidebar resize via drag
  const handleDragStart = useCallback((e: React.MouseEvent) => {
    e.preventDefault();
    dragRef.current = { startX: e.clientX, startWidth: sidebarWidth };
    const handleMouseMove = (ev: MouseEvent) => {
      if (!dragRef.current) return;
      const delta = ev.clientX - dragRef.current.startX;
      const newWidth = Math.max(MIN_SIDEBAR_WIDTH, Math.min(MAX_SIDEBAR_WIDTH, dragRef.current.startWidth + delta));
      setSidebarWidth(newWidth);
    };
    const handleMouseUp = () => {
      dragRef.current = null;
      document.removeEventListener("mousemove", handleMouseMove);
      document.removeEventListener("mouseup", handleMouseUp);
      document.body.style.cursor = "";
      document.body.style.userSelect = "";
    };
    document.addEventListener("mousemove", handleMouseMove);
    document.addEventListener("mouseup", handleMouseUp);
    document.body.style.cursor = "col-resize";
    document.body.style.userSelect = "none";
  }, [sidebarWidth]);

  const toggleSidebar = useCallback(() => {
    setSidebarCollapsed((prev) => {
      if (!prev) {
        prevWidthRef.current = sidebarWidth;
        return true;
      }
      setSidebarWidth(prevWidthRef.current);
      return false;
    });
  }, [sidebarWidth]);

  const recentWorkspaces = getRecentWorkspaces();

  return (
    <div style={{ display: "flex", flexDirection: "column", height: "100%", overflow: "hidden" }}>
      {/* Workspace Selector */}
      <div style={{ padding: "8px 12px", borderBottom: "1px solid #30363d", display: "flex", gap: 8, alignItems: "center", flexShrink: 0 }}>
        <Button
          type="text"
          size="small"
          icon={sidebarCollapsed ? <MenuUnfoldOutlined /> : <MenuFoldOutlined />}
          onClick={toggleSidebar}
          title={sidebarCollapsed ? "Expand sidebar" : "Collapse sidebar"}
          style={{ color: "#8b949e" }}
        />
        <FolderOpenOutlined style={{ color: "#8b949e" }} />
        <Input
          size="small"
          placeholder="Workspace path..."
          value={rootInput || workspaceRoot}
          onChange={(e) => setRootInput(e.target.value)}
          onPressEnter={handleSwitchWorkspace}
          style={{ flex: 1, maxWidth: 400 }}
        />
        <Button size="small" onClick={handleSwitchWorkspace} disabled={!rootInput.trim()}>
          Switch
        </Button>
        <Button
          size="small"
          icon={<ReloadOutlined />}
          title="Refresh file tree"
          aria-label="Refresh file tree"
          onClick={() => setRefreshKey((k) => k + 1)}
        />
        {recentWorkspaces.length > 0 && (
          <Select
            size="small"
            placeholder="Recent..."
            value={undefined}
            onChange={(v) => {
              if (!v) return;
              setWorkspaceRoot(v);
              saveRecentWorkspace(v);
            }}
            options={recentWorkspaces.map((r) => ({ value: r, label: r }))}
            style={{ minWidth: 120 }}
            allowClear
          />
        )}
      </div>

      {/* File header */}
      {selectedFile && (
        <div style={{ padding: "4px 12px", borderBottom: "1px solid #30363d", display: "flex", alignItems: "center", gap: 8, flexShrink: 0, fontSize: 13, flexWrap: "wrap" }}>
          <span style={{ color: "#e6edf3", flex: 1, minWidth: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{selectedFile}</span>
          {selectedChangeKind && (
            <Tag color={selectedChangeKind === "deleted" ? "red" : selectedChangeKind === "untracked" ? "default" : "gold"}>
              {selectedChangeKind}
            </Tag>
          )}
          {isDirty && <Tag color="warning">Unsaved</Tag>}
          {isLargeFile && <Tag color="warning">Large File (Read Only)</Tag>}
          {/* Switching to the Git diff must not lose unsaved edits: the editor
              stays mounted with its content, this only changes what is shown. */}
          <Segmented
            size="small"
            value={editorView}
            onChange={(value) => setEditorView(value as "editor" | "git")}
            options={[
              { value: "editor", label: t("files.viewEditor") || "Editor" },
              { value: "git", label: t("files.viewGitDiff") || "Git diff" },
            ]}
            data-testid="files-page-view-mode"
          />
          {!isLargeFile && !isDeletedSelection && (
            <Button size="small" icon={<SaveOutlined />} disabled={!isDirty} onClick={handleSave}>
              Save
            </Button>
          )}
        </div>
      )}

      {/* Main content */}
      <div style={{ flex: 1, display: "flex", overflow: "hidden" }}>
        {/* Sidebar */}
        {!sidebarCollapsed && (
          <div style={{ width: sidebarWidth, minWidth: MIN_SIDEBAR_WIDTH, maxWidth: MAX_SIDEBAR_WIDTH, borderRight: "1px solid #30363d", overflow: "hidden", display: "flex", flexShrink: 0 }}>
            <div style={{ flex: 1, overflow: "hidden", display: "flex", flexDirection: "column", minWidth: 0 }}>
              {/* File tree ↔ Git changes. The change list is the discoverable
                  entry point for Git state; the tree stays the file browser. */}
              <Segmented
                size="small"
                block
                value={sidebarView}
                onChange={(value) => setSidebarView(value as "files" | "changes")}
                options={[
                  { value: "files", label: t("files.fileTree") || "Files" },
                  { value: "changes", label: `${t("files.gitChanges") || "Changes"}${changedPaths.size ? ` (${changedPaths.size})` : ""}` },
                ]}
                data-testid="files-page-sidebar-mode"
                style={{ margin: "4px 6px" }}
              />
              <div style={{ flex: 1, minHeight: 0, overflow: "auto" }}>
                {sidebarView === "changes" ? (
                  <FilesGitChanges
                    token={token}
                    cwd={workspaceRoot}
                    refreshKey={refreshKey}
                    selectedPath={selectedFile ?? undefined}
                    onSelectPath={async (path) => {
                      // Same guard as the tree: opening a change must not silently
                      // discard the edited buffer.
                      const action = await handleUnsavedPrompt(path);
                      if (action === "cancel") return;
                      handleSelectFile(path);
                      setEditorView("git");
                    }}
                    onRefresh={() => setRefreshKey((k) => k + 1)}
                    t={t}
                  />
                ) : (
                  <FileTree
                    workspaceRoot={workspaceRoot}
                    selectedPath={selectedFile}
                    focusPath={selectedFile}
                    onSelectFile={handleSelectFile}
                    onUnsavedPrompt={handleUnsavedPrompt}
                    onNewFile={handleNewFile}
                    onNewFolder={handleNewFolder}
                    onDelete={handleDelete}
                    onRename={handleRename}
                    refreshKey={refreshKey}
                    searchQuery=""
                  />
                )}
              </div>
            </div>
            {/* Drag handle */}
            <div
              onMouseDown={handleDragStart}
              style={{
                width: 5,
                cursor: "col-resize",
                background: "transparent",
                flexShrink: 0,
                position: "relative",
                zIndex: 10,
              }}
              onMouseEnter={(e) => { (e.currentTarget as HTMLElement).style.background = "#1f6feb"; }}
              onMouseLeave={(e) => { (e.currentTarget as HTMLElement).style.background = "transparent"; }}
            />
          </div>
        )}

        {/* Editor / Git diff */}
        <div style={{ flex: 1, overflow: "hidden", minWidth: 0 }}>
          {selectedFile ? (
            editorView === "git" || isDeletedSelection ? (
              <GitDiffPane
                token={token}
                path={selectedFile}
                cwd={workspaceRoot}
                base={gitBase}
                onBaseChange={setGitBase}
                refreshKey={refreshKey}
                t={t}
              />
            ) : (
              <CodeEditor
                content={editorContent}
                filePath={selectedFile}
                readOnly={isLargeFile}
                onSave={handleSave}
                onChange={handleEditorChange}
              />
            )
          ) : (
            <div style={{ display: "flex", alignItems: "center", justifyContent: "center", height: "100%", color: "#8b949e" }}>
              Select a file from the tree to edit
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
