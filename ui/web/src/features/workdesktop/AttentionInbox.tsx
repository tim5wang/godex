import { useState } from "react";
import { Badge, Button, Card, Dropdown, Input, Popconfirm, Segmented, Space, Tag, Typography } from "antd";
import { DownOutlined, ThunderboltOutlined } from "@ant-design/icons";
import dayjs from "dayjs";
import { useI18n } from "../../i18n";
import { InfoHint } from "./workDesktopHints";
import { FoldCard } from "./workDesktopFold";
import { formatTimestamp } from "./workDesktopView";
import type { AttentionAction, AttentionItem, AttentionVerb, DeferredAttention } from "../../lib/apiWorkContext";

/** The explicit snooze windows offered in the inbox. */
export type SnoozeChoice = "tomorrow" | "threeDays" | "nextWeek";

export const SNOOZE_CHOICES: ReadonlyArray<SnoozeChoice> = ["tomorrow", "threeDays", "nextWeek"];

/**
 * The wake time for a snooze choice, computed at click time.
 *
 * Explicit windows instead of one fixed rule: how long "later" is depends on the item, and a
 * single hard-coded window is wrong in both directions — too short for something you will only
 * revisit next week, too long for something you just need to see again tomorrow.
 *
 * Every choice is hours-to-days out, so a small clock skew between browser and server cannot
 * produce an until_at in the past (the server rejects those outright). "Tomorrow" lands on a
 * stable 09:00 rather than "24h from now", because a reminder arriving at 23:40 is not what
 * anyone means by tomorrow.
 */
export function snoozeUntilAt(choice: SnoozeChoice, now: dayjs.Dayjs = dayjs()): string {
  switch (choice) {
    case "tomorrow":
      return now.add(1, "day").hour(9).minute(0).second(0).millisecond(0).toISOString();
    case "nextWeek":
      return now.add(7, "day").toISOString();
    default:
      return now.add(3, "day").toISOString();
  }
}

/**
 * AttentionInbox is the cross-stream "needs me" list.
 *
 * It exists so six or seven parallel streams can be triaged from one place: each
 * row names its stream and jumps straight to the tab where the item is resolved,
 * instead of making the user open desktops one by one to find out which moved.
 * The list is compact by design (one line per item) because it is a triage view,
 * not a place to work.
 */
