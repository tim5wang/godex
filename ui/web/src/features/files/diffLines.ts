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
export function computeLineDiff(oldText: string, newText: string): string {
  if (oldText === newText) {
    return oldText;
  }
  const oldLines = oldText.split("\n");
  const newLines = newText.split("\n");
  const n = oldLines.length;
  const m = newLines.length;

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
  return out.join("\n");
}
