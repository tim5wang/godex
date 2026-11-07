import { Space } from "antd";
import { BarChartOutlined } from "@ant-design/icons";
import { FoldCard } from "./workDesktopFold";
import { InfoHint } from "./workDesktopHints";
import type { AttentionMetrics } from "../../lib/apiWorkContext";

/**
 * AttentionLedger is the attention ledger: how much of today's notification budget
 * is spent, and how much unread work is open.
 *
 * It is COLLAPSED BY DEFAULT, deliberately. These numbers answer "is my attention
 * being spent well?", which is worth asking occasionally and is not worth screen
 * space every time — a permanently expanded dashboard becomes one more thing to
 * read, which is the opposite of what it is for. Folding it away is the whole
 * "support hiding it" requirement: no extra dismiss affordance, no persisted
 * preference, and the attention board is unchanged for anyone who never opens it.
 *
 * Every figure is derived from data the inbox already reads, so it is not a second
 * source of truth and can never disagree with the list above it.
 */
export function AttentionLedger(props: {
  metrics: AttentionMetrics;
  tr: (key: string, vars?: Record<string, string | number>) => string;
}) {
  const { metrics, tr } = props;

  return (
    <FoldCard
      className="wd-metrics"
      defaultOpen={false}
      openLabel={tr("foldExpand")}
      closeLabel={tr("foldCollapse")}
      title={<Space size={6}><BarChartOutlined aria-hidden="true" />{tr("metricsTitle")}<InfoHint title={tr("showHint")} text={tr("metricsHint")} /></Space>}
    >
      <div className="wd-metrics-grid">
        <div className="wd-metrics-cell">
          <div className="wd-muted">{tr("metricsNudges")}</div>
          {/* Reported as "used of cap" so a bare count always has a scale to read
              against — a number without its limit invites the wrong conclusion. */}
          <strong>{tr("metricsNudgesValue", { used: metrics.nudges_today, cap: metrics.owner_cap })}</strong>
          <div className="wd-muted">{tr("metricsPerStream", { cap: metrics.per_stream_cap })}</div>
        </div>
        <div className="wd-metrics-cell">
          <div className="wd-muted">{tr("metricsUnread")}</div>
          <strong>{metrics.unread_total}</strong>
        </div>
        <div className="wd-metrics-cell">
          <div className="wd-muted">{tr("metricsAged")}</div>
          <strong>{metrics.aged_streams}</strong>
        </div>
        {/* Decisions made today — the denominator of the yield ratio. Shown next to
            the supply-side numbers so "how much was spent" and "what did I decide"
            are read together; either alone invites the wrong conclusion. */}
        <div className="wd-metrics-cell">
          <div className="wd-muted">{tr("metricsDecisions")}</div>
          <strong>{metrics.actions_today}</strong>
        </div>
      </div>
    </FoldCard>
  );
}
