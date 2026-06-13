import { useEffect, useRef, useState, type ReactNode } from "react";
import { Button } from "antd";
import { FolderOpenOutlined } from "@ant-design/icons";
import { CenterGrid, panelLabel, slotLabel, type CenterGridRenderSlot } from "../../components/workspace/CenterGrid";
import { useLayoutStore, type GridOccupancy, type PanelKey } from "../../store/layout";
import { FilesPanel } from "../files/FilesPanel";
import { TerminalPanel } from "../terminal/TerminalPanel";

// P2 / T6b (SPEC §3.2 + §4.1 + §4.3): ChatWorkspaceCanvas is the
// thin shell that ChatPage mounts inside its <section className="chat-main">.
// It pulls the current grid preset + occupancy from the layout store,
// wires the default slot mapping (files panel in the topLeft when the
// preset is "topFilesChat_bottomTerminal"), and hands the rest of the
// cells over to the parent's `renderCenter` prop (the chat
// MessageFeed + banners + Composer stack).
//
// Why a wrapper instead of editing ChatPage directly?
//   1. ChatPage is 154KB of stateful rendering (SPEC §4.7 — do not
//      rewrite 154KB files). A thin shell keeps the diff minimal.
//   2. The shell owns the slot → panel mapping. The chat feed remains a
//      pure prop on the shell, so the same ChatWorkspaceCanvas could be
//      reused by a future "workbench" route with a different center
//      content.
//   3. The default grid preset is read once at mount time; users who
//      want to switch presets do so via the store's setGridPreset
//      action, not via a prop on this shell.

export type ChatWorkspaceCanvasProps = {
  /** The chat workspace's main content (MessageFeed + banners + Composer). */
  renderCenter: () => ReactNode;
  /** Cwd passed to the embedded <FilesPanel mode="dock"> when mounted. */
  filesCwd?: string;
  /** Optional: render the terminal panel when the bottom row holds "terminal". */
  renderTerminal?: () => ReactNode;
  /** Optional: render the tasks panel (used by future presets). */
  renderTasks?: () => ReactNode;
  /** Attach a workspace file from the files grid panel to the chat composer. */
  onAttachFile?: (file: File) => void;
};

