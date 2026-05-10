import { Suspense, lazy } from "react";

const MarkdownRenderer = lazy(async () => ({ default: (await import("./MarkdownRenderer")).MarkdownRenderer }));

interface MarkdownContentProps {
  content: string;
  className?: string;
  forceMarkdown?: boolean;
}

export function MarkdownContent({ content, className, forceMarkdown = false }: MarkdownContentProps) {
  if (!forceMarkdown && !needsMarkdownRendering(content)) {
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

function needsMarkdownRendering(content: string) {
  const text = content.trim();
  if (!text) {
    return false;
  }
  return /(^|\n)\s{0,3}(#{1,6}\s|[-*+]\s|\d+\.\s|>\s)|```|`[^`\n]+`|\[[^\]]+\]\([^)]+\)|\*\*[^*]+\*\*|\*[^*\n]+\*|_[^_]+_|(^|\n)\|.+\|/.test(text);
}
