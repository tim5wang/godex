import { useCallback, useEffect, useState } from "react";
import { Tree, Spin, message, Menu } from "antd";
import type { MenuProps } from "antd";
import { FolderOutlined, FolderOpenOutlined, FileOutlined, CopyOutlined, EditOutlined, DeleteOutlined, UploadOutlined } from "@ant-design/icons";
import type { DataNode } from "antd/es/tree";
import { useSettingsStore } from "../../store/settings";
import { listFiles, type FileEntry } from "../../lib/api";
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
}: FileTreeProps) {
  const token = useSettingsStore((s) => s.token);
  const [treeData, setTreeData] = useState<DataNode[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadedDirs, setLoadedDirs] = useState<Set<string>>(new Set());
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
    loadDir(".");
  }, [workspaceRoot, refreshKey, loadDir]);

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

  return (
    <div style={{ overflow: "auto", height: "100%", padding: 8 }}>
      <Spin spinning={loading}>
        <Tree.DirectoryTree
          treeData={searchQuery ? filterTree(treeData, searchQuery) : treeData}
          loadData={onLoadData}
          onSelect={handleSelect}
          onRightClick={handleRightClick}
          selectedKeys={selectedPath ? [selectedPath] : []}
          style={{ fontSize: 13 }}
        />
      </Spin>
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
