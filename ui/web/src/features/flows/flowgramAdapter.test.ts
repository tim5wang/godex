import { describe, expect, it } from "vitest";
import type { FlowDefinition } from "../../lib/api";
import { flowSpecToWorkflow, workflowToFlowSpec } from "./flowgramAdapter";

const sampleDef: FlowDefinition = {
  flow_id: "fl_support",
  version: "2",
  status: "draft",
  inputs: [{ name: "ticket", type: "string" }],
  nodes: [
    {
      id: "classify",
      kind: "step",
      title: "工单分类",
      prompt: "对工单进行分类并提取关键信息",
      canvas_pos: { x: 10, y: 20 },
    },
    {
      id: "decide",
      kind: "decision",
      title: "能否自动解决",
      prompt: "该工单能否由 AI 自动解决？",
      decision: {
        decision_type: "choice",
        choices: [{ id: "yes" }, { id: "no" }],
      },
    },
    {
      id: "human",
      kind: "human",
      title: "人工处理",
      prompt: "人工跟进处理该工单",
      human: { queue: "support", assignee_policy: "any", result_var: "resolution" },
    },
    {
      id: "br",
      kind: "branch",
      branch: {
        cases: [
          { name: "yes", to: "decide", condition: { choice: "yes" } },
          { name: "no", to: "human", condition: { choice: "no" } },
        ],
        default_to: "human",
      },
    },
  ],
  edges: [
    { id: "e1", from: "classify", to: "decide", edge_type: "data_dependency" },
    { id: "e2", from: "decide", to: "br", edge_type: "data_dependency" },
    { id: "e3", from: "br", to: "human", edge_type: "condition", when: { choice: "no" } },
  ],
};

describe("flowgramAdapter round-trip", () => {
  it("spec → workflow → spec preserves every Flow Spec field", () => {
    const wf = flowSpecToWorkflow(sampleDef);
    const back = workflowToFlowSpec(wf, sampleDef.flow_id, "2", "draft");

    expect(back.flow_id).toBe("fl_support");
    expect(back.nodes).toHaveLength(4);
    expect(back.edges).toHaveLength(3);

    // Node identity + layout survives.
    const classify = back.nodes.find((n) => n.id === "classify");
    expect(classify?.title).toBe("工单分类");
    expect(classify?.prompt).toBe("对工单进行分类并提取关键信息");
    expect(classify?.canvas_pos).toEqual({ x: 10, y: 20 });

    // Kind-specific spec fields survive (regression: they were read from
    // data.spec while the form writes them at data top-level).
    const decide = back.nodes.find((n) => n.id === "decide");
    expect(decide?.kind).toBe("decision");
    expect(decide?.decision?.decision_type).toBe("choice");
    expect(decide?.decision?.choices).toEqual([{ id: "yes" }, { id: "no" }]);

    const human = back.nodes.find((n) => n.id === "human");
    expect(human?.human?.queue).toBe("support");
    expect(human?.human?.result_var).toBe("resolution");

    const br = back.nodes.find((n) => n.id === "br");
    expect(br?.branch?.default_to).toBe("human");
    expect(br?.branch?.cases).toHaveLength(2);

    // Edges preserve edge_type + when.
    const e3 = back.edges.find((e) => e.id === "e3");
    expect(e3?.edge_type).toBe("condition");
    expect(e3?.when).toEqual({ choice: "no" });
    expect(e3?.from).toBe("br");
    expect(e3?.to).toBe("human");
  });

  it("canvas positions cascade when absent", () => {
    const def: FlowDefinition = {
      flow_id: "f",
      version: "1",
      status: "draft",
      nodes: [{ id: "a", kind: "step" }, { id: "b", kind: "step" }],
      edges: [],
    };
    const wf = flowSpecToWorkflow(def);
    expect(wf.nodes[0].meta?.position).toBeDefined();
    expect(wf.nodes[1].meta?.position).toBeDefined();
  });
});
