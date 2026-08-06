// joinCommand builds the one-line onboarding command that a node operator
// pastes into a remote (intranet) godex runtime to join this center.
// Keeping this in a pure module makes it unit-testable without a DOM.

export interface JoinCommandOptions {
  centerURL: string;
  nodeID: string;
  credential: string;
  trustLevel?: string;
  name?: string;
}

/** Single-quote a value for POSIX shell, escaping embedded single quotes. */
export function quoteForShell(value: string): string {
  return "'" + value.replace(/'/g, "'\\''") + "'";
}

/**
 * Build the `godex node join` command line. The center URL and name may
 * contain shell metacharacters, so they are single-quoted; nodeID and
 * credential are validated to be shell-safe tokens.
 */
export function buildJoinCommand(opts: JoinCommandOptions): string {
  const { centerURL, nodeID, credential } = opts;
  if (!centerURL || !nodeID || !credential) {
    throw new Error("centerURL, nodeID and credential are required");
  }
  const parts = ["godex", "node", "join", quoteForShell(centerURL), "--id", nodeID, "--credential", credential];
  const trustLevel = (opts.trustLevel ?? "trusted").trim();
  if (trustLevel) {
    parts.push("--trust", trustLevel);
  }
  const name = (opts.name ?? "").trim();
  if (name) {
    parts.push("--name", quoteForShell(name));
  }
  return parts.join(" ");
}
