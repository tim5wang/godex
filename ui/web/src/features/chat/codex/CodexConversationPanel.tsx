import { Alert, Badge, Button, Card, Collapse, Modal, Select, Space, Spin, Tag, Typography } from "antd";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { Composer, type ComposerSubmission } from "../../../components/Composer";
import { MessageFeedV2 } from "../../../components/MessageFeedV2";
import { APIError } from "../../../lib/apiClient";
import { useI18n } from "../../../i18n";
import type { ChatSessionDriver } from "../sessionDriver";
import { codexAccess, codexApprove, codexHistory, codexInterrupt, codexQueue, codexStartQueue, codexStatus, codexSubmit, isCodexActiveWriterError, watchCodex, type CodexEvent } from "./codexApi";
import { codexFeedItems, emptyCodexConversation, hydrateCodex, mergeCodexTurns, reduceCodexEvent } from "./codexModel";
import { CodexModelSettings } from "./CodexModelSettings";
import "./codex.css";

interface Props { threadId?: string; token: string | null; nodeId?: string | null; boundNode: string | null }

export function CodexConversationPanel(props: Props) {
  // Keyed by the full source identity: late requests cannot update another node
  // or thread, and a newly opened conversation always starts in read-only mode.
  return <CodexConversation key={`${props.token || ""}:${props.nodeId || "local"}:${props.threadId || ""}`} {...props} />;
}

