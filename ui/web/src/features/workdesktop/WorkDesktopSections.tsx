import type { ReactNode } from "react";
import { Alert, Button, Card, Empty, Input, Select, Space, Spin, Tag, Timeline, Typography } from "antd";
import {
  ArrowRightOutlined,
  ClockCircleOutlined,
  CommentOutlined,
  FileTextOutlined,
  FolderOutlined,
  LinkOutlined,
  ProfileOutlined,
  PlusOutlined,
  SearchOutlined,
  ThunderboltOutlined,
} from "@ant-design/icons";
import { InfoHint } from "./workDesktopHints";
import { AcceptancePanel } from "./AcceptancePanel";
import { AttentionInbox } from "./AttentionInbox";
import { AttentionLedger } from "./AttentionLedger";
import { KnowledgeCard } from "./KnowledgeCard";
import { WorkMemoryPanel } from "./WorkMemoryPanel";
import { InlineEdit } from "./workDesktopInline";
import { LinkSessionPicker } from "./LinkSessionPicker";
import { OverviewPanel } from "./OverviewPanel";
import { ResourceChips } from "./ResourceChips";
import { TodoBoard } from "./TodoBoard";
import { formatTimestamp, otherSide } from "./workDesktopView";
import { useWorkDesktopStore, sectionForTarget } from "./workDesktopStore";
import { useOpenWorkConversation, type WorkDesktopData } from "./useWorkDesktopData";
import type { WorkFact, WorkTodo, WorkTodoInput } from "../../lib/apiWorkContext";

// Fixed palette per status: a status never borrows another status's colour, so the
// rail, the header and the delegation rows cannot disagree about what "in_review" looks like.
const cardStatusColor: Record<string, string> = { backlog: "default", todo: "blue", in_progress: "processing", in_review: "gold", done: "green" };
const factStatusColor: Record<string, string> = { candidate: "gold", confirmed: "green", stale: "orange", superseded: "default", rejected: "default" };

/** Board header: icon + title + hover hint, plus actions. */
function SectionHead(props: { icon: ReactNode; title: string; hint?: ReactNode; actions?: ReactNode }) {
  return (
    <div className="wd-inline-head">
      <Space size={6} align="center">
        {props.icon}
        <Typography.Text strong>{props.title}</Typography.Text>
        {props.hint}
      </Space>
      {props.actions ? <Space size={6} wrap>{props.actions}</Space> : null}
    </div>
  );
}

/**
 * The desktop's boards, rendered inside the chat right dock.
 *
 * The fusion moved the standalone desktop page's panels here (OverviewPanel,
 * TodoBoard, KnowledgeCard, ResourceChips, AttentionInbox) rather than
 * reimplementing them, so every board still edits in place exactly as before and
 * there is no second copy to drift. Only one board is mounted at a time, which is
 * why this is a switch and not six always-rendered sections.
 */
