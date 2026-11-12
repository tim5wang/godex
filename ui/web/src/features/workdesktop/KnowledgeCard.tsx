import { useState } from "react";
import dayjs, { type Dayjs } from "dayjs";
import { Alert, Button, Card, DatePicker, Form, Input, Modal, Space, Tag, Typography } from "antd";
import { DownOutlined, RightOutlined, ThunderboltOutlined } from "@ant-design/icons";
import { MarkdownContent } from "../../components/MarkdownContent";
import { formatTimestamp } from "./workDesktopView";
import type { WorkFact } from "../../lib/apiWorkContext";

/**
 * KnowledgeCard renders one work fact.
 *
 * Collapsed by default, showing only the title: a stream accumulates many facts
 * and a wall of full-length entries is unscannable, so the board reads as a list
 * of headline rows and the user opens the one they care about. The whole title
 * row is the toggle (a large hit target, and the disclosure is announced through
 * aria-expanded) rather than a small link at the bottom of the body.
 *
 * Only the title row stays visible when collapsed — the content, scope/source
 * details and the review actions are all part of what opening reveals. The status
 * tag stays in the card's `extra` so the review state is readable while collapsed
 * (a candidate that needs a decision must not look identical to a confirmed one).
 */
export function KnowledgeCard(props: {
  fact: WorkFact;
  contextTitle?: string;
  statusColor: string;
  readOnly: boolean;
  tr: (key: string, vars?: Record<string, string | number>) => string;
  trLabel: (prefix: string, value: string) => string;
  onAccept: (fact: WorkFact, evidence?: { observed_at: string; source: string }) => void;
  onDismiss: (fact: WorkFact) => void;
  onPromote: (fact: WorkFact) => void;
}) {
  const { fact, contextTitle, statusColor, readOnly, tr, trLabel, onAccept, onDismiss, onPromote } = props;
  const [reviewOpen, setReviewOpen] = useState(false);
  const [reviewForm] = Form.useForm<{ observed: Dayjs; source: string }>();
  const [open, setOpen] = useState(false);

  // Label describes what the NEXT click does, which is the disclosure convention.
  const toggleLabel = open ? tr("collapse") : tr("expand");

  return (
    <Card
      size="small"
      className="wd-knowledge"
      data-expanded={open ? "true" : "false"}
      title={
        // The whole head is one button, so the title is both the label and the
        // click target. It carries no aria-label on purpose: that would override
        // the accessible name and hide the title (the actual content) from screen
        // readers; aria-expanded conveys the state and `title` the hover hint.
        <button
          type="button"
          className="wd-knowledge-head"
          aria-expanded={open}
          title={toggleLabel}
          onClick={() => setOpen((prev) => !prev)}
        >
          {open ? <DownOutlined aria-hidden="true" /> : <RightOutlined aria-hidden="true" />}
          <Tag>{trLabel("factKind", fact.kind)}</Tag>
          {/* pre-wrap: an entry title can carry its own line break, and the card
              head's default `white-space: normal` collapses it into one run-on line. */}
          <span className="wd-knowledge-title whitespace-pre-wrap break-words">{fact.title}</span>
        </button>
      }
      extra={<Tag color={statusColor}>{trLabel("factStatus", fact.status)}</Tag>}
    >
      {open ? <>
        {/* forceMarkdown, not the usual "does it look like markdown?" heuristic: a
            knowledge entry is authored text where headings and lists are intended,
            whereas that heuristic exists to keep incidental tool output un-mangled. */}
        <MarkdownContent content={fact.content} forceMarkdown />

        <div className="wd-muted">{tr("factScopeLabel")} · {fact.scope || tr("noItems")}</div>
        <div className="wd-muted">{tr("factObserved")} · {formatTimestamp(fact.observed_at) || tr("noItems")}</div>
        {fact.valid_until && <div className="wd-muted">{tr("factValidUntil")} · {formatTimestamp(fact.valid_until)}</div>}
        {fact.verified_at && <div className="wd-muted">{tr("verifiedAt")} · {formatTimestamp(fact.verified_at)}</div>}
        {fact.source && <div className="wd-muted">{tr("factSource")} · <Typography.Text code className="wd-wrap">{fact.source}</Typography.Text></div>}
        {/* Evidence keeps its own line breaks: excerpts are quoted from logs/reports
            where the original layout is the evidence. */}
        {fact.source_excerpt && <Typography.Paragraph type="secondary" className="whitespace-pre-wrap break-words">“{fact.source_excerpt}”</Typography.Paragraph>}
        {fact.supersedes_id && <div className="wd-muted">{tr("factSupersedes")} · {fact.supersedes_id}</div>}

        <Space wrap style={{ marginTop: 12 }}>
          {contextTitle && <Tag>{contextTitle}</Tag>}
          {fact.status === "candidate" && !readOnly && <>
            <Button size="small" type="primary" onClick={() => { if (!fact.observed_at || !fact.source || fact.source.startsWith("dream://")) { reviewForm.setFieldsValue({ source: fact.source.startsWith("dream://") ? "" : fact.source, observed: fact.observed_at ? dayjs(fact.observed_at) : undefined }); setReviewOpen(true); } else onAccept(fact); }}>{tr("accept")}</Button>
            <Button size="small" danger onClick={() => onDismiss(fact)}>{tr("dismiss")}</Button>
          </>}
          {!fact.memory_candidate_id && fact.status === "confirmed" && !readOnly && <Button size="small" icon={<ThunderboltOutlined aria-hidden="true" />} onClick={() => onPromote(fact)}>{tr("promoteMemory")}</Button>}
          {fact.memory_candidate_id && <Tag color="purple">{tr("memoryCandidate")}</Tag>}
        </Space>
      </> : null}
      <Modal open={reviewOpen} title={tr("reviewEvidence")} onCancel={() => setReviewOpen(false)} onOk={() => { void reviewForm.validateFields().then((values) => { onAccept(fact, { observed_at: values.observed.toISOString(), source: values.source }); setReviewOpen(false); }); }}>
        <Alert type="info" title={tr("reviewEvidenceHint")} />
        <Form form={reviewForm} layout="vertical" style={{ marginTop: 12 }}>
          <Form.Item name="observed" label={tr("factObserved")} rules={[{ required: true }]}><DatePicker showTime /></Form.Item>
          <Form.Item name="source" label={tr("factSource")} rules={[{ required: true }]}><Input /></Form.Item>
        </Form>
      </Modal>
    </Card>
  );
}
