import { useState } from "react";
import { Button, Card, Space, Tag, Typography } from "antd";
import { ThunderboltOutlined } from "@ant-design/icons";
import { MarkdownContent } from "../../components/MarkdownContent";
import { formatTimestamp } from "./workDesktopView";
import type { WorkFact } from "../../lib/apiWorkContext";

/**
 * KnowledgeCard renders one work fact.
 *
 * Long entries fold to their opening lines: a stream accumulates many facts and a
 * wall of full-length text is unscannable. The fold is line-based rather than a CSS
 * clamp so what is hidden is predictable, and the full text is one click away.
 */
const PREVIEW_LINES = 4;

export function KnowledgeCard(props: {
  fact: WorkFact;
  contextTitle?: string;
  statusColor: string;
  readOnly: boolean;
  tr: (key: string, vars?: Record<string, string | number>) => string;
  trLabel: (prefix: string, value: string) => string;
  onAccept: (fact: WorkFact) => void;
  onDismiss: (fact: WorkFact) => void;
  onPromote: (fact: WorkFact) => void;
}) {
  const { fact, contextTitle, statusColor, readOnly, tr, trLabel, onAccept, onDismiss, onPromote } = props;
  const [open, setOpen] = useState(false);

  const lines = fact.content.split("\n");
  const collapsible = lines.length > PREVIEW_LINES;
  const folded = collapsible && !open;
  const shown = folded ? lines.slice(0, PREVIEW_LINES).join("\n") : fact.content;

  return (
    <Card
      size="small"
      className="wd-knowledge"
      // Title with pre-wrap: an entry title can carry its own line break, and the
      // card head's default `white-space: normal` collapses it into one run-on line.
      title={<Space wrap><Tag>{trLabel("factKind", fact.kind)}</Tag><span className="whitespace-pre-wrap break-words">{fact.title}</span></Space>}
      extra={<Tag color={statusColor}>{trLabel("factStatus", fact.status)}</Tag>}
    >
      {/* forceMarkdown, not the usual "does it look like markdown?" heuristic: a
          knowledge entry is authored text where headings and lists are intended,
          whereas that heuristic exists to keep incidental tool output un-mangled. */}
      <MarkdownContent content={shown} forceMarkdown />
      {folded ? (
        <Button size="small" type="link" className="wd-knowledge-toggle" aria-expanded={false} onClick={() => setOpen(true)}>
          {tr("expand")}
        </Button>
      ) : null}

      {/* Details are part of what the fold hides — collapsed stays concise. */}
      {!folded && <>
        <div className="wd-muted">{tr("factScopeLabel")} · {fact.scope || tr("noItems")}</div>
        <div className="wd-muted">{tr("factObserved")} · {formatTimestamp(fact.observed_at) || tr("noItems")}</div>
        {fact.verified_at && <div className="wd-muted">{tr("verifiedAt")} · {formatTimestamp(fact.verified_at)}</div>}
        {fact.source && <div className="wd-muted">{tr("factSource")} · <Typography.Text code className="wd-wrap">{fact.source}</Typography.Text></div>}
        {/* Evidence keeps its own line breaks: excerpts are quoted from logs/reports
            where the original layout is the evidence. */}
        {fact.source_excerpt && <Typography.Paragraph type="secondary" className="whitespace-pre-wrap break-words">“{fact.source_excerpt}”</Typography.Paragraph>}
        {fact.supersedes_id && <div className="wd-muted">{tr("factSupersedes")} · {fact.supersedes_id}</div>}
        {collapsible ? (
          <Button size="small" type="link" className="wd-knowledge-toggle" aria-expanded onClick={() => setOpen(false)}>
            {tr("collapse")}
          </Button>
        ) : null}
      </>}

      <Space wrap style={{ marginTop: 12 }}>
        {contextTitle && <Tag>{contextTitle}</Tag>}
        {fact.status === "candidate" && !readOnly && <>
          <Button size="small" type="primary" onClick={() => onAccept(fact)}>{tr("accept")}</Button>
          <Button size="small" danger onClick={() => onDismiss(fact)}>{tr("dismiss")}</Button>
        </>}
        {!fact.memory_candidate_id && fact.status === "confirmed" && !readOnly && <Button size="small" icon={<ThunderboltOutlined aria-hidden="true" />} onClick={() => onPromote(fact)}>{tr("promoteMemory")}</Button>}
        {fact.memory_candidate_id && <Tag color="purple">{tr("memoryCandidate")}</Tag>}
      </Space>
    </Card>
  );
}
