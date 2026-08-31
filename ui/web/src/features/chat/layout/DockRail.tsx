import type { ReactNode } from "react";
import { Button, Tooltip } from "antd";
import {
  CodeOutlined,
  DashboardOutlined,
  EyeOutlined,
  FolderOpenOutlined,
  SafetyCertificateOutlined,
} from "@ant-design/icons";
import { DOCK_TABS, type DockTab } from "./layoutStore";

export const DOCK_TAB_META: Record<DockTab, { label: string; icon: ReactNode }> = {
  files: { label: "Files", icon: <FolderOpenOutlined /> },
  terminal: { label: "Terminal", icon: <CodeOutlined /> },
  tasks: { label: "Tasks", icon: <SafetyCertificateOutlined /> },
  preview: { label: "Preview", icon: <EyeOutlined /> },
  status: { label: "Status", icon: <DashboardOutlined /> },
};

interface DockRailProps {
  collapsed: boolean;
  activeTab: DockTab;
  /** Badge counts per tab (e.g. pending approvals on tasks). */
  badges?: Partial<Record<DockTab, number>>;
  onSelectTab: (tab: DockTab) => void;
  /** Panel content for the active tab; rendered only when expanded. */
  children: ReactNode;
}

/** Chat right dock: a vertical icon tab strip (files / terminal / tasks /
 *  preview / status) with an expandable content pane. Selecting a tab while
 *  collapsed expands the dock; clicking the active tab collapses it. */
export function DockRail(props: DockRailProps) {
  const meta = DOCK_TAB_META[props.activeTab];
  return (
    <div className={`chat-v2-dock${props.collapsed ? " chat-v2-dock-collapsed" : ""}`} data-testid="chat-v2-dock">
      <div className="chat-v2-dock-strip">
        {DOCK_TABS.map((tab) => {
          const tabMeta = DOCK_TAB_META[tab];
          const active = !props.collapsed && tab === props.activeTab;
          const badge = props.badges?.[tab] ?? 0;
          return (
            <Tooltip key={tab} title={tabMeta.label} placement="left">
              <Button
                type="text"
                icon={tabMeta.icon}
                aria-label={tabMeta.label}
                data-testid={`chat-v2-dock-tab-${tab}`}
                className={`chat-v2-dock-tab${active ? " chat-v2-dock-tab-active" : ""}`}
                onClick={() => props.onSelectTab(tab)}
              >
                {badge > 0 ? <span className="chat-v2-dock-badge">{badge}</span> : null}
              </Button>
            </Tooltip>
          );
        })}
      </div>
      {!props.collapsed ? (
        <div className="chat-v2-dock-pane">
          <div className="chat-v2-dock-pane-header">{meta.label}</div>
          <div className="chat-v2-dock-pane-body">{props.children}</div>
        </div>
      ) : null}
    </div>
  );
}
