import { Alert, Button, Drawer, Empty, List, Popconfirm, Segmented, Space, Tag, Tooltip, Typography } from "antd";
import { CheckOutlined, CopyOutlined, EyeOutlined, PlayCircleOutlined, StopOutlined } from "@ant-design/icons";
import { useEffect, useMemo, useState, type MouseEvent } from "react";
import type { DurableSubagentMerge, DurableSubagentReview } from "../../lib/types";
import type { TaskOutcome } from "./taskCenterOutcome";
import type { ReviewMergeFilter, ReviewMergeItem, ReviewMergeSummary } from "./reviewMergeCenter";
import {
  buildReviewMergeDiffPreview,
  buildReviewMergeMergeResult,
  buildReviewMergeOutcomeTrail,
  buildReviewMergeSafety,
  filterReviewMergeItems,
  reviewMergeStatusColor,
  reviewMergeStatusLabel,
} from "./reviewMergeCenter";
import { useReviewMergeText, reviewMergeStatusI18n, type ReviewMergeText } from "./reviewMergeCenter.i18n";

interface ReviewMergeCenterPanelProps {
  open: boolean;
  summary: ReviewMergeSummary;
  filter: ReviewMergeFilter;
  selectedJobId?: string;
  outcomes?: TaskOutcome[];
  review?: DurableSubagentReview | null;
  mergeResult?: DurableSubagentMerge | null;
  reviewingJobId?: string;
  mergingJobId?: string;
  resumingJobId?: string;
  cancelingJobId?: string;
  onClose: () => void;
  onFilterChange: (filter: ReviewMergeFilter) => void;
  onSelectJob: (jobId: string) => void;
  onReview: (jobId: string) => void;
  onMerge: (jobId: string) => void;
  onResume: (jobId: string) => void;
  onCancel: (jobId: string) => void;
}

export function ReviewMergeCenterPanel(props: ReviewMergeCenterPanelProps) {
  const t = useReviewMergeText();
  const visibleItems = filterReviewMergeItems(props.summary.items, props.filter);
  const selected = visibleItems.find((item) => item.jobId === props.selectedJobId) ?? visibleItems[0] ?? props.summary.items.find((item) => item.jobId === props.selectedJobId);
  return (
    <Drawer
      title={t.title}
      width={860}
      open={props.open}
      onClose={props.onClose}
      className="review-merge-drawer"
      extra={<ReviewMergeCounters summary={props.summary} t={t} />}
    >
      <div className="review-merge-layout">
        <section className="review-merge-queue">
          <Segmented
            size="small"
            value={props.filter}
            onChange={(value) => props.onFilterChange(value as ReviewMergeFilter)}
            options={[
              { value: "reviewable", label: t.ready },
              { value: "conflicted", label: t.conflicted },
              { value: "merged", label: t.merged },
              { value: "failed", label: t.failed },
              { value: "all", label: t.all },
            ]}
            className="review-merge-filter"
          />
          <List
            size="small"
            dataSource={visibleItems}
            locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t.noWorkers} /> }}
            renderItem={(item) => (
              <List.Item
                className={item.jobId === selected?.jobId ? "review-merge-queue-item-selected" : "review-merge-queue-item"}
                onClick={() => props.onSelectJob(item.jobId)}
                actions={[<ReviewMergeActions key="actions" item={item} t={t} {...props} />]}
              >
                <List.Item.Meta
                  title={
                    <Space size={6} wrap>
                      <Tag color={reviewMergeStatusColor(item.status)}>{reviewMergeStatusI18n(item.status, t)}</Tag>
                      <Typography.Text strong ellipsis className="review-merge-title">
                        {item.title}
                      </Typography.Text>
                    </Space>
                  }
                  description={
                    <Space direction="vertical" size={2} className="review-merge-row-detail">
                      <Typography.Text type="secondary" ellipsis={{ tooltip: item.detail || item.jobId }}>
                        {item.detail || item.jobId}
                      </Typography.Text>
                      <Typography.Text code ellipsis={{ tooltip: item.writeScope.length ? item.writeScope.join(", ") : item.jobId }}>
                        {item.writeScope.length ? item.writeScope.join(", ") : item.jobId}
                      </Typography.Text>
                    </Space>
                  }
                />
              </List.Item>
            )}
          />
        </section>
        <section className="review-merge-detail">
          {selected ? (
            <ReviewMergeDetail
              item={selected}
              outcomes={props.outcomes ?? []}
              review={props.review}
              mergeResult={props.mergeResult}
              loading={props.reviewingJobId === selected.jobId}
              t={t}
            />
          ) : (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t.selectWorker} />
          )}
        </section>
      </div>
    </Drawer>
  );
}

