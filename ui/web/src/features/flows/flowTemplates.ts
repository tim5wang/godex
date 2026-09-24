import type { FlowDefinition } from "../../lib/api";

// ---------------------------------------------------------------------------
// Flow templates — presets so a user can create a flow without writing JSON.
// Each template builds a full FlowDefinition for the given flow_id/version.
// Prompts intentionally avoid {{...}} variable references so the definition
// passes P2.3 compile-time variable validation out of the box; the visual
// editor can add references later.
// ---------------------------------------------------------------------------

export interface FlowTemplate {
  id: string;
  name: string;
  description: string;
  build: (flowId: string, version: string) => FlowDefinition;
}

function base(flowId: string, version: string, templateId: string): FlowDefinition {
  return {
    flow_id: flowId,
    version,
    status: "draft",
    template_id: templateId,
    nodes: [],
    edges: [],
  };
}

/** 1. 人工审批流：step → human 审批 → finalize */
function approvalTemplate(flowId: string, version: string): FlowDefinition {
  const d = base(flowId, version, "approval");
  d.name = "人工审批流";
  d.description = "step → human 审批 → finalize（提交请求、人工审批、结果处理）";
  d.inputs = [{ name: "request", type: "string", desc: "审批请求内容" }];
  d.nodes = [
    { id: "prepare", kind: "step", title: "整理请求", prompt: "整理并提交审批请求" },
    { id: "approve", kind: "human", title: "人工审批", prompt: "审批该请求（同意/拒绝）", human: { queue: "ops", assignee_policy: "any", result_var: "approved" } },
    { id: "finalize", kind: "step", title: "结果处理", prompt: "根据审批结果完成后续处理" },
  ];
  d.edges = [
    { id: "e1", from: "prepare", to: "approve", edge_type: "data_dependency" },
    { id: "e2", from: "approve", to: "finalize", edge_type: "data_dependency" },
  ];
  return d;
}

/** 2. 客服工单处理：classify → decision → (llm 自动回复 | human 升级) */
function supportTemplate(flowId: string, version: string): FlowDefinition {
  const d = base(flowId, version, "support-ticket");
  d.name = "客服工单处理";
  d.description = "工单分类 → 判断能否自动解决 → LLM 自动回复或转人工";
  d.inputs = [{ name: "ticket", type: "string", desc: "工单内容" }];
  d.nodes = [
    { id: "classify", kind: "step", title: "工单分类", prompt: "对工单进行分类并提取关键信息" },
    { id: "decide", kind: "decision", title: "能否自动解决", prompt: "该工单能否由 AI 自动解决？", decision: { decision_type: "choice", choices: [{ id: "yes" }, { id: "no" }] } },
    { id: "auto_reply", kind: "llm", title: "AI 自动回复", prompt: "生成工单回复草稿" },
    { id: "human", kind: "human", title: "人工处理", prompt: "人工跟进处理该工单", human: { queue: "support", assignee_policy: "any", result_var: "resolution" } },
    { id: "br", kind: "branch", branch: { cases: [{ name: "yes", to: "auto_reply", condition: { choice: "yes" } }, { name: "no", to: "human", condition: { choice: "no" } }], default_to: "human" } },
  ];
  d.edges = [
    { id: "e1", from: "classify", to: "decide", edge_type: "data_dependency" },
    { id: "e2", from: "decide", to: "br", edge_type: "data_dependency" },
  ];
  return d;
}

/** 3. 订单售后：classify → decision（金额阈值）→ (自动退款 | 人工审核) */
function refundTemplate(flowId: string, version: string): FlowDefinition {
  const d = base(flowId, version, "order-refund");
  d.name = "订单售后";
  d.description = "订单问题分类 → 金额判断 → 小额自动退款或转人工审核";
  d.inputs = [
    { name: "order_id", type: "string", desc: "订单号" },
    { name: "amount", type: "number", desc: "退款金额" },
  ];
  d.nodes = [
    { id: "classify", kind: "step", title: "问题分类", prompt: "判断订单问题类型并提取关键信息" },
    { id: "decide", kind: "decision", title: "是否自动退款", prompt: "该订单是否可以自动退款？", decision: { decision_type: "choice", choices: [{ id: "auto" }, { id: "review" }] } },
    { id: "auto_refund", kind: "step", title: "自动退款", prompt: "执行自动退款" },
    { id: "human_review", kind: "human", title: "人工审核", prompt: "人工审核该退款申请", human: { queue: "finance", assignee_policy: "any", result_var: "approved" } },
    { id: "br", kind: "branch", branch: { cases: [{ name: "auto", to: "auto_refund", condition: { choice: "auto" } }, { name: "review", to: "human_review", condition: { choice: "review" } }], default_to: "human_review" } },
  ];
  d.edges = [
    { id: "e1", from: "classify", to: "decide", edge_type: "data_dependency" },
    { id: "e2", from: "decide", to: "br", edge_type: "data_dependency" },
  ];
  return d;
}

