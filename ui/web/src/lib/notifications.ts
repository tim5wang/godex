import { APIError } from "./api";
import { formatBytes } from "./byteSize";
import { translate } from "../i18n";
import { useLocaleStore } from "../store/locale";

type MessageApi = {
  error: (content: string) => unknown;
};

/** QuotaBody is the structured half of a relay quota refusal (429). Every field is
 *  optional because a proxy or an older build may answer with only `error`. */
type QuotaBody = {
  quota?: string;
  used_bytes?: number;
  limit_bytes?: number;
  reset_at?: string;
};

/** quotaMessage renders a user-facing sentence for an exhausted allowance, or null when
 *  the error is not one.
 *
 *  Why this exists: the quota refusal is the ONE error a self-service user is expected to
 *  ACT on (wait for the reset, or ask for a bigger allowance), and the server's own
 *  sentence is English prose that does not carry the numbers. Forwarding it would leave a
 *  Chinese user reading the only message that matters in the wrong language, and without
 *  "8.2 MB of 100 MB" — the two facts that make it actionable.
 *
 *  Requires limit_bytes > 0 so an unlimited tenant can never see it: "unlimited" inside a
 *  refusal would be a contradiction. Other 429s (a full mailbox, a rate limit) fall through
 *  to the plain message, which is right — they have no used/limit to report. */
export function quotaMessage(error: unknown): string | null {
  if (!(error instanceof APIError) || error.status !== 429) {
    return null;
  }
  const body = error.body as QuotaBody | undefined;
  if (!body || typeof body.limit_bytes !== "number" || body.limit_bytes <= 0) {
    return null;
  }
  const reset = body.reset_at ? new Date(body.reset_at).toLocaleString() : "";
  return translate(useLocaleStore.getState().locale, "quota.exceeded", {
    used: formatBytes(body.used_bytes),
    limit: formatBytes(body.limit_bytes),
    reset,
  });
}

export function showError(messageApi: MessageApi, error: unknown, fallback: string) {
  const quota = quotaMessage(error);
  if (quota) {
    void messageApi.error(quota);
    return;
  }
  const text = error instanceof APIError ? error.message : error instanceof Error ? error.message : fallback;
  void messageApi.error(text);
}
