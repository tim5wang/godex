import { describe, expect, it } from "vitest";
import { flowTemplateById } from "./flowTemplates";
import {
  buildVoiceAgentDesignerPrompt,
  isVoiceAgentDefinition,
  voiceFlowProgressLabel,
} from "./voiceAgent";

describe("Voice Agent Flow template", () => {
  it("creates a durable, read-only session flow that returns speech and session state", () => {
    const template = flowTemplateById("voice-agent");
    expect(template).toBeDefined();
    const definition = template!.build("fl_voice_test", "1");

    expect(isVoiceAgentDefinition(definition)).toBe(true);
    expect(definition.session_workflow?.triggers).toEqual([
      { event_type: "voice.asr_final", entry_node: "respond", delivery: "durable" },
    ]);
    expect(definition.nodes[0]).toMatchObject({
      kind: "step",
      agent_type: "Explore",
      outputs: [
        { name: "speech", type: "string", required: true },
        { name: "session_state", type: "object" },
      ],
    });
  });

  it("rejects session flows that cannot be driven and spoken by Voice Agent", () => {
    const definition = flowTemplateById("voice-agent")!.build("fl_voice_test", "1");
    definition.session_workflow!.triggers[0].event_type = "other.event";
    expect(isVoiceAgentDefinition(definition)).toBe(false);
  });

  it("prefills Flow Designer with the goal and asks it to wait for save approval", () => {
    const prompt = buildVoiceAgentDesignerPrompt("回答订单配送问题");
    expect(prompt).toContain("业务目标：回答订单配送问题");
    expect(prompt).toContain("durable trigger 为 voice.asr_final");
    expect(prompt).toContain("Explore 只读 Agent");
    expect(prompt).toContain("未得到我确认前，不要保存草稿、发布或创建会话");
  });

  it("shows planning and model-request progress before an Agent tool starts", () => {
    expect(voiceFlowProgressLabel({ phase: "agent_started" })).toContain("规划下一步");
    expect(voiceFlowProgressLabel({ phase: "model_request" })).toContain("判断是否需要工具");
    expect(voiceFlowProgressLabel({ phase: "tool_started", tool_name: "read_file" })).toContain(
      "正在调用 read_file",
    );
  });
});
