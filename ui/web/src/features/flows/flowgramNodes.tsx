import React from "react";
import {
  Field,
  type FieldRenderProps,
  type FormRenderProps,
  FormMeta,
  ValidateTrigger,
  type WorkflowNodeRegistry,
  type WorkflowNodeRenderProps,
  type WorkflowNodeEntity,
  type WorkflowPortEntity,
  WorkflowPortRender,
  useNodeRender,
  useService,
  useWatch,
  WorkflowDragService,
} from "@flowgram.ai/free-layout-editor";
import { Button, Collapse, Input, InputNumber, Select, Space, Tag, AutoComplete, Typography } from "antd";
import { DeleteOutlined, PlusOutlined } from "@ant-design/icons";
import { useQuery } from "@tanstack/react-query";
import { useI18n } from "../../i18n";
import { listProviders, type FlowDefinition, type FlowNode } from "../../lib/api";
import { useSettingsStore } from "../../store/settings";
import { FunctionFields, ServiceFields } from "./flowgramServiceFields";

// ---------------------------------------------------------------------------
// FlowGram node registries — the six Flow Spec v1 node materials mapped to
// flowgram.ai free-layout nodes, each with a built-in form (formMeta).
//
// Data contract (see flowgramAdapter.ts): every Flow Spec field is carried
// verbatim on node.data:
//   data = { ...spec (id/kind/title/prompt/decision/human/branch/loop/...),
//            kind }
// The form engine reads/writes these fields directly; on save the adapter
// reassembles a FlowDefinition. Nothing is lost in round-trips.
// ---------------------------------------------------------------------------

export const KIND_COLOR: Record<string, string> = {
  step: "#1677ff",
  llm: "#722ed1",
  decision: "#fa8c16",
  human: "#13c2c2",
  branch: "#eb2f96",
  loop: "#52c41a",
  function: "#2f54eb",
  service: "#389e0d",
};

// ---- run-time status highlight -------------------------------------------
// The editor overlays per-node run status (derived from flow-run events) on
// the canvas: FlowGramFlowEditor renders <RunStatusContext.Provider value={map}>
// around the editor; FlowGramBaseNode colors its border accordingly.

export const RUN_STATUS_BORDER: Record<string, string> = {
  pending: "rgba(6,7,9,0.15)",
  running: "#1677ff",
  completed: "#52c41a",
  failed: "#ff4d4f",
  waiting_human: "#fa8c16",
};

const RunStatusContext = React.createContext<
  Map<string, string> | undefined
>(undefined);

export function RunStatusProvider({
  statuses,
  children,
}: {
  statuses?: Map<string, string>;
  children: React.ReactNode;
}) {
  return (
    <RunStatusContext.Provider value={statuses}>{children}</RunStatusContext.Provider>
  );
}

export function useRunStatus(nodeId: string): string | undefined {
  const map = React.useContext(RunStatusContext);
  return map?.get(nodeId);
}

const KIND_LABEL: Record<string, string> = {
  step: "step",
  llm: "llm",
  decision: "decision",
  human: "human",
  branch: "branch",
  loop: "loop",
  function: "function",
  service: "service",
};

// ---- small form helpers (antd + flowgram Field) ---------------------------

function TextField({
  name,
  label,
  rows,
}: {
  name: string;
  label: string;
  rows?: number;
}) {
  return (
    <div style={{ marginBottom: 8 }}>
      <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{label}</div>
      <Field
        name={name}
        render={({ field }: FieldRenderProps<string>) =>
          rows ? (
            <Input.TextArea
              rows={rows}
              value={field.value ?? ""}
              onChange={(e) => field.onChange(e.target.value)}
            />
          ) : (
            <Input value={field.value ?? ""} onChange={(e) => field.onChange(e.target.value)} />
          )
        }
      />
    </div>
  );
}

