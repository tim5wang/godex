import { Suspense, lazy } from "react";

const MarkdownRenderer = lazy(async () => ({ default: (await import("./MarkdownRenderer")).MarkdownRenderer }));

interface MarkdownContentProps {
  content: string;
  className?: string;
  forceMarkdown?: boolean;
  /** Render as plain text even if the content looks like markdown. */
  forcePlain?: boolean;
}

export function MarkdownContent({ content, className, forceMarkdown = false, forcePlain = false }: MarkdownContentProps) {
  const shouldRender = !forcePlain && (forceMarkdown || needsMarkdownRendering(content));
  if (!shouldRender) {
    return (
      <div className={["markdown-body whitespace-pre-wrap break-words", className].filter(Boolean).join(" ")}>
        {content}
      </div>
    );
  }

  return (
    <div className={["markdown-body", className].filter(Boolean).join(" ")}>
      <Suspense fallback={<div className="whitespace-pre-wrap break-words">{content}</div>}>
        <MarkdownRenderer content={content} />
      </Suspense>
    </div>
  );
}

/**
 * Decide whether text should go through the markdown pipeline.
 *
 * Strong signals (headings, fenced code, tables, blockquotes, lists, links,
 * bold) each count as 1; weak signals (inline code, italics) count as 0.5.
 * We render only when we have at least one strong signal, or enough weak
 * signals to be confident — this stops tool output containing incidental
 * `*`, `_`, or `[x](y)` (paths, shell globs, log lines) from being mangled
 * by the markdown renderer.
 */
export function needsMarkdownRendering(content: string): boolean {
  const text = content.trim();
  if (!text) {
    return false;
  }

  let strong = 0;
  // Headings
  if (/(^|\n)\s{0,3}#{1,6}\s/.test(text)) strong += 1;
  // Fenced code block
  if (/```/.test(text)) strong += 1;
  // Table row
  if (/(^|\n)\s*\|.+\|/.test(text)) strong += 1;
  // Blockquote
  if (/(^|\n)\s{0,3}>\s/.test(text)) strong += 1;
  // Unordered or ordered list
  if (/(^|\n)\s{0,3}([-*+]|\d+\.)\s/.test(text)) strong += 1;
  // Link [text](url)
  if (/\[[^\]]+\]\([^)]+\)/.test(text)) strong += 1;
  // Bold **text**
  if (/\*\*[^*]+\*\*/.test(text)) strong += 1;

  let weak = 0;
  // Inline code `code`
  if (/`[^`\n]+`/.test(text)) weak += 0.5;
  // Italic *text* or _text_ (the most common false positive in tool output)
  if (/(^|\s)\*[^*\n]+\*(?=\s|$)|(^|\s)_[^_\n]+_(?=\s|$)/.test(text)) weak += 0.5;

  return strong >= 1 || weak >= 2;
}
