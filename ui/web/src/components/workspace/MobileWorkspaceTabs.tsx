import { useMemo, type ReactNode } from "react";
import { Grid, Segmented } from "antd";
import {
  selectMobileWorkspaceTabs,
  useLayoutStore,
  type MobileTab,
  type MobileWorkspaceTabsSnapshot,
} from "../../store/layout";
import { useI18n } from "../../i18n";
import { FilesPanel } from "../../features/files/FilesPanel";
import { TerminalPanel } from "../../features/terminal/TerminalPanel";

// P0-D / T9 + P2 / T6d (SPEC §3.3 + §3.4): Secondary workspace tab bar
// for screens < 1024px. Shows 5 tabs: chat | terminal | files | drawer
// | tasks. Active tab is driven by useLayoutStore.mobileActiveTab. On
// >= 1024px the component returns null — primary navigation there is
// the 2x2 grid preset (CenterGrid) and AppNav is unchanged.
//
// In addition to the tab bar, the component renders the active tab's
// content below the bar:
//   * files  → <FilesPanel mode="dock" cwd={filesCwd ?? "."} /> (P2 / T6)
//   * chat   → renderCenter() (MessageFeed + banners + Composer)
//   * terminal | drawer | tasks → labelled placeholder
//     (P3 lands the real xterm; drawer + tasks are the App.tsx Drawer
//     on PC — they are non-essential on mobile and the placeholder keeps
//     the tab non-empty and discoverable)
//
// The component is intentionally thin: all state is in the store, the
// selector is the contract. Visibility is decided by the renderer via
// Grid.useBreakpoint; that is a UI concern, not store concern (so we
// don't push viewport state into the layout store).

export type MobileWorkspaceTabsProps = {
  /** The chat workspace's main content (MessageFeed + banners + Composer). */
  renderCenter?: () => ReactNode;
  /** Cwd passed to the embedded <FilesPanel mode="dock"> when mounted. */
  filesCwd?: string;
};

export function MobileWorkspaceTabs(props: MobileWorkspaceTabsProps = {}) {
  const screens = Grid.useBreakpoint();
  // Calling these as separate selectors keeps the store granular so
  // unrelated panel changes don't re-render this component.
  const snap = useLayoutStore(selectMobileWorkspaceTabs);
  const setMobileActiveTab = useLayoutStore((state) => state.setMobileActiveTab);
  const { t } = useI18n();
  const options = useMemo(
    () =>
      snap.tabs.map((tab) => ({
        label: t(tab.i18nKey),
        value: tab.key,
      })),
    [snap.tabs, t],
  );

  if (screens.lg) {
    return null;
  }

  return (
    <div
      data-testid="mobile-workspace"
      data-active={snap.active}
      className="mobile-workspace"
      style={{ display: "flex", flexDirection: "column", minHeight: 0, height: "100%" }}
    >
      <div
        data-testid="mobile-workspace-tabs"
        data-active={snap.active}
        className="border-b border-[color:var(--border)] bg-[color:var(--panel)] px-3 py-2"
      >
        <Segmented<MobileWorkspaceTabsSnapshot["active"]>
          block
          size="middle"
          value={snap.active}
          onChange={(value) => setMobileActiveTab(value)}
          options={options}
        />
      </div>
      <div
        data-testid="mobile-workspace-content"
        data-active={snap.active}
        className="mobile-workspace-content"
        style={{ flex: 1, minHeight: 0, overflow: "auto" }}
      >
        {renderActive(snap.active, props)}
      </div>
    </div>
  );
}

function renderActive(active: MobileTab, props: MobileWorkspaceTabsProps): ReactNode {
  if (active === "files") {
    return (
      <div
        data-testid="mobile-workspace-files"
        data-tab="files"
        className="mobile-workspace-files"
        style={{ height: "100%" }}
      >
        <FilesPanel mode="dock" cwd={props.filesCwd ?? "."} />
      </div>
    );
  }
  if (active === "chat") {
    return (
      <div
        data-testid="mobile-workspace-chat"
        data-tab="chat"
        className="mobile-workspace-chat"
        style={{ height: "100%" }}
      >
        {props.renderCenter ? props.renderCenter() : null}
      </div>
    );
  }
  if (active === "terminal") {
    // P3 / T7 (SPEC §4.4 v1.0): mount the same <TerminalPanel> the
    // PC center grid uses. Mobile users get a full-width xterm;
    // the polling-fallback mock PTY is the same code path on
    // both surfaces.
    return (
      <div
        data-testid="mobile-workspace-terminal"
        data-tab="terminal"
        className="mobile-workspace-terminal"
        style={{ height: "100%" }}
      >
        <TerminalPanel />
      </div>
    );
  }
  if (active === "drawer") {
    return <PlaceholderPanel tabKey="drawer" label="Drawer" />;
  }
  return <PlaceholderPanel tabKey="tasks" label="Task Center" />;
}

function PlaceholderPanel(props: { tabKey: string; label: string }) {
  return (
    <div
      data-testid={`mobile-workspace-${props.tabKey}-placeholder`}
      data-tab={props.tabKey}
      className="mobile-workspace-placeholder"
      style={{
        height: "100%",
        display: "grid",
        placeItems: "center",
        background: "var(--panel)",
        color: "var(--muted)",
        fontSize: 12,
        padding: 24,
      }}
    >
      {props.label}
    </div>
  );
}
