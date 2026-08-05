import { useState } from "react";
import { Button, Space, Tooltip, Typography } from "antd";
import { CopyOutlined } from "@ant-design/icons";
import { CodeViewer } from "./CodeViewer";
import { MarkdownContent } from "./MarkdownContent";
import { writeClipboardText } from "../lib/clipboard";
import type { FeedItem } from "../lib/types";

const OUTPUT_TRUNCATE_LIMIT = 8000;

/**
 * Shared expanded details for a tool call (used by ToolCallRow and ToolCard).
 *
 * Improvements over the old plain-<pre> dump:
 *   - JSON Input/Output rendered through CodeMirror with syntax highlighting
 *   - non-JSON Output defaults to plain text (no more markdown mangling of
 *     paths / globs / log lines); a "Render markdown" toggle opts in
 *   - long Output is truncated with a "Show more" button
 *   - per-block copy button; Error gets its own dark block
 */
export function ToolDetails({ item }: { item: FeedItem }) {
  const [outputAsMarkdown, setOutputAsMarkdown] = useState(false);

  const inputText = item.input ? JSON.stringify(item.input, null, 2) : "";
  const outputText = item.output ?? "";
  const outputIsJSON = looksLikeJSON(outputText);
  const outputIsMarkdown = !outputIsJSON && needsMarkdown(outputText);
  const showMarkdownToggle = !outputIsJSON && outputIsMarkdown;

  return (
    <div className="tool-call-row-details">
      {item.input ? (
        <ToolDetailBlock label="Input" copyText={inputText}>
          <CodeViewer value={inputText} language="json" className="tool-card-code" />
        </ToolDetailBlock>
      ) : null}

      {item.output ? (
        <ToolDetailBlock
          label="Output"
          copyText={outputText}
          extra={
            showMarkdownToggle ? (
              <Button
                type="text"
                size="small"
                className="tool-card-toggle"
                onClick={() => setOutputAsMarkdown((v) => !v)}
              >
                {outputAsMarkdown ? "Plain" : "Markdown"}
              </Button>
            ) : undefined
          }
        >
          {outputIsJSON ? (
            <CodeViewer value={formatJSONText(outputText)} language="json" className="tool-card-code" />
          ) : outputAsMarkdown ? (
            <MarkdownContent className="tool-card-output" content={outputText} forceMarkdown />
          ) : (
            <TruncatableText text={outputText} limit={OUTPUT_TRUNCATE_LIMIT} />
          )}
        </ToolDetailBlock>
      ) : null}

      {item.error ? (
        <ToolDetailBlock label="Error" copyText={item.error}>
          <pre className="tool-card-pre tool-card-pre-error">{item.error}</pre>
        </ToolDetailBlock>
      ) : null}
    </div>
  );
}

function ToolDetailBlock({
  label,
  copyText,
  extra,
  children,
}: {
  label: string;
  copyText: string;
  extra?: React.ReactNode;
  children: React.ReactNode;
}) {
  const copy = async () => {
    try {
      await writeClipboardText(copyText);
    } catch {
      /* clipboard may be blocked; ignore */
    }
  };
  return (
    <section className="tool-card-detail-block">
      <div className="tool-card-detail-head">
        <Typography.Text className="tool-card-detail-label" type="secondary">
          {label}
        </Typography.Text>
        <Space size={2}>
          {extra}
          <Tooltip title="Copy">
            <Button
              type="text"
              size="small"
              className="tool-card-toggle"
              icon={<CopyOutlined />}
              onClick={copy}
              aria-label={`Copy ${label}`}
            />
          </Tooltip>
        </Space>
      </div>
      {children}
    </section>
  );
}

function TruncatableText({ text, limit }: { text: string; limit: number }) {
  const [expanded, setExpanded] = useState(false);
  const tooLong = text.length > limit;
  const shown = tooLong && !expanded ? `${text.slice(0, limit).trimEnd()}\n…` : text;
  return (
    <div>
      <pre className="tool-card-pre tool-card-pre-plain">{shown}</pre>
      {tooLong ? (
        <Button type="link" size="small" onClick={() => setExpanded((v) => !v)}>
          {expanded ? "Show less" : `Show more (${(text.length - limit).toLocaleString()} more chars)`}
        </Button>
      ) : null}
    </div>
  );
}

export function looksLikeJSON(value: string): boolean {
  const text = value.trim();
  return (text.startsWith("{") && text.endsWith("}")) || (text.startsWith("[") && text.endsWith("]"));
}

export function formatJSONText(value: string): string {
  try {
    return JSON.stringify(JSON.parse(value), null, 2);
  } catch {
    return value;
  }
}

function needsMarkdown(text: string): boolean {
  // Reuse the tightened detection: at least one strong signal or two weak ones.
  // Inline import to avoid a circular dependency on MarkdownContent? No —
  // MarkdownContent already imports only renderer lazily; this is a plain
  // function we keep local to keep ToolDetails self-contained.
  const content = text.trim();
  if (!content) return false;
  let strong = 0;
  if (/(^|\n)\s{0,3}#{1,6}\s/.test(content)) strong += 1;
  if (/```/.test(content)) strong += 1;
  if (/(^|\n)\s*\|.+\|/.test(content)) strong += 1;
  if (/(^|\n)\s{0,3}>\s/.test(content)) strong += 1;
  if (/(^|\n)\s{0,3}([-*+]|\d+\.)\s/.test(content)) strong += 1;
  if (/\[[^\]]+\]\([^)]+\)/.test(content)) strong += 1;
  if (/\*\*[^*]+\*\*/.test(content)) strong += 1;
  let weak = 0;
  if (/`[^`\n]+`/.test(content)) weak += 0.5;
  if (/(^|\s)\*[^*\n]+\*(?=\s|$)|(^|\s)_[^_\n]+_(?=\s|$)/.test(content)) weak += 0.5;
  return strong >= 1 || weak >= 2;
}
