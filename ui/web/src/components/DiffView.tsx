import { useMemo } from "react";
import { Alert } from "antd";

interface DiffViewProps {
  diff: string;
  /** Cap lines rendered to avoid huge diffs freezing the UI. */
  maxLines?: number;
}

type DiffLineKind = "hunk" | "add" | "del" | "meta" | "context";

interface DiffLine {
  kind: DiffLineKind;
  text: string;
}

/**
 * Renders a unified diff (as produced by `git diff --no-color`) with
 * per-line add/remove highlighting. Pure presentation: no external diff
 * library — the unified format is simple enough to parse in ~20 lines.
 */
export function DiffView({ diff, maxLines = 500 }: DiffViewProps) {
  const lines = useMemo(() => parseUnifiedDiff(diff, maxLines), [diff, maxLines]);
  const truncated = lines.length >= maxLines;

  if (!diff.trim()) {
    return <Alert type="info" showIcon message="No diff" />;
  }

  return (
    <div className="diff-view" role="region" aria-label="Diff">
      {truncated ? (
        <div className="diff-view-truncated">Diff truncated to {maxLines} lines</div>
      ) : null}
      <pre className="diff-view-pre">
        {lines.map((line, index) => (
          <div key={index} className={`diff-line diff-line-${line.kind}`} data-kind={line.kind}>
            {line.text}
          </div>
        ))}
      </pre>
    </div>
  );
}

/** Parse unified diff text into typed lines, stopping at maxLines. */
export function parseUnifiedDiff(diff: string, maxLines: number): DiffLine[] {
  const result: DiffLine[] = [];
  const raw = diff.split("\n");
  for (let i = 0; i < raw.length && result.length < maxLines; i++) {
    const line = raw[i];
    if (line.startsWith("@@")) {
      result.push({ kind: "hunk", text: line });
    } else if (line.startsWith("+++") || line.startsWith("---") || line.startsWith("diff --git") || line.startsWith("index ")) {
      result.push({ kind: "meta", text: line });
    } else if (line.startsWith("+")) {
      result.push({ kind: "add", text: line });
    } else if (line.startsWith("-")) {
      result.push({ kind: "del", text: line });
    } else if (line.startsWith("\\")) {
      // "\ No newline at end of file"
      result.push({ kind: "meta", text: line });
    } else {
      result.push({ kind: "context", text: line });
    }
  }
  return result;
}