function KindBadge({ kind }: { kind: string }) {
  return <Tag color={KIND_COLOR[kind] ?? "default"}>{KIND_LABEL[kind] ?? kind}</Tag>;
}

// ---------------------------------------------------------------------------
// Flow definition context — the variable scope chain available to node forms.
// The editor wraps the canvas in <FlowDefProvider def={...}> so each node's
// variable summary can show what it consumes ({{...}} refs), what it produces
// (declared outputs) and the scope chain it can read (flow inputs + every
// other node's declared outputs).
// ---------------------------------------------------------------------------

const FlowDefContext = React.createContext<FlowDefinition | null>(null);

export function FlowDefProvider({
  def,
  children,
}: {
  def?: FlowDefinition | null;
  children: React.ReactNode;
}) {
  return <FlowDefContext.Provider value={def ?? null}>{children}</FlowDefContext.Provider>;
}

export function useFlowDef(): FlowDefinition | null {
  return React.useContext(FlowDefContext);
}

const VAR_REF_RE = /\{\{\s*([^}]+?)\s*\}\}/g;

/** Extract {{...}} variable references from a prompt/script text. */
function scanVarRefs(text: string | undefined): string[] {
  if (!text) return [];
  const re = new RegExp(VAR_REF_RE.source, "g");
  const out: string[] = [];
  let m: RegExpExecArray | null;
  while ((m = re.exec(text)) !== null) out.push(m[1].trim());
  return out;
}

const { Text } = Typography;

/**
 * Variable handling summary for one canvas node (E2 下钻):
 *  - consumes: {{...}} references found in the prompt/scripts
 *  - produces: the node's declared outputs
 *  - scope:    flow inputs + other nodes' declared outputs (作用域链)
 */
function VariableSummary({ form }: { form: FormRenderProps<FlowNode>["form"] }) {
  const prompt = useWatch<string>("prompt") ?? "";
  const pre = useWatch<string>("pre_script") ?? "";
  const post = useWatch<string>("post_script") ?? "";
  const service = useWatch<{
    url?: string;
    headers?: Record<string, string>;
    body?: unknown;
  }>("service") ?? {};
  const outputs = useWatch<{ name?: string; type?: string }[]>("outputs") ?? [];
  const def = useFlowDef();
  const nodeId = (form.getValueIn<string>("id") ?? "").trim();

  const consumed = [
    ...scanVarRefs(prompt),
    ...scanVarRefs(pre),
    ...scanVarRefs(post),
    ...scanVarRefs([
      service.url ?? "",
      ...Object.values(service.headers ?? {}),
      JSON.stringify(service.body ?? ""),
    ].join("\n")),
  ];
  // Scope chain: flow inputs + every OTHER node's declared outputs.
  const scope: { path: string; type?: string }[] = [];
  for (const inp of def?.inputs ?? []) {
    if (inp.name) scope.push({ path: `inputs.${inp.name}`, type: inp.type });
  }
  for (const nd of def?.nodes ?? []) {
    if (nd.id === nodeId) continue;
    for (const o of nd.outputs ?? []) {
      if (o.name) scope.push({ path: `nodes.${nd.id}.outputs.${o.name}`, type: o.type });
    }
  }

  return (
    <div
      style={{
        marginBottom: 8,
        border: "1px dashed #d9d9d9",
        borderRadius: 6,
        padding: "6px 8px",
        background: "#fafafa",
      }}
    >
      <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>变量处理</div>
      <div style={{ fontSize: 11, marginBottom: 3 }}>
        <Text type="secondary">消费:</Text>{" "}
        {consumed.length === 0 ? (
          <Text type="secondary" style={{ fontSize: 11 }}>—</Text>
        ) : (
          consumed.map((r) => (
            <Tag key={r} style={{ marginRight: 2, fontSize: 10 }} color="geekblue">
              {r}
            </Tag>
          ))
        )}
      </div>
      <div style={{ fontSize: 11, marginBottom: 3 }}>
        <Text type="secondary">产出:</Text>{" "}
        {outputs.length === 0 ? (
          <Text type="secondary" style={{ fontSize: 11 }}>—</Text>
        ) : (
          outputs.map((o, i) => (
            <Tag key={`${o.name ?? i}`} style={{ marginRight: 2, fontSize: 10 }} color="green">
              {o.name ?? "?"}
              {o.type ? `: ${o.type}` : ""}
            </Tag>
          ))
        )}
      </div>
      <div style={{ fontSize: 11 }}>
        <Text type="secondary">作用域链:</Text>{" "}
        {scope.length === 0 ? (
          <Text type="secondary" style={{ fontSize: 11 }}>—</Text>
        ) : (
          scope.slice(0, 6).map((s) => (
            <Tag key={s.path} style={{ marginRight: 2, fontSize: 10 }} color="default">
              {s.path}
              {s.type ? `: ${s.type}` : ""}
            </Tag>
          ))
        )}
        {scope.length > 6 && (
          <Text type="secondary" style={{ fontSize: 10 }}>+{scope.length - 6}</Text>
        )}
      </div>
    </div>
  );
}

