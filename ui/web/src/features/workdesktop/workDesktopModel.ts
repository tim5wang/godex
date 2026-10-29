import type { Locale } from "../../store/locale";

export type StreamStatus = "active" | "paused" | "archived";
export type KnowledgeStatus = "confirmed" | "candidate" | "stale" | "rejected";
export type RecallScope = "current" | "related" | "all";
export type Source = { id: string; contextId: string; title: string; locator: string; excerpt: string; at: string };
export type Resource = { id: string; title: string; kind: "directory" | "document" | "dashboard" | "service" | "artifact"; locator: string; node?: string; purpose: string };
export type DemoTask = { id: string; title: string; status: "draft" | "running" | "review" | "taskDone"; acceptance: string; checkpointRevision: number; sourceId?: string };
export type Topic = { id: string; title: string; entries: { id: string; text: string; at: string; sourceId?: string }[] };
export type Knowledge = { id: string; contextId: string; title: string; content: string; kind: "fact" | "decision" | "trace"; status: KnowledgeStatus; scope: string; observed: string; sourceId: string };
export type Stream = {
  id: string; title: string; background: string; goal: string; status: StreamStatus; tags: string[];
  summary: string; completed: string[]; next: string[]; blockers: string[]; openQuestions: string[];
  revision: number; checkpointAt: string; sourceIds: string[]; resourceIds: string[];
  tasks: DemoTask[]; topics: Topic[]; events: { id: string; title: string; at: string; sourceId?: string }[];
};
export type DesktopDemo = { streams: Stream[]; resources: Resource[]; sources: Source[]; knowledge: Knowledge[]; relations: { from: string; to: string; kind: "related" | "depends"; condition: string }[] };

