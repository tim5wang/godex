import { useEffect, useMemo, useState, type RefObject } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { App as AntApp, Button, Checkbox, Input, InputNumber, Modal, Popconfirm, Select, Segmented, Space, Switch, Tag, Typography } from "antd";
import { ApartmentOutlined, BugOutlined, CloseOutlined, PlayCircleOutlined, PlusOutlined, RocketOutlined, StepForwardOutlined } from "@ant-design/icons";
import { showError } from "../../lib/notifications";
import { createFlowRun, getFlowRun, listFlowVersions, replyFlowRunHuman, stepFlowRun, streamFlowRunEvents, type FlowDefinition, type FlowRunEvent, type StepFlowView, type FlowSummaryView } from "../../lib/api";
import { FlowGramFlowEditor, type FlowGramFlowEditorHandle } from "./FlowGramFlowEditor";
import { TemplateLibrary } from "./TemplateLibrary";

const { Title, Text, Paragraph } = Typography;

export function FlowCanvasMain(props: {
  flow: FlowSummaryView;
  token: string | null;
  t: (k: string, v?: Record<string, string | number>) => string;
  message: ReturnType<typeof AntApp.useApp>["message"];
  onRefresh: () => void;
  onPublish: (version: string) => void;
  onRun: (version: string) => void;
  onOpenDetail: () => void;
  externalDef?: { def: FlowDefinition; stamp: number };
  editorHandleRef: RefObject<FlowGramFlowEditorHandle | null>;
  onExternalDefConsumed: () => void;
}) {
  const {
    flow,
    token,
    t,
    message,
    onRefresh,
    onPublish,
    onRun,
    onOpenDetail,
    externalDef,
    editorHandleRef,
    onExternalDefConsumed,
  } = props;

  const queryClient = useQueryClient();

  const versionsQuery = useQuery({
    queryKey: ["flow", flow.flow_id],
    queryFn: () => listFlowVersions(token, flow.flow_id),
  });
  const versions = versionsQuery.data ?? [];

  const [editorVersion, setEditorVersion] = useState<string>();
  const editorDef = useMemo(() => {
    // Editor defaults to the latest draft with a definition.
    const pick = editorVersion ?? [...versions].reverse().find((v) => v.definition)?.version;
    return versions.find((v) => v.version === pick)?.definition;
  }, [versions, editorVersion]);

  // ---- debug mode ---------------------------------------------------------
  // The canvas is a debugger: pick a version + test inputs, start a run, and
  // the editor highlights per-node status from the polled event log. Production
  // traffic goes through POST /v1/gateway/{route} (see DetailDrawer).
  // Step mode (单步运行): the run is created WITHOUT auto-start (step_mode=1)
  // and the user advances it node-by-node via POST /step; each step returns
  // the per-node outputs/context so the debug panel shows live variable values.
  const [debugMode, setDebugMode] = useState(false);
  const [debugVersion, setDebugVersion] = useState<string>();
  // 表单模式：声明字段值（key=name）+ 自定义字段（原始字符串，提交时解析）
  const [debugForm, setDebugForm] = useState<Record<string, unknown>>({});
  const [debugCustomFields, setDebugCustomFields] = useState<{ name: string; raw: string }[]>([]);
  // JSON 模式：整块文本兑底（手工编辑复杂结构时用）
  const [debugJsonMode, setDebugJsonMode] = useState(false);
  const [debugJsonText, setDebugJsonText] = useState("{}");
  const [debugRunId, setDebugRunId] = useState<string>();
  const [debugStarted, setDebugStarted] = useState(false);
  const [debugStepMode, setDebugStepMode] = useState(false);
  const [stepView, setStepView] = useState<StepFlowView | null>(null);
  const [stepBusy, setStepBusy] = useState(false);
  // 单步调试中 waiting_human 节点的回复值（提交后推进到下一步）。
  const [humanReplyValue, setHumanReplyValue] = useState<Record<string, string>>({});
  const [humanReplyBusy, setHumanReplyBusy] = useState(false);

  // 调试所用版本的定义（表单字段按它的 inputs 声明生成）。
  const debugDef = useMemo(() => {
    const pick = debugVersion ?? [...versions].reverse().find((v) => v.definition)?.version;
    return versions.find((v) => v.version === pick)?.definition;
  }, [versions, debugVersion]);

  const debugMutation = useMutation({
    mutationFn: ({ version, inputs }: { version: string; inputs: Record<string, unknown> }) =>
      createFlowRun(token, flow.flow_id, { version, inputs, step_mode: debugStepMode }),
    onSuccess: (run) => {
      setDebugRunId(run.run_id);
      setDebugStarted(true);
      message.success(`${t("flows.debugStarted")} ${run.run_id}`);
    },
    onError: (err) => showError(message, err, t("flows.runFailed")),
  });

  // Single-step: advance the debug run by one node and refresh per-node state.
  const stepOnce = async () => {
    if (!debugRunId || !debugStepMode) return;
    setStepBusy(true);
    try {
      const view = await stepFlowRun(token, debugRunId, flow.flow_id);
      setStepView(view);
      // Terminal: stop the SSE stream and refresh the run record.
      if (view.terminal) setDebugStarted(false);
    } catch (err) {
      showError(message, err, t("flows.stepFailed"));
    } finally {
      setStepBusy(false);
    }
  };

  // Submit a value for a waiting_human node (单步调试中人工节点完成并推进).
  const replyHuman = async (nodeId: string) => {
    if (!debugRunId) return;
    const value = humanReplyValue[nodeId] ?? "";
    if (value.trim() === "") {
      message.warning(t("flows.humanReplyEmpty"));
      return;
    }
    setHumanReplyBusy(true);
    try {
      const view = await replyFlowRunHuman(token, debugRunId, flow.flow_id, nodeId, value.trim());
      // 回复完成节点后刷新 step 视图（后续节点变为 ready）。
      if (debugStepMode) {
        const next = await stepFlowRun(token, debugRunId, flow.flow_id);
        setStepView(next);
        if (next.terminal) setDebugStarted(false);
      } else {
        void queryClient.invalidateQueries({ queryKey: ["flow-run", flow.flow_id, debugRunId] });
      }
      message.success(`${t("flows.humanReplied")} ${nodeId}`);
    } catch (err) {
      showError(message, err, t("flows.humanReplyFailed"));
    } finally {
      setHumanReplyBusy(false);
    }
  };

  // Live debug events: SSE stream (P3 余项 4) — the backend pushes new
  // workflow events incrementally (500ms tick) until the run reaches a
  // terminal state; the canvas highlights nodes as events arrive (replaces
  // the previous 1.5s full-log polling).
  const [debugEvents, setDebugEvents] = useState<FlowRunEvent[]>([]);
  useEffect(() => {
    if (!debugRunId || !debugStarted) {
      setDebugEvents([]);
      return;
    }
    const ctrl = new AbortController();
    void streamFlowRunEvents(token, debugRunId, flow.flow_id, (ev) => {
      setDebugEvents((prev) => [...prev, ev]);
    }, ctrl.signal).catch(() => {
      /* stream closed / aborted — snapshot queries still work */
    });
    return () => ctrl.abort();
  }, [debugRunId, debugStarted, token, flow.flow_id]);

  const debugRunQuery = useQuery({
    queryKey: ["flow-run", flow.flow_id, debugRunId],
    queryFn: () =>
      debugRunId
        ? getFlowRun(token, debugRunId, flow.flow_id)
        : Promise.reject(new Error("no run")),
    enabled: Boolean(debugRunId),
    refetchInterval: debugStarted ? 1500 : false,
  });

  const startDebug = () => {
    const version = debugVersion ?? editorDef?.version;
    if (!version) {
      message.warning(t("flows.noDefinition"));
      return;
    }
    let inputs: Record<string, unknown> = {};
    if (debugJsonMode) {
      // JSON 模式：整块文本解析（兑底）。
      try {
        inputs = debugJsonText.trim() ? (JSON.parse(debugJsonText) as Record<string, unknown>) : {};
      } catch (err) {
        message.error(
          `${t("flows.jsonInvalid")}: ${err instanceof Error ? err.message : String(err)}`,
        );
        return;
      }
    } else {
      inputs = mergeDebugInputs();
    }
    debugMutation.mutate({ version, inputs });
  };

  // mergeDebugInputs 把表单模式的声明字段 + 自定义字段合并为提交值：
  // 声明字段按 type 已转换（number/boolean 由控件直出）；object/array/any 与
  // 自定义字段若是字符串则尝试 JSON.parse（失败保留字面量）。
  const mergeDebugInputs = (): Record<string, unknown> => {
    const merged: Record<string, unknown> = { ...debugForm };
    for (const inp of debugDef?.inputs ?? []) {
      const t = (inp.type ?? "string").toLowerCase();
      if ((t === "object" || t === "array" || t === "any") && typeof merged[inp.name] === "string") {
        const s = (merged[inp.name] as string).trim();
        if (s !== "") {
          try {
            merged[inp.name] = JSON.parse(s);
          } catch {
            // 保留字符串字面量
          }
        }
      }
    }
    for (const f of debugCustomFields) {
      const name = f.name.trim();
      if (!name) continue;
      const raw = f.raw.trim();
      if (raw === "") continue;
      try {
        merged[name] = JSON.parse(raw);
      } catch {
        merged[name] = raw;
      }
    }
    return merged;
  };

  const switchDebugMode = (json: boolean) => {
    setDebugJsonMode(json);
    if (json) {
      // 切到 JSON 模式时把当前表单值序列化进去，方便继续微调。
      try {
        setDebugJsonText(JSON.stringify(mergeDebugInputs(), null, 2));
      } catch {
        // 保留原文本
      }
    }
  };

  const clearDebug = () => {
    setDebugRunId(undefined);
    setDebugStarted(false);
  };

  const debugRun = debugRunQuery.data;
  const debugStatus = debugRun?.status ?? (debugStarted ? "running" : undefined);

  // Aggregate run context for the debug panel: the exact shape a function
  // node handler sees as ctx = { inputs, outputs } at the current step point —
  // inputs from the run + completed nodes' outputs keyed by node id. This is
  // the "执行到某个节点时的上下文变量值" the user asked for in step mode.
  const stepCtx = useMemo(() => {
    if (!stepView) return null;
    const outputs: Record<string, unknown> = {};
    for (const n of stepView.nodes) {
      if (n.status === "completed" && n.outputs && Object.keys(n.outputs).length > 0) {
        outputs[n.id] = n.outputs;
      }
    }
    return { inputs: debugRun?.inputs ?? {}, outputs };
  }, [stepView, debugRun]);

  // 模板库（B3）：从抽屉移到主界面 — 工具栏「模板库」按钮打开 Modal，
  // 选模板一键应用为新版本（不改动画布内容）。
  const [tplOpen, setTplOpen] = useState(false);

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 8, height: "100%" }}>
      <Space style={{ justifyContent: "space-between", width: "100%" }} align="center" wrap>
        <Space align="center">
          <Title level={5} style={{ margin: 0 }}>
            {flow.flow_id}
          </Title>
          {flow.published && <Tag color="green">{`pub ${flow.published}`}</Tag>}
        </Space>
        <Space wrap>
          <Select
            size="small"
            style={{ width: 160 }}
            placeholder={t("flows.version")}
            value={editorVersion}
            onChange={setEditorVersion}
            options={versions.map((v) => ({ value: v.version, label: `${v.version} (${v.status})` }))}
          />
          <Popconfirm
            title={t("flows.publishConfirm")}
            onConfirm={() => editorDef && onPublish(editorDef.version)}
          >
            <Button size="small" icon={<RocketOutlined />} disabled={!editorDef}>
              {t("flows.publish")}
            </Button>
          </Popconfirm>
          <Button size="small" icon={<ApartmentOutlined />} onClick={() => setTplOpen(true)}>
            {t("flows.templates")}
          </Button>
          <Button
            size="small"
            icon={<BugOutlined />}
            type={debugMode ? "primary" : "default"}
            disabled={!editorDef}
            onClick={() => setDebugMode((v) => !v)}
          >
            {t("flows.debug")}
          </Button>
          <Button size="small" onClick={onOpenDetail}>
            {t("flows.detail")}
          </Button>
        </Space>
      </Space>

      <div style={{ flex: 1, minHeight: 420, display: "flex", gap: 8, minWidth: 0 }}>
        <div style={{ flex: 1, minWidth: 0 }}>
          <FlowGramFlowEditor
            ref={editorHandleRef}
            flowId={flow.flow_id}
            token={token}
            t={t}
            versions={versions}
            externalDef={externalDef}
            runEvents={debugEvents}
            onSaved={(savedVersion) => {
              message.success(t("flows.savedVersion", { v: savedVersion }));
              onExternalDefConsumed();
              onRefresh();
            }}
            onSaveError={(err) => showError(message, err, t("flows.saveFailed"))}
          />
        </div>

        {debugMode && (
          <div
            style={{
              width: 360,
              flexShrink: 0,
              border: "1px solid #e5e5e5",
              borderRadius: 8,
              padding: 10,
              background: "#fafafa",
              display: "flex",
              flexDirection: "column",
              gap: 8,
              overflowY: "auto",
              maxHeight: "100%",
            }}
          >
            <Space wrap align="center">
              <Text strong style={{ fontSize: 12 }}>
                {t("flows.debugPanel")}
              </Text>
              <Select
                size="small"
                style={{ width: 140 }}
                placeholder={t("flows.version")}
                value={debugVersion}
                onChange={setDebugVersion}
                options={versions.map((v) => ({ value: v.version, label: `${v.version} (${v.status})` }))}
              />
            </Space>
            <Space align="center" style={{ justifyContent: "space-between", width: "100%" }}>
              <Segmented
                size="small"
                value={debugJsonMode ? "json" : "form"}
                options={[
                  { label: t("flows.debugInputFormMode"), value: "form" },
                  { label: t("flows.debugInputJsonMode"), value: "json" },
                ]}
                onChange={(v) => switchDebugMode(v === "json")}
              />
              <Text type="secondary" style={{ fontSize: 10 }}>
                {t("flows.debugInputTitle")}
              </Text>
            </Space>
            {debugJsonMode ? (
              <Input.TextArea
                size="small"
                style={{ width: "100%", fontFamily: "monospace", fontSize: 11 }}
                rows={4}
                placeholder='{"task": "..."}'
                value={debugJsonText}
                onChange={(e) => setDebugJsonText(e.target.value)}
              />
            ) : (
              <div
                style={{
                  display: "flex",
                  flexDirection: "column",
                  gap: 4,
                  maxHeight: 220,
                  overflowY: "auto",
                  border: "1px solid #f0f0f0",
                  borderRadius: 6,
                  padding: 6,
                  background: "#fff",
                }}
              >
                {(debugDef?.inputs ?? []).length === 0 && debugCustomFields.length === 0 && (
                  <Text type="secondary" style={{ fontSize: 11 }}>
                    {t("flows.debugInputEmpty")}
                  </Text>
                )}
                {(debugDef?.inputs ?? []).map((inp) => {
                  const key = inp.name;
                  const value = debugForm[key];
                  const type = (inp.type ?? "string").toLowerCase();
                  return (
                    <div key={key} style={{ display: "flex", gap: 6, alignItems: "center" }}>
                      <Text style={{ fontSize: 11, width: 96, flexShrink: 0, overflow: "hidden", textOverflow: "ellipsis", whiteSpace: "nowrap" }} title={inp.desc ?? inp.name}>
                        {key}
                      </Text>
                      {type === "number" ? (
                        <InputNumber
                          size="small"
                          style={{ width: "100%" }}
                          placeholder={inp.desc ?? "number"}
                          value={typeof value === "number" ? value : undefined}
                          onChange={(v) => setDebugForm((prev) => ({ ...prev, [key]: v }))}
                        />
                      ) : type === "boolean" ? (
                        <Switch
                          size="small"
                          checked={value === true}
                          onChange={(v) => setDebugForm((prev) => ({ ...prev, [key]: v }))}
                        />
                      ) : type === "object" || type === "array" || type === "any" ? (
                        <Input
                          size="small"
                          style={{ width: "100%", fontFamily: "monospace", fontSize: 11 }}
                          placeholder={inp.desc ?? (type === "array" ? '[{"k": 1}]' : '{"k": 1}')}
                          value={typeof value === "string" ? value : value === undefined ? "" : JSON.stringify(value)}
                          onChange={(e) => setDebugForm((prev) => ({ ...prev, [key]: e.target.value }))}
                        />
                      ) : (
                        <Input
                          size="small"
                          style={{ width: "100%" }}
                          placeholder={inp.desc ?? "string"}
                          value={typeof value === "string" ? value : value === undefined ? "" : String(value)}
                          onChange={(e) => setDebugForm((prev) => ({ ...prev, [key]: e.target.value }))}
                        />
                      )}
                    </div>
                  );
                })}
                {debugCustomFields.map((f, idx) => (
                  <div key={idx} style={{ display: "flex", gap: 6, alignItems: "center" }}>
                    <Input
                      size="small"
                      style={{ width: 96, flexShrink: 0, fontSize: 11 }}
                      placeholder={t("flows.debugInputName")}
                      value={f.name}
                      onChange={(e) =>
                        setDebugCustomFields((prev) =>
                          prev.map((x, i) => (i === idx ? { ...x, name: e.target.value } : x)),
                        )
                      }
                    />
                    <Input
                      size="small"
                      style={{ width: "100%", fontSize: 11 }}
                      placeholder={t("flows.debugInputPlaceholder")}
                      value={f.raw}
                      onChange={(e) =>
                        setDebugCustomFields((prev) =>
                          prev.map((x, i) => (i === idx ? { ...x, raw: e.target.value } : x)),
                        )
                      }
                    />
                    <Button
                      size="small"
                      type="text"
                      icon={<CloseOutlined />}
                      onClick={() =>
                        setDebugCustomFields((prev) => prev.filter((_, i) => i !== idx))
                      }
                    />
                  </div>
                ))}
                <Button
                  size="small"
                  type="dashed"
                  icon={<PlusOutlined />}
                  style={{ fontSize: 11 }}
                  onClick={() => setDebugCustomFields((prev) => [...prev, { name: "", raw: "" }])}
                >
                  {t("flows.debugInputAdd")}
                </Button>
              </div>
            )}
            <Space wrap>
              <Checkbox
                checked={debugStepMode}
                onChange={(e) => setDebugStepMode(e.target.checked)}
              >
                <Text style={{ fontSize: 12 }}>{t("flows.debugStepMode")}</Text>
              </Checkbox>
              <Button
                size="small"
                type="primary"
                icon={<PlayCircleOutlined />}
                loading={debugMutation.isPending}
                onClick={startDebug}
              >
                {t("flows.debugStart")}
              </Button>
            </Space>
            {debugRunId && (
              <Space wrap>
                <Tag color={debugStatus === "completed" ? "green" : debugStatus === "error" ? "red" : "processing"}>
                  {debugRunId.slice(0, 12)}… {debugStatus ?? "running"}
                </Tag>
                {debugStepMode && !stepView?.terminal && (
                  <Button
                    size="small"
                    type="primary"
                    icon={<StepForwardOutlined />}
                    loading={stepBusy}
                    onClick={stepOnce}
                  >
                    {t("flows.stepOnce")}
                  </Button>
                )}
                <Button size="small" onClick={clearDebug}>
                  {t("flows.debugClear")}
                </Button>
              </Space>
            )}
            <Paragraph type="secondary" style={{ fontSize: 11, marginBottom: 0 }}>
              {t("flows.debugHint")}
              {debugRunId && ` · ${t("flows.debugStatus", { status: debugStatus ?? "running" })}`}
            </Paragraph>

            {debugStepMode && stepView && stepCtx && (
              <div
                style={{
                  border: "1px solid #d9d9d9",
                  borderRadius: 6,
                  background: "#fffbe6",
                  padding: 6,
                  maxHeight: 180,
                  overflowY: "auto",
                }}
              >
                <Text strong style={{ fontSize: 11 }}>
                  {t("flows.stepCtxTitle")}
                </Text>
                <pre
                  style={{
                    margin: "4px 0 0 0",
                    fontSize: 10,
                    whiteSpace: "pre-wrap",
                    wordBreak: "break-all",
                    fontFamily: "monospace",
                  }}
                >
                  {JSON.stringify(stepCtx, null, 2)}
                </pre>
                <Text type="secondary" style={{ fontSize: 10 }}>
                  {t("flows.stepCtxHint")}
                </Text>
              </div>
            )}

            {debugStepMode && stepView && (
              <div style={{ border: "1px solid #eee", borderRadius: 6, background: "#fff", padding: 6 }}>
                <Text type="secondary" style={{ fontSize: 11 }}>
                  {t("flows.stepNodeContext")}
                </Text>
                <div style={{ marginTop: 4, display: "flex", flexDirection: "column", gap: 4 }}>
                  {stepView.nodes.map((n) => (
                    <div
                      key={n.id}
                      style={{
                        border: "1px solid #f0f0f0",
                        borderRadius: 4,
                        padding: "4px 6px",
                        background: n.status === "completed" ? "#f6ffed" : n.status === "error" ? "#fff2f0" : "#fafafa",
                      }}
                    >
                      <Space size={6} style={{ width: "100%", justifyContent: "space-between" }}>
                        <Space size={4} style={{ minWidth: 0 }}>
                          <Text style={{ fontSize: 11, fontFamily: "monospace" }} strong>
                            {n.id}
                          </Text>
                          <Tag style={{ marginRight: 0, fontSize: 10, color: "#555", background: "#f0f0f0", border: "none" }}>
                            {n.kind || "?"}
                          </Tag>
                        </Space>
                        <Tag style={{ marginRight: 0, fontSize: 10 }}>{n.status}</Tag>
                      </Space>
                      {n.error && (
                        <Text type="danger" style={{ fontSize: 10, display: "block" }}>
                          {n.error}
                        </Text>
                      )}
                      {n.outputs && Object.keys(n.outputs).length > 0 && (
                        <div style={{ marginTop: 2 }}>
                          <pre
                            style={{
                              margin: 0,
                              fontSize: 10,
                              whiteSpace: "pre-wrap",
                              wordBreak: "break-all",
                              maxHeight: 90,
                              overflowY: "auto",
                            }}
                          >
                            {JSON.stringify(n.outputs, null, 2)}
                          </pre>
                        </div>
                      )}
                      {n.status === "waiting_human" && (
                        <Space.Compact style={{ width: "100%", marginTop: 4 }}>
                          <Input
                            size="small"
                            placeholder={t("flows.humanReplyPlaceholder")}
                            value={humanReplyValue[n.id] ?? ""}
                            onChange={(e) =>
                              setHumanReplyValue((prev) => ({ ...prev, [n.id]: e.target.value }))
                            }
                            onPressEnter={() => replyHuman(n.id)}
                          />
                          <Button
                            size="small"
                            type="primary"
                            loading={humanReplyBusy}
                            onClick={() => replyHuman(n.id)}
                          >
                            {t("flows.humanReplySubmit")}
                          </Button>
                        </Space.Compact>
                      )}
                    </div>
                  ))}
                </div>
              </div>
            )}

            {debugRunId && debugEvents.length > 0 && (
              <div
                style={{
                  border: "1px solid #eee",
                  borderRadius: 6,
                  background: "#fff",
                  maxHeight: 160,
                  overflowY: "auto",
                  padding: 6,
                  fontFamily: "monospace",
                  fontSize: 11,
                }}
              >
                <div
                  style={{
                    display: "flex",
                    justifyContent: "space-between",
                    alignItems: "center",
                    marginBottom: 4,
                  }}
                >
                  <Text type="secondary" style={{ fontSize: 11 }}>
                    {t("flows.debugEvents")}
                  </Text>
                  <Button
                    size="small"
                    type="text"
                    style={{ fontSize: 11, height: 20, padding: "0 4px" }}
                    onClick={() => setDebugEvents([])}
                  >
                    {t("flows.debugClearEvents")}
                  </Button>
                </div>
                {debugEvents.map((ev, i) => {
                  const at = ev.at ? new Date(ev.at as string).toLocaleTimeString() : "";
                  const node = ev.node_id ? `[${ev.node_id}]` : "";
                  const { event, node_id: _n, at: _a, ...rest } = ev;
                  const payload = Object.keys(rest).length > 0 ? JSON.stringify(rest) : "";
                  return (
                    <div key={i} style={{ whiteSpace: "pre-wrap", lineHeight: 1.5 }}>
                      {`${at} ${event} ${node} ${payload}`.trim()}
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        )}
      </div>

      {/* 模板库（B3）：主界面 Modal，选模板一键应用为新版本 */}
      <Modal
        title={t("flows.templates")}
        open={tplOpen}
        onCancel={() => setTplOpen(false)}
        footer={null}
        width={680}
      >
        <TemplateLibrary
          flowId={flow.flow_id}
          token={token}
          t={t}
          versions={versions}
          onApplied={() => {
            onRefresh();
            setTplOpen(false);
          }}
        />
      </Modal>
    </div>
  );
}
