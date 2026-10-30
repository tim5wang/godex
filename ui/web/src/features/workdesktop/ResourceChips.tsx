import { useState, type ReactNode } from "react";
import { Button, Tag, Typography } from "antd";
import {
  AppstoreOutlined,
  DashboardOutlined,
  DatabaseOutlined,
  FileTextOutlined,
  FolderOutlined,
  GlobalOutlined,
  LinkOutlined,
  RobotOutlined,
  ThunderboltOutlined,
} from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { InlineEdit } from "./workDesktopInline";
import { formatTimestamp, isExternalLink } from "./workDesktopView";
import type { WorkResource } from "../../lib/apiWorkContext";

/**
 * Per-kind icon and colour. A resource's category is visible at a glance in the
 * chip waterfall without reading the label, which is the point of the bubbles.
 */
const KIND_META: Record<string, { icon: ReactNode; color: string }> = {
  web: { icon: <GlobalOutlined />, color: "#2563eb" },
  directory: { icon: <FolderOutlined />, color: "#d97706" },
  repository: { icon: <DatabaseOutlined />, color: "#7c3aed" },
  note: { icon: <FileTextOutlined />, color: "#0891b2" },
  document: { icon: <FileTextOutlined />, color: "#0f766e" },
  dashboard: { icon: <DashboardOutlined />, color: "#db2777" },
  service: { icon: <ThunderboltOutlined />, color: "#ca8a04" },
  model: { icon: <RobotOutlined />, color: "#4f46e5" },
  artifact: { icon: <AppstoreOutlined />, color: "#64748b" },
};
const DEFAULT_KIND = { icon: <LinkOutlined />, color: "#64748b" };

export function kindMeta(kind: string) {
  return KIND_META[kind] ?? DEFAULT_KIND;
}

/**
 * ResourceChips renders resources as a compact chip waterfall: one bubble per
 * resource, colour/icon-coded by kind, expanding IN PLACE on click instead of
 * opening a drawer. Fields are edited in place; an external locator opens in a
 * new tab and a directory locator can jump to the desktop's file browser.
 */
export function ResourceChips(props: {
  resources: WorkResource[];
  readOnly: boolean;
  /** Merge a partial change into an existing resource (upsert by id). */
  onUpdate: (
    resource: WorkResource,
    patch: Partial<Pick<WorkResource, "title" | "kind" | "locator" | "purpose">>,
  ) => Promise<unknown> | unknown;
  onRemove: (resource: WorkResource) => void;
  onOpenFiles?: (resource: WorkResource) => void;
}) {
  const { resources, readOnly, onUpdate, onRemove, onOpenFiles } = props;
  const { t } = useI18n();
  const tr = (key: string) => t(`workDesktop.${key}`);
  const [expanded, setExpanded] = useState<string | null>(null);

  return (
    <div className="wd-chips" role="list">
      {resources.map((resource) => {
        const meta = kindMeta(resource.kind);
        const open = expanded === resource.id;
        const shared = (resource.context_ids?.length ?? 0) > 1;
        return (
          <div
            key={resource.id}
            role="listitem"
            className={`wd-chip ${open ? "wd-chip-open" : ""}`}
            style={{ borderColor: open ? meta.color : undefined }}
          >
            <button
              type="button"
              className="wd-chip-head"
              aria-expanded={open}
              aria-label={`${resource.title} · ${resource.kind}`}
              onClick={() => setExpanded(open ? null : resource.id)}
            >
              <span className="wd-chip-icon" style={{ color: meta.color, background: `${meta.color}1a` }} aria-hidden="true">
                {meta.icon}
              </span>
              <span className="wd-chip-title">{resource.title}</span>
              {shared ? <Tag className="wd-chip-tag">{tr("shared")}</Tag> : null}
            </button>
            {open ? (
              <div className="wd-chip-body">
                <div className="wd-chip-field">
                  <span className="wd-muted">{tr("resourceTitle")}</span>
                  <InlineEdit
                    ariaLabel={`${tr("resourceTitle")}: ${resource.title}`}
                    value={resource.title}
                    disabled={readOnly}
                    required
                    onSave={(next) => onUpdate(resource, { title: next })}
                  />
                </div>
                <div className="wd-chip-field">
                  <span className="wd-muted">{tr("resourceKindLabel")}</span>
                  <span style={{ color: meta.color }}>
                    {meta.icon} {tr(`kind.${resource.kind}`)}
                  </span>
                </div>
                <div className="wd-chip-field">
                  <span className="wd-muted">{tr("resourceLocator")}</span>
                  {isExternalLink(resource.locator) ? (
                    <a href={resource.locator} target="_blank" rel="noreferrer" className="wd-chip-link">
                      {resource.locator} ↗
                    </a>
                  ) : (
                    <InlineEdit
                      ariaLabel={`${tr("resourceLocator")}: ${resource.title}`}
                      value={resource.locator}
                      disabled={readOnly}
                      required
                      onSave={(next) => onUpdate(resource, { locator: next })}
                    />
                  )}
                </div>
                <div className="wd-chip-field">
                  <span className="wd-muted">{tr("resourcePurpose")}</span>
                  <InlineEdit
                    ariaLabel={`${tr("resourcePurpose")}: ${resource.title}`}
                    value={resource.purpose}
                    placeholder={tr("inlineHint")}
                    disabled={readOnly}
                    onSave={(next) => onUpdate(resource, { purpose: next })}
                  />
                </div>
                <div className="wd-muted wd-chip-created">
                  {tr("resourceCreated")} · {formatTimestamp(resource.created_at)}
                </div>
                <div className="wd-chip-actions">
                  {resource.kind === "directory" && onOpenFiles ? (
                    <Button size="small" onClick={() => onOpenFiles(resource)}>
                      {tr("openInFiles")}
                    </Button>
                  ) : null}
                  {isExternalLink(resource.locator) ? (
                    <Button size="small" type="link" href={resource.locator} target="_blank" rel="noreferrer">
                      {tr("openExternal")}
                    </Button>
                  ) : null}
                  {!readOnly ? (
                    <Button size="small" danger onClick={() => onRemove(resource)}>
                      {tr("removeResource")}
                    </Button>
                  ) : null}
                </div>
              </div>
            ) : null}
          </div>
        );
      })}
      {resources.length === 0 ? (
        <Typography.Text type="secondary" className="wd-muted">
          {tr("noResources")}
        </Typography.Text>
      ) : null}
    </div>
  );
}