/** Pre/post bash script editors (E3b) — collapsed so they don't crowd the form. */
function ScriptFields({ form }: { form: FormRenderProps<FlowNode>["form"] }) {
  const pre = useWatch<string>("pre_script") ?? "";
  const post = useWatch<string>("post_script") ?? "";
  return (
    <Collapse
      ghost
      size="small"
      style={{ marginTop: 4 }}
      items={[
        {
          key: "scripts",
          label: "前后置脚本 (bash)",
          children: (
            <div>
              <div style={{ marginBottom: 8 }}>
                <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>
                  pre_script（节点执行前）
                </div>
                <Input.TextArea
                  rows={2}
                  style={{ fontFamily: "monospace", fontSize: 11 }}
                  value={pre}
                  placeholder={"echo start"}
                  onChange={(e) => form.setValueIn("pre_script", e.target.value)}
                />
              </div>
              <div>
                <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>
                  post_script（节点完成后）
                </div>
                <Input.TextArea
                  rows={2}
                  style={{ fontFamily: "monospace", fontSize: 11 }}
                  value={post}
                  placeholder={"echo done"}
                  onChange={(e) => form.setValueIn("post_script", e.target.value)}
                />
              </div>
              <Text type="secondary" style={{ fontSize: 10 }}>
                在 agent workspace 执行；环境变量 FLOW_NODE_ID / FLOW_INPUTS_JSON / FLOW_CTX_JSON
              </Text>
            </div>
          ),
        },
      ]}
    />
  );
}

function NodeTimeoutField() {
  const { t } = useI18n();
  return (
    <div style={{ marginBottom: 8 }}>
      <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.nodeTimeout")}</div>
      <Field
        name="timeout_sec"
        render={({ field }: FieldRenderProps<number>) => (
          <InputNumber
            style={{ width: "100%" }}
            min={0}
            max={2_592_000}
            value={field.value ?? 0}
            onChange={(value) => field.onChange(value ?? 0)}
            addonAfter="s"
          />
        )}
      />
      <Text type="secondary" style={{ fontSize: 10 }}>{t("flows.nodeTimeoutHint")}</Text>
    </div>
  );
}

/** Base render for every node: kind badge + title + prompt (+ kind-specific fields). */
function baseForm(extra?: (form: FormRenderProps<FlowNode>["form"]) => React.ReactNode) {
  return ({ form }: FormRenderProps<FlowNode>) => {
    const kind = useWatch<string>("kind") ?? "step";
    return (
      <div style={{ width: 280, padding: 10 }}>
        <div style={{ marginBottom: 8 }}>
          <KindBadge kind={kind} />
        </div>
        <TextField name="title" label="Title" />
        {kind !== "service" && <TextField name="prompt" label="Prompt" rows={3} />}
        <VariableSummary form={form} />
        {kind !== "branch" && kind !== "loop" && <NodeTimeoutField />}
        {extra?.(form)}
        <ScriptFields form={form} />
      </div>
    );
  };
}

