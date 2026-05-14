# GoDex Web Review & Merge Center Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build a Web UI Review & Merge Center that turns completed durable subagent jobs into a clear review queue with diff inspection, merge decisions, conflicts, and applied results.

**Architecture:** Keep this as a frontend-first MVP on top of the existing session subagent APIs. Add a derived review queue view model, a focused Review & Merge panel, and reuse the existing `reviewSessionSubagent` / `mergeSessionSubagent` mutations from `ChatPage` without changing backend routes or persisted schemas.

**Tech Stack:** React 19, TypeScript, Ant Design, TanStack Query, existing GoDex Web API client, lightweight Node TypeScript tests using `node --experimental-strip-types`.

---

## Product Scope

### MVP User Story

When a worker finishes, the user should be able to open one Web UI surface and answer:

- What workers are waiting for review?
- What files changed?
- Is the diff complete or truncated?
- Are there conflicts?
- What has already been merged or had no changes?
- What action is safe next: review, merge, resume, cancel, or inspect?

### Non-Goals

- No new backend API.
- No new persisted schema.
- No Monaco editor or side-by-side code review.
- No multi-worker batch merge in MVP.
- No graph canvas.
- No auto-merge policy.
- No change to CLI, TUI, HTTP, or session storage behavior.

## File Structure

- Create `ui/web/src/features/chat/reviewMergeCenter.ts`
  - Pure TypeScript view model for review queue items, counters, filtering, and status classification.
- Create `ui/web/src/features/chat/ReviewMergeCenterPanel.tsx`
  - React UI for queue, details, diff preview, conflicts, and actions.
- Modify `ui/web/src/features/chat/TaskCenterPanel.tsx`
  - Add a single entry button from Task Center Review section into the Review & Merge Center.
- Modify `ui/web/src/features/chat/ChatPage.tsx`
  - Own open/close state, selected job ID, review result cache, and mutation wiring for the new panel.
- Modify `ui/web/src/styles.css`
  - Add compact, dense operational styles for the center.
- Create `ui/web/test/reviewMergeCenter.test.ts`
  - Tests for classification, ordering, counters, and safe filtering.
- Modify `docs/superpowers/specs/2026-05-14-godex-web-task-center-design.md`
  - Add a short implementation note once MVP is implemented.

## Data Contracts

Use existing frontend types:

- `DurableSubagentJob`
- `DurableSubagentReview`
- `DurableSubagentMerge`
- `DurableSubagentFileChange`
- `FeedItem`

The Review & Merge view model should accept `FeedItem[]` because `ChatPage` already merges persisted subagents and live timeline subagent overlays into `subagentJobs`.

Create these internal-only types:

```ts
export type ReviewMergeStatus =
  | "running"
  | "blocked"
  | "ready"
  | "review_loaded"
  | "merged"
  | "no_changes"
  | "conflicted"
  | "failed";

export interface ReviewMergeItem {
  id: string;
  jobId: string;
  status: ReviewMergeStatus;
  title: string;
  detail?: string;
  worker: FeedItem;
  changedPathCount: number;
  writeScope: string[];
  mergeStatus?: string;
  workerStatus?: string;
  updatedAt?: string;
  finishedAt?: string;
}

export interface ReviewMergeSummary {
  items: ReviewMergeItem[];
  ready: number;
  merged: number;
  blocked: number;
  failed: number;
  running: number;
}

export interface BuildReviewMergeSummaryOptions {
  reviewedJobId?: string;
}
```

## Task 1: Review Queue View Model

**Files:**
- Create: `ui/web/src/features/chat/reviewMergeCenter.ts`
- Create: `ui/web/test/reviewMergeCenter.test.ts`

- [ ] **Step 1: Write failing tests for status classification**

Create `ui/web/test/reviewMergeCenter.test.ts`:

