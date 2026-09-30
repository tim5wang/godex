import type { FlowDefinition } from "../../lib/api";

export interface VoiceFlowProgress {
  status?: string;
  node_id?: string;
  phase?: string;
  tool_name?: string;
}

export function voiceFlowProgressLabel(progress: VoiceFlowProgress): string {
  if (progress.status === "canceling") return "正在停止上一轮…";
  if (progress.status === "queued") return "请求已接收，正在启动工作流…";

  switch (progress.phase) {
    case "agent_started":
      return "Agent 正在理解请求并规划下一步…";
    case "model_request":
      return "Agent 正在请求模型并判断是否需要工具…";
    case "awaiting_tools":
      return "Agent 已规划工具调用，正在准备执行…";
    case "tool_started":
      return progress.tool_name
        ? `Agent 正在调用 ${progress.tool_name}…`
        : "Agent 正在调用工具…";
    case "tool_finished":
      return progress.tool_name
        ? `${progress.tool_name} 已完成，Agent 正在继续…`
        : "工具已完成，Agent 正在继续…";
    case "tools_completed":
    case "tool_results":
      return "工具已完成，Agent 正在分析结果…";
    case "assistant_message":
    case "final_response":
      return "Agent 正在整理回复…";
    case "recovery_attempted":
      return "Agent 正在处理异常并尝试恢复…";
  }

  if (progress.node_id) return `正在执行流程节点 ${progress.node_id}…`;
  return "Agent 正在处理…";
}

export function buildVoiceAgentDesignerPrompt(goal: string): string {
  const normalizedGoal = goal.trim();
  return [
    "请帮我设计一个可运行的 Godex Voice Agent Flow。",
    normalizedGoal ? `业务目标：${normalizedGoal}` : "请先询问我希望这个语音 Agent 完成什么业务目标。",
    "兼容要求：session execution；durable trigger 为 voice.asr_final；使用 Explore 只读 Agent；返回简短 speech 字符串和完整更新后的 session_state 对象；不要配置写工具或交互审批。",
    "请先生成方案并运行 Flow 校验，把画布结构、只读边界和校验结果展示给我。未得到我确认前，不要保存草稿、发布或创建会话。",
  ].join("\n");
}

export function isVoiceAgentDefinition(definition?: FlowDefinition): boolean {
  if (!definition || definition.execution_mode !== "session") return false;
  const trigger = definition.session_workflow?.triggers.some(
    (item) => item.event_type === "voice.asr_final" && (item.delivery ?? "durable") === "durable",
  );
  if (!trigger) return false;
  return definition.nodes.some((node) => {
    if (node.kind !== "step" || node.agent_type !== "Explore") return false;
    const outputs = node.outputs ?? [];
    return outputs.some((output) => output.name === "speech" && ["string", "text"].includes(output.type ?? "string")) &&
      outputs.some((output) => output.name === "session_state" && output.type === "object");
  });
}
