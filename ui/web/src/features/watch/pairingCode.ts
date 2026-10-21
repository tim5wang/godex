/**
 * Pairing-code helpers (mockup 10, trusted side).
 *
 * The node generates an 8-digit user code and displays it on the watch as two
 * groups of four ("9903 7225"). The web form must therefore accept what the user
 * actually sees — spaces, and pasted text with stray characters — without making
 * them clean it up by hand. The node normalises the same way server-side
 * (`watchauth.normalizeUserCode` strips everything non-digit), so this is about
 * matching that rule in the UI rather than inventing a second one.
 */

/** Digits in a pairing code, matching watchauth.userCodeDigits. */
export const PAIRING_CODE_LENGTH = 8;

/**
 * Strips everything that is not a digit, and caps the length.
 *
 * Capping matters for a controlled input: without it a paste of a long string
 * would fill the field and the user would have to delete the overflow.
 */
export function normalizePairingCode(raw: string): string {
  return (raw ?? "").replace(/\D/g, "").slice(0, PAIRING_CODE_LENGTH);
}

/** Groups the digits for display, e.g. "99037225" -> "9903 7225". */
export function formatPairingCode(raw: string): string {
  const digits = normalizePairingCode(raw);
  if (digits.length <= 4) {
    return digits;
  }
  return `${digits.slice(0, 4)} ${digits.slice(4)}`;
}

/**
 * True when the code is complete enough to submit.
 *
 * Length is the only client-side check: whether the code is *valid* is the
 * node's answer, and a wrong-but-well-formed code must reach the server to get
 * its "invalid or expired" response rather than an error invented here.
 */
export function isPairingCodeComplete(raw: string): boolean {
  return normalizePairingCode(raw).length === PAIRING_CODE_LENGTH;
}
