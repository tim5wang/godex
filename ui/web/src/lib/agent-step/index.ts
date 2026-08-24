/**
 * Agent Step Platform — TypeScript SDK (Phase B).
 *
 * Self-contained, zero-dependency client for embedding godex as an agent
 * runtime in business UIs:
 *
 * ```ts
 * import { createStepClient } from "godex/agent-step";
 *
 * const step = createStepClient({ baseUrl: "https://godex.example.com", apiKey: "biz_..." });
 * const result = await step.createStep({
 *   prompt: "分析订单 ORD-1234 的延迟原因",
 *   inputs: { order_id: "ORD-1234" },
 *   context: { recall: ["sales_crm", "godex://memory"] },
 *   structured_output: { schema: { type: "object", properties: { reason: { type: "string" } } } },
 * });
 * ```
 */
export {
  StepClient,
  StepAPIError,
  createStepClient,
  type StepClientOptions,
  type StepRequest,
  type StepResult,
  type StepReply,
  type StepError,
  type StepTools,
  type StepContext,
  type StepToolUse,
  type StructuredOutput,
  type RuntimeStepEvent,
  type UiCardData,
  type UiCardField,
  type UiCardAction,
} from "./client";
