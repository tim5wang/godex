import React, { useEffect, useMemo, useState } from "react";
import { Button, Input, Splitter, Typography, Space, Spin, Alert } from "antd";
import { PlusOutlined, UploadOutlined, SearchOutlined, FolderOpenOutlined } from "@ant-design/icons";
import FileTree from "./FileTree";
import CodeEditor from "./CodeEditor";
import { useLayoutStore } from "../../store/layout";
import { useSettingsStore } from "../../store/settings";
import { useI18n } from "../../i18n";
import { readFile } from "../../lib/api";

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

  const collapsed = !props.fillContainer && layoutCollapsed;
  const columnWidth = props.fillContainer ? "100%" : collapsed ? 40 : layoutWidth;
  const iconOnlyWidth = 40;

  useEffect(() => {
    if (!selectedPath) {
      setPreviewContent("");
      setPreviewError("");
      return;
    }
    let cancelled = false;
    setPreviewLoading(true);
    setPreviewError("");
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
            onClick={() => togglePanel("files")}
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
                onUnsavedPrompt={async () => "discard" as const}
                onNewFile={() => {}}
                onNewFolder={() => {}}
                onDelete={() => {}}
                onRename={() => {}}
              />
            </div>
          </Splitter.Panel>
          <Splitter.Panel>
            <div style={{ display: "flex", height: "100%", minHeight: 0, flexDirection: "column" }}>
              <div
                style={{
                  display: "flex",
                  alignItems: "center",
                  justifyContent: "space-between",
                  gap: 8,
                  borderBottom: "1px solid var(--border)",
                  padding: "6px 8px",
                }}
              >
                <Typography.Text data-testid="files-panel-preview-path" type="secondary" ellipsis>
                  {selectedPath ?? (t("files.noFileSelected") || "Select a file")}
                </Typography.Text>
                {props.onAttachFile ? (
                  <Button
                    size="small"
                    icon={<UploadOutlined />}
                    disabled={!selectedPath || previewLoading || !!previewError}
                    onClick={attachSelected}
                    data-testid="files-panel-attach-selected"
                  >
                    {t("files.attachToChat") || "Attach"}
                  </Button>
                ) : null}
              </div>
              <div data-testid="files-panel-preview" style={{ flex: 1, minHeight: 0, overflow: "auto" }}>
                {previewLoading ? (
                  <div style={{ display: "grid", height: "100%", placeItems: "center" }}>
                    <Spin size="small" />
                  </div>
                ) : previewError ? (
                  <Alert type="error" showIcon message={previewError} />
                ) : (
                  <CodeEditor content={previewContent} filePath={selectedPath ?? "(no file)"} readOnly />
                )}
              </div>
            </div>
          </Splitter.Panel>
        </Splitter>
      </div>
    </div>
  );
}
