import { Alert, Button, Drawer, Empty, List, Popconfirm, Segmented, Space, Tag, Tooltip, Typography } from "antd";
import { CheckOutlined, EyeOutlined, PlayCircleOutlined, StopOutlined } from "@ant-design/icons";
import type { MouseEvent } from "react";
import type { DurableSubagentReview } from "../../lib/types";
import type { ReviewMergeFilter, ReviewMergeItem, ReviewMergeSummary } from "./reviewMergeCenter";
import { buildReviewMergeSafety, filterReviewMergeItems, reviewMergeStatusColor, reviewMergeStatusLabel } from "./reviewMergeCenter";

interface ReviewMergeCenterPanelProps {
  open: boolean;
  summary: ReviewMergeSummary;
  filter: ReviewMergeFilter;
  selectedJobId?: string;
  review?: DurableSubagentReview | null;
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
  const visibleItems = filterReviewMergeItems(props.summary.items, props.filter);
  const selected = visibleItems.find((item) => item.jobId === props.selectedJobId) ?? visibleItems[0] ?? props.summary.items.find((item) => item.jobId === props.selectedJobId);
  return (
    <Drawer
      title="Review & Merge"
      width={860}
      open={props.open}
      onClose={props.onClose}
      className="review-merge-drawer"
      extra={<ReviewMergeCounters summary={props.summary} />}
    >
      <div className="review-merge-layout">
        <section className="review-merge-queue">
          <Segmented
            size="small"
            value={props.filter}
            onChange={(value) => props.onFilterChange(value as ReviewMergeFilter)}
            options={[
              { value: "reviewable", label: "Ready" },
              { value: "conflicted", label: "Conflicted" },
              { value: "merged", label: "Merged" },
              { value: "failed", label: "Failed" },
              { value: "all", label: "All" },
            ]}
            className="review-merge-filter"
          />
          <List
            size="small"
            dataSource={visibleItems}
            locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No workers to review" /> }}
            renderItem={(item) => (
              <List.Item
                className={item.jobId === selected?.jobId ? "review-merge-queue-item-selected" : "review-merge-queue-item"}
                onClick={() => props.onSelectJob(item.jobId)}
                actions={[<ReviewMergeActions key="actions" item={item} {...props} />]}
              >
                <List.Item.Meta
                  title={
                    <Space size={6} wrap>
                      <Tag color={reviewMergeStatusColor(item.status)}>{reviewMergeStatusLabel(item.status)}</Tag>
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
            <ReviewMergeDetail item={selected} review={props.review} loading={props.reviewingJobId === selected.jobId} />
          ) : (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="Select a worker" />
          )}
        </section>
      </div>
    </Drawer>
  );
}

function ReviewMergeCounters({ summary }: { summary: ReviewMergeSummary }) {
  return (
    <Space size={6} wrap>
      <Tag color={summary.ready ? "blue" : "default"}>{summary.ready} ready</Tag>
      <Tag color={summary.blocked ? "gold" : "default"}>{summary.blocked} blocked</Tag>
      <Tag color={summary.merged ? "green" : "default"}>{summary.merged} merged</Tag>
      <Tag color={summary.failed ? "red" : "default"}>{summary.failed} failed</Tag>
    </Space>
  );
}

function ReviewMergeActions(props: ReviewMergeCenterPanelProps & { item: ReviewMergeItem }) {
  const { item } = props;
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
        <Tooltip title="Load review">
          <Button size="small" type="text" icon={<EyeOutlined />} loading={props.reviewingJobId === item.jobId} onClick={() => props.onReview(item.jobId)} />
        </Tooltip>
      ) : null}
      {canMerge ? (
        <Popconfirm
          title={item.status === "conflicted" ? "This worker has conflicts. Merge anyway?" : "Merge this worker's changes into the workspace?"}
          onConfirm={() => props.onMerge(item.jobId)}
        >
          <Tooltip title="Merge">
            <Button size="small" type="text" icon={<CheckOutlined />} loading={props.mergingJobId === item.jobId} />
          </Tooltip>
        </Popconfirm>
      ) : null}
      {canResume ? (
        <Tooltip title="Resume">
          <Button size="small" type="text" icon={<PlayCircleOutlined />} loading={props.resumingJobId === item.jobId} onClick={() => props.onResume(item.jobId)} />
        </Tooltip>
      ) : null}
      {canCancel ? (
        <Popconfirm title="Cancel this worker?" onConfirm={() => props.onCancel(item.jobId)}>
          <Tooltip title="Cancel">
            <Button size="small" danger type="text" icon={<StopOutlined />} loading={props.cancelingJobId === item.jobId} />
          </Tooltip>
        </Popconfirm>
      ) : null}
    </Space>
  );
}

function ReviewMergeDetail({ item, review, loading }: { item: ReviewMergeItem; review?: DurableSubagentReview | null; loading?: boolean }) {
  const safety = buildReviewMergeSafety(item, review);
  if (loading && !review) {
    return <Alert type="info" showIcon message="Loading review..." />;
  }
  if (!review || review.job_id !== item.jobId) {
    return (
      <Space direction="vertical" size={12} style={{ width: "100%" }}>
        <ReviewMergeSafetyBar safety={safety} />
        <Alert type="info" showIcon message="Review not loaded" description="Load review to inspect changed files and diff before merge." />
      </Space>
    );
  }
  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      <ReviewMergeSafetyBar safety={safety} />
      {review.conflicts?.length ? <Alert type="warning" showIcon message="Merge conflicts" description={review.conflicts.join("\n")} /> : null}
      {review.diff_truncated ? <Alert type="warning" showIcon message="Diff is truncated" description="Use the changed file list and worktree path for deeper inspection." /> : null}
      <div className="review-merge-file-list">
        {review.changes.length ? (
          review.changes.map((change) => (
            <Space key={`${change.status}:${change.path}`} size={6} wrap>
              <Tag>{change.status}</Tag>
              <Typography.Text code>{change.path}</Typography.Text>
              {change.binary ? <Tag>binary</Tag> : null}
            </Space>
          ))
        ) : (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No changed files" />
        )}
      </div>
      <pre className="review-merge-diff">{review.diff?.trim() || "No diff"}</pre>
    </Space>
  );
}

function ReviewMergeSafetyBar({ safety }: { safety: ReturnType<typeof buildReviewMergeSafety> }) {
  return (
    <div className="review-merge-safety-bar">
      <Tag color={safety.diffStatus === "truncated" ? "gold" : safety.diffStatus === "complete" ? "green" : "default"}>
        Diff {safety.diffStatus.replace("_", " ")}
      </Tag>
      <Tag color={safety.conflictStatus === "conflicts" ? "volcano" : safety.conflictStatus === "none" ? "green" : "default"}>
        {safety.conflictStatus === "conflicts" ? "Conflicts" : safety.conflictStatus === "none" ? "No conflicts" : "Conflicts unknown"}
      </Tag>
      <Tag>{safety.changedFiles} files</Tag>
      <Tag>{safety.writeScope.length ? safety.writeScope.join(", ") : "scope unknown"}</Tag>
      {safety.mergeCaution ? <Tag color="gold">extra confirmation</Tag> : null}
    </div>
  );
}