export function AttentionInbox(props: {
  items: AttentionItem[];
  loading: boolean;
  onOpen: (contextId: string, tab: string) => void;
  /** Which streams the list covers: everything, or only the open desktop. */
  scope: "current" | "all";
  onScopeChange: (scope: "current" | "all") => void;
  /** Rows the owner already ruled on, reported so deferral is visible and undoable. */
  deferred?: DeferredAttention[];
  onAction?: (contextId: string, itemKey: string, verb: AttentionVerb, opts?: { reason?: string; untilAt?: string }) => void;
  onUndo?: (contextId: string, itemKey: string) => void;
  /**
   * Turn a row into delegated work. Not a recorded verb (see the store's openDelegateEditor):
   * delegating creates work through the existing delegate flow, it does not rule on the row.
   */
  onDelegate?: (contextId: string, title: string) => void;
}) {
  const { items, loading, onOpen, scope, onScopeChange, deferred = [], onAction, onUndo, onDelegate } = props;
  const { t } = useI18n();
  const tr = (key: string, vars?: Record<string, string | number>) => t(`workDesktop.${key}`, vars);
  // The dismiss reason lives here rather than in the server payload because it is
  // typed into a popover: a blank reason must not become a recorded verdict, so the
  // confirm button stays disabled until something is written. It is keyed by whatever
  // action key the verdict is for, so row and entry dismissals share one map.
  const [dismissReason, setDismissReason] = useState<Record<string, string>>({});
  // A grouped row's entries are behind a toggle that starts CLOSED: per-entry verbs are
  // the exception (most rows are ruled on as a whole), and rendering every entry's three
  // buttons would turn the triage list into a wall of affordances.
  const [expanded, setExpanded] = useState<Record<string, boolean>>({});

  /**
   * One set of verbs, for either a whole row or a single entry of it. Kept as a helper
   * (rather than duplicated) so the two levels cannot drift apart in behaviour —
   * including the "dismiss needs a reason" rule, which is the one that protects work
   * from being silently dropped.
   */
  const renderVerbs = (contextId: string, actionKey: string, action?: AttentionAction) => (
    <Space size={2} className="wd-attention-verbs">
      {action?.verb === "claim" ? (
        // A claim is still work (the opposite of deferral), so it stays on the list
        // and says so rather than vanishing when picked up.
        <Tag color="processing" className="wd-attention-claimed">{tr("attentionClaimed")}</Tag>
      ) : (
        <Button size="small" type="link" onClick={() => onAction?.(contextId, actionKey, "claim")}>
          {tr("attentionClaim")}
        </Button>
      )}
      <Button
        size="small"
        type="link"
        title={tr("attentionSnoozeHint")}
        onClick={() => onAction?.(contextId, actionKey, "snooze")}
      >
        {tr("attentionSnooze")}
      </Button>
      {/* Explicit windows live behind a caret so the COMMON case stays one click: the plain
          button keeps the server's default, and this only adds the choice. A compact triage
          row cannot afford three snooze buttons, and a picker on every click would tax the
          case that does not care. */}
      <Dropdown
        trigger={["click"]}
        menu={{
          items: SNOOZE_CHOICES.map((choice) => ({ key: choice, label: tr(`attentionSnooze${choice[0]!.toUpperCase()}${choice.slice(1)}`) })),
          onClick: ({ key }) => onAction?.(contextId, actionKey, "snooze", { untilAt: snoozeUntilAt(key as SnoozeChoice) }),
        }}
      >
        <Button size="small" type="text" icon={<DownOutlined aria-hidden="true" />} aria-label={tr("attentionSnoozePick")} />
      </Dropdown>
      {/* Dismiss hides work, so it takes a reason first: a verdict with no recorded
          reason is indistinguishable from losing the work, and this is what makes undo
          meaningful. */}
      <Popconfirm
        title={tr("attentionDismissTitle")}
        description={(
          <Input
            size="small"
            value={dismissReason[actionKey] ?? ""}
            placeholder={tr("attentionDismissPlaceholder")}
            onChange={(event) => setDismissReason((prev) => ({ ...prev, [actionKey]: event.target.value }))}
          />
        )}
        okText={tr("attentionDismiss")}
        cancelText={tr("attentionCancel")}
        okButtonProps={{ disabled: !(dismissReason[actionKey] ?? "").trim() }}
        onConfirm={() => {
          const reason = (dismissReason[actionKey] ?? "").trim();
          if (!reason) return;
          onAction?.(contextId, actionKey, "dismiss", { reason });
          setDismissReason((prev) => ({ ...prev, [actionKey]: "" }));
        }}
      >
        <Button size="small" type="link">{tr("attentionDismiss")}</Button>
      </Popconfirm>
    </Space>
  );

  const label = (item: AttentionItem) => {
    switch (item.kind) {
      case "delegation_review": return tr("attentionReview");
      case "delegation_missing": return tr("attentionMissing");
      case "fact_review": return tr("attentionFactReview");
      case "todo_overdue": return tr("attentionOverdue");
      case "todo_soon": return tr("attentionSoon");
      case "todo_blocked": return tr("attentionBlocked");
      case "checkpoint_blocker": return tr("attentionBlocker");
      case "checkpoint_question": return tr("attentionQuestion");
      default: return tr("attention");
    }
  };
  const color = (item: AttentionItem) => {
    switch (item.priority) {
      case "high": return "red";
      case "normal": return "orange";
      default: return "default";
    }
  };

  return (
    <Card
      size="small"
      className="wd-attention"
      title={
        <Space size={6}>
          <ThunderboltOutlined aria-hidden="true" />
          {tr("attention")}
          {!loading && items.length ? <Badge count={items.length} size="small" /> : null}
          <InfoHint title={tr("showHint")} text={tr("attentionHint")} />
        </Space>
      }
      extra={
        <Segmented
          size="small"
          aria-label={tr("attentionScope")}
          value={scope}
          onChange={(value) => onScopeChange(value as "current" | "all")}
          options={[
            { value: "current", label: tr("attentionScopeCurrent") },
            { value: "all", label: tr("attentionScopeAll") },
          ]}
        />
      }
    >
      {items.length === 0 ? (
        <Typography.Text type="secondary">{tr("attentionEmpty")}</Typography.Text>
      ) : (
        <ul className="wd-attention-list">
          {items.map((item, index) => (
            <li key={`${item.kind}-${item.context_id}-${index}`} className={`wd-attention-item wd-attention-${item.priority}`}>
              <Tag color={color(item)}>{label(item)}</Tag>
              {/* A grouped row must SAY it stands for several entries, otherwise the
                  collapsed line silently hides work. */}
              {item.count && item.count > 1 ? <Tag>{tr("attentionGrouped", { count: item.count })}</Tag> : null}
              {/* Leverage: other streams waiting on this one. It is why this row sorts
                  above an equally urgent row nobody depends on. */}
              {item.blocked_count ? <Tag color="geekblue">{tr("attentionLeverage", { count: item.blocked_count })}</Tag> : null}
              <button type="button" className="wd-attention-main" onClick={() => onOpen(item.context_id, item.tab)}>
                <strong>{item.title}</strong>
                <span className="wd-muted">{item.context_title}</span>
                {/* The collapsed titles stay readable without opening the stream. */}
                {item.also?.length ? <span className="wd-muted wd-wrap">{item.also.join(" · ")}</span> : null}
              </button>
              {item.due_at ? <span className="wd-muted wd-attention-due">{formatTimestamp(item.due_at)}</span> : null}
              <Button size="small" type="link" onClick={() => onOpen(item.context_id, item.tab)}>
                {tr("attentionOpen")}
              </Button>
              {/* The verbs. Each one is a recorded decision, so the row must say which
                  one a single click applies — no verb silently defaults. They only
                  appear when a handler is wired, so the board degrades to the old
                  read-only list instead of offering buttons that do nothing. */}
              {onAction ? renderVerbs(item.context_id, item.item_key, item.action) : null}
              {/* Delegating is NOT one of the recorded verbs: it does not rule on the row, it
                  creates work in the existing delegate flow (which the human still has to
                  give acceptance criteria for). Offered at ROW level only — the entry list is
                  already opt-in and would become a wall of buttons with a fourth per entry. */}
              {onDelegate ? (
                <Button
                  size="small"
                  type="link"
                  title={tr("attentionDelegateHint")}
                  onClick={() => onDelegate(item.context_id, item.title)}
                >
                  {tr("attentionDelegate")}
                </Button>
              ) : null}
              {/* Per-entry verbs are opt-in: most rows are ruled on as a whole, so the
                  entry list starts collapsed rather than stacking three buttons per
                  entry into a triage list. */}
              {onAction && item.entries?.length ? (
                <button
                  type="button"
                  className="wd-attention-entries-toggle"
                  aria-expanded={expanded[item.item_key] === true}
                  onClick={() => setExpanded((prev) => ({ ...prev, [item.item_key]: !prev[item.item_key] }))}
                >
                  {expanded[item.item_key] ? tr("attentionEntriesHide") : tr("attentionEntriesShow", { count: item.entries.length })}
                </button>
              ) : null}
              {onAction && item.entries?.length && expanded[item.item_key] ? (
                <ul className="wd-attention-entries">
                  {item.entries.map((entry) => (
                    <li key={entry.key || entry.title} className="wd-attention-entry">
                      <span className="wd-attention-entry-title">{entry.title}</span>
                      {entry.due_at ? <span className="wd-muted">{formatTimestamp(entry.due_at)}</span> : null}
                      {/* Entries with no stable identity (free-text checkpoint markers)
                          cannot be ruled on individually, so they get NO verbs rather
                          than buttons that would fail. The row's own verbs still apply
                          to them, which is why they are listed here at all. */}
                      {entry.key ? (
                        renderVerbs(item.context_id, entry.key, entry.action)
                      ) : (
                        <span className="wd-muted wd-attention-entry-group-only">{tr("attentionEntryGroupOnly")}</span>
                      )}
                    </li>
                  ))}
                </ul>
              ) : null}
            </li>
          ))}
        </ul>
      )}
      {/* Deferred rows sit behind their own fold and start CLOSED: they are a record
          of decisions already made, not work waiting on the user. Reporting them at
          all is the point — a verdict that makes an item disappear looks like a bug. */}
      {deferred.length ? (
        <FoldCard
          className="wd-attention-deferred"
          defaultOpen={false}
          openLabel={tr("foldExpand")}
          closeLabel={tr("foldCollapse")}
          title={<Space size={6}>{tr("attentionDeferredTitle")}<Badge count={deferred.length} size="small" /></Space>}
        >
          <ul className="wd-attention-list">
            {deferred.map((entry) => (
              <li key={entry.item.item_key} className="wd-attention-item wd-attention-deferred-row">
                <Tag>{entry.action.verb === "snooze" ? tr("attentionDeferredSnoozed") : tr("attentionDeferredDismissed")}</Tag>
                <span className="wd-attention-main wd-attention-deferred-text">
                  <strong>{entry.item.title}</strong>
                  <span className="wd-muted">{entry.item.context_title}</span>
                </span>
                {/* The wake time (or the reason) is the context needed to trust the
                    deferral: "snoozed" alone does not say until when, and a dismissed
                    group with no reason cannot be judged. */}
                {entry.action.verb === "snooze" && entry.action.until_at ? (
                  <span className="wd-muted">{tr("attentionDeferredUntil", { time: formatTimestamp(entry.action.until_at) })}</span>
                ) : null}
                {entry.action.verb === "dismiss" && entry.action.reason ? (
                  <span className="wd-muted wd-wrap">{tr("attentionDeferredReason", { reason: entry.action.reason })}</span>
                ) : null}
                {onUndo ? (
                  <Button size="small" type="link" onClick={() => onUndo(entry.item.context_id, entry.item.item_key)}>
                    {tr("attentionUndo")}
                  </Button>
                ) : null}
              </li>
            ))}
          </ul>
        </FoldCard>
      ) : null}
    </Card>
  );
}
