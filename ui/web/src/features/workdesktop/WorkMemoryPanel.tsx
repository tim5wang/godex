import { useEffect, useRef, useState } from "react";
import { App, Alert, Button, Card, Collapse, Select, Space, Switch, Tag, Typography } from "antd";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useI18n } from "../../i18n";
import { useSettingsStore } from "../../store/settings";
import { useNodeContextStore } from "../../store/nodeContext";
import { getWorkMemory, getWorkMemoryRuns, getWorkMemoryIssues, resolveWorkMemoryIssue, organizeWorkContext, revertWorkMemoryRun, updateWorkMemorySchedule, workContextQueryKey, type WorkFact, type WorkMemoryIssue } from "../../lib/apiWorkContext";
import { formatTimestamp } from "./workDesktopView";

export function WorkMemoryPanel({ contextId, readOnly, onChanged, facts = [] }: { contextId: string; readOnly: boolean; onChanged: () => void; facts?: WorkFact[] }) {
  const { t } = useI18n();
  const tr = (key: string, vars?: Record<string, string | number>) => t(`workDesktop.${key}`, vars);
  const token = useSettingsStore((state) => state.token) || null;
  const nodeID = useNodeContextStore((state) => state.nodeID);
  const { message } = App.useApp();
  const client = useQueryClient();
  const [historyOpen, setHistoryOpen] = useState(false);
  const onChangedRef = useRef(onChanged);
  onChangedRef.current = onChanged;
  const stateKey = workContextQueryKey(token, nodeID, "memory", contextId);
  const historyKey = workContextQueryKey(token, nodeID, "memory-runs", contextId);
  const issuesKey = workContextQueryKey(token, nodeID, "memory-issues", contextId);
  const issues = useQuery({ queryKey: issuesKey, queryFn: () => getWorkMemoryIssues(token, contextId), refetchInterval: 30000 });
  const state = useQuery({
    queryKey: stateKey, queryFn: () => getWorkMemory(token, contextId),
    refetchInterval: (query) => query.state.data?.status === "running" ? 2000 : 30000,
  });
  const history = useQuery({
    queryKey: historyKey, queryFn: () => getWorkMemoryRuns(token, contextId), enabled: historyOpen,
    refetchInterval: historyOpen && state.data?.status === "running" ? 2000 : false,
  });
  const completion = `${contextId}:${state.data?.processed_seq}:${state.data?.status}`;
  const lastCompletion = useRef("");
  useEffect(() => {
    if (state.data?.status === "completed" && lastCompletion.current !== completion) {
      lastCompletion.current = completion;
      onChangedRef.current();
      void client.invalidateQueries({ queryKey: historyKey });
      void client.invalidateQueries({ queryKey: issuesKey });
    }
  }, [completion, state.data?.status, client]);
  const fail = (error: unknown) => void message.error(error instanceof Error ? error.message : tr("saveFailed"));
  const organize = useMutation({
    mutationFn: (force: boolean) => organizeWorkContext(token, contextId, force),
    onSuccess: (result) => {
      void message.info(tr(result.queued ? "organized" : result.reason === "already_running" ? "dreamRunning" : "dreamUnchanged"));
      void state.refetch();
      void client.invalidateQueries({ queryKey: historyKey });
    }, onError: fail,
  });
  const schedule = useMutation({
    mutationFn: ({ enabled, interval, timeout }: { enabled: boolean; interval: number; timeout?: number }) => updateWorkMemorySchedule(token, contextId, enabled, interval, timeout),
    onSuccess: (result) => client.setQueryData(stateKey, result), onError: fail,
  });
  const resolve = useMutation({
    mutationFn: ({ issue, keep }: { issue: WorkMemoryIssue; keep: string }) => resolveWorkMemoryIssue(token, contextId, issue, keep),
    onSuccess: () => { void issues.refetch(); void state.refetch(); onChangedRef.current(); },
    onError: fail,
  });
  const revert = useMutation({
    mutationFn: (id: string) => revertWorkMemoryRun(token, contextId, id),
    onSuccess: () => { void state.refetch(); void history.refetch(); void issues.refetch(); onChangedRef.current(); void message.success(tr("dreamReverted")); }, onError: fail,
  });
  if (state.isError) return <Alert type="warning" title={tr("dreamUnavailable")} />;
  const memory = state.data;
  const running = memory?.status === "running" && (!memory.lease_until || Date.parse(memory.lease_until) > Date.now());
  const dirty = !!memory && (memory.source_seq > memory.processed_seq || memory.rescan_cursor >= 0);
  const status = running ? "dreamRunning" : memory?.status === "failed" ? "dreamFailed" : dirty ? "dreamPending" : "dreamCurrent";
  const latestCompleted = history.data?.find((run) => run.status === "completed")?.id;
  return <Card size="small" title={tr("dreamTitle")} loading={state.isLoading} extra={<Tag color={running ? "processing" : dirty ? "gold" : "default"}>{tr(status)}</Tag>}>
    <Space orientation="vertical" size={12} style={{ width: "100%" }}>
      <Typography.Text type="secondary">{tr("dreamHint")}</Typography.Text>
      <Space wrap>
        <Switch aria-label={tr("dreamAuto")} checked={memory?.enabled ?? false} loading={schedule.isPending} disabled={readOnly || !memory} onChange={(enabled) => schedule.mutate({ enabled, interval: memory?.interval_seconds ?? 21600 })} />
        <span>{tr("dreamAuto")}</span>
        <Select aria-label={tr("dreamInterval")} value={memory?.interval_seconds ?? 21600} disabled={readOnly || !memory || schedule.isPending} onChange={(interval) => schedule.mutate({ enabled: memory?.enabled ?? true, interval })} options={[
          { value: 3600, label: tr("dreamHourly") }, { value: 21600, label: tr("dreamSixHours") }, { value: 86400, label: tr("dreamDaily") },
        ]} />
      </Space>
      <Space wrap><span>{tr("dreamTimeout")}</span><Select aria-label={tr("dreamTimeout")} disabled={readOnly || running || schedule.isPending} value={memory?.timeout_seconds ?? 180} onChange={(timeout) => schedule.mutate({ enabled: memory?.enabled ?? true, interval: memory?.interval_seconds ?? 21600, timeout })} options={[60,90,180,300,600].map((value) => ({ value, label: `${value}s` }))} /></Space>
      <Space wrap>
        <Button size="small" disabled={readOnly || running || !memory} loading={organize.isPending} onClick={() => organize.mutate(false)}>{tr("organize")}</Button>
        <Button size="small" disabled={readOnly || running || !memory} onClick={() => organize.mutate(true)}>{tr("dreamFull")}</Button>
      </Space>
      {memory?.last_error && <Alert type="warning" title={memory.last_error} />}
      {memory?.summary && memory.status === "completed" && <div><Typography.Text strong>{tr("dreamDerived")}</Typography.Text><Typography.Paragraph style={{ whiteSpace: "pre-wrap", marginBottom: 0 }}>{memory.summary}</Typography.Paragraph></div>}
      {issues.isError && <Alert type="warning" title={tr("dreamIssuesUnavailable")} />}
      {!!issues.data?.length && <div><Typography.Text strong>{tr("dreamIssues")}</Typography.Text><Typography.Paragraph type="secondary">{tr("dreamIssuesHint")}</Typography.Paragraph>{issues.data.map((issue) => <Card key={issue.id} size="small" style={{ marginTop: 8 }}>{issue.facts.map((fact) => <div key={fact.id} style={{ marginBottom: 12 }}><Typography.Text strong>{fact.title}</Typography.Text><Typography.Paragraph style={{ whiteSpace: "pre-wrap", marginBottom: 4 }}>{fact.content}</Typography.Paragraph><Typography.Paragraph type="secondary">{fact.source}</Typography.Paragraph><Button size="small" disabled={readOnly || running || resolve.isPending} onClick={() => resolve.mutate({ issue, keep: fact.id })}>{tr("dreamKeepThis")}</Button></div>)}</Card>)}</div>}
      <Collapse size="small" onChange={(keys) => setHistoryOpen(keys.length > 0)} items={[{ key: "history", label: tr("dreamHistory"), children: <Space orientation="vertical" style={{ width: "100%" }}>
        {history.data?.map((run) => <div key={run.id}>
          <Space wrap><Tag color={run.result?.skipped_candidates?.length ? "gold" : undefined}>{tr(run.status === "completed" ? run.result?.skipped_candidates?.length ? "dreamCompletedWithSkipped" : "dreamCompleted" : run.status === "reverted" ? "dreamReverted" : run.status === "failed" ? "dreamFailed" : "dreamRunning")}</Tag><Typography.Text type="secondary">{formatTimestamp(run.created_at)}</Typography.Text>
            {run.id === latestCompleted && <Button size="small" disabled={readOnly || running || dirty} loading={revert.isPending} onClick={() => revert.mutate(run.id)}>{tr("dreamRevert")}</Button>}
          </Space>
          {run.result?.summary && <Typography.Paragraph style={{ whiteSpace: "pre-wrap" }}>{run.result.summary}</Typography.Paragraph>}
          {run.result?.metrics && <Typography.Paragraph type="secondary">{tr("dreamTiming", { seconds: (run.result.metrics.model_ms / 1000).toFixed(1), bytes: run.result.metrics.input_bytes, timeout: run.result.metrics.timeout_seconds })}</Typography.Paragraph>}
          {run.result?.mutations?.map((change, index) => <Typography.Paragraph key={`change:${index}`} type="secondary">{tr(change.reason === "duplicate" ? "dreamMerged" : change.reason === "conflict" ? "dreamConflictGrouped" : change.reason === "observation" ? "dreamObservation" : "dreamArchived")} · {change.before.title}{change.canonical_id ? ` → ${facts.find((f) => f.id === change.canonical_id)?.title || change.canonical_id}` : ""}</Typography.Paragraph>)}
          {run.result?.references?.map((reference, index) => <Alert key={`reference:${index}`} type="info" title={`${tr("dreamReference")} · ${reference.title}`} description={reference.content} style={{ marginBottom: 8 }} />)}
          {(["duplicates", "conflicts"] as const).map((kind) => run.result?.[kind]?.map((ids, index) => <Typography.Paragraph key={`${kind}:${index}`} type="secondary">{tr(kind === "duplicates" ? "dreamDuplicates" : "dreamConflicts")} · {ids.map((id) => facts.find((fact) => fact.id === id)?.title || id).join(" / ")}</Typography.Paragraph>))}
          {run.result?.skipped_candidates?.map((candidate, index) => <Alert key={`skipped:${index}`} type="warning" title={candidate.title} description={tr(candidate.reason === "missing_applicability" ? "dreamSkippedMissingScope" : "dreamSkippedMixedScope")} style={{ marginBottom: 8 }} />)}
          {run.error && <Typography.Text type="danger">{run.error}</Typography.Text>}
        </div>)}
      </Space> }]} />
    </Space>
  </Card>;
}
