import { useState } from "react";
import { useNavigate, useSearchParams } from "react-router-dom";
import { Alert, App as AntApp, Badge, Button, Card, Descriptions, Drawer, Empty, Form, Input, Modal, Select, Space, Tabs, Tag, Timeline, Typography } from "antd";
import { ApartmentOutlined, ArrowRightOutlined, BookOutlined, CheckCircleOutlined, ClockCircleOutlined, CommentOutlined, EditOutlined, FileTextOutlined, FolderOutlined, LinkOutlined, PauseCircleOutlined, PlayCircleOutlined, PlusOutlined, ProfileOutlined, SearchOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { createDesktopDemo, recallKnowledge, splitLines, updateStream, type DemoTask, type RecallScope, type Resource, type Stream } from "./workDesktopModel";
import "./workDesktop.css";

const statusColor = { active: "processing", paused: "orange", archived: "default", confirmed: "green", candidate: "gold", stale: "orange", rejected: "default", draft: "default", running: "processing", review: "gold", taskDone: "green" } as const;
type Editor = "create" | "checkpoint" | "delegate" | "topic";
type EditorValues = { title?: string; background?: string; goal?: string; summary?: string; next?: string; completed?: string; blockers?: string; openQuestions?: string; acceptance?: string };
const nonBlank = { required: true, whitespace: true };

export function WorkDesktopPage() {
  const { locale, t } = useI18n();
  const { message } = AntApp.useApp();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const [data, setData] = useState(() => createDesktopDemo(locale));
  const [search, setSearch] = useState("");
  const [recallQuery, setRecallQuery] = useState("");
  const [recallScope, setRecallScope] = useState<RecallScope>("current");
  const [briefOpen, setBriefOpen] = useState(false);
  const [sourceId, setSourceId] = useState<string>();
  const [resourceId, setResourceId] = useState<string>();
  const [taskId, setTaskId] = useState<string>();
  const [editor, setEditor] = useState<Editor>();
  const [form] = Form.useForm<EditorValues>();
  const [topicSelections, setTopicSelections] = useState<Record<string, string>>({});
  const [noteDrafts, setNoteDrafts] = useState<Record<string, string>>({});
  const tr = (key: string, vars?: Record<string, string | number>) => t(`workDesktop.${key}`, vars);
  const requestedId = params.get("context");
  const stream = data.streams.find((item) => item.id === requestedId) ?? data.streams[0];
  const selectedTab = params.get("tab") ?? "overview";
  const tab = ["overview", "conversations", "tasks", "knowledge", "timeline"].includes(selectedTab) ? selectedTab : "overview";
  const readOnly = stream.status === "archived";
  const filteredStreams = data.streams.filter((item) => `${item.title} ${item.background} ${item.goal}`.toLocaleLowerCase().includes(search.trim().toLocaleLowerCase()));
  const resources = data.resources.filter((item) => stream.resourceIds.includes(item.id));
  const relations = data.relations.filter((item) => item.from === stream.id || item.to === stream.id);
  const knowledge = recallKnowledge(data, stream.id, recallScope, recallQuery);
  const source = data.sources.find((item) => item.id === sourceId);
  const resource = data.resources.find((item) => item.id === resourceId);
  const task = stream.tasks.find((item) => item.id === taskId);
  const topic = stream.topics.find((item) => item.id === topicSelections[stream.id]) ?? stream.topics[0];
  const noteKey = `${stream.id}:${topic?.id ?? ""}`;

  function selectStream(id: string) {
    setParams({ context: id, tab });
    setTaskId(undefined);
    setSourceId(undefined);
    setResourceId(undefined);
    setBriefOpen(false);
  }

  function eventPatch(patch: Partial<Stream>, kind: string, detail?: string) {
    // Every update names its owning stream; the visible desktop is not a global sink.
    const id = stream.id;
    setData((current) => {
      const owner = current.streams.find((item) => item.id === id)!;
      return updateStream(current, id, { ...patch, events: [{ id: crypto.randomUUID(), title: `${tr(kind)}${detail ? ` · ${detail}` : ""}`, at: new Date().toISOString() }, ...owner.events] });
    });
  }

  function openEditor(kind: Editor) {
    form.resetFields();
    if (kind === "checkpoint") {
      form.setFieldsValue({ summary: stream.summary, next: stream.next.join("\n"), completed: stream.completed.join("\n"), blockers: stream.blockers.join("\n"), openQuestions: stream.openQuestions.join("\n") });
    }
    setEditor(kind);
  }

  function submitEditor(values: EditorValues) {
    const id = crypto.randomUUID();
    if (editor === "create") {
      const created: Stream = {
        id, title: values.title!.trim(), background: values.background?.trim() ?? "", goal: values.goal!.trim(), status: "active", tags: [],
        summary: tr("newSummary"), next: [tr("newNext")], completed: [], blockers: [], openQuestions: [], revision: 1, checkpointAt: new Date().toISOString(),
        sourceIds: [], resourceIds: [], tasks: [], topics: [], events: [],
      };
      setData((current) => ({ ...current, streams: [...current.streams, created] }));
      setParams({ context: id, tab: "overview" });
      setSearch("");
    } else if (!readOnly && editor === "checkpoint") {
      if (!values.summary?.trim() || !splitLines(values.next ?? "").length) { void message.warning(tr("checkBlank")); return; }
      eventPatch({ summary: values.summary.trim(), next: splitLines(values.next!), completed: splitLines(values.completed ?? ""), blockers: splitLines(values.blockers ?? ""), openQuestions: splitLines(values.openQuestions ?? ""), revision: stream.revision + 1, checkpointAt: new Date().toISOString() }, "eventCheckpoint");
    } else if (!readOnly && editor === "delegate") {
      const draft: DemoTask = { id, title: values.title!.trim(), acceptance: values.acceptance!.trim(), status: "draft", checkpointRevision: stream.revision };
      eventPatch({ tasks: [...stream.tasks, draft] }, "eventTask", draft.title);
      setParams({ context: stream.id, tab: "tasks" });
    } else if (!readOnly && editor === "topic") {
      eventPatch({ topics: [...stream.topics, { id, title: values.title!.trim(), entries: [] }] }, "eventNote", values.title!.trim());
      setTopicSelections((current) => ({ ...current, [stream.id]: id }));
      setParams({ context: stream.id, tab: "conversations" });
    }
    setEditor(undefined);
    void message.success(tr("saved"));
  }

  function saveNote() {
    const text = noteDrafts[noteKey]?.trim();
    if (!text || !topic || readOnly) return;
    eventPatch({ topics: stream.topics.map((item) => item.id === topic.id ? { ...item, entries: [...item.entries, { id: crypto.randomUUID(), text, at: new Date().toISOString() }] } : item) }, "eventNote", topic.title);
    setNoteDrafts((current) => ({ ...current, [noteKey]: "" }));
    void message.success(tr("recorded"));
  }

  function reviewKnowledge(id: string, status: "confirmed" | "rejected") {
    if (readOnly) return;
    setData((current) => ({ ...current, knowledge: current.knowledge.map((item) => item.id === id ? { ...item, status } : item) }));
    const owner = data.knowledge.find((item) => item.id === id)?.contextId;
    if (owner) setData((current) => {
      const owningStream = current.streams.find((item) => item.id === owner)!;
      return updateStream(current, owner, { events: [{ id: crypto.randomUUID(), title: tr("eventKnowledge"), at: new Date().toISOString() }, ...owningStream.events] });
    });
    void message.success(tr("saved"));
  }

  function bullets(title: string, items: string[], className?: string) {
    return <section className={`wd-section ${className ?? ""}`}><Typography.Text strong>{title}</Typography.Text>{items.length ? <ul>{items.map((item, index) => <li key={index}>{item}</li>)}</ul> : <Typography.Paragraph type="secondary">{tr("noItems")}</Typography.Paragraph>}</section>;
  }

  function evidenceButtons(ids: string[]) {
    return <Space wrap>{ids.map((id) => {
      const item = data.sources.find((entry) => entry.id === id);
      return item ? <Button key={id} size="small" icon={<FileTextOutlined aria-hidden="true" />} onClick={() => setSourceId(id)}>{item.title}</Button> : null;
    })}</Space>;
  }

  const overview = <div className="wd-stack">
    <Card size="small" className="wd-next-card" title={<Space><PlayCircleOutlined aria-hidden="true" />{tr("next")}</Space>} extra={<Tag>{tr("checkpointRevision", { revision: stream.revision })}</Tag>}>
      <Typography.Paragraph>{stream.summary}</Typography.Paragraph>
      <ol className="wd-next-list">{stream.next.map((item, index) => <li key={index}>{item}</li>)}</ol>
      <Typography.Text type="secondary">{tr("savedAt")} · {stream.checkpointAt}</Typography.Text>
    </Card>
    <div className="wd-overview-grid">
      <Card size="small" title={<Space><BookOutlined aria-hidden="true" />{tr("background")}</Space>}>
        <Typography.Paragraph>{stream.background || tr("noItems")}</Typography.Paragraph>
        <Typography.Text strong>{tr("goal")}</Typography.Text><Typography.Paragraph>{stream.goal}</Typography.Paragraph>
      </Card>
      <Card size="small" title={<Space><CheckCircleOutlined aria-hidden="true" />{tr("completed")}</Space>}>{bullets(tr("completed"), stream.completed)}{evidenceButtons(stream.sourceIds)}</Card>
    </div>
    <div className="wd-overview-grid">
      <Card size="small" title={<Space><PauseCircleOutlined aria-hidden="true" />{tr("blockers")}</Space>}>{bullets(tr("blockers"), stream.blockers)}{bullets(tr("openQuestions"), stream.openQuestions)}</Card>
      <Card size="small" title={<Space><ClockCircleOutlined aria-hidden="true" />{tr("changes")}</Space>}>
        <Typography.Paragraph type="secondary">{tr("changesHint")}</Typography.Paragraph>
        {stream.events.length ? <div className="wd-stack">{stream.events.slice(0, 3).map((item) => <div key={item.id}><Typography.Text>{item.title}</Typography.Text><div className="wd-muted">{item.at}</div></div>)}</div> : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("noChanges")} />}
      </Card>
    </div>
    <Alert type="warning" showIcon title={tr("currentUnknown")} description={tr("currentUnknownHint")} />
  </div>;

  const taskView = <div className="wd-stack">
    <Alert type="info" showIcon title={tr("taskBoundary")} />
    <Space wrap><Button type="primary" icon={<PlusOutlined aria-hidden="true" />} disabled={readOnly} onClick={() => openEditor("delegate")}>{tr("delegate")}</Button><Button icon={<ProfileOutlined aria-hidden="true" />} onClick={() => navigate("/taskboard")}>{tr("actualBoard")}</Button></Space>
    {stream.tasks.length ? stream.tasks.map((item) => <button key={item.id} type="button" className="wd-task" onClick={() => setTaskId(item.id)}>
      <span className="wd-item-heading"><strong>{item.title}</strong><Tag color={statusColor[item.status]}>{tr(item.status)}</Tag></span>
      <span className="wd-muted">{item.acceptance}</span><span className="wd-muted">{tr("checkpointRevision", { revision: item.checkpointRevision })}</span>
    </button>) : <Empty description={tr("noTasks")} />}
  </div>;

  const conversationView = <div className="wd-stack">
    <Alert type="info" showIcon title={tr("topicHint")} description={tr("noteHint")} />
    <Space wrap className="wd-topic-select"><Select aria-label={tr("topicSelect")} value={topic?.id} placeholder={tr("topicSelect")} options={stream.topics.map((item) => ({ value: item.id, label: item.title }))} onChange={(id) => setTopicSelections((current) => ({ ...current, [stream.id]: id }))} /><Button icon={<PlusOutlined aria-hidden="true" />} disabled={readOnly} onClick={() => openEditor("topic")}>{tr("addTopic")}</Button></Space>
    {topic ? <>
      <div className="wd-topic-records">{topic.entries.length ? topic.entries.map((item) => <Card key={item.id} size="small" title={item.sourceId ? tr("demoRecord") : tr("humanNote")} extra={<Typography.Text type="secondary">{item.at}</Typography.Text>}>
        <Typography.Paragraph style={{ whiteSpace: "pre-wrap" }}>{item.text}</Typography.Paragraph>{item.sourceId && evidenceButtons([item.sourceId])}
      </Card>) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("noItems")} />}</div>
      <Card size="small" title={tr("topicNote")}><Input.TextArea aria-label={tr("topicNote")} rows={3} maxLength={6000} showCount placeholder={tr("notePlaceholder")} value={noteDrafts[noteKey] ?? ""} onChange={(event) => setNoteDrafts((current) => ({ ...current, [noteKey]: event.target.value }))} disabled={readOnly} /><Button style={{ marginTop: 16 }} type="primary" disabled={readOnly || !noteDrafts[noteKey]?.trim()} onClick={saveNote}>{tr("record")}</Button></Card>
    </> : <Empty description={tr("noTopics")} />}
  </div>;

  const knowledgeView = <div className="wd-stack">
    <Alert type="info" showIcon title={tr("recallHint")} description={tr("governanceHint")} />
    <div className="wd-recall-tools"><Input prefix={<SearchOutlined aria-hidden="true" />} aria-label={tr("recallPlaceholder")} placeholder={tr("recallPlaceholder")} value={recallQuery} allowClear onChange={(event) => setRecallQuery(event.target.value)} /><Select aria-label={tr("recallScope")} value={recallScope} onChange={setRecallScope} options={[{ value: "current", label: tr("scopeCurrent") }, { value: "related", label: tr("scopeRelated") }, { value: "all", label: tr("scopeAll") }]} /></div>
    <Typography.Text type="secondary">{tr("searchCount", { count: knowledge.length })}</Typography.Text>
    {knowledge.length ? knowledge.map((item) => <Card key={item.id} size="small" className="wd-knowledge" title={<Space wrap><Tag>{tr(item.kind)}</Tag><span>{item.title}</span></Space>} extra={<Tag color={statusColor[item.status]}>{tr(item.status)}</Tag>}>
      <Typography.Paragraph>{item.content}</Typography.Paragraph>
      <div className="wd-muted">{tr("scope")} · {item.scope}</div><div className="wd-muted">{tr("observed")} · {item.observed}</div>
      <Space wrap style={{ marginTop: 12 }}><Button size="small" icon={<FileTextOutlined aria-hidden="true" />} onClick={() => setSourceId(item.sourceId)}>{tr("source")}</Button><Tag>{data.streams.find((entry) => entry.id === item.contextId)?.title}</Tag>
        {item.status === "candidate" && <><Button size="small" disabled={readOnly || data.streams.find((entry) => entry.id === item.contextId)?.status === "archived"} onClick={() => reviewKnowledge(item.id, "confirmed")}>{tr("accept")}</Button><Button size="small" disabled={readOnly || data.streams.find((entry) => entry.id === item.contextId)?.status === "archived"} onClick={() => reviewKnowledge(item.id, "rejected")}>{tr("dismiss")}</Button></>}
      </Space>
    </Card>) : <Empty description={tr("noKnowledge")} />}
  </div>;

  const resourceButton = (item: Resource) => <button className="wd-resource" key={item.id} type="button" onClick={() => setResourceId(item.id)}>
    <span className="wd-resource-icon">{item.kind === "directory" ? <FolderOutlined aria-hidden="true" /> : item.kind === "document" ? <FileTextOutlined aria-hidden="true" /> : <LinkOutlined aria-hidden="true" />}</span>
    <span className="wd-resource-text"><strong>{item.title}</strong><span className="wd-muted">{tr(`kind.${item.kind}`)}{data.streams.filter((entry) => entry.resourceIds.includes(item.id)).length > 1 ? ` · ${tr("shared")}` : ""}</span></span>
    <ArrowRightOutlined aria-hidden="true" />
  </button>;

  return <div className="wd-page">
    <Alert className="wd-prototype" type="info" showIcon title={tr("prototype")} description={tr("prototypeHint")} />
    {requestedId && !data.streams.some((item) => item.id === requestedId) && <Alert type="warning" title={tr("invalidContext")} />}
    <div className="wd-layout">
      <aside className="wd-streams" aria-label={tr("contexts")}>
        <div className="wd-item-heading"><Typography.Text strong>{tr("contexts")}</Typography.Text><Badge count={data.streams.length} color="var(--godex-accent)" /></div>
        <Input aria-label={tr("searchContexts")} placeholder={tr("searchContexts")} prefix={<SearchOutlined aria-hidden="true" />} value={search} onChange={(event) => setSearch(event.target.value)} allowClear />
        <Select className="wd-mobile-switch" aria-label={tr("contexts")} value={stream.id} options={data.streams.map((item) => ({ value: item.id, label: item.title }))} onChange={selectStream} />
        <nav className="wd-stream-list">{filteredStreams.length ? filteredStreams.map((item) => <button type="button" key={item.id} className={`wd-stream ${stream.id === item.id ? "wd-stream-selected" : ""}`} aria-current={stream.id === item.id ? "page" : undefined} onClick={() => selectStream(item.id)}>
          <span className="wd-item-heading"><strong>{item.title}</strong><span className={`wd-status-dot wd-status-${item.status}`} /></span><span className="wd-stream-summary">{item.summary}</span><span><Tag color={statusColor[item.status]}>{tr(item.status)}</Tag><span className="wd-muted">{tr("tasksCount", { count: item.tasks.length })}</span></span>
        </button>) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("emptyContexts")} />}</nav>
        <Button block icon={<PlusOutlined aria-hidden="true" />} onClick={() => openEditor("create")}>{tr("newContext")}</Button>
        <Typography.Paragraph type="secondary" className="wd-focus-hint">{tr("focusHint")}</Typography.Paragraph>
      </aside>
      <main className="wd-main">
        <header className="wd-context-header"><div><Space wrap><Tag>{tr("owner")}</Tag><Tag color={statusColor[stream.status]}>{tr(stream.status)}</Tag>{stream.tags.map((tag) => <Tag key={tag}>{tag}</Tag>)}</Space><Typography.Title level={3} className="wd-title">{stream.title}</Typography.Title><Typography.Paragraph type="secondary">{stream.goal}</Typography.Paragraph></div>
          <Space wrap><Button icon={<PlayCircleOutlined aria-hidden="true" />} type="primary" onClick={() => setBriefOpen(true)}>{tr("continueWork")}</Button><Button icon={<EditOutlined aria-hidden="true" />} disabled={readOnly} onClick={() => openEditor("checkpoint")}>{tr("checkpoint")}</Button><Button icon={<PlusOutlined aria-hidden="true" />} disabled={readOnly} onClick={() => openEditor("delegate")}>{tr("delegate")}</Button></Space>
        </header>
        <div className="wd-stat-row"><span><FolderOutlined aria-hidden="true" /> {tr("resourcesCount", { count: resources.length })}</span><span><ProfileOutlined aria-hidden="true" /> {tr("tasksCount", { count: stream.tasks.length })}</span><span><ClockCircleOutlined aria-hidden="true" /> {tr("pendingCount", { count: stream.openQuestions.length + stream.blockers.length })}</span></div>
        {readOnly && <Alert type="warning" title={tr("readOnly")} />}
        <Tabs activeKey={tab} onChange={(key) => setParams({ context: stream.id, tab: key })} items={[
          { key: "overview", label: tr("overview"), children: overview },
          { key: "conversations", label: <Space size={4}><CommentOutlined aria-hidden="true" />{tr("conversations")}</Space>, children: conversationView },
          { key: "tasks", label: tr("tasks"), children: taskView },
          { key: "knowledge", label: tr("knowledge"), children: knowledgeView },
          { key: "timeline", label: tr("timeline"), children: <div className="wd-stack"><Typography.Paragraph type="secondary">{tr("timelineHint")}</Typography.Paragraph>{stream.events.length ? <Timeline items={stream.events.map((item) => ({ content: <div><Typography.Text strong>{item.title}</Typography.Text><div className="wd-muted">{item.at}</div>{item.sourceId && evidenceButtons([item.sourceId])}</div> }))} /> : <Empty description={tr("noItems")} />}</div> },
        ]} />
      </main>
      <aside className="wd-sidebar" aria-label={tr("resources")}>
        <Card size="small" title={<Space><LinkOutlined aria-hidden="true" />{tr("resources")}</Space>}><Typography.Paragraph type="secondary">{tr("resourceHint")}</Typography.Paragraph>{resources.length ? resources.map(resourceButton) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("noItems")} />}</Card>
        <Card size="small" title={<Space><ApartmentOutlined aria-hidden="true" />{tr("relations")}</Space>}><Typography.Paragraph type="secondary">{tr("relationHint")}</Typography.Paragraph>{relations.length ? relations.map((item) => {
          const outgoing = item.from === stream.id;
          const other = data.streams.find((entry) => entry.id === (outgoing ? item.to : item.from));
          if (!other) return null;
          return <div className="wd-relation" key={`${item.from}-${item.to}`}><Tag color={item.kind === "depends" ? "orange" : "default"}>{tr(item.kind)}</Tag><div className="wd-muted">{item.kind === "depends" ? `${data.streams.find((entry) => entry.id === item.from)?.title} → ${data.streams.find((entry) => entry.id === item.to)?.title}` : null}</div><Button type="link" onClick={() => selectStream(other.id)}>{other.title} <ArrowRightOutlined aria-hidden="true" /></Button><div className="wd-muted">{item.condition}</div></div>;
        }) : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={tr("noItems")} />}</Card>
        <Card size="small"><Space wrap>{stream.status === "archived" ? <Button onClick={() => eventPatch({ status: "active" }, "eventStatus")}>{tr("restore")}</Button> : <><Button onClick={() => eventPatch({ status: stream.status === "active" ? "paused" : "active" }, "eventStatus")}>{tr(stream.status === "active" ? "pause" : "resume")}</Button><Button onClick={() => eventPatch({ status: "archived" }, "eventStatus")}>{tr("archive")}</Button></>}</Space></Card>
      </aside>
    </div>
    <Drawer zIndex={1300} styles={{ wrapper: { maxWidth: "100vw" } }} title={tr("briefTitle")} open={briefOpen} onClose={() => setBriefOpen(false)} size={600}>
      <div className="wd-stack"><Alert type="info" showIcon title={tr("briefHint")} /><Typography.Title level={4}>{stream.title}</Typography.Title><Typography.Paragraph>{stream.background}</Typography.Paragraph>{bullets(tr("completed"), stream.completed)}{bullets(tr("next"), stream.next)}{bullets(tr("blockers"), stream.blockers)}{bullets(tr("openQuestions"), stream.openQuestions)}{bullets(tr("changes"), stream.events.slice(0, 3).map((item) => item.title))}<Typography.Text strong>{tr("evidence")}</Typography.Text>{evidenceButtons(stream.sourceIds)}<Typography.Text type="secondary">{tr("checkpointRevision", { revision: stream.revision })} · {stream.checkpointAt}</Typography.Text><Alert type="warning" title={tr("currentUnknown")} description={tr("currentUnknownHint")} /></div>
    </Drawer>
    <Drawer zIndex={1400} styles={{ wrapper: { maxWidth: "100vw" } }} title={tr("sourceTitle")} open={!!source} onClose={() => setSourceId(undefined)} size={520}>
      {source && <div className="wd-stack"><Alert type="info" title={tr("sourceHint")} /><Typography.Title level={4}>{source.title}</Typography.Title><Descriptions column={1} items={[{ key: "locator", label: tr("sourceLocator"), children: <Typography.Text code className="wd-wrap">{source.locator}</Typography.Text> }, { key: "at", label: tr("sourceTime"), children: source.at }, { key: "owner", label: tr("sourceRelation"), children: data.streams.find((item) => item.id === source.contextId)?.title }]} /><Typography.Text strong>{tr("sourceExcerpt")}</Typography.Text><Typography.Paragraph>{source.excerpt}</Typography.Paragraph></div>}
    </Drawer>
    <Drawer zIndex={1300} styles={{ wrapper: { maxWidth: "100vw" } }} title={tr("resourceDetail")} open={!!resource} onClose={() => setResourceId(undefined)} size={520}>
      {resource && <div className="wd-stack"><Alert type="info" title={tr("resourceBoundary")} /><Typography.Title level={4}>{resource.title}</Typography.Title><Tag>{tr(`kind.${resource.kind}`)}</Tag><Descriptions column={1} items={[{ key: "locator", label: tr("locator"), children: <Typography.Text code className="wd-wrap">{resource.locator}</Typography.Text> }, ...(resource.node ? [{ key: "node", label: tr("node"), children: resource.node }] : []), { key: "purpose", label: tr("purpose"), children: resource.purpose }]} /><Typography.Text strong>{tr("linkedContexts")}</Typography.Text>{data.streams.filter((item) => item.resourceIds.includes(resource.id)).map((item) => <Button key={item.id} onClick={() => selectStream(item.id)}>{item.title}</Button>)}</div>}
    </Drawer>
    <Drawer zIndex={1300} styles={{ wrapper: { maxWidth: "100vw" } }} title={tr("taskDetail")} open={!!task} onClose={() => setTaskId(undefined)} size={520}>
      {task && <div className="wd-stack"><Alert type="info" title={tr("taskBoundary")} /><Typography.Title level={4}>{task.title}</Typography.Title><Tag color={statusColor[task.status]}>{tr(task.status)}</Tag><Typography.Text strong>{tr("taskOrigin")}</Typography.Text><Typography.Paragraph>{stream.title}</Typography.Paragraph><Typography.Text strong>{tr("acceptance")}</Typography.Text><Typography.Paragraph>{task.acceptance}</Typography.Paragraph><Tag>{tr("checkpointRevision", { revision: task.checkpointRevision })}</Tag>{task.sourceId && evidenceButtons([task.sourceId])}<Button onClick={() => navigate("/taskboard")}>{tr("actualBoard")}</Button></div>}
    </Drawer>
    <Modal zIndex={1300} title={editor ? tr(({ create: "newContext", checkpoint: "checkpoint", delegate: "delegate", topic: "addTopic" } as const)[editor]) : ""} open={!!editor} onCancel={() => setEditor(undefined)} footer={null} width={640} destroyOnHidden>
      <Form form={form} layout="vertical" onFinish={submitEditor}>
        {(editor === "create" || editor === "delegate" || editor === "topic") && <Form.Item label={tr(editor === "create" ? "contextName" : editor === "topic" ? "topicName" : "taskTitle")} name="title" rules={[{ ...nonBlank, message: tr("required") }]}><Input maxLength={160} autoFocus placeholder={editor === "delegate" ? tr("taskTitleHint") : undefined} /></Form.Item>}
        {editor === "create" && <><Form.Item label={tr("background")} name="background"><Input.TextArea rows={3} maxLength={4000} /></Form.Item><Form.Item label={tr("goal")} name="goal" rules={[{ ...nonBlank, message: tr("required") }]}><Input.TextArea rows={2} maxLength={2000} /></Form.Item></>}
        {editor === "checkpoint" && <>{(["summary", "next", "completed", "blockers", "openQuestions"] as const).map((key) => <Form.Item key={key} name={key} label={`${tr(key)}${key !== "summary" ? ` · ${tr("linesHint")}` : ""}`} rules={key === "summary" || key === "next" ? [{ ...nonBlank, message: tr("required") }] : undefined}><Input.TextArea rows={key === "summary" ? 3 : 2} maxLength={4000} /></Form.Item>)}</>}
        {editor === "delegate" && <><Form.Item label={tr("taskAcceptance")} name="acceptance" rules={[{ ...nonBlank, message: tr("required") }]}><Input.TextArea rows={3} maxLength={4000} /></Form.Item><Card size="small" title={tr("contextPack")}><Typography.Paragraph type="secondary">{tr("packHint")}</Typography.Paragraph><Typography.Text strong>{stream.title}</Typography.Text><Typography.Paragraph>{stream.summary}</Typography.Paragraph><Tag>{tr("checkpointRevision", { revision: stream.revision })}</Tag><Tag>{tr("resourcesCount", { count: resources.length })}</Tag></Card></>}
        <Space style={{ marginTop: 20 }}><Button type="primary" htmlType="submit">{tr(editor === "create" ? "create" : editor === "delegate" ? "taskSave" : "save")}</Button><Button onClick={() => setEditor(undefined)}>{tr("cancel")}</Button></Space>
      </Form>
    </Modal>
  </div>;
}
