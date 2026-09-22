import React from "react";
import {
  Field,
  type FieldRenderProps,
  type FormRenderProps,
  FormMeta,
  ValidateTrigger,
  type WorkflowNodeRegistry,
  type WorkflowNodeRenderProps,
  type WorkflowPortEntity,
  WorkflowPortRender,
  useNodeRender,
  useService,
  useWatch,
  WorkflowDragService,
} from "@flowgram.ai/free-layout-editor";
import { Button, Input, InputNumber, Select, Space, Tag } from "antd";
import { DeleteOutlined, PlusOutlined } from "@ant-design/icons";
import type { FlowNode } from "../../lib/api";

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
};

const KIND_LABEL: Record<string, string> = {
  step: "step",
  llm: "llm",
  decision: "decision",
  human: "human",
  branch: "branch",
  loop: "loop",
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

/** Base render for every node: kind badge + title + prompt (+ kind-specific fields). */
function baseForm(extra?: (form: FormRenderProps<FlowNode>["form"]) => React.ReactNode) {
  return ({ form }: FormRenderProps<FlowNode>) => (
    <div style={{ width: 280, padding: 10 }}>
      <div style={{ marginBottom: 8 }}>
        <KindBadge kind={form.getValueIn<string>("kind") ?? "step"} />
      </div>
      <TextField name="title" label="Title" />
      <TextField name="prompt" label="Prompt" rows={3} />
      {extra?.(form)}
    </div>
  );
}

// ---- decision node fields -------------------------------------------------

function DecisionFields({ form }: { form: FormRenderProps<FlowNode>["form"] }) {
  // useWatch subscribes to form value changes so edits re-render this panel
  // (getValueIn alone does not re-render → Add choice had no visible effect).
  const decision = useWatch<{
    decision_type?: string;
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

  return (
    <div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>Decision type</div>
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
          <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>Choices</div>
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
              Add choice
            </Button>
          </Space>
        </div>
      )}
    </div>
  );
}

// ---- human node fields ----------------------------------------------------

function HumanFields({ form }: { form: FormRenderProps<FlowNode>["form"] }) {
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
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>Queue</div>
        <Input value={human.queue ?? ""} onChange={(e) => setHuman({ queue: e.target.value })} />
      </div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>Assignee policy</div>
        <Input
          value={human.assignee_policy ?? ""}
          onChange={(e) => setHuman({ assignee_policy: e.target.value })}
        />
      </div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>Result var</div>
        <Input value={human.result_var ?? ""} onChange={(e) => setHuman({ result_var: e.target.value })} />
      </div>
    </div>
  );
}

// ---- branch node fields ---------------------------------------------------

function BranchFields({ form }: { form: FormRenderProps<FlowNode>["form"] }) {
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
        Branch cases come from the canvas edges: draw an edge from this node
        to a target to add a branch case, delete the edge to remove it.
      </div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>Default target</div>
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
  const loop = form.getValueIn<{
    max_iterations?: number;
    iteration_key?: string;
  }>("loop") ?? {};
  return (
    <div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>Max iterations</div>
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
    </div>
  );
}

// ---- the six node registries ----------------------------------------------

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
  meta: { size, defaultPorts },
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

export const FLOWGRAM_NODE_REGISTRIES: WorkflowNodeRegistry[] = [
  stepRegistry,
  llmRegistry,
  decisionRegistry,
  humanRegistry,
  branchRegistry,
  loopRegistry,
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
  } = useNodeRender(node);
  const dragService = useService(WorkflowDragService);

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
        className={`flowgram-node${selected ? " selected" : ""}`}
        draggable
        onDragStart={(e) => startDrag(e)}
        onTouchStart={(e) => startDrag(e as unknown as React.MouseEvent)}
        onClick={(e) => selectNode(e)}
        onFocus={onFocus}
        onBlur={onBlur}
        data-node-selected={String(selected)}
        style={{
          border: selected ? "1px solid #4d53e8" : "1px solid rgba(6,7,9,0.15)",
          borderRadius: 8,
          background: "#fff",
          boxShadow: "0 2px 6px 0 rgba(0,0,0,0.04), 0 4px 12px 0 rgba(0,0,0,0.02)",
          overflow: "hidden",
          fontSize: 12,
          cursor: "grab",
          width: 300,
          minHeight: 60,
        }}
      >
        {form?.render?.() ?? <div style={{ padding: 8 }}>{node.id}</div>}
      </div>
      {ports.map((p) => (
        <WorkflowPortRender key={p.id} entity={p} onClick={!readonly ? onPortClick : undefined} />
      ))}
    </>
  );
}