// ---- decision node fields -------------------------------------------------

function DecisionFields({ form }: { form: FormRenderProps<FlowNode>["form"] }) {
  const { t } = useI18n();
  // useWatch subscribes to form value changes so edits re-render this panel
  // (getValueIn alone does not re-render → Add choice had no visible effect).
  const decision = useWatch<{
    decision_type?: string;
    provider?: string;
    choices?: { id: string; label?: string }[];
  }>("decision") ?? {};
  const setDecision = (patch: Record<string, unknown>) => {
    const cur = (form.getValueIn<Record<string, unknown>>("decision") ?? {}) as Record<
      string,
      unknown
    >;
    form.setValueIn("decision", { ...cur, ...patch });
  };
  const choices = decision.choices ?? [];
  // Provider dropdown: configured providers (id) from /providers; empty =
  // fall back to the global agent.decision.provider default.
  const token = useSettingsStore((state) => state.token);
  const providersQuery = useQuery({
    queryKey: ["providers", token],
    queryFn: () => listProviders(token),
    enabled: Boolean(token),
  });
  const providerOptions = (providersQuery.data?.providers ?? []).map((p) => ({
    value: p.id,
    label: p.id,
  }));

  return (
    <div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.nodeProvider")}</div>
        <Select
          style={{ width: "100%" }}
          value={decision.provider ?? undefined}
          onChange={(v) => setDecision({ provider: v })}
          allowClear
          placeholder="default (agent.decision.provider)"
          options={providerOptions}
        />
      </div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.nodeDecisionType")}</div>
        <Select
          style={{ width: "100%" }}
          value={decision.decision_type ?? "choice"}
          onChange={(v) => setDecision({ decision_type: v })}
          options={[
            { value: "choice", label: "choice" },
            { value: "boolean", label: "boolean" },
            { value: "score", label: "score" },
          ]}
        />
      </div>
      {(decision.decision_type ?? "choice") === "choice" && (
        <div style={{ marginBottom: 8 }}>
          <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.nodeChoices")}</div>
          <Space direction="vertical" style={{ width: "100%" }}>
            {choices.map((c, i) => (
              <Space key={i} style={{ width: "100%" }}>
                <Input
                  placeholder="id"
                  value={c.id}
                  onChange={(e) => {
                    const next = [...choices];
                    next[i] = { ...c, id: e.target.value };
                    setDecision({ choices: next });
                  }}
                />
                <Button
                  size="small"
                  type="text"
                  danger
                  icon={<DeleteOutlined />}
                  onClick={() => setDecision({ choices: choices.filter((_, j) => j !== i) })}
                />
              </Space>
            ))}
            <Button
              size="small"
              icon={<PlusOutlined />}
              onClick={() =>
                setDecision({ choices: [...choices, { id: `c${choices.length + 1}` }] })
              }
            >
              {t("flows.nodeAddChoice")}
            </Button>
          </Space>
        </div>
      )}
    </div>
  );
}

// ---- human node fields ----------------------------------------------------

