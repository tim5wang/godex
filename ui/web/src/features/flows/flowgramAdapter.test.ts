import { describe, expect, it } from "vitest";
import type { FlowDefinition } from "../../lib/api";
import { blankFlowNode, flowSpecToWorkflow, workflowToFlowSpec } from "./flowgramAdapter";

const sampleDef: FlowDefinition = {
  flow_id: "fl_support",
  name: "Support routing",
  description: "Classify and route support tickets",
  version: "2",
  status: "draft",
  template_id: "support-agent",
  inputs: [{ name: "ticket", type: "string" }],
  outputs: [{ name: "resolution", type: "string" }],
  network: { policy: "allowlist", allowed_domains: ["api.example.com"] },
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
    const back = workflowToFlowSpec(wf, sampleDef.flow_id, "2", "draft", sampleDef);

    expect(back.flow_id).toBe("fl_support");
    expect(back.name).toBe(sampleDef.name);
    expect(back.description).toBe(sampleDef.description);
    expect(back.template_id).toBe(sampleDef.template_id);
    expect(back.inputs).toEqual(sampleDef.inputs);
    expect(back.outputs).toEqual(sampleDef.outputs);
    expect(back.network).toEqual(sampleDef.network);
    expect(back.nodes).toHaveLength(4);
    // branch-sourced edges are folded back into branch.cases; only the two
    // non-branch data_dependency edges survive as definition edges.
    expect(back.edges).toHaveLength(2);

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

    // Branch routing: cases + default survive the round-trip (folded back
    // from the visible condition edges on canvas).
    const br = back.nodes.find((n) => n.id === "br");
    expect(br?.branch?.default_to).toBe("human");
    expect(br?.branch?.cases).toHaveLength(2);
    expect(br?.branch?.cases.map((c) => c.to)).toEqual(["decide", "human"]);

    // Non-branch edges preserve edge_type + when.
    const e1 = back.edges.find((e) => e.id === "e1");
    expect(e1?.from).toBe("classify");
    expect(e1?.to).toBe("decide");
    expect(e1?.edge_type).toBe("data_dependency");
  });

  it("preserves extension fields from the base definition and canvas node data", () => {
    const extensionDef = {
      ...sampleDef,
      custom_metadata: { owner: "flow-platform", revision: 7 },
      nodes: sampleDef.nodes.map((node, index) =>
        index === 0
          ? ({ ...node, custom_node_policy: { retries: 4 } } as FlowDefinition["nodes"][number])
          : node,
      ),
    };
    const back = workflowToFlowSpec(
      flowSpecToWorkflow(extensionDef),
      extensionDef.flow_id,
      "3",
      "draft",
      extensionDef,
    ) as FlowDefinition & { custom_metadata: unknown };

    expect(back.custom_metadata).toEqual({ owner: "flow-platform", revision: 7 });
    expect((back.nodes[0] as unknown as Record<string, unknown>).custom_node_policy).toEqual({
      retries: 4,
    });
  });

  it("branch routing becomes visible condition edges on canvas", () => {
    const wf = flowSpecToWorkflow(sampleDef);
    // 2 non-branch edges + 2 branch cases + 1 default = 5 visible edges.
    const brEdges = wf.edges.filter((e) => e.sourceNodeID === "br");
    expect(brEdges).toHaveLength(3); // “3 个分支 = 3 条出边”
    expect(brEdges.map((e) => e.targetNodeID).sort()).toEqual(["decide", "human", "human"]);
    const caseEdge = brEdges.find((e) => e.data?.spec?.route === "no");
    expect(caseEdge?.data?.spec?.edge_type).toBe("condition");
    expect(caseEdge?.data?.spec?.when).toEqual({ choice: "no" });
    const defEdge = brEdges.find((e) => e.data?.spec?.is_default);
    expect(defEdge?.targetNodeID).toBe("human");
  });

  it("hand-drawn branch edges are folded into cases on save (no mixed-use error)", () => {
    // Simulate the user drawing an extra data_dependency edge from the branch
    // node — it must fold into branch.cases instead of being emitted as a
    // static edge (which used to trigger the F1a mixed-use error).
    const wf = flowSpecToWorkflow(sampleDef);
    wf.edges.push({
      sourceNodeID: "br",
      targetNodeID: "human",
      data: { spec: { id: "br_hand", edge_type: "data_dependency", route: "manual" } },
    });
    const back = workflowToFlowSpec(wf, "fl_support", "3", "draft");
    // No static edge out of the branch survives.
    expect(back.edges.some((e) => e.from === "br")).toBe(false);
    const br = back.nodes.find((n) => n.id === "br");
    expect(br?.branch?.cases.some((c) => c.to === "human")).toBe(true);
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

  it("service node specs survive canvas round-trip and blank nodes have usable defaults", () => {
    const def: FlowDefinition = {
      flow_id: "fl_service",
      version: "1",
      status: "draft",
      nodes: [{
        id: "call",
        kind: "service",
        service: {
          method: "POST",
          url: "https://api.example.com/v1/tasks",
          headers: { "X-Trace": "{{inputs.trace}}" },
          body: { task: "{{inputs.task}}" },
          auth: { type: "api_key", token_env: "TASK_API_KEY", header_name: "X-Api-Key" },
        },
      }],
      edges: [],
    };
    const back = workflowToFlowSpec(
      flowSpecToWorkflow(def),
      def.flow_id,
      "1",
      "draft",
    );
    expect(back.nodes[0].service).toEqual(def.nodes[0].service);
    expect(blankFlowNode("new_call", "service").service).toEqual({
      method: "GET",
      url: "https://api.example.com/",
    });
  });

  it("decision condition edges snap to labelled choice ports (E4 round-trip)", () => {
    const def: FlowDefinition = {
      flow_id: "fl_decision_ports",
      version: "1",
      status: "draft",
      nodes: [
        {
          id: "decide",
          kind: "decision",
          title: "可否自动",
          prompt: "能否自动处理？",
          decision: { decision_type: "choice", choices: [{ id: "yes" }, { id: "no" }] },
        },
        { id: "auto", kind: "step", title: "自动处理", prompt: "自动处理" },
        { id: "human", kind: "human", title: "人工", prompt: "人工处理", human: { queue: "support" } },
      ],
      edges: [
        { id: "e_yes", from: "decide", to: "auto", edge_type: "condition", when: { choice: "yes" } },
        { id: "e_no", from: "decide", to: "human", edge_type: "condition", when: { choice: "no" } },
      ],
    };

    // spec → canvas: each decision condition edge carries sourcePortID = choice
    // so the line attaches to the matching labelled branch port.
    const wf = flowSpecToWorkflow(def);
    const yesEdge = wf.edges.find((e) => e.data?.spec?.id === "e_yes");
    expect(yesEdge?.sourcePortID).toBe("yes");
    expect(yesEdge?.data?.spec?.edge_type).toBe("condition");
    const noEdge = wf.edges.find((e) => e.data?.spec?.id === "e_no");
    expect(noEdge?.sourcePortID).toBe("no");

    // canvas → spec: sourcePortID folds back into when.choice, and the edge
    // stays a condition edge (routing exactly like the canvas showed).
    const back = workflowToFlowSpec(wf, def.flow_id, "2", "draft");
    const bYes = back.edges.find((e) => e.id === "e_yes");
    expect(bYes?.edge_type).toBe("condition");
    expect(bYes?.when?.choice).toBe("yes");
    const bNo = back.edges.find((e) => e.id === "e_no");
    expect(bNo?.edge_type).toBe("condition");
    expect(bNo?.when?.choice).toBe("no");

    // A hand-drawn data_dependency edge from a decision node with a port id
    // (drawn from the labelled pill) also folds into a condition edge.
    const drawn: FlowDefinition = {
      ...def,
      edges: [{ id: "e_draw", from: "decide", to: "auto", edge_type: "data_dependency" }],
    };
    const drawnWf = flowSpecToWorkflow(drawn);
    drawnWf.edges[0].sourcePortID = "yes"; // simulate snapping to the pill
    const drawnBack = workflowToFlowSpec(drawnWf, def.flow_id, "2", "draft");
    expect(drawnBack.edges[0].edge_type).toBe("condition");
    expect(drawnBack.edges[0].when?.choice).toBe("yes");
  });

  it("flow_03 real definition: branch 3 cases + default become visible edges, hand-drawn edge folds back without mixed-use error", () => {
    // flow_03（决策分流模板）真实定义：br 有 3 个 case + 1 default。
    // 画布上应显示 4 条出边；用户手拖 br→auto_done 静态边后保存，
    // 必须折叠回 cases，不再产生静态出边 → Go 编译不再报
    // “mixed use as static target and append target”。
    const flow03: FlowDefinition = {
      flow_id: "flow_03",
      version: "1",
      status: "draft",
      nodes: [
        { id: "handle", kind: "step", title: "任务处理", prompt: "处理任务并给出结论" },
        {
          id: "decide",
          kind: "decision",
          title: "置信度判断",
          prompt: "处理结果是否可信？",
          decision: {
            decision_type: "choice",
            choices: [{ id: "auto" }, { id: "llm" }, { id: "human" }],
          },
        },
        { id: "auto_done", kind: "step", title: "自动完成", prompt: "直接采用处理结果" },
        { id: "llm_review", kind: "llm", title: "LLM 兜底", prompt: "复核并完善处理结果" },
        {
          id: "human",
          kind: "human",
          title: "人工介入",
          prompt: "人工处理该任务",
          human: { queue: "ops" },
        },
        {
          id: "br",
          kind: "branch",
          branch: {
            cases: [
              { name: "auto", to: "auto_done", condition: { choice: "auto" } },
              { name: "llm", to: "llm_review", condition: { choice: "llm" } },
              { name: "human", to: "human", condition: { choice: "human" } },
            ],
            default_to: "auto_done",
          },
        },
      ],
      edges: [
        { id: "e1", from: "handle", to: "decide", edge_type: "data_dependency" },
        { id: "e2", from: "decide", to: "br", edge_type: "data_dependency" },
      ],
    };

    // 1. 加载：branch 出边可见（3 cases + 1 default）
    const wf = flowSpecToWorkflow(flow03);
    const brEdges = wf.edges.filter((e) => e.sourceNodeID === "br");
    expect(brEdges).toHaveLength(4);
    expect(brEdges.filter((e) => e.data?.spec?.is_default)).toHaveLength(1);
    expect(brEdges.filter((e) => !e.data?.spec?.is_default)).toHaveLength(3);

    // 2. 模拟用户手拖 br→auto_done 静态边（之前触发 mixed-use 报错）
    wf.edges.push({
      sourceNodeID: "br",
      targetNodeID: "auto_done",
      data: { spec: { id: "br_hand", edge_type: "data_dependency" } },
    });

    // 3. 保存：折叠回 cases，无静态出边
    const back = workflowToFlowSpec(wf, "flow_03", "2", "draft");
    expect(back.edges.some((e) => e.from === "br")).toBe(false);
    const br = back.nodes.find((n) => n.id === "br");
    expect(br?.branch?.default_to).toBe("auto_done");
    expect(br?.branch?.cases.map((c) => c.to).sort()).toEqual([
      "auto_done",
      "human",
      "llm_review",
    ]);
    // 非 branch 静态边保持原样
    expect(back.edges).toHaveLength(2);
  });
});
