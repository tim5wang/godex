import { Alert, Space, Tag, Typography } from "antd";
import { CheckCircleOutlined, MinusCircleOutlined } from "@ant-design/icons";
import { acceptancePackage, type AcceptanceCard } from "./workDesktopView";

/**
 * AcceptancePanel is the acceptance package for one delegated card: everything a
 * person needs to decide "do I accept this?" without opening the execution
 * transcript.
 *
 * It exists because accepting used to mean reading the run's whole timeline. The
 * card already carried the answers — the executor's summary, a per-item evidence
 * string on the checklist, and the paths the run reported touching — the desktop
 * simply rendered none of it.
 *
 * The one rule it never breaks: never present a CLAIM as proof. A ticked checkbox
 * with no recorded evidence is shown as exactly that, declared paths (gate 1) are
 * listed apart from observed ones (gate 3), and when nothing was evidenced the
 * panel says so outright rather than letting a green tick imply verification.
 */
export function AcceptancePanel(props: {
  card: AcceptanceCard | undefined | null;
  /** The frozen snapshot's acceptance text, used only when the card has no list. */
  fallback: string;
  tr: (key: string, vars?: Record<string, string | number>) => string;
}) {
  const { card, fallback, tr } = props;
  const pkg = acceptancePackage(card);

  const hasChecks = pkg.checks.length > 0;
  const hasAnyContent = pkg.summary || hasChecks || pkg.observedPaths.length || pkg.declaredPaths.length
    || pkg.open.length || pkg.conflicts.length;
  if (!hasAnyContent) {
    return <Typography.Paragraph type="secondary">{fallback || tr("noItems")}</Typography.Paragraph>;
  }

  return (
    <Space direction="vertical" size={8} className="wd-accept">
      {/* What was done. Absent when no execution ever ran: the brief must not be
          dressed up as a result. */}
      {pkg.summary ? <section className="wd-accept-part">
        <div className="wd-muted">{tr("acceptSummary")}</div>
        {/* pre-wrap: a run's summary carries its own line breaks, and collapsing
            them is how a status report becomes an unreadable run-on sentence. */}
        <Typography.Paragraph className="whitespace-pre-wrap break-words">{pkg.summary}</Typography.Paragraph>
      </section> : null}

      <section className="wd-accept-part">
        <div className="wd-muted">{tr("acceptance")}</div>
        {hasChecks ? <ul className="wd-accept-checks">
          {pkg.checks.map((check, index) => <li key={index}>
            <Space size={4} align="start">
              {check.done
                ? <CheckCircleOutlined className="wd-accept-done" aria-label={tr("acceptDone")} />
                : <MinusCircleOutlined className="wd-accept-todo" aria-label={tr("acceptTodo")} />}
              <span>
                {check.text}
                {/* The evidence string is the whole point: it is how a tick becomes
                    checkable. Rendered with its own line breaks (logs, command
                    output) and marked as a quote so it reads as recorded, not authored. */}
                {check.evidence
                  ? <Typography.Text type="secondary" className="wd-accept-evidence whitespace-pre-wrap break-words">“{check.evidence}”</Typography.Text>
                  : check.done ? <Typography.Text type="warning" className="wd-accept-evidence"> {tr("acceptNoItemEvidence")}</Typography.Text> : null}
              </span>
            </Space>
          </li>)}
        </ul> : <Typography.Paragraph type="secondary">{fallback || tr("noItems")}</Typography.Paragraph>}
        {/* The honest bottom line, stated only when it is true. */}
        {hasChecks && !pkg.hasEvidence
          ? <Alert type="warning" showIcon title={tr("acceptNoEvidence")} description={tr("acceptNoEvidenceHint")} />
          : null}
      </section>

      {/* Observed vs declared, kept apart on purpose: gate 3 is what the run
          reported, gate 1 is what someone claimed before running. */}
      {pkg.observedPaths.length ? <section className="wd-accept-part">
        <div className="wd-muted">{tr("acceptObserved")}</div>
        <Space size={4} wrap>{pkg.observedPaths.map((path) => <Tag key={path}>{path}</Tag>)}</Space>
      </section> : null}
      {pkg.declaredPaths.length ? <section className="wd-accept-part">
        <div className="wd-muted">{tr("acceptDeclared")}</div>
        <Space size={4} wrap>{pkg.declaredPaths.map((path) => <Tag key={path} color="default">{path}</Tag>)}</Space>
      </section> : null}

      {pkg.open.length ? <section className="wd-accept-part">
        <div className="wd-muted">{tr("acceptOpen")}</div>
        <ul className="wd-accept-open">{pkg.open.map((item, index) => <li key={index}>{item}</li>)}</ul>
      </section> : null}

      {/* Cross-card conflicts found when the card entered review: adjudicating
          these IS the reviewer's job, so they must not be buried. */}
      {pkg.conflicts.length ? <Alert type="error" showIcon title={tr("acceptConflicts")}
        description={<ul className="wd-accept-conflicts">{pkg.conflicts.map((conflict, index) => <li key={index}>
          {conflict.other ? tr("acceptConflictLine", { path: conflict.path, other: conflict.other }) : conflict.path}
        </li>)}</ul>}
      /> : null}
    </Space>
  );
}
