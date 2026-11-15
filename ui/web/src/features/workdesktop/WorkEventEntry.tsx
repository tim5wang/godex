import { useState } from "react";
import { Alert, Button, Drawer, Empty, Space, Spin, Tag, Typography } from "antd";
import { useQuery } from "@tanstack/react-query";
import { useI18n } from "../../i18n";
import { useNodeContextStore } from "../../store/nodeContext";
import { useSettingsStore } from "../../store/settings";
import { getWorkEvent, workContextQueryKey, type WorkCheckpoint, type WorkEvent } from "../../lib/apiWorkContext";
import { useOpenWorkConversation } from "./useWorkDesktopData";
import { useWorkDesktopStore, sectionForTarget } from "./workDesktopStore";
import { formatTimestamp } from "./workDesktopView";

/** One read-on-demand inspector shared by Timeline and Overview. No detail N+1
 * requests on mount; old checkpoint events resolve their exact immutable revision. */
export function WorkEventEntry({ contextId, event }: { contextId: string; event: WorkEvent }) {
  const [open, setOpen] = useState(false);
  const token = useSettingsStore((state) => state.token);
  const nodeID = useNodeContextStore((state) => state.nodeID);
  const { t } = useI18n();
  const tr = (key: string) => t(`workDesktop.${key}`);
  const openConversation = useOpenWorkConversation();
  const setSection = useWorkDesktopStore((state) => state.setSection);
  const detail = useQuery({
    queryKey: workContextQueryKey(token, nodeID, "event", contextId, event.id),
    queryFn: () => getWorkEvent(token || null, contextId, event.id),
    enabled: open && !!contextId,
  });
  const data = detail.data;
  const kindLabel = tr(`eventKind.${event.kind}`);
  const title = event.title.replace(/ · (completed|error|canceled)$/, (_, status: string) => ` · ${tr(`turnOutcome.${status}`)}`);
  const cp = data?.checkpoint;
  const listFields: Array<[keyof WorkCheckpoint, string]> = [
    ["completed", "checkpointCompleted"], ["in_progress", "checkpointInProgress"],
    ["next_actions", "checkpointNext"], ["blockers", "checkpointBlockers"],
    ["open_questions", "checkpointOpenQuestions"], ["evidence_refs", "checkpointEvidenceRefs"],
  ];
  return <>
    <button type="button" className="wd-event-open" aria-label={`${tr("eventDetails")}: ${event.title}`} onClick={() => setOpen(true)}>{title}</button>
    <div className="wd-muted">{kindLabel.startsWith("workDesktop.") ? event.kind : kindLabel} · {formatTimestamp(event.created_at)}</div>
    <Drawer title={tr("eventDetails")} open={open} onClose={() => setOpen(false)} size={560} zIndex={1300} styles={{ wrapper: { maxWidth: "100vw" } }} destroyOnHidden>
      <div className="wd-stack wd-wrap">
        <Typography.Title level={5}>{title}</Typography.Title>
        <Typography.Text type="secondary">{formatTimestamp(event.created_at)}</Typography.Text>
        {detail.isLoading ? <Spin /> : detail.isError ? <Alert type="error" showIcon title={tr("loadFailed")} action={<Button onClick={() => void detail.refetch()}>{tr("retry")}</Button>} /> : <>
          {cp && <>
            <Tag>{tr("checkpoint")} r{cp.revision}</Tag>
            <Typography.Paragraph style={{ whiteSpace: "pre-wrap" }}>{cp.summary || tr("noItems")}</Typography.Paragraph>
            {listFields.map(([field, label]) => {
              const values = cp[field];
              return Array.isArray(values) && values.length ? <div key={field}><Typography.Text strong>{tr(label)}</Typography.Text><ul>{values.map((value, i) => <li key={i} style={{ whiteSpace: "pre-wrap" }}>{value}</li>)}</ul></div> : null;
            })}
          </>}
          {data?.request && <div><Typography.Text strong>{tr("eventRequest")}</Typography.Text><Typography.Paragraph style={{ whiteSpace: "pre-wrap" }}>{data.request}</Typography.Paragraph></div>}
          {data?.result && <div><Typography.Text strong>{tr("eventResult")}</Typography.Text><Typography.Paragraph style={{ whiteSpace: "pre-wrap" }}>{data.result}</Typography.Paragraph></div>}
          {!cp && !data?.request && !data?.result && <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("eventNoSnapshot")} />}
          <Space wrap>
            {data?.session_id && <Button onClick={() => { void openConversation(data.session_id!); setOpen(false); }}>{tr("openConversation")}</Button>}
            {data?.target && <Button onClick={() => { setSection(sectionForTarget(data.target!)); setOpen(false); }}>{tr("eventOpenTarget")}</Button>}
          </Space>
          {data?.turn_id && <Typography.Text type="secondary">{tr("eventTurn")} · {data.turn_id}</Typography.Text>}
          <Typography.Text code className="wd-wrap">{data?.source || event.source}</Typography.Text>
        </>}
      </div>
    </Drawer>
  </>;
}