function ReviewMergeCounters({ summary, t }: { summary: ReviewMergeSummary; t: ReviewMergeText }) {
  return (
    <Space size={6} wrap>
      <Tag color={summary.ready ? "blue" : "default"}>{summary.ready} {t.readyCount}</Tag>
      <Tag color={summary.blocked ? "gold" : "default"}>{summary.blocked} {t.blockedCount}</Tag>
      <Tag color={summary.merged ? "green" : "default"}>{summary.merged} {t.mergedCount}</Tag>
      <Tag color={summary.failed ? "red" : "default"}>{summary.failed} {t.failedCount}</Tag>
    </Space>
  );
}

function ReviewMergeActions(props: ReviewMergeCenterPanelProps & { item: ReviewMergeItem; t: ReviewMergeText }) {
  const { item, t } = props;
  const workerStatus = (item.workerStatus || "").toLowerCase();
  const reviewLoaded = props.review?.job_id === item.jobId;
  const canReview = !!item.jobId && workerStatus !== "running";
  const canMerge = canReview && reviewLoaded && item.status !== "merged" && item.status !== "no_changes";
  const canCancel = ["running", "pending", "resuming"].includes(workerStatus);
  const canResume = ["canceled", "cancelled", "interrupted", "error", "failed"].includes(workerStatus);
  const stop = (event: MouseEvent) => event.stopPropagation();

  return (
    <Space size={4} onClick={stop}>
      {canReview ? (
        <Tooltip title={t.loadReview}>
          <Button size="small" type="text" icon={<EyeOutlined />} loading={props.reviewingJobId === item.jobId} onClick={() => props.onReview(item.jobId)} />
        </Tooltip>
      ) : null}
      {canMerge ? (
        <Popconfirm
          title={item.status === "conflicted" ? t.mergeConflicted : t.mergeConfirm}
          onConfirm={() => props.onMerge(item.jobId)}
        >
          <Tooltip title={t.merge}>
            <Button size="small" type="text" icon={<CheckOutlined />} loading={props.mergingJobId === item.jobId} />
          </Tooltip>
        </Popconfirm>
      ) : null}
      {canResume ? (
        <Tooltip title={t.resume}>
          <Button size="small" type="text" icon={<PlayCircleOutlined />} loading={props.resumingJobId === item.jobId} onClick={() => props.onResume(item.jobId)} />
        </Tooltip>
      ) : null}
      {canCancel ? (
        <Popconfirm title={t.cancelWorker} onConfirm={() => props.onCancel(item.jobId)}>
          <Tooltip title={t.cancel}>
            <Button size="small" danger type="text" icon={<StopOutlined />} loading={props.cancelingJobId === item.jobId} />
          </Tooltip>
        </Popconfirm>
      ) : null}
    </Space>
  );
}

