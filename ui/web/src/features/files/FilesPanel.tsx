import React, { useMemo, useState } from "react";
import { Button, Input, Splitter, Typography, Space } from "antd";
import { PlusOutlined, UploadOutlined, SearchOutlined, FolderOpenOutlined } from "@ant-design/icons";
import FileTree from "./FileTree";
import CodeEditor from "./CodeEditor";
import { useLayoutStore } from "../../store/layout";
import { useI18n } from "../../i18n";

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
  const layoutCollapsed = useLayoutStore((state) => state.panels.files.collapsed);
  const layoutWidth = useLayoutStore((state) => state.panels.files.width ?? 320);
  const setWidth = useLayoutStore((state) => state.setWidth);
  const [search, setSearch] = useState("");
  const [selectedPath, setSelectedPath] = useState<string | undefined>(props.selectedPath);

  const columnWidth = layoutCollapsed ? 40 : layoutWidth;
  const iconOnlyWidth = 40;

  // The 40px collapsed strip shows the bare expand affordance.
  if (layoutCollapsed) {
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
          onClick={() => setWidth("files", Math.max(320, layoutWidth))}
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
        borderRight: "1px solid var(--border)",
      }}
    >
      <div
        style={{
          padding: "8px 12px",
          borderBottom: "1px solid var(--border)",
          display: "flex",
          flexDirection: "column",
          gap: 8,
        }}
      >
        <Space size={6} wrap>
          <Button size="small" icon={<PlusOutlined />} aria-label="New folder">
            {t("files.newFolder") || "New folder"}
          </Button>
          <Button size="small" icon={<UploadOutlined />} aria-label="Upload">
            {t("files.upload") || "Upload"}
          </Button>
          <Button
            size="small"
            type="text"
            aria-label={t("files.collapse") || "Collapse files"}
            title={t("files.collapse") || "Collapse files"}
            onClick={() => setWidth("files", 40)}
            data-testid="files-panel-collapse"
          >
            «
          </Button>
        </Space>
        <Input
          allowClear
          size="small"
          prefix={<SearchOutlined />}
          placeholder={t("files.searchPlaceholder") || "Search files"}
          value={search}
          onChange={(event) => setSearch(event.target.value)}
        />
        <Typography.Text type="secondary" ellipsis style={{ fontSize: 11 }}>
          {props.cwd ?? "."}
        </Typography.Text>
      </div>
      <div style={{ flex: 1, minHeight: 0 }}>
        <Splitter layout="vertical" style={{ height: "100%" }}>
          <Splitter.Panel defaultSize="40%" min="20%">
            <div style={{ height: "100%", overflow: "auto" }}>
              <FileTree
                workspaceRoot={props.cwd ?? "."}
                selectedPath={selectedPath ?? null}
                refreshKey={0}
                onSelectFile={(path) => {
                  setSelectedPath(path);
                  props.onSelect?.(path);
                }}
                onUnsavedPrompt={async () => "cancel" as const}
                onNewFile={() => {}}
                onNewFolder={() => {}}
                onDelete={() => {}}
                onRename={() => {}}
              />
            </div>
          </Splitter.Panel>
          <Splitter.Panel>
            <div style={{ height: "100%", overflow: "auto" }}>
              <CodeEditor content="" filePath={selectedPath ?? "(no file)"} readOnly />
            </div>
          </Splitter.Panel>
        </Splitter>
      </div>
    </div>
  );
}