function HumanFields({ form }: { form: FormRenderProps<FlowNode>["form"] }) {
  const { t } = useI18n();
  const human = useWatch<{
    queue?: string;
    assignee_policy?: string;
    result_var?: string;
  }>("human") ?? {};
  const setHuman = (patch: Record<string, unknown>) => {
    const cur = (form.getValueIn<Record<string, unknown>>("human") ?? {}) as Record<
      string,
      unknown
    >;
    form.setValueIn("human", { ...cur, ...patch });
  };
  return (
    <div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.nodeQueue")}</div>
        <Select
          style={{ width: "100%" }}
          value={human.queue ?? undefined}
          onChange={(v) => setHuman({ queue: v })}
          allowClear
          placeholder="ops / support / finance"
          options={[
            { value: "ops", label: "ops" },
            { value: "support", label: "support" },
            { value: "finance", label: "finance" },
          ]}
        />
      </div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.nodeAssigneePolicy")}</div>
        <AutoComplete
          style={{ width: "100%" }}
          value={human.assignee_policy ?? ""}
          onChange={(v) => setHuman({ assignee_policy: v })}
          placeholder="any / role:<id>"
          options={[
            { value: "any", label: "any" },
            { value: "role:ops", label: "role:ops" },
            { value: "role:support", label: "role:support" },
            { value: "role:finance", label: "role:finance" },
          ]}
        />
      </div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.nodeResultVar")}</div>
        <Input value={human.result_var ?? ""} onChange={(e) => setHuman({ result_var: e.target.value })} />
      </div>
    </div>
  );
}

// ---- branch node fields ---------------------------------------------------

function BranchFields({ form }: { form: FormRenderProps<FlowNode>["form"] }) {
  const { t } = useI18n();
  // Branch routing is expressed on canvas as visible condition edges
  // (adapter folds them back into branch.cases on save), so the case list is
  // defined by the canvas edges — not edited here. Only the default target
  // is configured in the form.
  const branch = useWatch<{
    cases?: { name?: string; to?: string; condition?: unknown }[];
    default_to?: string;
  }>("branch") ?? {};
  const setBranch = (patch: Record<string, unknown>) => {
    const cur = (form.getValueIn<Record<string, unknown>>("branch") ?? {}) as Record<
      string,
      unknown
    >;
    form.setValueIn("branch", { ...cur, ...patch });
  };

  return (
    <div>
      <div style={{ marginBottom: 8, fontSize: 12, color: "#888" }}>
        {t("flows.nodeBranchHint")}
      </div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.nodeDefaultTarget")}</div>
        <Input
          value={branch.default_to ?? ""}
          placeholder="node id (or draw a default edge)"
          onChange={(e) => setBranch({ default_to: e.target.value })}
        />
      </div>
    </div>
  );
}

// ---- loop node fields -----------------------------------------------------

function LoopFields({ form }: { form: FormRenderProps<FlowNode>["form"] }) {
  const { t } = useI18n();
  const loop = form.getValueIn<{
    max_iterations?: number;
    iteration_key?: string;
  }>("loop") ?? {};
  return (
    <div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.nodeMaxIterations")}</div>
        <Field
          name="loop.max_iterations"
          render={({ field }: FieldRenderProps<number>) => (
            <InputNumber
              style={{ width: "100%" }}
              min={1}
              value={field.value ?? loop.max_iterations ?? 5}
              onChange={(v) => field.onChange(v ?? 5)}
            />
          )}
        />
      </div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.nodeIterationKey")}</div>
        <Field
          name="loop.iteration_key"
          render={({ field }: FieldRenderProps<string>) => (
            <Input
              value={field.value ?? ""}
              placeholder="iteration (optional, for concurrent loop dedup)"
              onChange={(e) => field.onChange(e.target.value)}
            />
          )}
        />
      </div>
    </div>
  );
}

// ---- the eight node registries ----------------------------------------------

const size = { width: 300, height: 120 };
const defaultPorts = [
  { type: "input" as const },
  { type: "output" as const },
];

const stepRegistry: WorkflowNodeRegistry = {
  type: "step",
  meta: { size, defaultPorts },
  info: { icon: "", description: "fixed step: subagent_task / tool_call" },
  formMeta: {
    render: baseForm(),
    validateTrigger: ValidateTrigger.onChange,
  } as FormMeta,
};

const llmRegistry: WorkflowNodeRegistry = {
  type: "llm",
  meta: { size, defaultPorts },
  info: { icon: "", description: "pure reasoning node: llm_task" },
  formMeta: {
    render: baseForm(),
    validateTrigger: ValidateTrigger.onChange,
  } as FormMeta,
};

