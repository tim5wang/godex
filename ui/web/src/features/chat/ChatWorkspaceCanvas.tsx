import type { ReactNode } from "react";
import { CenterGrid, type CenterGridRenderSlot } from "../../components/workspace/CenterGrid";
import { useLayoutStore } from "../../store/layout";
import { FilesPanel } from "../files/FilesPanel";

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
};

export function ChatWorkspaceCanvas(props: ChatWorkspaceCanvasProps) {
  const preset = useLayoutStore((state) => state.centerGridPreset);
  const occupancy = useLayoutStore((state) => state.centerGrid);
  const ratios = useLayoutStore((state) => state.centerGridRatios);

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
          <FilesPanel mode="dock" cwd={props.filesCwd ?? "."} />
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
      // v1 placeholder for the terminal panel (P3 lands the real xterm).
      return (
        <div
          data-testid="center-grid-cell-terminal-placeholder"
          className="center-grid-cell-terminal-placeholder"
          style={{
            height: "100%",
            display: "grid",
            placeItems: "center",
            background: "var(--panel)",
            color: "var(--muted)",
            fontSize: 12,
          }}
        >
          Terminal (P3)
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
    <CenterGrid
      preset={preset}
      occupancy={occupancy}
      outerSplit={ratios.outerSplit}
      innerTopSplit={ratios.innerTopSplit}
      innerBottomSplit={ratios.innerBottomSplit}
      renderSlot={renderSlot}
    />
  );
}
