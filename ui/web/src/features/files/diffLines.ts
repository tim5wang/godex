/**
 * Lightweight line-level diff for the Files panel "preview changes" view.
 *
 * Semantics:
 *   - identical inputs → the original text unchanged (no prefixes)
 *   - otherwise → one line per source line, prefixed:
 *       " " context, "+" added, "-" removed
 *
 * The output feeds the shared <DiffView> component (unified-diff styling)
 * without depending on an external diff library.
 */

/**
 * Cap on LCS table cells (middle-section lines × lines).
 *
 * The table is quadratic, so a large file edit used to allocate lines² cells
 * and block the main thread for seconds — this runs synchronously inside a
 * render, so the whole panel froze. Beyond this budget the middle section is
 * reported as a plain replacement block: the diff loses intra-block alignment
 * but stays O(n) and always renders.
 *
 * 1M cells ≈ 1000×1000 changed lines, far above a normal edit once the common
 * prefix/suffix have been trimmed away.
 */
export const MAX_LCS_CELLS = 1_000_000;

export function computeLineDiff(oldText: string, newText: string): string {
  if (oldText === newText) {
    return oldText;
  }
  const oldLines = oldText.split("\n");
  const newLines = newText.split("\n");

  // Strip the common prefix and suffix first. A typical edit touches a handful
  // of lines in a long file, so this alone usually reduces the LCS input to a
  // few lines and keeps the quadratic part tiny.
  const oldLen = oldLines.length;
  const newLen = newLines.length;
  let prefix = 0;
  while (prefix < oldLen && prefix < newLen && oldLines[prefix] === newLines[prefix]) {
    prefix++;
  }
  let suffix = 0;
  while (
    suffix < oldLen - prefix &&
    suffix < newLen - prefix &&
    oldLines[oldLen - 1 - suffix] === newLines[newLen - 1 - suffix]
  ) {
    suffix++;
  }

  const out: string[] = [];
  for (let i = 0; i < prefix; i++) {
    out.push(" " + oldLines[i]);
  }

  const oldMiddle = oldLines.slice(prefix, oldLen - suffix);
  const newMiddle = newLines.slice(prefix, newLen - suffix);

  if (oldMiddle.length * newMiddle.length > MAX_LCS_CELLS) {
    // Too big to align line by line: show the whole changed region.
    for (const line of oldMiddle) out.push("-" + line);
    for (const line of newMiddle) out.push("+" + line);
  } else {
    out.push(...lcsDiff(oldMiddle, newMiddle));
  }

  for (let i = oldLen - suffix; i < oldLen; i++) {
    out.push(" " + oldLines[i]);
  }
  return out.join("\n");
}

/** Classic LCS backtrack over two line arrays. */
function lcsDiff(oldLines: string[], newLines: string[]): string[] {
  const n = oldLines.length;
  const m = newLines.length;
  if (n === 0) return newLines.map((line) => "+" + line);
  if (m === 0) return oldLines.map((line) => "-" + line);

  // LCS table (row-major, bottom-up).
  const dp: number[][] = Array.from({ length: n + 1 }, () => new Array<number>(m + 1).fill(0));
  for (let i = n - 1; i >= 0; i--) {
    for (let j = m - 1; j >= 0; j--) {
      if (oldLines[i] === newLines[j]) {
        dp[i][j] = dp[i + 1][j + 1] + 1;
      } else {
        dp[i][j] = Math.max(dp[i + 1][j], dp[i][j + 1]);
      }
    }
  }

  const out: string[] = [];
  let i = 0;
  let j = 0;
  while (i < n && j < m) {
    if (oldLines[i] === newLines[j]) {
      out.push(" " + oldLines[i]);
      i++;
      j++;
    } else if (dp[i + 1][j] >= dp[i][j + 1]) {
      out.push("-" + oldLines[i]);
      i++;
    } else {
      out.push("+" + newLines[j]);
      j++;
    }
  }
  while (i < n) {
    out.push("-" + oldLines[i]);
    i++;
  }
  while (j < m) {
    out.push("+" + newLines[j]);
    j++;
  }
  return out;
}
