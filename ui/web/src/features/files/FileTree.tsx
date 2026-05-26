import { useCallback, useEffect, useState } from "react";
import { Tree, Spin, message, Button, Modal } from "antd";
import { FolderOutlined, FolderOpenOutlined, FileOutlined, PlusOutlined } from "@ant-design/icons";
import type { DataNode } from "antd/es/tree";
import { useSettingsStore } from "../../store/settings";
import { listFiles, type FileEntry } from "../../lib/api";

interface FileTreeProps {
  workspaceRoot: string;
  selectedPath: string | null;
  onSelectFile: (path: string) => void;
  onUnsavedPrompt: (path: string) => Promise<"save" | "discard" | "cancel">;
  onNewFile: (parentPath: string) => void;
  onNewFolder: (parentPath: string) => void;
  onDelete: (path: string) => void;
  onRename: (path: string) => void;
  refreshKey: number;
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
  onDelete,
  onRename,
  refreshKey,
}: FileTreeProps) {
  const token = useSettingsStore((s) => s.token);
  const [treeData, setTreeData] = useState<DataNode[]>([]);
  const [loading, setLoading] = useState(false);
  const [loadedDirs, setLoadedDirs] = useState<Set<string>>(new Set());

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
    const path = info.node.key as string;
    const isLeaf = info.node.isLeaf;
    Modal.confirm({
      title: `Actions for ${info.node.title}`,
      content: null,
      footer: null,
      children: (
        <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
          <Button block onClick={() => { Modal.destroyAll(); onNewFile(isLeaf ? parentPath(path) : path); }}>
            <PlusOutlined /> New File
          </Button>
          <Button block onClick={() => { Modal.destroyAll(); onNewFolder(isLeaf ? parentPath(path) : path); }}>
            <FolderOutlined /> New Folder
          </Button>
          <Button block onClick={() => { Modal.destroyAll(); onRename(path); }}>
            Rename
          </Button>
          <Button block danger onClick={() => { Modal.destroyAll(); onDelete(path); }}>
            Delete
          </Button>
        </div>
      ),
    });
  };

  return (
    <div style={{ overflow: "auto", height: "100%", padding: 8 }}>
      <Spin spinning={loading}>
        <Tree.DirectoryTree
          treeData={treeData}
          loadData={onLoadData}
          onSelect={handleSelect}
          onRightClick={handleRightClick}
          selectedKeys={selectedPath ? [selectedPath] : []}
          style={{ fontSize: 13 }}
        />
      </Spin>
    </div>
  );
}

function parentPath(path: string): string {
  const idx = path.lastIndexOf("/");
  return idx === -1 ? "." : path.substring(0, idx);
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