function ReviewMergeDetail({
  item,
  outcomes,
  review,
  mergeResult,
  loading,
  t,
}: {
  item: ReviewMergeItem;
  outcomes: TaskOutcome[];
  review?: DurableSubagentReview | null;
  mergeResult?: DurableSubagentMerge | null;
  loading?: boolean;
  t: ReviewMergeText;
}) {
  const [diffExpanded, setDiffExpanded] = useState(false);
  const [activePath, setActivePath] = useState("");
  const safety = buildReviewMergeSafety(item, review);
  const diffPreview = buildReviewMergeDiffPreview(review, 8000, diffExpanded);
  const trail = buildReviewMergeOutcomeTrail(item, outcomes, review, mergeResult);
  const merge = buildReviewMergeMergeResult(item, mergeResult);
  const diffSections = useMemo(() => buildDiffSections(diffPreview.fullDiff || diffPreview.diff, diffPreview.files), [diffPreview.diff, diffPreview.files, diffPreview.fullDiff]);

  useEffect(() => {
    setDiffExpanded(false);
    setActivePath("");
  }, [item.jobId, review?.job_id]);

  if (loading && !review) {
    return <Alert type="info" showIcon message={t.loadingReview} />;
  }
  if (!review || review.job_id !== item.jobId) {
    return (
      <Space direction="vertical" size={12} style={{ width: "100%" }}>
        <ReviewMergeTrail trail={trail} t={t} />
        <ReviewMergeSafetyBar safety={safety} t={t} />
        <Alert type="info" showIcon message={t.reviewNotLoaded} description={t.reviewNotLoadedDesc} />
      </Space>
    );
  }
  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      <ReviewMergeTrail trail={trail} t={t} />
      <ReviewMergeSafetyBar safety={safety} t={t} />
      {merge ? <ReviewMergeResultPanel merge={merge} t={t} /> : null}
      {review.conflicts?.length ? <Alert type="warning" showIcon message={t.mergeConflicts} description={review.conflicts.join("\n")} /> : null}
      {review.diff_truncated ? <Alert type="warning" showIcon message={t.diffTruncated} description={t.diffTruncatedDesc} /> : null}
      <div className="review-merge-file-list">
        {review.changes.length ? (
          review.changes.map((change) => (
            <Space key={`${change.status}:${change.path}`} size={6} wrap>
              <Tag>{change.status}</Tag>
              <Button
                size="small"
                type="link"
                className="review-merge-file-jump"
                onClick={() => {
                  setDiffExpanded(true);
                  setActivePath(change.path);
                  window.setTimeout(() => document.getElementById(diffSectionId(change.path))?.scrollIntoView({ block: "start" }), 0);
                }}
              >
                <Typography.Text code>{change.path}</Typography.Text>
              </Button>
              <Tooltip title={t.copyPath}>
                <Button size="small" type="text" icon={<CopyOutlined />} onClick={() => void copyText(change.path)} />
              </Tooltip>
              {change.binary ? <Tag>{t.binary}</Tag> : null}
            </Space>
          ))
        ) : (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t.noChangedFiles} />
        )}
      </div>
      <div className="review-merge-diff-toolbar">
        <Space size={6} wrap>
          {diffPreview.large ? (
            <Button size="small" onClick={() => setDiffExpanded((value) => !value)}>
              {diffExpanded ? t.collapseDiff : t.showFullDiff}
            </Button>
          ) : null}
          <Button size="small" icon={<CopyOutlined />} onClick={() => void copyText(diffPreview.fullDiff)}>
            {t.copyDiff}
          </Button>
          {activePath ? <Tag color="blue">{t.focused} {activePath}</Tag> : null}
        </Space>
      </div>
      <div className="review-merge-diff">
        {diffPreview.large && !diffExpanded ? (
          <pre>{diffPreview.diff}</pre>
        ) : diffSections.length ? (
          diffSections.map((section) => (
            <section key={section.id} id={section.id} className="review-merge-diff-section">
              {section.path ? <Typography.Text code>{section.path}</Typography.Text> : null}
              <pre>{section.diff}</pre>
            </section>
          ))
        ) : (
          <pre>{diffPreview.diff || t.noDiff}</pre>
        )}
      </div>
    </Space>
  );
}

function ReviewMergeTrail({ trail, t }: { trail: ReturnType<typeof buildReviewMergeOutcomeTrail>; t: ReviewMergeText }) {
  return (
    <div className="review-merge-trail">
      {trail.steps.map((step) => (
        <Tag key={step.label} color={trailToneColor(step.tone)}>
          {step.label}
        </Tag>
      ))}
      {trail.recovered ? <Tag color="green">{t.recovered}</Tag> : null}
    </div>
  );
}