/** 4. 决策分流：step → decision → branch → (llm 兜底 | human) */
function routingTemplate(flowId: string, version: string): FlowDefinition {
  const d = base(flowId, version, "decision-routing");
  d.name = "决策分流";
  d.description = "任务处理 → 判断 → 高置信自动执行，低置信转 LLM 兜底或人工";
  d.inputs = [{ name: "task", type: "string", desc: "任务描述" }];
  d.nodes = [
    { id: "handle", kind: "step", title: "任务处理", prompt: "处理任务并给出结论" },
    { id: "decide", kind: "decision", title: "置信度判断", prompt: "处理结果是否可信？", decision: { decision_type: "choice", choices: [{ id: "auto" }, { id: "llm" }, { id: "human" }] } },
    { id: "auto_done", kind: "step", title: "自动完成", prompt: "直接采用处理结果" },
    { id: "llm_review", kind: "llm", title: "LLM 兜底", prompt: "复核并完善处理结果" },
    { id: "human", kind: "human", title: "人工介入", prompt: "人工处理该任务", human: { queue: "ops", assignee_policy: "any", result_var: "resolution" } },
    { id: "br", kind: "branch", branch: { cases: [{ name: "auto", to: "auto_done", condition: { choice: "auto" } }, { name: "llm", to: "llm_review", condition: { choice: "llm" } }, { name: "human", to: "human", condition: { choice: "human" } }], default_to: "auto_done" } },
  ];
  d.edges = [
    { id: "e1", from: "handle", to: "decide", edge_type: "data_dependency" },
    { id: "e2", from: "decide", to: "br", edge_type: "data_dependency" },
  ];
  return d;
}

/** 5. 多分支汇聚：decision → branch → 多分支处理 → condition 边汇聚到 finalize */
function convergeTemplate(flowId: string, version: string): FlowDefinition {
  const d = base(flowId, version, "branch-converge");
  d.name = "多分支汇聚";
  d.description = "判断 → 多分支并行处理 → 各分支完成后汇聚到汇总节点（branch 网关静态 + 分支/汇聚节点作 append 模板）";
  d.inputs = [{ name: "ticket", type: "string", desc: "工单内容" }];
  d.nodes = [
    { id: "classify", kind: "step", title: "工单分类", prompt: "对工单进行分类并提取关键信息" },
    { id: "decide", kind: "decision", title: "处理方式", prompt: "该工单如何处理？", decision: { decision_type: "choice", choices: [{ id: "auto" }, { id: "llm" }, { id: "human" }] } },
    { id: "br", kind: "branch", branch: { cases: [{ name: "auto", to: "auto_run", condition: { choice: "auto" } }, { name: "llm", to: "llm_run", condition: { choice: "llm" } }, { name: "human", to: "human_run", condition: { choice: "human" } }], default_to: "auto_run" } },
    { id: "auto_run", kind: "step", title: "自动处理", prompt: "自动处理工单并产出结论" },
    { id: "llm_run", kind: "llm", title: "LLM 复核", prompt: "复核工单并给出结论" },
    { id: "human_run", kind: "human", title: "人工处理", prompt: "人工处理该工单", human: { queue: "ops", assignee_policy: "any", result_var: "resolution" } },
    { id: "finalize", kind: "step", title: "汇总输出", prompt: "汇总各分支处理结论，输出最终 resolution" },
  ];
  d.edges = [
    { id: "e1", from: "classify", to: "decide", edge_type: "data_dependency" },
    { id: "e2", from: "decide", to: "br", edge_type: "data_dependency" },
    // 汇聚：每个分支节点完成（status=completed）时各自触发 finalize 一次。
    { id: "e3", from: "auto_run", to: "finalize", edge_type: "condition", when: { status: "completed" } },
    { id: "e4", from: "llm_run", to: "finalize", edge_type: "condition", when: { status: "completed" } },
    { id: "e5", from: "human_run", to: "finalize", edge_type: "condition", when: { status: "completed" } },
  ];
  return d;
}

export const FLOW_TEMPLATES: FlowTemplate[] = [
  { id: "approval", name: "人工审批流", description: "step → 人工审批 → 结果处理", build: approvalTemplate },
  { id: "support-ticket", name: "客服工单处理", description: "工单分类 → 决策 → AI 回复或转人工", build: supportTemplate },
  { id: "order-refund", name: "订单售后", description: "问题分类 → 金额判断 → 自动退款或人工审核", build: refundTemplate },
  { id: "decision-routing", name: "决策分流", description: "处理 → 置信度判断 → 自动/LLM/人工", build: routingTemplate },
  { id: "branch-converge", name: "多分支汇聚", description: "判断 → 多分支处理 → condition 边汇聚到汇总", build: convergeTemplate },
];

export function flowTemplateById(id: string): FlowTemplate | undefined {
  return FLOW_TEMPLATES.find((t) => t.id === id);
}