// No localStorage: fictional business data deliberately lives only in this mounted page.
export function createDesktopDemo(locale: Locale): DesktopDemo {
  const text = (zh: string, en: string) => locale === "zh" ? zh : en;
  const streams: Stream[] = [
    {
      id: "speech", title: text("语音服务 · 长期维护", "Speech service · operations"), status: "active", tags: ["ASR", "Runbook"],
      background: text("维护多业务共用的语音识别能力，统一故障处理、模型发布与运行知识。业务接入和效果专项共享同一服务，不重复维护配置事实。", "Maintain shared speech recognition across business teams: incident handling, model releases and operational knowledge. Onboarding and evaluation share the same service records."),
      goal: text("建立可追溯的发布与回滚流程，降低重复排查和上下文恢复成本。", "Make releases and rollback traceable, reducing repeated investigations and context recovery cost."),
      summary: text("已完成新版离线评测；短句回归仍需拆分模型与参数因素。生产版本尚未查询，不能据此认定已发布。", "Offline evaluation is complete. Short-utterance regressions still need model/parameter isolation. No production query has verified a release."),
      completed: [text("补齐评测脚本入口与复现命令", "Documented evaluation entry points and reproduction commands"), text("整理上一轮发布的回滚检查清单", "Prepared rollback checks for the previous release")],
      next: [text("固定模型权重，对比两组解码参数", "Fix model weights and compare two decoding configurations"), text("关联新版评测报告，提交人工验收", "Link the evaluation report and request human review")],
      blockers: [text("等待业务甲提供脱敏短句样例", "Waiting for redacted short-utterance samples from Team A")],
      openQuestions: [text("参数变化是否是退化主因？", "Are decoding parameters the main cause of regression?")],
      revision: 3, checkpointAt: "2026-10-28 17:40", sourceIds: ["report", "runbook"], resourceIds: ["service", "repo", "runbook", "monitor", "eval"],
      tasks: [{ id: "t-eval", title: text("隔离模型与参数影响", "Isolate model and parameter effects"), status: "running", acceptance: text("同一评测集、同一权重；附命令、参数 hash、分组指标和报告。", "Use identical data and weights; attach commands, config hashes, sliced metrics and a report."), checkpointRevision: 3, sourceId: "report" }, { id: "t-runbook", title: text("补充发布回滚 Runbook", "Update release rollback Runbook"), status: "review", acceptance: text("能够从部署记录定位回滚目标；验证步骤可独立执行。", "Locate the rollback target from release records and provide runnable verification steps."), checkpointRevision: 2, sourceId: "runbook" }],
      topics: [{ id: "speech-eval", title: text("短句效果回归排查", "Short-utterance regression"), entries: [{ id: "e1", text: text("先固定权重，仅切换参数，避免把两个变量混在一起。当前只有离线结果，生产状态需另查。", "Keep weights fixed and change parameters only. We have offline results; production state requires a separate query."), at: "2026-10-28 17:20", sourceId: "report" }] }, { id: "speech-release", title: text("发布与回滚交接", "Release and rollback handoff"), entries: [{ id: "e2", text: text("交付必须保留镜像、模型、配置与评测版本的关联，不能只保存聊天结论。", "Preserve image, model, config and evaluation version links, not just the chat conclusion."), at: "2026-10-27 16:10", sourceId: "runbook" }] }],
      events: [{ id: "s-ev1", title: text("效果专项关联了新一轮离线评测报告", "Evaluation stream linked a new offline report"), at: "2026-10-29 09:20", sourceId: "report" }, { id: "s-ev2", title: text("回滚清单进入演示待验收状态", "Rollback checklist moved to demo review"), at: "2026-10-28 18:10", sourceId: "runbook" }],
    },
    {
      id: "alignment", title: text("模型效果 · 对齐与更新", "Model quality · alignment"), status: "active", tags: ["Evaluation", "ID"],
      background: text("围绕印尼语和短句场景做效果对齐，复用服务维护主线的仓库与评测资产。", "Align Indonesian and short-utterance quality using the operations stream's repository and evaluation assets."),
      goal: text("输出可复现的效果差异、样例分类与升级建议。", "Produce reproducible quality deltas, sample categories and an upgrade recommendation."),
      summary: text("整体指标改善，但短句子集未达门禁，尚不能批准生产升级。", "Aggregate metrics improved, but short utterances have not passed the release gate."),
      completed: [text("锁定评测集 demo-id-v7", "Pinned evaluation dataset demo-id-v7")], next: [text("按句长和噪声条件拆分错误样例", "Slice errors by utterance length and noise")], blockers: [], openQuestions: [text("整体提升是否掩盖了少数业务场景退化？", "Does aggregate improvement hide regressions in specific business scenarios?")],
      revision: 2, checkpointAt: "2026-10-29 09:20", sourceIds: ["report"], resourceIds: ["repo", "eval", "service"],
      tasks: [{ id: "t-slice", title: text("短句错误分类", "Classify short-utterance errors"), status: "draft", acceptance: text("逐类附脱敏样例、基线结果和新版本结果。", "Provide redacted samples and baseline/new-version results per category."), checkpointRevision: 2, sourceId: "report" }],
      topics: [{ id: "quality", title: text("评测结论与门禁", "Evaluation findings and gates"), entries: [{ id: "e3", text: text("离线 WER 只能描述此评测集，不应直接推广为所有业务效果改善。", "Offline WER describes this dataset, not every business scenario."), at: "2026-10-29 09:10", sourceId: "report" }] }],
      events: [{ id: "a-ev1", title: text("补充短句子集未达门禁的结论", "Recorded the short-utterance gate failure"), at: "2026-10-29 09:20", sourceId: "report" }],
    },
    {
      id: "onboarding", title: text("业务甲 · 接入语音能力", "Team A · speech onboarding"), status: "paused", tags: ["Integration", "API"],
      background: text("新业务接入共享语音服务，关注接口契约、场景验收和上线依赖，不另建一套模型事实。", "Onboard a new business to shared speech APIs, focusing on contracts, acceptance and launch dependencies."),
      goal: text("完成接口与效果验收，再安排灰度接入。", "Pass API and quality acceptance before planning a canary rollout."),
      summary: text("接口联调已通过；效果验收依赖短句回归修复，当前暂停上线。", "API integration passed. Launch is paused pending the short-utterance quality gate."),
      completed: [text("接口超时、重试和错误码已对齐", "Aligned timeouts, retries and error codes")], next: [text("收到新报告后重跑业务样例并验收", "Rerun business samples after the updated report")], blockers: [text("依赖效果专项通过短句门禁", "Blocked on the alignment stream's short-utterance gate")], openQuestions: [text("业务样例是否覆盖真实噪声环境？", "Do business samples cover real noise conditions?")],
      revision: 1, checkpointAt: "2026-10-28 15:00", sourceIds: ["contract"], resourceIds: ["service", "contract", "eval"], tasks: [],
      topics: [{ id: "business", title: text("接入验收与上线条件", "Onboarding acceptance and launch"), entries: [{ id: "e4", text: text("接口联调通过不等于效果通过；必须独立记录业务验收结果。", "Passing API tests is not passing quality acceptance; record each separately."), at: "2026-10-28 15:00", sourceId: "contract" }] }],
      events: [{ id: "o-ev1", title: text("等待效果门禁，暂停灰度接入", "Paused canary onboarding pending quality acceptance"), at: "2026-10-28 15:00", sourceId: "contract" }],
    },
  ];
  return {
    streams,
    resources: [
      { id: "service", title: text("共享语音服务", "Shared speech service"), kind: "service", locator: "service://demo/speech-recognition", purpose: text("服务标识与能力入口；当前部署需另行核实。", "Service identity and capabilities; verify deployments separately.") },
      { id: "repo", title: text("服务代码仓库", "Service repository"), kind: "directory", locator: "/workspaces/demo/speech-service", node: "demo-dev-node", purpose: text("代码、测试脚本与运行说明。", "Code, test scripts and runnable instructions.") },
      { id: "runbook", title: text("发布与回滚 Runbook", "Release & rollback Runbook"), kind: "document", locator: "https://docs.example.invalid/speech/runbook", purpose: text("发布门禁、核验步骤与回滚入口。", "Release gates, verification and rollback entry points.") },
      { id: "monitor", title: text("服务运行监控", "Service monitoring"), kind: "dashboard", locator: "https://monitor.example.invalid/d/speech", purpose: text("请求、延迟与错误率；原型不连接监控。", "Traffic, latency and errors; not connected in this prototype.") },
      { id: "eval", title: text("短句评测报告 · r12", "Short-utterance report · r12"), kind: "artifact", locator: "artifact://demo/evaluation/r12", purpose: text("可复现离线报告，不代表生产部署。", "Reproducible offline results, not production state.") },
      { id: "contract", title: text("业务甲接入契约", "Team A API contract"), kind: "document", locator: "https://docs.example.invalid/team-a/contract", purpose: text("接口、场景验收与业务上线条件。", "API contract, scenario acceptance and launch conditions.") },
    ],
    sources: [
      { id: "report", contextId: "alignment", title: text("离线评测报告 r12", "Offline evaluation r12"), locator: "artifact://demo/evaluation/r12#short-utterances", excerpt: text("演示评测集 demo-id-v7：整体 WER 从 12.4% 降至 11.8%，短句子集从 9.1% 升至 10.2%。仅描述离线样本，未证明生产效果。", "Fictional dataset demo-id-v7: aggregate WER 12.4% → 11.8%; short utterances 9.1% → 10.2%. Offline samples only, not a production claim."), at: "2026-10-29 09:20" },
      { id: "runbook", contextId: "speech", title: text("发布交接讨论与清单", "Release handoff and checklist"), locator: "session://demo/speech-release#handoff", excerpt: text("演示决策：每次发布关联 code commit、model hash、config hash、评测集版本与回滚目标。尚未执行生产发布。", "Demo decision: link code commit, model/config hashes, evaluation version and rollback target per release. No production release was performed."), at: "2026-10-27 16:10" },
      { id: "old-deploy", contextId: "speech", title: text("旧版本人工交接", "Old human deployment handoff"), locator: "session://demo/speech-operations#2026-10-20", excerpt: text("历史人工报告：当时提到生产使用 v2。未查询部署系统，未记录环境配置快照，不能据此判断当前版本。", "Historical human report: production v2 was mentioned then. No deployment query or environment snapshot was recorded; this cannot establish the current version."), at: "2026-10-20 10:00" },
      { id: "contract", contextId: "onboarding", title: text("业务甲联调验收记录", "Team A integration acceptance"), locator: "document://demo/team-a/acceptance", excerpt: text("演示记录：接口联调通过；业务效果验收仍待完成。上线依赖短句门禁，不可将接口通过写成全部验收通过。", "Demo record: API integration passed; business quality acceptance remains open. Launch depends on the short-utterance gate."), at: "2026-10-28 15:00" },
    ],
    knowledge: [
      { id: "k-wer", contextId: "alignment", title: text("r12 整体改善，但短句回归", "r12 improves aggregate WER but regresses on short utterances"), content: text("demo-id-v7 整体 WER 12.4% → 11.8%；短句 9.1% → 10.2%。此事实仅适用于离线评测。", "demo-id-v7 WER: aggregate 12.4% → 11.8%; short utterances 9.1% → 10.2%. Offline evaluation only."), kind: "fact", status: "confirmed", scope: "speech / offline / demo-id-v7 / r12", observed: "2026-10-29 09:20", sourceId: "report" },
      { id: "k-release", contextId: "speech", title: text("发布记录必须可追溯与回滚", "Releases must be traceable and reversible"), content: text("保留代码、模型、参数、评测、镜像与回滚关联，不只保存自然语言摘要。", "Keep code, model, config, evaluation, image and rollback links, not only a prose summary."), kind: "decision", status: "confirmed", scope: "speech / release-process", observed: "2026-10-27 16:10", sourceId: "runbook" },
      { id: "k-params", contextId: "speech", title: text("退化可能来自解码参数", "Decoding parameters may explain the regression"), content: text("讨论中出现的待验证线索；尚未完成单变量对比，不能作为根因结论。", "An unverified discussion clue. No controlled comparison yet; not a root-cause conclusion."), kind: "trace", status: "candidate", scope: "speech / short-utterances / hypothesis", observed: "2026-10-28 17:20", sourceId: "report" },
      { id: "k-prod", contextId: "speech", title: text("旧交接提到生产仍用 v2", "An old handoff mentioned production v2"), content: text("历史人工报告，未连接部署事实源；只能帮助追查，不能回答生产现在的版本。", "An old human report, not connected to deployment records. A historical lead, not the current production version."), kind: "fact", status: "stale", scope: "speech / production / human-report", observed: "2026-10-20 10:00", sourceId: "old-deploy" },
      { id: "k-contract", contextId: "onboarding", title: text("接口通过与效果验收分别记录", "Track API and quality acceptance separately"), content: text("接口联调完成，业务效果验收依然开放，灰度接入尚未批准。", "API integration is complete; quality acceptance is open and canary onboarding is not approved."), kind: "decision", status: "confirmed", scope: "team-a / onboarding / acceptance", observed: "2026-10-28 15:00", sourceId: "contract" },
    ],
    relations: [
      { from: "speech", to: "alignment", kind: "related", condition: text("共享服务仓库、评测报告与发布知识。", "Shares the service repository, reports and release knowledge.") },
      { from: "onboarding", to: "alignment", kind: "depends", condition: text("短句门禁通过并提供可复现报告后，才能继续业务验收。", "Quality acceptance can proceed after the short-utterance gate passes with a reproducible report.") },
      { from: "onboarding", to: "speech", kind: "related", condition: text("复用同一服务与 Runbook，不复制生产配置。", "Reuses the service and Runbook without copying production config.") },
    ],
  };
}

export function recallKnowledge(data: DesktopDemo, contextId: string, scope: RecallScope, query: string): Knowledge[] {
  const allowed = new Set([contextId]);
  if (scope === "related") {
    for (const link of data.relations) {
      if (link.from === contextId) allowed.add(link.to);
      if (link.to === contextId) allowed.add(link.from);
    }
  }
  const needle = query.trim().toLocaleLowerCase();
  return data.knowledge.filter((item) => (scope === "all" || allowed.has(item.contextId)) &&
    (!needle || `${item.title} ${item.content} ${item.scope}`.toLocaleLowerCase().includes(needle)));
}

export function updateStream(data: DesktopDemo, id: string, patch: Partial<Stream>): DesktopDemo {
  return { ...data, streams: data.streams.map((item) => item.id === id ? { ...item, ...patch, id: item.id } : item) };
}

export function splitLines(value: string): string[] {
  return value.split("\n").map((line) => line.trim()).filter(Boolean);
}