export function ChatWorkspaceCanvas(props: ChatWorkspaceCanvasProps) {
  const preset = useLayoutStore((state) => state.centerGridPreset);
  const occupancy = useLayoutStore((state) => state.centerGrid);
  const collapsedPanels = useLayoutStore((state) => state.centerGridCollapsedPanels);
  const ratios = useLayoutStore((state) => state.centerGridRatios);
  const movePanelToGrid = useLayoutStore((state) => state.movePanelToGrid);
  const swapGridSlots = useLayoutStore((state) => state.swapGridSlots);
  const setCenterGridPanelCollapsed = useLayoutStore((state) => state.setCenterGridPanelCollapsed);
  const setGridRatio = useLayoutStore((state) => state.setGridRatio);
  const toggleGridRowCollapse = useLayoutStore((state) => state.toggleGridRowCollapse);
  const [feedback, setFeedback] = useState("");
  const feedbackTimerRef = useRef<number | null>(null);

  useEffect(() => {
    return () => {
      if (feedbackTimerRef.current !== null) {
        window.clearTimeout(feedbackTimerRef.current);
      }
    };
  }, []);

  const announceFeedback = (message: string) => {
    setFeedback(message);
    if (feedbackTimerRef.current !== null) {
      window.clearTimeout(feedbackTimerRef.current);
    }
    feedbackTimerRef.current = window.setTimeout(() => {
      setFeedback("");
      feedbackTimerRef.current = null;
    }, 1800);
  };

  const visibleOccupancy = hideCollapsedPanels(occupancy, collapsedPanels);
  const bookmarkPanels = panelsInOccupancy(occupancy).filter((panel) => collapsedPanels[panel]);

  const renderSlot: CenterGridRenderSlot = (panel) => {
    if (panel === null) {
      return <div data-testid="center-grid-empty-cell" className="center-grid-empty" />;
    }
    if (panel === "chat") {
      return (
        <div data-testid="center-grid-cell-chat" className="center-grid-cell-chat" style={{ height: "100%", minHeight: 0 }}>
          {props.renderCenter()}
        </div>
      );
    }
    if (panel === "files") {
      return (
        <div data-testid="center-grid-cell-files" className="center-grid-cell-files" style={{ height: "100%", minHeight: 0 }}>
          <FilesPanel mode="dock" cwd={props.filesCwd ?? "."} fillContainer onAttachFile={props.onAttachFile} />
        </div>
      );
    }
    if (panel === "terminal") {
      if (props.renderTerminal) {
        return (
          <div data-testid="center-grid-cell-terminal" className="center-grid-cell-terminal" style={{ height: "100%", minHeight: 0 }}>
            {props.renderTerminal()}
          </div>
        );
      }
      // P3 / T7 (SPEC §4.4 v1.0 polling fallback): when the parent
      // does not pass a custom renderTerminal, mount the default
      // <TerminalPanel> which drives a mock PTY on a polling
      // interval. v2.0 will swap the mock client for the real Go
      // PTY (internal/acp CreateTerminal / TerminalOutput) without
      // touching this slot.
      return (
        <div
          data-testid="center-grid-cell-terminal-default"
          className="center-grid-cell-terminal-default"
          style={{ height: "100%", minHeight: 0 }}
        >
          <TerminalPanel />
        </div>
      );
    }
    if (panel === "tasks") {
      if (props.renderTasks) {
        return (
          <div data-testid="center-grid-cell-tasks" className="center-grid-cell-tasks" style={{ height: "100%", minHeight: 0 }}>
            {props.renderTasks()}
          </div>
        );
      }
      return <div data-testid="center-grid-cell-tasks-placeholder" />;
    }
    // Unknown / unsupported panel (e.g. "drawer" or "appNav" within the
    // grid): the outer Layout places those in the sider columns, not in
    // the center grid. Render a labelled placeholder so the test surface
    // stays explicit.
    return (
      <div
        data-testid={`center-grid-cell-${panel}-unsupported`}
        className="center-grid-cell-unsupported"
        style={{ height: "100%" }}
      />
    );
  };

  return (
    <div className="center-grid-shell">
      <div className="center-grid-bookmarks" data-testid="center-grid-bookmarks">
        {bookmarkPanels.map((panel) => (
          <Button
            key={panel}
            size="small"
            icon={<FolderOpenOutlined />}
            data-testid={`center-grid-bookmark-${panel}`}
            onClick={() => setCenterGridPanelCollapsed(panel, false)}
          >
            {panelLabel(panel)}
          </Button>
        ))}
      </div>
      <CenterGrid
        preset={preset}
        occupancy={visibleOccupancy}
        outerSplit={ratios.outerSplit}
        innerTopSplit={ratios.innerTopSplit}
        innerBottomSplit={ratios.innerBottomSplit}
        row0Split={ratios.row0Split}
        row1Split={ratios.row1Split}
        col0Split={ratios.col0Split}
        col1Split={ratios.col1Split}
        renderSlot={renderSlot}
        onGridRatiosChange={(nextRatios) => {
          for (const [key, value] of Object.entries(nextRatios)) {
            if (typeof value === "number") {
              setGridRatio(key as keyof typeof ratios, value);
            }
          }
        }}
        onGridRowCollapseToggle={toggleGridRowCollapse}
        onPanelCollapse={(panel) => {
          setCenterGridPanelCollapsed(panel, true);
          announceFeedback(`${panelLabel(panel)} collapsed to bookmark`);
        }}
        onPanelMove={(panel, from, to, action) => {
          if (action === "move") {
            movePanelToGrid(panel, to);
            announceFeedback(`${panelLabel(panel)} moved to ${slotLabel(to)}`);
            return;
          }
          const targetPanel = occupancy[to];
          swapGridSlots(from, to);
          announceFeedback(`${panelLabel(panel)} swapped with ${targetPanel ? panelLabel(targetPanel) : slotLabel(to)}`);
        }}
      />
      {feedback ? (
        <div className="center-grid-action-feedback" data-testid="center-grid-action-feedback" role="status" aria-live="polite">
          {feedback}
        </div>
      ) : null}
    </div>
  );
}

function hideCollapsedPanels(
  occupancy: GridOccupancy,
  collapsedPanels: Partial<Record<PanelKey, boolean>>,
): GridOccupancy {
  const next: GridOccupancy = { ...occupancy };
  for (const key of Object.keys(next) as Array<keyof GridOccupancy>) {
    const value = next[key];
    if (typeof value === "string" && collapsedPanels[value]) {
      (next as Record<string, unknown>)[key] = null;
    }
  }
  return next;
}

function panelsInOccupancy(occupancy: GridOccupancy): PanelKey[] {
  const seen = new Set<PanelKey>();
  for (const value of Object.values(occupancy)) {
    if (typeof value === "string") {
      seen.add(value);
    }
  }
  return [...seen];
}
