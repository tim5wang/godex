import { countColumn, type EditorState, type Text } from "@codemirror/state";
import { foldService } from "@codemirror/language";

type FoldRange = { from: number; to: number };
const cache = new WeakMap<Text, { tabSize: number; ranges: Map<number, FoldRange> }>();

// ponytail: indentation is a structural fallback, not a grammar. Use only for
// text/stream/basic modes; native language parsers keep their precise folds.
// Build once per immutable document in O(lines), not once per gutter line.
export function indentationFold(state: EditorState, lineStart: number): FoldRange | null {
  const cached = cache.get(state.doc);
  let ranges = cached?.tabSize === state.tabSize ? cached.ranges : undefined;
  if (!ranges) {
    ranges = new Map();
    const stack: { indent: number; from: number; start: number }[] = [];
    let previousEnd = 0;
    for (let n = 1; n <= state.doc.lines; n++) {
      const line = state.doc.line(n);
      if (!line.text.trim()) continue;
      const indent = countColumn(line.text.match(/^[\t ]*/)?.[0] ?? "", state.tabSize);
      while (stack.length && stack[stack.length - 1].indent >= indent) {
        const head = stack.pop()!;
        if (previousEnd > head.from) ranges.set(head.start, { from: head.from, to: previousEnd });
      }
      stack.push({ indent, from: line.to, start: line.from });
      previousEnd = line.to;
    }
    for (const head of stack) {
      if (previousEnd > head.from) ranges.set(head.start, { from: head.from, to: previousEnd });
    }
    cache.set(state.doc, { tabSize: state.tabSize, ranges });
  }
  return ranges.get(lineStart) ?? null;
}

export const indentationFolding = foldService.of(indentationFold);
