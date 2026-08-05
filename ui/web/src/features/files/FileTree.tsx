import { useCallback, useEffect, useState } from "react";
import { Tree, Spin, message, Menu } from "antd";
import type { MenuProps } from "antd";
import { FolderOutlined, FolderOpenOutlined, FileOutlined, CopyOutlined, EditOutlined, DeleteOutlined, UploadOutlined } from "@ant-design/icons";
import type { DataNode } from "antd/es/tree";
import { useSettingsStore } from "../../store/settings";
import { listFiles, type FileEntry, type FileSearchResult } from "../../lib/api";
import { writeClipboardText } from "../../lib/clipboard";

interface FileTreeProps {
  workspaceRoot: string;
  selectedPath: string | null;
  onSelectFile: (path: string) => void;
  onUnsavedPrompt: (path: string) => Promise<"save" | "discard" | "cancel">;
  onNewFile: (targetDir: string) => void;
  onNewFolder: (targetDir: string) => void;
  onUploadTo?: (targetDir: string) => void;
  onDelete: (path: string) => void;
  onRename: (path: string) => void;
  refreshKey: number;
  searchQuery: string;
  searchResults?: FileSearchResult[] | null;
  searchLoading?: boolean;
  /** When set, the tree auto-expands the ancestor chain and highlights the file. */
  focusPath?: string | null;
}

function buildTreeNodes(entries: FileEntry[], parentPath: string): DataNode[] {
  const dirs: FileEntry[] = [];
  const files: FileEntry[] = [];
  for (const e of entries) {
    if (e.isDir) dirs.push(e);
    else files.push(e);
  }
  const nodes: DataNode[] = [];
  for (const d of dirs) {
    const fullPath = parentPath === "." ? d.name : `${parentPath}/${d.name}`;
    nodes.push({
      title: d.name,
      key: fullPath,
      icon: ({ expanded }) => (expanded ? <FolderOpenOutlined /> : <FolderOutlined />),
      isLeaf: false,
    });
  }
  for (const f of files) {
    const fullPath = parentPath === "." ? f.name : `${parentPath}/${f.name}`;
    nodes.push({
      title: f.name,
      key: fullPath,
      icon: <FileOutlined />,
      isLeaf: true,
    });
  }
  return nodes;
}

