import { useCallback, useState, useRef, useEffect } from "react";
import { Input, Button, Modal, message, Tag, Select } from "antd";
import { SaveOutlined, FolderOpenOutlined, MenuFoldOutlined, MenuUnfoldOutlined } from "@ant-design/icons";
import { useSettingsStore } from "../../store/settings";
import { useQuery, useMutation, useQueryClient } from "@tanstack/react-query";
import { readFile, writeFile, deleteFile, mkdirFile, renameFile } from "../../lib/api";
import FileTree from "./FileTree";
import CodeEditor from "./CodeEditor";
import type { MetaResponse } from "../../lib/types";
import { getMeta } from "../../lib/api";

const MAX_FILE_SIZE = 500 * 1024;
const DEFAULT_SIDEBAR_WIDTH = 280;
const MIN_SIDEBAR_WIDTH = 150;
const MAX_SIDEBAR_WIDTH = 600;

function useWorkspaceMeta() {
  return useQuery({
    queryKey: ["workspaceMeta"],
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
  const token = useSettingsStore((s) => s.token);
  const queryClient = useQueryClient();
  const { data: meta } = useWorkspaceMeta();

  const [workspaceRoot, setWorkspaceRoot] = useState(meta?.workspace_dir ?? "");
  const [rootInput, setRootInput] = useState("");
  const [selectedFile, setSelectedFile] = useState<string | null>(null);
  const [editorContent, setEditorContent] = useState("");
  const [originalContent, setOriginalContent] = useState("");
  const [isDirty, setIsDirty] = useState(false);
  const [refreshKey, setRefreshKey] = useState(0);
  const [isLargeFile, setIsLargeFile] = useState(false);

  // Sidebar layout state
  const [sidebarWidth, setSidebarWidth] = useState(DEFAULT_SIDEBAR_WIDTH);
  const [sidebarCollapsed, setSidebarCollapsed] = useState(false);
  const dragRef = useRef<{ startX: number; startWidth: number } | null>(null);
  const prevWidthRef = useRef(DEFAULT_SIDEBAR_WIDTH);

  const readFileQuery = useQuery({
    queryKey: ["readFile", workspaceRoot, selectedFile],
    queryFn: async () => {
      if (!selectedFile) return null;
      const res = await readFile(token, selectedFile, workspaceRoot);
      const isLarge = res.size > MAX_FILE_SIZE;
      setIsLargeFile(isLarge);
      return res;
    },
    enabled: !!selectedFile && !!workspaceRoot,
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
    setSelectedFile(path);
    setIsDirty(false);
  }, []);

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
    setSelectedFile(null);
    setIsDirty(false);
    setRootInput("");
  }, [rootInput]);

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
        {recentWorkspaces.length > 0 && (
          <Select
            size="small"
            placeholder="Recent..."
            value={undefined}
            onChange={(v) => {
              if (!v) return;
              setRootInput(v);
              setWorkspaceRoot(v);
              saveRecentWorkspace(v);
              setSelectedFile(null);
              setIsDirty(false);
            }}
            options={recentWorkspaces.map((r) => ({ value: r, label: r }))}
            style={{ minWidth: 120 }}
            allowClear
          />
        )}
      </div>

      {/* File header */}
      {selectedFile && (
        <div style={{ padding: "4px 12px", borderBottom: "1px solid #30363d", display: "flex", alignItems: "center", gap: 8, flexShrink: 0, fontSize: 13 }}>
          <span style={{ color: "#e6edf3", flex: 1 }}>{selectedFile}</span>
          {isDirty && <Tag color="warning">Unsaved</Tag>}
          {isLargeFile && <Tag color="warning">Large File (Read Only)</Tag>}
          {!isLargeFile && (
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
            <div style={{ flex: 1, overflow: "hidden" }}>
              <FileTree
                workspaceRoot={workspaceRoot}
                selectedPath={selectedFile}
                onSelectFile={handleSelectFile}
                onUnsavedPrompt={handleUnsavedPrompt}
                onNewFile={handleNewFile}
                onNewFolder={handleNewFolder}
                onDelete={handleDelete}
                onRename={handleRename}
                refreshKey={refreshKey}
                searchQuery=""
              />
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

        {/* Editor */}
        <div style={{ flex: 1, overflow: "hidden" }}>
          {selectedFile ? (
            <CodeEditor
              content={editorContent}
              filePath={selectedFile}
              readOnly={isLargeFile}
              onSave={handleSave}
              onChange={handleEditorChange}
            />
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