```ts
import assert from "node:assert/strict";
import { buildReviewMergeSummary, reviewMergeStatusLabel } from "../src/features/chat/reviewMergeCenter.ts";
import type { FeedItem } from "../src/lib/types.ts";

function worker(partial: Partial<FeedItem>): FeedItem {
  return {
    id: partial.jobId || partial.id || "subagent-1",
    kind: "subagent",
    title: "worker",
    body: "",
    ...partial,
  };
}

{
  const summary = buildReviewMergeSummary([
    worker({
      jobId: "job-ready",
      title: "Implement UI",
      status: "completed",
      writeScope: ["ui/web/src"],
      updatedAt: "2026-05-14T10:00:00Z",
    }),
    worker({
      jobId: "job-merged",
      title: "Docs",
      status: "completed",
      mergeStatus: "merged",
      updatedAt: "2026-05-14T11:00:00Z",
    }),
    worker({
      jobId: "job-blocked",
      title: "Needs approval",
      status: "pending_approval",
      updatedAt: "2026-05-14T12:00:00Z",
    }),
  ]);

  assert.equal(summary.ready, 1);
  assert.equal(summary.merged, 1);
  assert.equal(summary.blocked, 1);
  assert.equal(summary.items[0]?.jobId, "job-blocked");
  assert.equal(summary.items[1]?.jobId, "job-ready");
  assert.equal(summary.items[2]?.jobId, "job-merged");
  assert.equal(reviewMergeStatusLabel("ready"), "Ready for review");
}

{
  const summary = buildReviewMergeSummary([
    worker({ jobId: "job-no-changes", status: "completed", mergeStatus: "no_changes" }),
    worker({ jobId: "job-failed", status: "failed", error: "tests failed" }),
    worker({ jobId: "job-running", status: "running" }),
  ]);

  assert.equal(summary.merged, 1);
  assert.equal(summary.failed, 1);
  assert.equal(summary.running, 1);
  assert.equal(summary.items.find((item) => item.jobId === "job-no-changes")?.status, "no_changes");
}

console.log("reviewMergeCenter tests passed");
```

- [ ] **Step 2: Run test and confirm it fails**

Run:

```bash
node --experimental-strip-types ui/web/test/reviewMergeCenter.test.ts
```

Expected:

```text
Error [ERR_MODULE_NOT_FOUND]: Cannot find module .../reviewMergeCenter.ts
```

- [ ] **Step 3: Implement `reviewMergeCenter.ts`**

Create `ui/web/src/features/chat/reviewMergeCenter.ts`:

```ts
import type { FeedItem } from "../../lib/types";

export type ReviewMergeStatus =
  | "running"
  | "blocked"
  | "ready"
  | "review_loaded"
  | "merged"
  | "no_changes"
  | "conflicted"
  | "failed";

export interface ReviewMergeItem {
  id: string;
  jobId: string;
  status: ReviewMergeStatus;
  title: string;
  detail?: string;
  worker: FeedItem;
  changedPathCount: number;
  writeScope: string[];
  mergeStatus?: string;
  workerStatus?: string;
  updatedAt?: string;
  finishedAt?: string;
}

export interface ReviewMergeSummary {
  items: ReviewMergeItem[];
  ready: number;
  merged: number;
  blocked: number;
  failed: number;
  running: number;
}

export interface BuildReviewMergeSummaryOptions {
  reviewedJobId?: string;
}

export function buildReviewMergeSummary(workers: FeedItem[] = [], options: BuildReviewMergeSummaryOptions = {}): ReviewMergeSummary {
  const items = workers
    .filter((worker) => worker.kind === "subagent" && (worker.jobId || worker.id))
    .map((worker) => reviewMergeItemFromWorker(worker, options.reviewedJobId))
    .sort(compareReviewMergeItems);

  return {
    items,
    ready: items.filter((item) => item.status === "ready" || item.status === "review_loaded" || item.status === "conflicted").length,
    merged: items.filter((item) => item.status === "merged" || item.status === "no_changes").length,
    blocked: items.filter((item) => item.status === "blocked").length,
    failed: items.filter((item) => item.status === "failed").length,
    running: items.filter((item) => item.status === "running").length,
  };
}

export function reviewMergeStatusLabel(status: ReviewMergeStatus) {
  switch (status) {
    case "running":
      return "Running";
    case "blocked":
      return "Blocked";
    case "ready":
      return "Ready for review";
    case "review_loaded":
      return "Review loaded";
    case "merged":
      return "Merged";
    case "no_changes":
      return "No changes";
    case "conflicted":
      return "Conflicted";
    case "failed":
      return "Failed";
  }
}

export function reviewMergeStatusColor(status: ReviewMergeStatus) {
  switch (status) {
    case "running":
      return "processing";
    case "blocked":
      return "gold";
    case "ready":
    case "review_loaded":
      return "blue";
    case "merged":
    case "no_changes":
      return "green";
    case "conflicted":
      return "volcano";
    case "failed":
      return "red";
  }
}

function reviewMergeItemFromWorker(worker: FeedItem, reviewedJobId?: string): ReviewMergeItem {
  const jobId = worker.jobId || worker.id;
  const status = classifyWorker(worker);
  return {
    id: `review-merge:${jobId}`,
    jobId,
    status: status === "ready" && reviewedJobId === jobId ? "review_loaded" : status,
    title: firstNonBlank(worker.displayTitle, worker.title, worker.objective, worker.jobId, worker.id, "Worker"),
    detail: firstNonBlank(worker.lastMessage, worker.error, worker.summary, worker.body),
    worker,
    changedPathCount: worker.writeScope?.length ?? 0,
    writeScope: worker.writeScope ?? [],
    mergeStatus: worker.mergeStatus,
    workerStatus: worker.status,
    updatedAt: worker.updatedAt,
    finishedAt: worker.finishedAt,
  };
}

function classifyWorker(worker: FeedItem): ReviewMergeStatus {
  const status = (worker.status || "").toLowerCase();
  const merge = (worker.mergeStatus || "").toLowerCase();
  if (merge === "merged") {
    return "merged";
  }
  if (merge === "no_changes") {
    return "no_changes";
  }
  if (merge === "conflicted" || merge === "conflicts") {
    return "conflicted";
  }
  if (status === "pending_approval") {
    return "blocked";
  }
  if (status === "running" || status === "pending" || status === "resuming") {
    return "running";
  }
  if (status === "completed") {
    return "ready";
  }
  if (["failed", "error", "cancelled", "canceled", "interrupted", "timeout"].includes(status)) {
    return "failed";
  }
  return "ready";
}

function compareReviewMergeItems(left: ReviewMergeItem, right: ReviewMergeItem) {
  const rank = (item: ReviewMergeItem) => {
    switch (item.status) {
      case "blocked":
        return 0;
      case "conflicted":
        return 1;
      case "ready":
      case "review_loaded":
        return 2;
      case "running":
        return 3;
      case "failed":
        return 4;
      case "merged":
      case "no_changes":
        return 5;
    }
  };
  const rankDelta = rank(left) - rank(right);
  if (rankDelta !== 0) {
    return rankDelta;
  }
  return itemTime(right) - itemTime(left);
}

function firstNonBlank(...values: Array<string | undefined>) {
  return values.find((value) => value?.trim())?.trim() || "";
}

function itemTime(item: ReviewMergeItem) {
  const value = Date.parse(item.updatedAt || item.finishedAt || "");
  return Number.isFinite(value) ? value : 0;
}
```

- [ ] **Step 4: Run view model test**

Run:

```bash
node --experimental-strip-types ui/web/test/reviewMergeCenter.test.ts
```

Expected:

```text
reviewMergeCenter tests passed
```

## Task 2: Review & Merge Panel UI

**Files:**
- Create: `ui/web/src/features/chat/ReviewMergeCenterPanel.tsx`
- Modify: `ui/web/src/styles.css`

- [ ] **Step 1: Add panel component**

Create `ui/web/src/features/chat/ReviewMergeCenterPanel.tsx`:

```tsx
import { Alert, Button, Drawer, Empty, List, Popconfirm, Space, Tag, Tooltip, Typography } from "antd";
import { CheckOutlined, EyeOutlined, PlayCircleOutlined, StopOutlined } from "@ant-design/icons";
import type { DurableSubagentReview } from "../../lib/types";
import type { ReviewMergeItem, ReviewMergeSummary } from "./reviewMergeCenter";
import { reviewMergeStatusColor, reviewMergeStatusLabel } from "./reviewMergeCenter";

interface ReviewMergeCenterPanelProps {
  open: boolean;
  summary: ReviewMergeSummary;
  selectedJobId?: string;
  review?: DurableSubagentReview | null;
  reviewingJobId?: string;
  mergingJobId?: string;
  resumingJobId?: string;
  cancelingJobId?: string;
  onClose: () => void;
  onSelectJob: (jobId: string) => void;
  onReview: (jobId: string) => void;
  onMerge: (jobId: string) => void;
  onResume: (jobId: string) => void;
  onCancel: (jobId: string) => void;
}

export function ReviewMergeCenterPanel(props: ReviewMergeCenterPanelProps) {
  const selected = props.summary.items.find((item) => item.jobId === props.selectedJobId) ?? props.summary.items[0];
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
          <List
            size="small"
            dataSource={props.summary.items}
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
                      <Typography.Text strong>{item.title}</Typography.Text>
                    </Space>
                  }
                  description={
                    <Space direction="vertical" size={2}>
                      <Typography.Text type="secondary">{item.detail || item.jobId}</Typography.Text>
                      <Typography.Text code>{item.writeScope.length ? item.writeScope.join(", ") : item.jobId}</Typography.Text>
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
  const canReview = !!item.jobId && workerStatus !== "running";
  const canMerge = canReview && item.status !== "merged" && item.status !== "no_changes";
  const canCancel = ["running", "pending", "resuming"].includes(workerStatus);
  const canResume = ["canceled", "cancelled", "interrupted", "error", "failed"].includes(workerStatus);

  return (
    <Space size={4}>
      {canReview ? (
        <Tooltip title="Load review">
          <Button size="small" type="text" icon={<EyeOutlined />} loading={props.reviewingJobId === item.jobId} onClick={() => props.onReview(item.jobId)} />
        </Tooltip>
      ) : null}
      {canMerge ? (
        <Popconfirm title="Merge this worker's changes?" onConfirm={() => props.onMerge(item.jobId)}>
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
        <Tooltip title="Cancel">
          <Button size="small" danger type="text" icon={<StopOutlined />} loading={props.cancelingJobId === item.jobId} onClick={() => props.onCancel(item.jobId)} />
        </Tooltip>
      ) : null}
    </Space>
  );
}

function ReviewMergeDetail({ item, review, loading }: { item: ReviewMergeItem; review?: DurableSubagentReview | null; loading?: boolean }) {
  if (loading && !review) {
    return <Alert type="info" showIcon message="Loading review..." />;
  }
  if (!review || review.job_id !== item.jobId) {
    return <Alert type="info" showIcon message="Review not loaded" description="Load review to inspect changed files and diff before merge." />;
  }
  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
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
```

- [ ] **Step 2: Add panel styles**

Append to `ui/web/src/styles.css`:

```css
.review-merge-layout {
  display: grid;
  grid-template-columns: minmax(260px, 340px) minmax(0, 1fr);
  gap: 16px;
  min-height: 0;
}

.review-merge-queue,
.review-merge-detail {
  min-width: 0;
}

.review-merge-queue .ant-list-item {
  cursor: pointer;
  border-radius: 6px;
  padding-inline: 8px;
}

.review-merge-queue-item-selected {
  background: rgba(22, 119, 255, 0.08);
}

.review-merge-file-list {
  display: grid;
  gap: 8px;
}

.review-merge-diff {
  max-height: 52vh;
  overflow: auto;
  margin: 0;
  padding: 12px;
  border: 1px solid #d9d9d9;
  border-radius: 6px;
  background: #f6f8fa;
  font-size: 12px;
  line-height: 1.5;
  white-space: pre-wrap;
  word-break: break-word;
}

@media (max-width: 860px) {
  .review-merge-layout {
    grid-template-columns: 1fr;
  }
}
```

- [ ] **Step 3: Build to catch JSX/type errors**

Run:

```bash
pnpm --dir ui/web build
```

Expected: TypeScript and Vite build complete. Existing vendor chunk warning is acceptable.

## Task 3: Wire Panel Into ChatPage

**Files:**
- Modify: `ui/web/src/features/chat/ChatPage.tsx`
- Modify: `ui/web/src/features/chat/TaskCenterPanel.tsx`

- [ ] **Step 1: Add imports and state in `ChatPage.tsx`**

Add imports:

```ts
import { ReviewMergeCenterPanel } from "./ReviewMergeCenterPanel";
import { buildReviewMergeSummary } from "./reviewMergeCenter";
```

Add state near existing review drawer state:

```ts
const [reviewMergeOpen, setReviewMergeOpen] = useState(false);
const [reviewMergeSelectedJobId, setReviewMergeSelectedJobId] = useState<string>("");
```

Add memo after `subagentJobs`:

```ts
const reviewMergeSummary = useMemo(
  () => buildReviewMergeSummary(subagentJobs, { reviewedJobId: subagentReview?.job_id }),
  [subagentJobs, subagentReview?.job_id],
);
```

- [ ] **Step 2: Add open handler**

Add inside `ChatPage` near other local handlers:

```ts
const openReviewMergeCenter = (jobId?: string) => {
  setReviewMergeSelectedJobId(jobId || reviewMergeSummary.items[0]?.jobId || "");
  setReviewMergeOpen(true);
};
```

- [ ] **Step 3: Pass launcher into `TaskCenterPanel`**

Update `TaskCenterPanelProps` in `ui/web/src/features/chat/TaskCenterPanel.tsx`:

```ts
  onOpenReviewMergeCenter: (jobId?: string) => void;
```

Add a button in the card `extra` area before collapse:

```tsx
<Button size="small" type="text" icon={<EyeOutlined />} onClick={() => props.onOpenReviewMergeCenter()}>
  Review
</Button>
```

In `ChatPage.tsx`, update the `TaskCenterPanel` invocation:

```tsx
onOpenReviewMergeCenter={openReviewMergeCenter}
```

- [ ] **Step 4: Render the drawer**

Render after existing `TaskCenterPanel` or near existing drawers:

```tsx
<ReviewMergeCenterPanel
  open={reviewMergeOpen}
  summary={reviewMergeSummary}
  selectedJobId={reviewMergeSelectedJobId}
  review={subagentReview}
  reviewingJobId={reviewSubagentMutation.variables}
  mergingJobId={mergeSubagentMutation.variables}
  resumingJobId={resumeSubagentMutation.variables}
  cancelingJobId={cancelSubagentMutation.variables}
  onClose={() => setReviewMergeOpen(false)}
  onSelectJob={(jobId) => setReviewMergeSelectedJobId(jobId)}
  onReview={(jobId) => {
    setReviewMergeSelectedJobId(jobId);
    reviewSubagentMutation.mutate(jobId);
  }}
  onMerge={(jobId) => {
    setReviewMergeSelectedJobId(jobId);
    mergeSubagentMutation.mutate(jobId);
  }}
  onResume={(jobId) => resumeSubagentMutation.mutate(jobId)}
  onCancel={(jobId) => cancelSubagentMutation.mutate(jobId)}
/>
```

- [ ] **Step 5: Keep old review drawer behavior intact**

Do not remove the existing subagent review drawer yet. This MVP can have both:

- Existing drawer remains the deep-dive path launched from inspector cards.
- New Review & Merge Center is the queue-first path launched from Task Center.

If the same `subagentReview` state is shared, it is acceptable for both surfaces to show the last loaded review.

- [ ] **Step 6: Build**

Run:

```bash
pnpm --dir ui/web build
```

Expected: Build succeeds.

## Task 4: Add Review Detail Behavior Tests

**Files:**
- Modify: `ui/web/test/reviewMergeCenter.test.ts`
- Modify: `ui/web/src/features/chat/reviewMergeCenter.ts`

- [ ] **Step 1: Add tests for filtering and conflicts**

Append to `ui/web/test/reviewMergeCenter.test.ts`:

```ts
{
  const summary = buildReviewMergeSummary([
    worker({ jobId: "job-conflict", status: "completed", mergeStatus: "conflicted" }),
    worker({ jobId: "job-unrelated", kind: "assistant", id: "assistant-1" }),
  ]);

  assert.equal(summary.items.length, 1);
  assert.equal(summary.items[0]?.status, "conflicted");
  assert.equal(summary.ready, 1);
}
```

- [ ] **Step 2: Run test**

Run:

```bash
node --experimental-strip-types ui/web/test/reviewMergeCenter.test.ts
```

Expected:

```text
reviewMergeCenter tests passed
```

- [ ] **Step 3: Run Web build**

Run:

```bash
pnpm --dir ui/web build
```

Expected: Build succeeds.

## Task 5: UX Polish and Small-Screen Safety

**Files:**
- Modify: `ui/web/src/features/chat/ReviewMergeCenterPanel.tsx`
- Modify: `ui/web/src/styles.css`

- [ ] **Step 1: Ensure queue rows do not overflow**

In queue row title/detail, use `Typography.Text` with `ellipsis` and fixed max-width classes instead of raw long text:

```tsx
<Typography.Text strong ellipsis className="review-merge-title">
  {item.title}
</Typography.Text>
```

Add CSS:

```css
.review-merge-title {
  max-width: 220px;
}
```

- [ ] **Step 2: Make destructive actions explicit**

Keep `Popconfirm` around merge and cancel. Merge confirmation text:

```tsx
<Popconfirm title="Merge this worker's changes into the workspace?" onConfirm={() => props.onMerge(item.jobId)}>
```

Cancel confirmation text:

```tsx
<Popconfirm title="Cancel this worker?" onConfirm={() => props.onCancel(item.jobId)}>
```

- [ ] **Step 3: Verify mobile layout by build and visual inspection**

Run:

```bash
pnpm --dir ui/web build
```

Expected: Build succeeds.

Manual inspection checklist:

- Drawer queue stacks above detail below 860px.
- Long file paths wrap in diff and file list.
- Buttons remain icon-sized.
- Empty state is visible when no subagents exist.

## Task 6: Documentation Update

**Files:**
- Modify: `docs/superpowers/specs/2026-05-14-godex-web-task-center-design.md`

- [ ] **Step 1: Add implementation note**

Append:

```md
## Review & Merge Center Extension

The Review & Merge Center is the Task Center's queue-first deep-dive surface for durable subagent outputs. It stays frontend-only for the MVP and uses existing session subagent endpoints to load reviews, show changed files and diffs, merge worker outputs, resume interrupted workers, and cancel running workers.

It does not add backend routes, persisted task state, batch merge, or a full code review editor. The first version focuses on reducing ambiguity around completed workers: ready, conflicted, merged, no changes, blocked, failed, and running.
```

- [ ] **Step 2: Diff check**

Run:

```bash
git diff --check
```

Expected: no output.

## Task 7: Final Verification

**Files:**
- All touched files.

- [ ] **Step 1: Run focused Web tests**

Run:

```bash
node --experimental-strip-types ui/web/test/taskCenterOutcome.test.ts
node --experimental-strip-types ui/web/test/reviewMergeCenter.test.ts
```

Expected:

```text
taskCenterOutcome tests passed
reviewMergeCenter tests passed
```

- [ ] **Step 2: Run Web build**

Run:

```bash
pnpm --dir ui/web build
```

Expected: build succeeds. Existing vendor chunk-size warning is acceptable.

- [ ] **Step 3: Run backend/TUI regression only if shared files changed**

If only `ui/web/**` and docs changed, skip Go tests. If `internal/**` changed, run:

```bash
go test ./internal/tui -count=1
go test ./...
```

Expected: pass.

- [ ] **Step 4: Run whitespace check**

Run:

```bash
git diff --check
```

Expected: no output.

- [ ] **Step 5: Manual acceptance**

Start Web UI:

```bash
pnpm --dir ui/web dev --host 127.0.0.1 --port 5173
```

Open:

```text
http://127.0.0.1:5173/
```

Acceptance checklist:

- Task Center has a Review entry point.
- Review & Merge drawer opens without changing session state.
- Ready workers appear before merged workers.
- Loading review shows changed files and diff.
- Merge uses confirmation and refreshes subagent state.
- Existing inspector subagent review drawer still works.

## Commit Plan

Suggested commits:

```bash
git add ui/web/src/features/chat/reviewMergeCenter.ts ui/web/test/reviewMergeCenter.test.ts
git commit -m "feat(web): derive review merge queue"

git add ui/web/src/features/chat/ReviewMergeCenterPanel.tsx ui/web/src/features/chat/ChatPage.tsx ui/web/src/features/chat/TaskCenterPanel.tsx ui/web/src/styles.css
git commit -m "feat(web): add review and merge center"

git add docs/superpowers/specs/2026-05-14-godex-web-task-center-design.md docs/superpowers/plans/2026-05-14-godex-web-review-merge-center.md
git commit -m "docs(web): plan review merge center"
```

Keep unrelated untracked files such as `docs/superpowers/tmp/` out of these commits unless the user explicitly asks to include them.

## Self-Review Notes

- The plan keeps Review & Merge frontend-only and reuses existing APIs.
- The queue view model is pure and independently testable.
- The UI is additive: it does not remove the existing inspector drawer.
- The MVP avoids batch merge, backend route design, and diff editor scope creep.