function ReviewMergeResultPanel({ merge, t }: { merge: NonNullable<ReturnType<typeof buildReviewMergeMergeResult>>; t: ReviewMergeText }) {
  return (
    <Alert
      type={merge.conflicts.length ? "warning" : "success"}
      showIcon
      message={`${t.mergeStatus} ${merge.status}`}
      description={
        <Space direction="vertical" size={6}>
          <Typography.Text>{merge.appliedCount} {t.appliedFiles}{merge.appliedCount === 1 ? "" : "s"}</Typography.Text>
          {merge.worktreeDir ? <Typography.Text code>{merge.worktreeDir}</Typography.Text> : null}
          {merge.applied.map((change) => (
            <Typography.Text key={`${change.status}:${change.path}`} code>
              {change.status} {change.path}
            </Typography.Text>
          ))}
          {merge.conflicts.map((conflict) => (
            <Typography.Text key={conflict} type="warning">
              {conflict}
            </Typography.Text>
          ))}
        </Space>
      }
    />
  );
}

function ReviewMergeSafetyBar({ safety, t }: { safety: ReturnType<typeof buildReviewMergeSafety>; t: ReviewMergeText }) {
  return (
    <div className="review-merge-safety-bar">
      <Tag color={safety.diffStatus === "truncated" ? "gold" : safety.diffStatus === "complete" ? "green" : "default"}>
        {t.diffLabel} {safety.diffStatus === "not_loaded" ? t.notLoaded : safety.diffStatus === "complete" ? t.complete : t.truncated}
      </Tag>
      <Tag color={safety.conflictStatus === "conflicts" ? "volcano" : safety.conflictStatus === "none" ? "green" : "default"}>
        {safety.conflictStatus === "conflicts" ? t.conflicts : safety.conflictStatus === "none" ? t.noConflicts : t.conflictsUnknown}
      </Tag>
      <Tag>{safety.changedFiles} {t.files}</Tag>
      <Tag>{safety.writeScope.length ? safety.writeScope.join(", ") : t.scopeUnknown}</Tag>
      {safety.mergeCaution ? <Tag color="gold">{t.extraConfirmation}</Tag> : null}
    </div>
  );
}

function buildDiffSections(diff: string, files: Array<{ path: string }>) {
  const lines = diff.split("\n");
  const sections: Array<{ id: string; path: string; diff: string }> = [];
  let current: { id: string; path: string; lines: string[] } | null = null;
  const byPath = new Map(files.map((file) => [file.path, file.path]));

  for (const line of lines) {
    const nextPath = diffHeaderPath(line, byPath);
    if (nextPath) {
      if (current) {
        sections.push({ id: current.id, path: current.path, diff: current.lines.join("\n") });
      }
      current = { id: diffSectionId(nextPath), path: nextPath, lines: [line] };
      continue;
    }
    if (current) {
      current.lines.push(line);
    }
  }
  if (current) {
    sections.push({ id: current.id, path: current.path, diff: current.lines.join("\n") });
  }
  if (sections.length || !diff.trim()) {
    return sections;
  }
  return [{ id: "review-merge-diff-full", path: "", diff }];
}

function diffHeaderPath(line: string, paths: Map<string, string>) {
  const gitMatch = /^diff --git a\/(.+) b\/(.+)$/.exec(line);
  if (gitMatch?.[2]) {
    return paths.get(gitMatch[2]) || gitMatch[2];
  }
  const plusMatch = /^\+\+\+ b\/(.+)$/.exec(line);
  if (plusMatch?.[1]) {
    return paths.get(plusMatch[1]) || plusMatch[1];
  }
  return "";
}

function diffSectionId(path: string) {
  return `review-diff-${path.replace(/[^a-zA-Z0-9_-]+/g, "-")}`;
}

async function copyText(text: string) {
  if (!text || typeof navigator === "undefined" || !navigator.clipboard) {
    return;
  }
  await navigator.clipboard.writeText(text);
}

function trailToneColor(tone: ReturnType<typeof buildReviewMergeOutcomeTrail>["steps"][number]["tone"]) {
  switch (tone) {
    case "processing":
      return "processing";
    case "success":
      return "green";
    case "warning":
      return "gold";
    case "danger":
      return "red";
    default:
      return "default";
  }
}