export function WorkDesktopSectionBoard(props: { section: string; data: WorkDesktopData }) {
  const { data } = props;
  const {
    tr, trLabel, selected, readOnly, contextId, contexts,
    facts, relations, sessions, delegations, events, todos, focusChanges, lastSeenAt,
    scopedAttention, attentionQuery, attentionMetrics, scopedDeferred,
    attentionActionMutation, attentionUndoMutation, contextResources, recallResult,
  } = data;
  const openEditor = useWorkDesktopStore((state) => state.openEditor);
  const openDelegateEditor = useWorkDesktopStore((state) => state.openDelegateEditor);
  const setSection = useWorkDesktopStore((state) => state.setSection);
  const setContextId = useWorkDesktopStore((state) => state.setContextId);
  const attentionScope = useWorkDesktopStore((state) => state.attentionScope);
  const setAttentionScope = useWorkDesktopStore((state) => state.setAttentionScope);
  const recallQuery = useWorkDesktopStore((state) => state.recallQuery);
  const setRecallQuery = useWorkDesktopStore((state) => state.setRecallQuery);
  const recallScope = useWorkDesktopStore((state) => state.recallScope);
  const setRecallScope = useWorkDesktopStore((state) => state.setRecallScope);
  const openConversation = useOpenWorkConversation();

  // The desktop's own boards need a selected desktop; the attention inbox does not
  // (it is cross-desktop by design), so it is the one board that renders without one.
  if (!selected && props.section !== "attention") {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("noContexts")} />;
  }

  if (props.section === "plan") {
    return (
      <div className="wd-stack">
        <SectionHead
          icon={<ProfileOutlined aria-hidden="true" />}
          title={tr("todoTitle")}
          hint={<InfoHint title={tr("showHint")} text={tr("inlineHint")} />}
          actions={<Typography.Text type="secondary">{tr("pendingCount", { count: todos.filter((item) => item.status !== "done").length })}</Typography.Text>}
        />
        <TodoBoard
          todos={todos}
          readOnly={readOnly}
          onCreate={(input: WorkTodoInput) => data.todoCreateMutation.mutateAsync(input)}
          onPatch={(todo: WorkTodo, patch) => data.todoPatchMutation.mutateAsync({ todo, patch })}
          onDelete={(todo: WorkTodo) => data.todoDeleteMutation.mutateAsync(todo)}
        />
      </div>
    );
  }

  if (props.section === "knowledge") {
    return (
      <div className="wd-stack">
        <WorkMemoryPanel contextId={contextId!} readOnly={readOnly} onChanged={data.invalidateDetail} facts={facts} />
        <SectionHead
          icon={<FileTextOutlined aria-hidden="true" />}
          title={tr("knowledge")}
          hint={<InfoHint title={tr("showHint")} text={`${tr("recallHint")} ${tr("governanceHint")}`} />}
          actions={<>
            <Typography.Text type="secondary">{tr("searchCount", { count: facts.length })}</Typography.Text>
            <Button size="small" icon={<PlusOutlined aria-hidden="true" />} disabled={readOnly} onClick={() => openEditor("fact")}>{tr("addFact")}</Button>
          </>}
        />
        <div className="wd-recall-tools">
          <Input prefix={<SearchOutlined aria-hidden="true" />} aria-label={tr("recallPlaceholder")} placeholder={tr("recallPlaceholder")} value={recallQuery} allowClear onChange={(event) => setRecallQuery(event.target.value)} />
          <Select aria-label={tr("recallScope")} value={recallScope} onChange={setRecallScope} options={[{ value: "current", label: tr("scopeCurrent") }, { value: "related", label: tr("scopeRelated") }, { value: "all", label: tr("scopeAll") }]} />
        </div>
        {recallResult.isLoading ? <Spin /> : facts.length ? facts.map((fact) => <KnowledgeCard
          key={fact.id}
          fact={fact}
          contextTitle={contexts.find((item) => item.id === fact.context_id)?.title}
          statusColor={factStatusColor[fact.status] ?? "default"}
          readOnly={readOnly}
          tr={tr}
          trLabel={trLabel}
          onAccept={(item: WorkFact, evidence) => data.reviewMutation.mutate({ factId: item.id, status: "confirmed", version: item.version, evidence })}
          onDismiss={(item: WorkFact) => data.reviewMutation.mutate({ factId: item.id, status: "rejected", version: item.version })}
          onPromote={(item: WorkFact) => data.promoteMutation.mutate(item.id)}
        />) : <Empty description={tr("noChanges")} />}
      </div>
    );
  }

  if (props.section === "delegations") {
    return (
      <div className="wd-stack">
        <SectionHead
          icon={<ProfileOutlined aria-hidden="true" />}
          title={tr("tasks")}
          hint={<InfoHint title={tr("taskBoundary")} text={tr("delegationHint")} />}
          actions={<Button size="small" type="primary" icon={<PlusOutlined aria-hidden="true" />} disabled={readOnly} onClick={() => openEditor("delegate")}>{tr("delegate")}</Button>}
        />
        {delegations.length ? delegations.map((delegation) => {
          const card = delegation.card ?? undefined;
          return <Card size="small" className="wd-task" key={delegation.id} title={<Space wrap><span>{delegation.title}</span>{card && <Tag color={cardStatusColor[card.status] ?? "default"}>{trLabel("cardStatus", card.status)}</Tag>}</Space>}>
            {/* The acceptance package, not just the criterion text: what was done, how
                it was verified, what the run touched, and what is still open — assembled
                from the card's own fields so accepting does not require reading the
                execution transcript. */}
            <AcceptancePanel card={card} fallback={delegation.pack} tr={tr} />
            {!card && <Alert type="warning" showIcon title={tr("cardMissing")} />}
            {card && <div className="wd-muted">{tr("cardState")} · {trLabel("cardStatus", card.status)} · {card.id}</div>}
            <Space wrap style={{ marginTop: 12 }}>
              {card && !readOnly && <Button size="small" type="primary" icon={<ThunderboltOutlined aria-hidden="true" />} onClick={() => data.executeMutation.mutate(card.id)}>{tr("execute")}</Button>}
              {card && !readOnly && card.status === "in_review" && <>
                <Button size="small" onClick={() => data.reviewCardMutation.mutate({ cardId: card.id, version: card.version, action: "complete" })}>{tr("reviewAccept")}</Button>
                <Button size="small" danger onClick={() => data.reviewCardMutation.mutate({ cardId: card.id, version: card.version, action: "reject", reason: tr("reviewReject") })}>{tr("reviewReject")}</Button>
              </>}
            </Space>
          </Card>;
        }) : <Empty description={tr("noTasks")} />}
      </div>
    );
  }

  if (props.section === "timeline") {
    return (
      <div className="wd-stack">
        <SectionHead icon={<ClockCircleOutlined aria-hidden="true" />} title={tr("timeline")} hint={<InfoHint title={tr("showHint")} text={tr("timelineHint")} />} />
        {events.length ? <Timeline items={events.map((event) => ({ content: <div><Typography.Text strong>{event.title}</Typography.Text><div className="wd-muted">{event.source || tr("noItems")} · {formatTimestamp(event.created_at)}</div></div> }))} /> : <Empty description={tr("noChanges")} />}
      </div>
    );
  }

  if (props.section === "resources") {
    return (
      <div className="wd-stack">
        <SectionHead
          icon={<LinkOutlined aria-hidden="true" />}
          title={tr("resourcesTitle")}
          hint={<InfoHint title={tr("showHint")} text={tr("resourceHint")} />}
          actions={<Button size="small" disabled={readOnly} onClick={() => openEditor("resource")}>{tr("addResource")}</Button>}
        />
        <ResourceChips
          resources={contextResources}
          readOnly={readOnly}
          onUpdate={(resource, patch) => data.resourceUpdateMutation.mutateAsync({ resource, patch })}
          onRemove={(resource) => { if (window.confirm(tr("removeResourceConfirm"))) data.removeResourceMutation.mutate(resource.id); }}
        />
      </div>
    );
  }

  if (props.section === "conversations") {
    return (
      <div className="wd-stack">
        <SectionHead
          icon={<CommentOutlined aria-hidden="true" />}
          title={tr("conversations")}
          hint={<InfoHint title={tr("showHint")} text={tr("topicHint")} />}
          actions={<>
            <LinkSessionPicker contextId={contextId!} linkedIds={sessions.map((item) => item.session_id)} disabled={readOnly} onLinked={data.invalidateDetail} />
            <Button size="small" type="primary" icon={<PlusOutlined aria-hidden="true" />} disabled={readOnly} onClick={() => openEditor("session")}>{tr("newSession")}</Button>
          </>}
        />
        {sessions.length ? <div className="wd-topic-records">{sessions.map((link) => (
          <div className="wd-session-row" key={link.id}>
            <InlineEdit className="wd-title-edit" ariaLabel={tr("topicName")} editIconLabel={tr("editTopic")} value={link.topic} disabled={readOnly} required render={(value) => <span>{value || tr("topicName")}</span>} onSave={(next) => data.topicMutation.mutateAsync({ sessionId: link.session_id, topic: next })} />
            <div className="wd-muted wd-wrap">{link.session_id}</div>
            <Space size={4} wrap>
              <Button size="small" icon={<CommentOutlined aria-hidden="true" />} onClick={() => void openConversation(link.session_id)}>{tr("openConversation")}</Button>
              {!readOnly && <Button size="small" danger onClick={() => { if (window.confirm(tr("unlinkConfirm"))) data.unlinkSessionMutation.mutate(link.session_id); }}>{tr("unlinkSession")}</Button>}
            </Space>
          </div>
        ))}</div> : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("noSessions")} />}
      </div>
    );
  }

  if (props.section === "attention") {
    return (
      <div className="wd-stack">
        <AttentionInbox
          items={scopedAttention}
          loading={attentionQuery.isLoading}
          onOpen={(id, target) => { setContextId(id); setSection(sectionForTarget(target)); }}
          scope={attentionScope}
          onScopeChange={setAttentionScope}
          deferred={scopedDeferred}
          onAction={(contextId, itemKey, verb, opts) => attentionActionMutation.mutate({ contextId, itemKey, verb, reason: opts?.reason, untilAt: opts?.untilAt })}
          onUndo={(contextId, itemKey) => attentionUndoMutation.mutate({ contextId, itemKey })}
          onDelegate={openDelegateEditor}
        />
        {/* The ledger sits UNDER the list and starts folded: it answers "is my
            attention being spent well?", which is worth asking occasionally and not
            worth permanent screen space. It renders nothing until the numbers exist,
            so a failed read degrades to exactly the old inbox rather than an empty box. */}
        {attentionMetrics ? <AttentionLedger metrics={attentionMetrics} tr={tr} /> : null}
      </div>
    );
  }

  // overview (default): the desktop's own summary + relationships + lifecycle.
  return (
    <div className="wd-stack">
      <div className="wd-stat-row">
        <span><FolderOutlined aria-hidden="true" /> {tr("resourcesCount", { count: contextResources.length })}</span>
        <span><CommentOutlined aria-hidden="true" /> {tr("sessionsCount", { count: sessions.length })}</span>
        <span><ProfileOutlined aria-hidden="true" /> {tr("tasksCount", { count: delegations.length })}</span>
        <span><ClockCircleOutlined aria-hidden="true" /> {tr("savedAt")} · {formatTimestamp(selected!.checkpoint.created_at) || tr("noItems")}</span>
      </div>
      <OverviewPanel
        background={selected!.background}
        goal={selected!.goal}
        checkpoint={selected!.checkpoint}
        events={events}
        focusChanges={focusChanges}
        lastSeenAt={lastSeenAt}
        readOnly={readOnly}
        markSeenPending={data.markSeenMutation.isPending}
        onMarkSeen={() => data.markSeenMutation.mutate()}
        onPatchContext={(patch) => data.patchContextMutation.mutateAsync({ id: selected!.id, version: selected!.version, ...patch })}
        onPatchCheckpoint={(patch) => data.checkpointMutation.mutateAsync(patch)}
        tr={tr}
      />
      <Card size="small" title={<Space size={6}><LinkOutlined aria-hidden="true" />{tr("relationsTitle")}</Space>} extra={<Button size="small" disabled={readOnly} onClick={() => openEditor("relation")}>{tr("addRelation")}</Button>}>
        {relationList()}
      </Card>
      <Card size="small"><Space wrap>{readOnly
        ? <Button onClick={() => data.patchContextMutation.mutate({ id: selected!.id, version: selected!.version, status: "active" })}>{tr("restore")}</Button>
        : <>
          <Button onClick={() => data.patchContextMutation.mutate({ id: selected!.id, version: selected!.version, status: selected!.status === "active" ? "paused" : "active" })}>{tr(selected!.status === "active" ? "pause" : "resume")}</Button>
          <Button danger onClick={() => { if (window.confirm(tr("archive"))) data.patchContextMutation.mutate({ id: selected!.id, version: selected!.version, status: "archived" }); }}>{tr("archive")}</Button>
        </>}
      </Space></Card>
    </div>
  );

  function relationList() {
    if (!relations.length) return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("noRelations")} />;
    return relations.map((relation) => {
      const otherId = otherSide(relation, contextId!);
      const other = contexts.find((item) => item.id === otherId);
      const directed = relation.kind === "depends_on";
      const label = (id: string) => (id === contextId ? selected!.title : contexts.find((item) => item.id === id)?.title ?? id);
      return <div className="wd-relation" key={relation.id}>
        <Tag color={directed ? "orange" : "default"}>{trLabel("relationKind", relation.kind)}</Tag>
        {directed && <div className="wd-muted">{label(relation.from_id)} → {label(relation.to_id)}</div>}
        <Button type="link" disabled={!other} onClick={() => other && setContextId(other.id)}>{other?.title ?? otherId} <ArrowRightOutlined aria-hidden="true" /></Button>
        {relation.condition && <div className="wd-muted">{relation.condition}</div>}
        {!readOnly && <Button size="small" danger onClick={() => data.removeRelationMutation.mutate(relation.id)}>{tr("removeRelation")}</Button>}
      </div>;
    });
  }
}
