import { Button, Empty, Space, Tag, Typography } from "antd";
import { CheckCircleOutlined, ClockCircleOutlined, FileTextOutlined, PauseCircleOutlined, PlayCircleOutlined, ProfileOutlined, ThunderboltOutlined } from "@ant-design/icons";
import { InlineEdit } from "./workDesktopInline";
import { InfoHint } from "./workDesktopHints";
import { FoldCard } from "./workDesktopFold";
import { formatTimestamp, joinLines, splitLines } from "./workDesktopView";
import type { CheckpointInput, WorkCheckpoint, WorkEvent } from "../../lib/apiWorkContext";

/**
 * OverviewPanel is the stream's overview with every displayed value edited in
 * place, which is what retired the "edit checkpoint" modal.
 *
 * Layout: sections stack in ONE column and each is individually foldable. The
 * desktop lives in the chat's right dock now, which is much narrower than the old
 * standalone page — a two-column grid left each card too cramped to read, and a
 * single column of full cards is a long scroll. Folding is opt-in (everything
 * starts open) because this panel is work MEMORY: quietly hiding remembered
 * content is a worse failure than a long list.
 *
 * A checkpoint is stored as one document under a version (not per-field
 * patches), so editing one field here still submits the whole checkpoint with
 * only that field changed. That merge lives in the page's onPatchCheckpoint,
 * the only place that knows the current document.
 *
 * List fields are edited as one-item-per-line text and rendered back as a list,
 * so what the user types and what they read agree on the format.
 */
export function OverviewPanel(props: {
  background: string;
  goal: string;
  checkpoint: WorkCheckpoint;
  events: WorkEvent[];
  focusChanges: WorkEvent[];
  lastSeenAt: string;
  readOnly: boolean;
  markSeenPending: boolean;
  onMarkSeen: () => void;
  onPatchContext: (patch: { background?: string; goal?: string }) => Promise<unknown>;
  onPatchCheckpoint: (patch: Partial<CheckpointInput>) => Promise<unknown>;
  tr: (key: string, vars?: Record<string, string | number>) => string;
}) {
  const {
    background, goal, checkpoint, events, focusChanges, lastSeenAt, readOnly,
    markSeenPending, onMarkSeen, onPatchContext, onPatchCheckpoint, tr,
  } = props;

  // Every section is foldable; the labels describe what the NEXT click does.
  const foldProps = { openLabel: tr("foldExpand"), closeLabel: tr("foldCollapse") };

  // Lists use the pencil affordance instead of click-anywhere-to-edit: a list is
  // block content, and nesting <ul> inside the click target's <button> is invalid
  // HTML with confusing screen-reader output. The value stays static markup.
  const listEdit = (labelKey: string, items: string[], apply: (items: string[]) => Partial<CheckpointInput>, ordered = false) => (
    <InlineEdit
      multiline
      editIconLabel={tr(labelKey)}
      ariaLabel={tr(labelKey)}
      value={joinLines(items)}
      disabled={readOnly}
      render={(value) => {
        const lines = splitLines(value);
        if (!lines.length) return <Typography.Text type="secondary">{tr("noItems")}</Typography.Text>;
        if (ordered) return <ol className="wd-inline-list">{lines.map((line, index) => <li key={index}>{line}</li>)}</ol>;
        return <ul className="wd-inline-list">{lines.map((line, index) => <li key={index}>{line}</li>)}</ul>;
      }}
      onSave={(next) => onPatchCheckpoint(apply(splitLines(next)))}
    />
  );

  const seenLabel = lastSeenAt ? formatTimestamp(lastSeenAt) : tr("lastSeenNever");

  return <div className="wd-stack">
    <FoldCard
      {...foldProps}
      className="wd-next-card"
      title={<Space><PlayCircleOutlined aria-hidden="true" />{tr("next")}</Space>}
      extra={<Tag>{tr("revision", { revision: checkpoint.revision })}</Tag>}
    >
      <InlineEdit rich multiline ariaLabel={tr("checkpointSummary")} placeholder={tr("noItems")} value={checkpoint.summary} disabled={readOnly} onSave={(next) => onPatchCheckpoint({ summary: next })} />
      {listEdit("checkpointNext", checkpoint.next_actions, (items) => ({ next_actions: items }), true)}
      <Typography.Text type="secondary">{tr("savedAt")} · {formatTimestamp(checkpoint.created_at) || tr("noItems")}</Typography.Text>
    </FoldCard>

    <FoldCard {...foldProps} title={<Space><FileTextOutlined aria-hidden="true" />{tr("background")}</Space>}>
      <InlineEdit rich multiline ariaLabel={tr("background")} placeholder={tr("noItems")} value={background} disabled={readOnly} onSave={(next) => onPatchContext({ background: next })} />
      <Typography.Text strong>{tr("goal")}</Typography.Text>
      <InlineEdit rich multiline ariaLabel={tr("goal")} placeholder={tr("noItems")} value={goal} disabled={readOnly} onSave={(next) => onPatchContext({ goal: next })} />
    </FoldCard>

    <FoldCard {...foldProps} title={<Space><CheckCircleOutlined aria-hidden="true" />{tr("completed")}</Space>}>
      {listEdit("checkpointCompleted", checkpoint.completed, (items) => ({ completed: items }))}
    </FoldCard>

    <FoldCard {...foldProps} title={<Space><PauseCircleOutlined aria-hidden="true" />{tr("blockers")}</Space>}>
      {listEdit("checkpointBlockers", checkpoint.blockers, (items) => ({ blockers: items }))}
      {listEdit("checkpointOpenQuestions", checkpoint.open_questions, (items) => ({ open_questions: items }))}
    </FoldCard>

    <FoldCard {...foldProps} title={<Space size={6}><ClockCircleOutlined aria-hidden="true" />{tr("changes")}<InfoHint title={tr("showHint")} text={tr("changesHint")} /></Space>}>
      {events.length ? <div className="wd-stack">{events.slice(0, 3).map((event) => <div key={event.id}><Typography.Text>{event.title}</Typography.Text><div className="wd-muted">{formatTimestamp(event.created_at)}</div></div>)}</div> : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("noChanges")} />}
    </FoldCard>

    <FoldCard {...foldProps} title={<Space><ProfileOutlined aria-hidden="true" />{tr("checkpointEvidenceRefs")}</Space>}>
      {listEdit("checkpointEvidenceRefs", checkpoint.evidence_refs, (items) => ({ evidence_refs: items }))}
    </FoldCard>

    <FoldCard
      {...foldProps}
      className="wd-focus-card"
      title={<Space size={6}><ThunderboltOutlined aria-hidden="true" />{tr("changesSinceTitle")}<span className="wd-muted">{seenLabel}</span></Space>}
      extra={!readOnly && focusChanges.length > 0 ? <Button size="small" loading={markSeenPending} onClick={onMarkSeen}>{tr("markSeen")}</Button> : null}
    >
      {focusChanges.length ? <div className="wd-stack">{focusChanges.slice(0, 8).map((event) => <div key={event.id}><Typography.Text>{event.title}</Typography.Text><div className="wd-muted">{event.kind} · {formatTimestamp(event.created_at)}</div></div>)}</div> : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("noChanges")} />}
    </FoldCard>

    <Typography.Paragraph type="secondary" className="wd-foot-note">
      <InfoHint title={tr("currentUnknown")} text={tr("currentUnknownHint")} /> {tr("currentUnknown")}
    </Typography.Paragraph>
  </div>;
}