function CodexConversation({ threadId: routeId, token, nodeId, boundNode }: Props) {
  const { t, locale } = useI18n();
  const threadId = routeId && routeId !== "connect" ? routeId : "";
  const nodeMatches = boundNode === (nodeId || "local");
  const queryClient = useQueryClient();
  const [state, setState] = useState(emptyCodexConversation);
  const [observing, setObserving] = useState(false);
  const [collaborating, setCollaborating] = useState(false);
  const [connection, setConnection] = useState<"history" | "connecting" | "connected" | "reconnecting">("history");
  const [error, setError] = useState("");
  const [writerConflict, setWriterConflict] = useState(false);
  const [checkingAccess, setCheckingAccess] = useState(false);
  const [busy, setBusy] = useState(false);
  const [loadingEarlier, setLoadingEarlier] = useState(false);
  const [mode, setMode] = useState<"queue" | "steer">("queue");
  const [expanded, setExpanded] = useState<Set<string>>(new Set());
  const receiptKey = `godex:codex-pending:${nodeId || "local"}:${threadId}`;
  const [uncertain, setUncertain] = useState(() => { try { return localStorage.getItem(receiptKey) || ""; } catch { return ""; } });
  const scroller = useRef<HTMLDivElement>(null);
  const follow = useRef(true);
  const scrollAnchor = useRef<{ top: number; height: number } | null>(null);
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  const status = useQuery({ queryKey: ["codex-status", token, nodeId], queryFn: () => codexStatus(token), retry: false, staleTime: 10_000 });
  // Commands are displayed for the target node's terminal, never executed by UI.
  const quoteShell = (value: string) => `'${value.replace(/'/g, "'\\''")}'`;
  const executable = status.data?.executable ? quoteShell(status.data.executable) : "codex";
  const remoteAddress = quoteShell(`unix://${status.data?.socket || ""}`);
  const sharedCommand = `${executable} --remote ${remoteAddress}`;
  const resumeCommand = `${executable} resume --remote ${remoteAddress} ${quoteShell(threadId)}`;
  const access = useQuery({ queryKey: ["codex-access", token, nodeId, threadId], queryFn: ({ signal }) => codexAccess(token, threadId, signal), enabled: !!threadId && nodeMatches && !observing && status.data?.ready === true, retry: false, staleTime: 10_000 });
  const history = useQuery({ queryKey: ["codex-history", token, nodeId, threadId], queryFn: ({ signal }) => codexHistory(token, threadId, "", signal), enabled: !!threadId && nodeMatches && !observing && status.data?.ready === true, retry: false });
  useEffect(() => { if (history.data && !observing) setState((current) => hydrateCodex(current, history.data)); }, [history.data, observing]);
  const queue = useQuery({ queryKey: ["codex-queue", token, nodeId, threadId], queryFn: () => codexQueue(token, threadId), enabled: !!threadId && nodeMatches && connection === "connected", retry: false });
  const refreshQueue = useCallback(() => { void queryClient.invalidateQueries({ queryKey: ["codex-queue", token, nodeId, threadId] }); }, [queryClient, token, nodeId, threadId]);

  useEffect(() => {
    if (!observing || !threadId || !nodeMatches) return;
    const abort = new AbortController();
    let retryTimer: ReturnType<typeof setTimeout> | undefined;
    let flushTimer: ReturnType<typeof setTimeout> | undefined;
    let failures = 0;
    let pending: CodexEvent[] = [];
    const flush = () => { flushTimer = undefined; const events = pending; pending = []; if (!abort.signal.aborted) setState((current) => events.reduce(reduceCodexEvent, current)); };
    const connect = async () => {
      setConnection(failures ? "reconnecting" : "connecting");
      try {
        await watchCodex(token, threadId, abort.signal, (event) => {
          if (abort.signal.aborted) return;
          if (event.method === "codex/snapshot") {
            // The connection is usable only after its authoritative snapshot is
            // applied. Token deltas may be batched; the join barrier must not be.
            pending.push(event); clearTimeout(flushTimer); flush();
            failures = 0; setConnection("connected"); setError(""); refreshQueue();
            return;
          }
          if (["thread/queue/changed", "turn/completed", "turn/started"].includes(event.method)) refreshQueue();
          pending.push(event);
          if (!flushTimer) flushTimer = setTimeout(flush, 50);
        });
      } catch (caught) {
        if (abort.signal.aborted) return;
        flush();
        const activeWriter = isCodexActiveWriterError(caught);
        setWriterConflict(activeWriter);
        setError(activeWriter ? "" : caught instanceof Error ? caught.message : String(caught));
        if (activeWriter || caught instanceof APIError && [400, 401, 403, 404, 409].includes(caught.status)) { setConnection("history"); setObserving(false); setCollaborating(false); return; }
        setConnection("reconnecting");
        retryTimer = setTimeout(connect, Math.min(10_000, 1000 * 2 ** Math.min(failures++, 3)));
      }
    };
    void connect();
    return () => { abort.abort(); clearTimeout(retryTimer); clearTimeout(flushTimer); pending = []; };
  }, [observing, threadId, nodeMatches, token, refreshQueue]);

  const items = useMemo(() => codexFeedItems(state.turns, expanded, locale), [state.turns, expanded, locale]);
  useLayoutEffect(() => {
    const element = scroller.current;
    if (!element) return;
    if (scrollAnchor.current) { const anchor = scrollAnchor.current; scrollAnchor.current = null; element.scrollTop = anchor.top + element.scrollHeight - anchor.height; }
    else if (follow.current) element.scrollTop = element.scrollHeight;
  }, [items]);
  const activeTurn = [...state.turns].reverse().find((turn) => turn.status === "inProgress");
  const running = !!activeTurn || state.thread?.status?.type === "active";
  const connected = observing && connection === "connected";
  const writable = collaborating && connected && !uncertain && !busy && queue.isSuccess && state.thread?.canAcceptDirectInput !== false;
  const directInputBlocked = (connected ? state.thread : access.data?.thread)?.canAcceptDirectInput === false;
  const remember = (id: string) => { setUncertain(id); try { if (id) localStorage.setItem(receiptKey, id); else localStorage.removeItem(receiptKey); } catch { /* storage disabled */ } };

  const beginObservation = (collaborate: boolean) => {
    // Do not download/render a second history response after live hydration.
    void queryClient.cancelQueries({ queryKey: ["codex-history", token, nodeId, threadId] });
    setWriterConflict(false); setCollaborating(collaborate); setConnection("connecting"); setObserving(true);
  };
  const requestConnection = async (collaborate: boolean) => {
    if (checkingAccess || !nodeMatches) return;
    if (connected) { if (collaborate && !directInputBlocked) confirmJoin(true); return; }
    if (observing) return;
    setCheckingAccess(true); setError("");
    try {
      // Share the in-flight/read-only preflight, and briefly reuse its result.
      // A writer-conflict retry always probes again. Watch still authorizes the
      // thread against the native runtime, so this is never a cached lease.
      const result = await queryClient.fetchQuery({ queryKey: ["codex-access", token, nodeId, threadId], queryFn: ({ signal }) => codexAccess(token, threadId, signal), staleTime: writerConflict ? 0 : 10_000, retry: false });
      if (!mounted.current) return;
      if (collaborate && result.thread.canAcceptDirectInput === false) { setError(t("codex.directInputBlocked")); return; }
      if (collaborate || !result.loadedInRuntime) confirmJoin(result.loadedInRuntime, collaborate);
      else beginObservation(false);
    } catch (caught) { if (mounted.current) setError(t("codex.preflightFailed", { error: caught instanceof Error ? caught.message : String(caught) })); }
    finally { if (mounted.current) setCheckingAccess(false); }
  };
  const confirmJoin = (loaded: boolean, collaborate = true) => Modal.confirm({
    title: t(loaded ? "codex.joinTitle" : "codex.unloadedTitle"),
    content: t(loaded ? "codex.joinDescription" : "codex.unloadedDescription"),
    okText: t(collaborate ? "codex.join" : "codex.connectView"), cancelText: t("codex.cancel"),
    onOk: () => { if (mounted.current) { if (connected) setCollaborating(collaborate); else beginObservation(collaborate); } },
  });

  const driver: ChatSessionDriver = {
    source: "codex", capabilities: { attachments: false, queue: queue.isSuccess, steer: true, interrupt: true, approvals: true },
    submit: async (submission: ComposerSubmission) => {
      if (!writable) throw new Error(t("codex.connectFirst"));
      if (submission.files.length) throw new Error(t("codex.textOnly"));
      const requestId = crypto.randomUUID();
      // Persist BEFORE dispatch. A tab close after send cannot silently turn an
      // unknown outcome into a new message on the next visit.
      remember(requestId); setBusy(true); setError("");
      try {
        await codexSubmit(token, threadId, { requestId, text: submission.text, mode: running ? mode : "queue", ...(running && mode === "steer" ? { turnId: activeTurn?.id } : {}) });
        if (!mounted.current) return;
        remember(""); refreshQueue();
      } catch (caught) {
        if (!mounted.current) throw caught;
        const definitive = caught instanceof APIError && caught.status < 500 && !(caught.body as { outcomeUnknown?: boolean } | undefined)?.outcomeUnknown;
        if (definitive) remember("");
        setError(caught instanceof Error ? caught.message : String(caught));
        throw caught;
      } finally { if (mounted.current) setBusy(false); }
    },
    interrupt: (turnId) => codexInterrupt(token, threadId, turnId),
  };

  const action = async (run: () => Promise<unknown>) => { setBusy(true); setError(""); try { await run(); refreshQueue(); } catch (caught) { if (mounted.current) setError(caught instanceof Error ? caught.message : String(caught)); } finally { if (mounted.current) setBusy(false); } };
  const loadEarlier = async () => {
    if (!state.cursor || loadingEarlier) return;
    setLoadingEarlier(true);
    try {
      const snapshot = await codexHistory(token, threadId, state.cursor);
      if (!mounted.current) return;
      const element = scroller.current;
      if (element) scrollAnchor.current = { top: element.scrollTop, height: element.scrollHeight };
      setState((current) => ({ ...current, turns: mergeCodexTurns(current.turns, [...snapshot.turns.data].reverse(), true), cursor: snapshot.turns.nextCursor }));
    } catch (caught) { if (mounted.current) setError(String(caught)); } finally { if (mounted.current) setLoadingEarlier(false); }
  };
  const reconcile = async () => {
    const [currentQueue, snapshot] = await Promise.all([codexQueue(token, threadId), codexHistory(token, threadId)]);
    if (!mounted.current) return;
    setState((current) => hydrateCodex(current, snapshot));
    const accepted = currentQueue.data.some((entry) => entry.clientUserMessageId === uncertain) || snapshot.turns.data.some((turn) => turn.items.some((item) => item.clientUserMessageId === uncertain));
    if (accepted) remember(""); else setError(t("codex.uncertainRequest", { id: uncertain }));
  };

  if (!nodeMatches && threadId) return <Alert className="codex-alert" type="warning" showIcon title={t("codex.otherNode")} description={t("codex.otherNodeHint")} />;
  if (!threadId || status.data?.ready === false || status.error) return (
    <div className="codex-connection-guide">
      <Typography.Title level={4}>{t("codex.connectTitle")}</Typography.Title>
      <Typography.Paragraph>{t("codex.connectDescription")}</Typography.Paragraph>
      {status.isLoading ? <Spin /> : <Alert showIcon type={status.data?.ready ? "success" : "warning"} title={t(status.data?.ready ? "codex.readySelect" : "codex.notReady")} description={status.error?.message || status.data?.reason} />}
      <ol>
        <li>{t("codex.setupInstall")}</li>
        <li>{t("codex.setupStart")} <Typography.Text code copyable>{`${executable} app-server daemon start`}</Typography.Text></li>
        <li>{t("codex.setupConnect")} <Typography.Text code copyable>{sharedCommand}</Typography.Text> {t("codex.setupRefresh")}</li>
      </ol>
      <Typography.Paragraph type="secondary">{t("codex.executableHint")}</Typography.Paragraph>
      <Typography.Paragraph type="secondary">{t("codex.privateRuntimeHint")}</Typography.Paragraph>
      <Button onClick={() => { void status.refetch(); }}>{t("codex.recheck")}</Button>
      <Collapse items={[{ key: "diagnostics", label: t("codex.diagnostics"), children: <><pre>{JSON.stringify(status.data, null, 2)}</pre><p>{t("codex.environmentHint")}</p></> }]} />
    </div>
  );

  return (
    <div className="chat-v2-center-body codex-conversation" data-testid="codex-conversation">
      <div className="codex-conversation-toolbar">
        <div><Typography.Text strong>{state.thread?.name || state.thread?.preview || "Codex"}</Typography.Text><div><Typography.Text type="secondary">{state.thread?.cwd} · {state.thread?.model || state.thread?.modelProvider}</Typography.Text></div></div>
        <Space wrap>
          <Badge status={connected ? "success" : observing ? "processing" : "default"} text={t(connected ? running ? "codex.running" : "codex.connected" : observing ? connection === "connecting" ? "codex.connecting" : "codex.reconnecting" : "codex.historySnapshot")} />
          {connected || access.data ? <Tag color={writerConflict ? "warning" : connected || access.data?.loadedInRuntime ? "green" : "default"}>{t(writerConflict ? "codex.otherRuntime" : connected || access.data?.loadedInRuntime ? "codex.loaded" : "codex.unloaded")}</Tag> : null}
          {!observing ? <Button key="observe" aria-label={t("codex.observe")} disabled={writerConflict} loading={checkingAccess} onClick={() => void requestConnection(false)}>{t("codex.observe")}</Button> : <Button key="disconnect" aria-label={t("codex.disconnect")} onClick={() => { setObserving(false); setCollaborating(false); setConnection("history"); void access.refetch(); }}>{t("codex.disconnect")}</Button>}
          {!collaborating ? <Button key="join" aria-label={t("codex.join")} type="primary" disabled={writerConflict || directInputBlocked} loading={checkingAccess} onClick={() => void requestConnection(true)}>{t("codex.join")}</Button> : <Button key="read-only" aria-label={t("codex.readOnly")} onClick={() => setCollaborating(false)}>{t("codex.readOnly")}</Button>}
          <Button onClick={() => Modal.info({ title: t("codex.nativeGuideTitle"), okText: t("codex.close"), content: <><p>{t("codex.nativeGuideDescription")}</p><Typography.Paragraph code copyable>{resumeCommand}</Typography.Paragraph><p>{t("codex.releaseHint")}</p></> })}>{t("codex.nativeGuide")}</Button>
          {collaborating && activeTurn ? <Button danger disabled={!connected || busy} onClick={() => void action(() => driver.interrupt(activeTurn.id))}>{t("codex.stop")}</Button> : null}
        </Space>
      </div>
      {directInputBlocked ? <Alert className="codex-alert" type="info" title={t("codex.directInputBlocked")} /> : null}
      {access.error && !observing ? <Alert className="codex-alert" type="warning" title={t("codex.unknownOwner")} description={access.error.message} /> : null}
      {writerConflict ? <Alert className="codex-alert codex-writer-conflict" type="warning" showIcon title={t("codex.writerConflict")} description={<>
        <Typography.Paragraph>{t("codex.writerConflictHint")}</Typography.Paragraph>
        <Collapse size="small" ghost items={[{ key: "same-runtime", label: t("codex.sameRuntime"), children: <>
        <Typography.Paragraph>{t("codex.diskHistoryHint")}</Typography.Paragraph>
        <Typography.Paragraph>{t("codex.newSessionHint")} <Typography.Text code copyable>{sharedCommand}</Typography.Text> {t("codex.newSessionAfter")}</Typography.Paragraph>
        <Typography.Paragraph>{t("codex.resumeHint")} <Typography.Text code copyable>{resumeCommand}</Typography.Text> {t("codex.resumeAfter")}</Typography.Paragraph>
        </> }]} />
        <Space wrap><Button loading={checkingAccess} onClick={() => void requestConnection(false)}>{t("codex.reconnectReleased")}</Button><Button onClick={() => { void history.refetch(); void access.refetch(); }}>{t("codex.refreshHistory")}</Button></Space>
      </>} /> : null}
      {error || history.error ? <Alert className="codex-alert" type="error" showIcon title={error || history.error?.message} /> : null}
      {queue.error ? <Alert className="codex-alert" type="warning" title={t("codex.queueUnavailable")} description={t("codex.queueUnavailableHint", { error: queue.error.message })} /> : null}
      {uncertain ? <Alert className="codex-alert" type="warning" showIcon title={t("codex.uncertain")} description={<Space wrap><Typography.Text code>{uncertain}</Typography.Text><Button size="small" onClick={() => void action(reconcile)}>{t("codex.checkReceipt")}</Button><Button size="small" onClick={() => Modal.confirm({ title: t("codex.checkedTitle"), content: t("codex.checkedHint"), okText: t("codex.manuallyChecked"), cancelText: t("codex.cancel"), onOk: () => remember("") })}>{t("codex.manuallyChecked")}</Button></Space>} /> : null}
      {state.approvals.map((approval) => <Card size="small" key={approval.id} title={t("codex.approval")} className="codex-alert"><pre>{JSON.stringify(approval.params, null, 2)}</pre><Space><Button type="primary" disabled={!collaborating || !connected || busy} onClick={() => void action(() => codexApprove(token, threadId, approval.id, "accept"))}>{t("codex.acceptOnce")}</Button><Button disabled={!collaborating || !connected || busy} onClick={() => void action(() => codexApprove(token, threadId, approval.id, "decline"))}>{t("codex.decline")}</Button></Space></Card>)}
      <div className="chat-feed-v2-scrollport">
        <div className="chat-feed chat-feed-v2-scroll" ref={scroller} onScroll={() => { const e = scroller.current; if (e) { follow.current = e.scrollHeight - e.scrollTop - e.clientHeight < 100; if (e.scrollTop < 32 && state.cursor && !loadingEarlier) void loadEarlier(); } }}>
          <div className="chat-feed-inner chat-feed-v2-inner">
            {state.cursor ? <Button type="text" block loading={loadingEarlier} onClick={() => void loadEarlier()}>{t("codex.earlier")}</Button> : null}
            {history.isLoading && !observing ? <Spin /> : <MessageFeedV2 items={items} botName="Codex" running={connected && running} activeTurnId={activeTurn?.id} onToggleTool={(id) => setExpanded((current) => { const next = new Set(current); if (next.has(id)) next.delete(id); else next.add(id); return next; })} />}
          </div>
        </div>
      </div>
      <div className="chat-composer-area codex-composer-area">
        <CodexModelSettings token={token} nodeId={nodeId} thread={state.thread} enabled={nodeMatches && status.data?.ready === true} writable={collaborating && connected && !uncertain && !busy && !directInputBlocked} onUpdated={(thread) => setState((current) => ({ ...current, thread: current.thread ? { ...current.thread, model: thread.model, reasoningEffort: thread.reasoningEffort } : thread }))} onBusyChange={setBusy} />
        {queue.data?.data.length ? <div className="chat-queued-strip"><Typography.Text>{t("codex.queued", { count: queue.data.data.length })}</Typography.Text>{queue.data.data.map((entry) => <Typography.Paragraph key={entry.id} ellipsis={{ rows: 1 }}>{entry.input.map((block) => block.text || "").join(" ")}</Typography.Paragraph>)}{collaborating && !running ? <Button size="small" disabled={!connected || busy} onClick={() => void action(() => codexStartQueue(token, threadId))}>{t("codex.startNext")}</Button> : null}</div> : null}
        <Space className="codex-composer-mode" wrap><Typography.Text type="secondary">{t(collaborating ? "codex.collaborationMode" : "codex.readOnlyMode")}</Typography.Text>{running ? <Select size="small" aria-label={t("codex.sendMode")} value={mode} onChange={setMode} options={[{ value: "queue", label: t("codex.queueMode") }, { value: "steer", label: t("codex.steerMode") }]} /> : null}</Space>
        <Composer disabled={!writable} allowAttachments={driver.capabilities.attachments} placeholder={t(running && mode === "queue" ? "codex.queuePlaceholder" : "codex.placeholder")} draftScope={`codex:${nodeId || "local"}:${threadId}`} onSubmit={driver.submit} />
      </div>
    </div>
  );
}
