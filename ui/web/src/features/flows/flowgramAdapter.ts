import type {
  WorkflowJSON,
  WorkflowNodeJSON,
  WorkflowEdgeJSON,
} from "@flowgram.ai/free-layout-editor";
import type { FlowDefinition, FlowEdge, FlowNode } from "../../lib/api";

// ---------------------------------------------------------------------------
// FlowGram adapter — Flow Spec v1 ⇄ flowgram.ai WorkflowJSON (free-layout)
//
// Flow Spec v1 (internal model):
//   Definition { flow_id, version, status, inputs, nodes[], edges[] }
//   Node  { id, kind(step|llm|decision|human|branch|loop), title, prompt,
//           decision?, human?, branch?, loop?, canvas_pos? }
//   Edge  { id, from, to, edge_type(data_dependency|handoff|condition), when? }
//
// flowgram.ai WorkflowJSON (free-layout-editor):
//   { nodes: WorkflowNodeJSON[], edges: WorkflowEdgeJSON[] }
//   WorkflowNodeJSON { id, type, meta{position}, data(业务数据) }
//   WorkflowEdgeJSON { sourceNodeID, targetNodeID, data? }
//
// The spec fields that flowgram has no native shape for (decision/human/
// branch/loop/edge_type/when) are carried verbatim inside node.data.spec and
// edge.data.spec, so round-tripping never loses information.
// ---------------------------------------------------------------------------

/** Layout fallback: arrange nodes in a readable left-to-right cascade. */
function cascadePosition(i: number): { x: number; y: number } {
  const perRow = 5;
  const row = Math.floor(i / perRow);
  const col = i % perRow;
  return { x: 60 + col * 300, y: 60 + row * 160 };
}

/** Flow Spec v1 Definition → flowgram.ai WorkflowJSON (spec → canvas). */
export function flowSpecToWorkflow(def: FlowDefinition): WorkflowJSON {
  const nodes: WorkflowNodeJSON[] = (def.nodes ?? []).map((n, i) => ({
    id: n.id,
    type: n.kind,
    meta: { position: n.canvas_pos ?? cascadePosition(i) },
    // Full node carried in data.spec so the canvas form + cy round-trip keep
    // every field; the form engine reads/writes these fields directly.
    data: { ...specOf(n), title: n.title ?? n.prompt ?? n.id, kind: n.kind },
  }));

  const edges: WorkflowEdgeJSON[] = (def.edges ?? []).map((e) => ({
    sourceNodeID: e.from,
    targetNodeID: e.to,
    data: {
      spec: {
        id: e.id,
        edge_type: e.edge_type ?? "data_dependency",
        when: e.when,
        max_iterations: e.max_iterations,
        iteration_key: e.iteration_key,
      },
    },
  }));

  return { nodes, edges };
}

/** flowgram.ai WorkflowJSON → Flow Spec v1 Definition (canvas → spec). */
export function workflowToFlowSpec(
  wf: WorkflowJSON,
  flowId: string,
  version: string,
  status: string,
): FlowDefinition {
  // Node spec fields live at data top-level (the form engine reads/writes
  // them there via Field name="title" / getValueIn("decision") etc.).
  const nodes: FlowNode[] = (wf.nodes ?? []).map((n) => {
    const data = (n.data ?? {}) as Partial<FlowNode> & {
      kind?: string;
      title?: string;
    };
    return {
      id: n.id,
      kind: data.kind ?? n.type ?? "step",
      title: data.title ?? "",
      prompt: data.prompt,
      decision: data.decision,
      human: data.human,
      branch: data.branch,
      loop: data.loop,
      retry: data.retry,
      write_scope: data.write_scope,
      agent_ref: data.agent_ref,
      timeout_sec: data.timeout_sec,
      outputs: data.outputs,
      canvas_pos: n.meta?.position,
    } as FlowNode;
  });

  // Edge spec fields live at edge.data.spec (source/target are native).
  const edges: FlowEdge[] = (wf.edges ?? []).map((e) => {
    const spec = (e.data?.spec ?? {}) as Partial<FlowEdge>;
    return {
      ...spec,
      from: e.sourceNodeID,
      to: e.targetNodeID,
      edge_type: spec.edge_type ?? "data_dependency",
    } as FlowEdge;
  });

  return { flow_id: flowId, version, status, nodes, edges };
}

/** Extract the spec fields of a node (everything except canvas layout). */
function specOf(n: FlowNode): Record<string, unknown> {
  const { canvas_pos: _pos, ...rest } = n as FlowNode & { canvas_pos?: { x: number; y: number } };
  return { ...rest } as unknown as Record<string, unknown>;
}

/** Blank node for a given kind (mirrors the old FlowGramEditor.blankNode). */
export function blankFlowNode(id: string, kind: string): FlowNode {
  const base: FlowNode = { id, kind, title: "", prompt: "" };
  switch (kind) {
    case "decision":
      base.decision = { decision_type: "choice", choices: [{ id: "yes" }, { id: "no" }] };
      break;
    case "human":
      base.human = { queue: "ops", assignee_policy: "any" };
      break;
    case "branch":
      base.branch = { cases: [], default_to: "" };
      break;
    case "loop":
      base.loop = { body: [], exit_when: {}, max_iterations: 5 };
      break;
  }
  return base;
}