export default function FileTree({
  workspaceRoot,
  selectedPath,
  onSelectFile,
  onUnsavedPrompt,
  onNewFile,
  onNewFolder,
  onUploadTo,
  onDelete,
  onRename,
  refreshKey,
  searchQuery,
  searchResults,
  searchLoading,
  focusPath,
}: FileTreeProps) {
  const token = useSettingsStore((s) => s.token);
  const [treeData, setTreeData] = useState<DataNode[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadedDirs, setLoadedDirs] = useState<Set<string>>(new Set());
  const [expandedKeys, setExpandedKeys] = useState<React.Key[]>([]);
  const [contextMenu, setContextMenu] = useState<{ open: boolean; x: number; y: number; path: string; title: string; isLeaf: boolean }>({ open: false, x: 0, y: 0, path: "", title: "", isLeaf: true });

  const loadDir = useCallback(
    async (dir: string) => {
      setLoading(true);
      try {
        const res = await listFiles(token, dir, workspaceRoot);
        setLoadedDirs((prev) => new Set(prev).add(dir));
        const nodes = buildTreeNodes(res.items, dir);
        setTreeData((prev) => {
          if (dir === ".") return nodes;
          return updateTreeNodes(prev, dir, nodes);
        });
      } catch {
        message.error("Failed to load directory");
      } finally {
        setLoading(false);
      }
    },
    [token, workspaceRoot],
  );

  useEffect(() => {
    setLoadedDirs(new Set());
    setTreeData([]);
    setExpandedKeys([]);
    loadDir(".");
  }, [workspaceRoot, refreshKey, loadDir]);

  // Auto-expand the ancestor chain when the parent asks us to focus a file.
  useEffect(() => {
    if (!focusPath) return;
    const ancestors = ancestorDirs(focusPath);
    if (ancestors.length === 0) return;
    let cancelled = false;
    (async () => {
      // Ensure each ancestor dir is loaded (lazy tree), then expand them.
      for (const dir of ancestors) {
        if (cancelled) return;
        if (!loadedDirs.has(dir)) {
          await loadDir(dir);
        }
      }
      if (cancelled) return;
      setExpandedKeys((prev) => [...new Set([...prev, ...ancestors])]);
    })();
    return () => {
      cancelled = true;
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [focusPath]);

  const onLoadData = async (node: DataNode) => {
    const key = node.key as string;
    if (loadedDirs.has(key)) return;
    await loadDir(key);
  };

  const handleSelect = async (keys: React.Key[]) => {
    if (keys.length === 0) return;
    const path = keys[0] as string;
    const node = findNode(treeData, path);
    if (node?.isLeaf) {
      if (selectedPath && selectedPath !== path) {
        const action = await onUnsavedPrompt(path);
        if (action === "cancel") return;
      }
      onSelectFile(path);
    }
  };

  const handleRightClick = (info: any) => {
    setContextMenu({
      open: true,
      x: info.event.clientX,
      y: info.event.clientY,
      path: info.node.key as string,
      title: info.node.title as string,
      isLeaf: info.node.isLeaf as boolean,
    });
  };

  const closeContextMenu = () => setContextMenu((prev) => ({ ...prev, open: false }));

  const targetDir = contextMenu.isLeaf ? (contextMenu.path.substring(0, contextMenu.path.lastIndexOf("/") || 0) || ".") : contextMenu.path;

  const contextMenuItems: MenuProps["items"] = [
    {
      key: "new-file",
      label: "New file",
      icon: <FileOutlined />,
      onClick: () => { closeContextMenu(); onNewFile(targetDir); },
    },
    {
      key: "new-folder",
      label: "New folder",
      icon: <FolderOutlined />,
      onClick: () => { closeContextMenu(); onNewFolder(targetDir); },
    },
    ...(onUploadTo ? [{
      key: "upload",
      label: "Upload file",
      icon: <UploadOutlined />,
      onClick: () => { closeContextMenu(); onUploadTo(targetDir); },
    }] : []),
    { type: "divider" },
    {
      key: "copy-relative",
      label: "Copy relative path",
      icon: <CopyOutlined />,
      onClick: () => { closeContextMenu(); copyToClipboard(contextMenu.path); message.success("Relative path copied"); },
    },
    {
      key: "copy-absolute",
      label: "Copy absolute path",
      icon: <CopyOutlined />,
      onClick: () => { closeContextMenu(); copyToClipboard(workspaceRoot + "/" + contextMenu.path); message.success("Absolute path copied"); },
    },
    { type: "divider" },
    {
      key: "rename",
      label: "Rename",
      icon: <EditOutlined />,
      onClick: () => { closeContextMenu(); onRename(contextMenu.path); },
    },
    {
      key: "delete",
      label: "Delete",
      icon: <DeleteOutlined />,
      danger: true,
      onClick: () => { closeContextMenu(); onDelete(contextMenu.path); },
    },
  ];

  const isSearching = searchQuery.trim().length > 0;

  return (
    <div style={{ overflow: "auto", height: "100%", padding: 8 }}>
      {searchResults != null ? (
        <SearchResultsView
          results={searchResults}
          loading={searchLoading ?? false}
          selectedPath={selectedPath}
          onSelect={onSelectFile}
          workspaceRoot={workspaceRoot}
          onContextMenu={(path, title, isLeaf, x, y) => setContextMenu({ open: true, x, y, path, title, isLeaf })}
        />
      ) : (
        <Spin spinning={loading}>
          <Tree.DirectoryTree
            treeData={searchQuery ? filterTree(treeData, searchQuery) : treeData}
            loadData={onLoadData}
            onSelect={handleSelect}
            onRightClick={handleRightClick}
            selectedKeys={selectedPath ? [selectedPath] : []}
            expandedKeys={expandedKeys}
            onExpand={(keys) => setExpandedKeys(keys)}
            style={{ fontSize: 13 }}
          />
        </Spin>
      )}
      {contextMenu.open ? (
        <>
          {/* Backdrop to close menu on any click */}
          <div
            onClick={closeContextMenu}
            onContextMenu={(e) => { e.preventDefault(); closeContextMenu(); }}
            style={{ position: "fixed", inset: 0, zIndex: 999 }}
          />
          <div style={{ position: "fixed", left: contextMenu.x, top: contextMenu.y, zIndex: 1000 }}>
            <Menu
              items={contextMenuItems}
              style={{ minWidth: 200 }}
            />
          </div>
        </>
      ) : null}
    </div>
  );
}

function parentPath(path: string): string {
  const idx = path.lastIndexOf("/");
  return idx === -1 ? "." : path.substring(0, idx);
}

/** Ancestor directories of a file path, nearest-to-root order (e.g. "a/b/c.go" → ["a", "a/b"]). */
function ancestorDirs(path: string): string[] {
  const dirs: string[] = [];
  let cur = parentPath(path);
  while (cur && cur !== ".") {
    dirs.unshift(cur);
    const next = parentPath(cur);
    if (next === cur) break;
    cur = next;
  }
  return dirs;
}

async function copyToClipboard(text: string) {
  try {
    await writeClipboardText(text);
  } catch {
    // Fallback: use execCommand
    const el = document.createElement("textarea");
    el.value = text;
    el.style.position = "fixed";
    el.style.opacity = "0";
    document.body.appendChild(el);
    el.select();
    document.execCommand("copy");
    document.body.removeChild(el);
  }
}

function filterTree(nodes: DataNode[], query: string): DataNode[] {
  if (!query) return nodes;
  const q = query.toLowerCase();
  return nodes.reduce<DataNode[]>((acc, node) => {
    const title = String(node.title).toLowerCase();
    const children = node.children ? filterTree(node.children, query) : [];
    if (title.includes(q) || children.length > 0) {
      acc.push({ ...node, children: children.length > 0 ? children : node.children });
    }
    return acc;
  }, []);
}

function SearchResultsView(props: {
  results: FileSearchResult[];
  loading: boolean;
  selectedPath: string | null;
  onSelect: (path: string) => void;
  workspaceRoot: string;
  onContextMenu: (path: string, title: string, isLeaf: boolean, x: number, y: number) => void;
}) {
  if (props.loading) {
    return <div style={{ padding: 16, textAlign: "center" }}><Spin size="small" /></div>;
  }
  if (props.results.length === 0) {
    return <div style={{ padding: 16, color: "var(--godex-muted)", fontSize: 13 }}>No results found.</div>;
  }
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 1 }}>
      {props.results.map((r) => (
        <div
          key={r.path}
          onClick={() => props.onSelect(r.path)}
          onContextMenu={(e) => {
            e.preventDefault();
            props.onContextMenu(r.path, r.path.split("/").pop() || r.path, !r.isDir, e.clientX, e.clientY);
          }}
          style={{
            padding: "3px 8px",
            cursor: "pointer",
            borderRadius: 4,
            fontSize: 13,
            background: props.selectedPath === r.path ? "var(--godex-accent-muted)" : "transparent",
            display: "flex",
            alignItems: "center",
            gap: 6,
          }}
          onMouseEnter={(e) => {
            if (props.selectedPath !== r.path) (e.target as HTMLElement).style.background = "var(--godex-hover)";
          }}
          onMouseLeave={(e) => {
            if (props.selectedPath !== r.path) (e.target as HTMLElement).style.background = "transparent";
          }}
        >
          {r.isDir ? <FolderOutlined style={{ color: "#60a5fa" }} /> : <FileOutlined style={{ color: "#94a3b8" }} />}
          <span style={{ flex: 1, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }}>{r.path}</span>
        </div>
      ))}
    </div>
  );
}

function findNode(nodes: DataNode[], key: string): DataNode | null {
  for (const node of nodes) {
    if (node.key === key) return node;
    if (node.children) {
      const found = findNode(node.children, key);
      if (found) return found;
    }
  }
  return null;
}

function updateTreeNodes(nodes: DataNode[], parentPath: string, children: DataNode[]): DataNode[] {
  return nodes.map((node) => {
    if (node.key === parentPath) {
      return { ...node, children };
    }
    if (node.children) {
      return { ...node, children: updateTreeNodes(node.children, parentPath, children) };
    }
    return node;
  });
}
