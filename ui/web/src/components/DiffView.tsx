import { useMemo } from "react";
import { Alert } from "antd";

interface DiffViewProps {
  diff: string;
  /** Cap lines rendered to avoid huge diffs freezing the UI. */
  maxLines?: number;
  /** Show old/new line-number gutters. Off by default so existing compact
   *  call sites (the chat Changes card) keep their current look. */
  showLineNumbers?: boolean;
  /** Optional baseline caption, e.g. "HEAD → working tree". */
  baselineLabel?: string;
  /** Rendered before the diff when git itself reported a problem. */
  notice?: string;
}

type DiffLineKind = "hunk" | "add" | "del" | "meta" | "context";

interface DiffLine {
  kind: DiffLineKind;
  text: string;
  /** 1-based line number in the pre-image (undefined for added lines). */
  oldLine?: number;
  /** 1-based line number in the post-image (undefined for removed lines). */
  newLine?: number;
}

/**
 * Renders a unified diff (as produced by `git diff --no-color`) with
 * per-line add/remove highlighting. Pure presentation: no external diff
 * library — the unified format is simple enough to parse in ~40 lines.
 *
 * Line numbers are reconstructed from the `@@ -old,count +new,count @@`
 * hunk headers rather than guessed, so the gutter stays correct across
 * multiple hunks with gaps between them.
 */
export function DiffView({ diff, maxLines = 500, showLineNumbers = false, baselineLabel, notice }: DiffViewProps) {
  const lines = useMemo(() => parseUnifiedDiff(diff, maxLines), [diff, maxLines]);
  const truncated = lines.length >= maxLines;

  if (!diff.trim()) {
    return <Alert type="info" showIcon message={notice || "No diff"} />;
  }

  return (
    <div className="diff-view" role="region" aria-label="Diff">
      {baselineLabel ? (
        <div className="diff-view-baseline" data-testid="diff-view-baseline">
          {baselineLabel}
        </div>
      ) : null}
      {notice ? (
        <div className="diff-view-baseline" data-testid="diff-view-notice">
          {notice}
        </div>
      ) : null}
      {truncated ? (
        <div className="diff-view-truncated">Diff truncated to {maxLines} lines</div>
      ) : null}
      <pre className="diff-view-pre">
        {lines.map((line, index) => (
          <div key={index} className={`diff-line diff-line-${line.kind}`} data-kind={line.kind}>
            {showLineNumbers ? (
              <>
                <span className="diff-line-no" aria-hidden="true">
                  {line.oldLine ?? ""}
                </span>
                <span className="diff-line-no" aria-hidden="true">
                  {line.newLine ?? ""}
                </span>
              </>
            ) : null}
            <span className="diff-line-text">{line.text}</span>
          </div>
        ))}
      </pre>
    </div>
  );
}

const HUNK_HEADER = /^@@ -(\d+)(?:,(\d+))? \+(\d+)(?:,(\d+))? @@/;

/** Parse unified diff text into typed lines, stopping at maxLines. */
export function parseUnifiedDiff(diff: string, maxLines: number): DiffLine[] {
  const result: DiffLine[] = [];
  const raw = diff.split("\n");
  // Line cursors for the pre- and post-image; a hunk header resets both.
  let oldCursor = 0;
  let newCursor = 0;
  for (let i = 0; i < raw.length && result.length < maxLines; i++) {
    const line = raw[i];
    const hunk = HUNK_HEADER.exec(line);
    if (hunk) {
      oldCursor = Number(hunk[1]);
      newCursor = Number(hunk[3]);
      result.push({ kind: "hunk", text: line });
      continue;
    }
    if (line.startsWith("+++") || line.startsWith("---") || line.startsWith("diff --git") || line.startsWith("index ") || line.startsWith("new file mode") || line.startsWith("deleted file mode") || line.startsWith("similarity index") || line.startsWith("rename from") || line.startsWith("rename to")) {
      result.push({ kind: "meta", text: line });
    } else if (line.startsWith("+")) {
      result.push({ kind: "add", text: line, newLine: newCursor++ });
    } else if (line.startsWith("-")) {
      result.push({ kind: "del", text: line, oldLine: oldCursor++ });
    } else if (line.startsWith("\\")) {
      // "\ No newline at end of file"
      result.push({ kind: "meta", text: line });
    } else {
      result.push({ kind: "context", text: line, oldLine: oldCursor++, newLine: newCursor++ });
    }
  }
  return result;
}
