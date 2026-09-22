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
// branch/loop/edge_type/when) are carried verbatim: node fields at data
// top-level (the form engine reads/writes them there), edge fields at
// edge.data.spec. Branch routing is expressed on canvas as visible condition
// edges and is re-folded into branch.cases on save.
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

  // Ordinary edges: everything NOT sourced from a branch node. Branch routing
  // is expressed on canvas as visible condition edges (see below) and is
  // re-folded into branch.cases on save, so definition-level edges that
  // duplicate a branch case are skipped here.
  const branchIDs = new Set(
    (def.nodes ?? []).filter((n) => n.kind === "branch").map((n) => n.id),
  );
  const edges: WorkflowEdgeJSON[] = (def.edges ?? [])
    .filter((e) => !branchIDs.has(e.from))
    .map((e) => ({
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

  // Branch routing → visible condition edges (one per case + one default).
  // This makes "3 branches = 3 outgoing edges" true on the canvas.
  for (const n of def.nodes ?? []) {
    if (n.kind !== "branch" || !n.branch) continue;
    const cases = n.branch.cases ?? [];
    cases.forEach((c, i) => {
      const route = c.name ?? c.to;
      edges.push({
        sourceNodeID: n.id,
        targetNodeID: c.to,
        data: {
          spec: {
            id: `${n.id}_case_${i}`,
            edge_type: "condition",
            when: c.condition ?? { choice: route },
            route,
          },
        },
      });
    });
    if (n.branch.default_to) {
      edges.push({
        sourceNodeID: n.id,
        targetNodeID: n.branch.default_to,
        data: {
          spec: {
            id: `${n.id}_default`,
            edge_type: "condition",
            when: {},
            route: "default",
            is_default: true,
          },
        },
      });
    }
  }

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
  const branchIDs = new Set(nodes.filter((n) => n.kind === "branch").map((n) => n.id));

  // Re-fold branch-sourced edges (both loaded condition edges and edges the
  // user drew by hand, which flowgram types as data_dependency) into the
  // branch node's cases — the canvas edges ARE the routing truth. Non-branch
  // edges stay as definition edges.
  const edges: FlowEdge[] = [];
  for (const e of wf.edges ?? []) {
    if (branchIDs.has(e.sourceNodeID)) {
      continue; // collected per branch below
    }
    const spec = (e.data?.spec ?? {}) as Partial<FlowEdge>;
    edges.push({
      ...spec,
      from: e.sourceNodeID,
      to: e.targetNodeID,
      edge_type: spec.edge_type ?? "data_dependency",
    } as FlowEdge);
  }

  // Rebuild branch routing from the visible canvas edges (one pass per
  // branch): non-default edges become cases, the default edge (if any) sets
  // default_to. Deleting an edge deletes the branch; drawing one adds it.
  for (const br of nodes) {
    if (br.kind !== "branch") continue;
    const outEdges = (wf.edges ?? []).filter((e) => e.sourceNodeID === br.id);
    const cases: NonNullable<FlowNode["branch"]>["cases"] = [];
    let defaultTo = br.branch?.default_to ?? "";
    for (const e of outEdges) {
      const spec = (e.data?.spec ?? {}) as {
        edge_type?: string;
        when?: FlowEdge["when"];
        route?: string;
        is_default?: boolean;
      };
      if (spec.is_default === true || spec.route === "default") {
        defaultTo = e.targetNodeID;
        continue;
      }
      if (cases.some((c) => c.to === e.targetNodeID)) continue;
      const route = spec.route ?? e.targetNodeID;
      cases.push({
        name: route,
        to: e.targetNodeID,
        condition: spec.when ?? { choice: route },
      });
    }
    br.branch = { cases, default_to: defaultTo };
  }

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
    case "function":
      base.function = {
        runtime: "js",
        handler: "handle",
        source: "function handle(ctx, event) {\n  return { result: 1 };\n}",
      };
      break;
  }
  return base;
}