const decisionRegistry: WorkflowNodeRegistry = {
  type: "decision",
  // E4: dynamic ports — the node body renders one labelled pill per choice
  // (see DecisionBranchPorts below); flowgram auto-creates a real output
  // port per pill via the data-port-id mechanism, so every branch gets its
  // own visible anchor and the canvas shows N branches = N ports.
  meta: { size, defaultPorts: [{ type: "input" }], useDynamicPort: true },
  info: { icon: "", description: "low-cost structured decision (System-1 model)" },
  formMeta: {
    render: baseForm((form) => <DecisionFields form={form} />),
    validateTrigger: ValidateTrigger.onChange,
  } as FormMeta,
};

const humanRegistry: WorkflowNodeRegistry = {
  type: "human",
  meta: { size, defaultPorts },
  info: { icon: "", description: "manual fallback: user_input + human task store" },
  formMeta: {
    render: baseForm((form) => <HumanFields form={form} />),
    validateTrigger: ValidateTrigger.onChange,
  } as FormMeta,
};

const branchRegistry: WorkflowNodeRegistry = {
  type: "branch",
  meta: { size, defaultPorts },
  info: { icon: "", description: "no-job gateway: evaluates cases synchronously" },
  formMeta: {
    render: baseForm((form) => <BranchFields form={form} />),
    validateTrigger: ValidateTrigger.onChange,
  } as FormMeta,
};

const loopRegistry: WorkflowNodeRegistry = {
  type: "loop",
  meta: { size, defaultPorts },
  info: { icon: "", description: "compiled to control_flow append edges" },
  formMeta: {
    render: baseForm((form) => <LoopFields form={form} />),
    validateTrigger: ValidateTrigger.onChange,
  } as FormMeta,
};

const functionRegistry: WorkflowNodeRegistry = {
  type: "function",
  meta: { size, defaultPorts },
  info: { icon: "", description: "code node: js (goja) or wasm (plugin ref)" },
  formMeta: {
    render: baseForm((form) => <FunctionFields form={form} />),
    validateTrigger: ValidateTrigger.onChange,
} as FormMeta,
};

const serviceRegistry: WorkflowNodeRegistry = {
  type: "service",
  meta: { size, defaultPorts },
  info: { icon: "", description: "HTTP(S) JSON service call" },
  formMeta: {
    render: baseForm((form) => <ServiceFields form={form} />),
    validateTrigger: ValidateTrigger.onChange,
  } as FormMeta,
};

export const FLOWGRAM_NODE_REGISTRIES: WorkflowNodeRegistry[] = [
  stepRegistry,
  llmRegistry,
  decisionRegistry,
  humanRegistry,
  branchRegistry,
  loopRegistry,
  functionRegistry,
  serviceRegistry,
];

/**
 * Default node render — full interactive node wrapper (official demo pattern):
 * - draggable + startDrag  → 节点可拖拽
 * - WorkflowPortRender for every port → 端口渲染，拖线连线锚点
 * - selectNode / nodeRef / onFocus / onBlur → 选中与焦点
 * - form.render() → 节点自带表单（nodeEngine 开启时）
 */
export function FlowGramBaseNode({ node }: WorkflowNodeRenderProps) {
  const {
    form,
    selected,
    startDrag,
    ports,
    selectNode,
    nodeRef,
    onFocus,
    onBlur,
    readonly,
    data,
  } = useNodeRender(node);
  const dragService = useService(WorkflowDragService);
  const runStatus = useRunStatus(node.id);
  const statusBorder = runStatus ? RUN_STATUS_BORDER[runStatus] : undefined;

  // Decision nodes: render one labelled branch pill per choice. Each pill
  // carries data-port-id/data-port-type so flowgram's dynamic-port mechanism
  // auto-creates a REAL output port anchored on it — the canvas shows N
  // branches = N labelled ports, and each line starts from its own pill.
  const nodeType = String((node as { type?: string | number }).type ?? "");
  const decisionData = (form?.values?.decision ?? data?.decision ?? {}) as {
    choices?: { id: string; label?: string }[];
  };
  const decisionChoices = nodeType === "decision" ? (decisionData.choices ?? []) : [];

  // E4 robustness: flowgram's dynamic-port mechanism only rescans
  // [data-port-id] on node size change, so on first render and whenever the
  // choice list changes (add/remove a branch) the labelled branch ports would
  // go stale. Manually trigger updateDynamicPorts() after the DOM commits.
  const choiceKey = decisionChoices.map((c) => c.id).join(",");
  React.useEffect(() => {
    if (nodeType !== "decision") return;
    const t = setTimeout(() => {
      const portsData = (node as unknown as {
        ports?: { updateDynamicPorts?: () => void };
      }).ports;
      portsData?.updateDynamicPorts?.();
    }, 0);
    return () => clearTimeout(t);
  }, [choiceKey, nodeType, node]);

  // Click an OUTPUT port to start drawing a connection line; dropping on
  // another node's input port is completed natively by the core drag service.
  const onPortClick = React.useCallback(
    (e: React.MouseEvent, port: WorkflowPortEntity) => {
      if (readonly) return;
      if (port.portType === "input") return;
      void dragService.startDrawingLine(port, { clientX: e.clientX, clientY: e.clientY });
    },
    [dragService, readonly],
  );

  return (
    <>
      <div
        ref={nodeRef}
        className={`flowgram-node${selected ? " selected" : ""}${runStatus ? ` run-${runStatus}` : ""}`}
        draggable
        onDragStart={(e) => startDrag(e)}
        onTouchStart={(e) => startDrag(e as unknown as React.MouseEvent)}
        onClick={(e) => selectNode(e)}
        onFocus={onFocus}
        onBlur={onBlur}
        data-node-selected={String(selected)}
        style={{
          border: selected
            ? "1px solid #4d53e8"
            : statusBorder
              ? `1px solid ${statusBorder}`
              : "1px solid rgba(6,7,9,0.15)",
          borderRadius: 8,
          background: "#fff",
          boxShadow:
            runStatus === "running"
              ? "0 0 0 3px rgba(22,119,255,0.18)"
              : "0 2px 6px 0 rgba(0,0,0,0.04), 0 4px 12px 0 rgba(0,0,0,0.02)",
          overflow: "hidden",
          fontSize: 12,
          cursor: "grab",
          width: 300,
          minHeight: 60,
        }}
      >
        {form?.render?.() ?? <div style={{ padding: 8 }}>{node.id}</div>}
        {decisionChoices.length > 0 && (
          <div
            style={{
              display: "flex",
              flexDirection: "column",
              gap: 2,
              padding: "2px 6px 6px",
              borderTop: "1px dashed #e8e8e8",
            }}
          >
            <div style={{ fontSize: 10, color: "#999", marginBottom: 2 }}>分支（拖出连线）</div>
            {decisionChoices.map((c) => (
              <div
                key={c.id}
                data-port-id={c.id}
                data-port-type="output"
                data-port-location="right"
                style={{
                  position: "relative",
                  display: "flex",
                  alignItems: "center",
                  gap: 4,
                  padding: "1px 8px",
                  borderRadius: 10,
                  background: "#f6f7fb",
                  border: "1px solid #e0e3f0",
                  fontSize: 11,
                  color: "#333",
                  cursor: "grab",
                }}
              >
                <span style={{ fontFamily: "monospace", fontSize: 10, color: "#7a6ff0" }}>
                  {c.label ?? c.id}
                </span>
              </div>
            ))}
          </div>
        )}
      </div>
      {ports.map((p) => (
        <WorkflowPortRender key={p.id} entity={p} onClick={!readonly ? onPortClick : undefined} />
      ))}
    </>
  );
}
