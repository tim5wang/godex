import { useEffect, useMemo, useRef, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Divider,
  Dropdown,
  Empty,
  Form,
  Input,
  List,
  Modal,
  Popconfirm,
  Select,
  Space,
  Spin,
  Tabs,
  Tag,
  Typography,
} from "antd";
import {
  BookOutlined,
  DatabaseOutlined,
  DeleteOutlined,
  EditOutlined,
  HistoryOutlined,
  MessageOutlined,
  PlayCircleOutlined,
  PlusOutlined,
  ReloadOutlined,
  SaveOutlined,
  SearchOutlined,
  ThunderboltOutlined,
} from "@ant-design/icons";
import { parse, stringify as toYaml } from "yaml";
import { MarkdownContent } from "../../components/MarkdownContent";
import { UiCardView, type UiCardData } from "./components/UiCardView";
import { useI18n } from "../../i18n";
import {
  approveSessionPermission,
  deleteNote,
  denySessionPermission,
  getMeta,
  getSessionPermissions,
  listMemory,
  listNotes,
  openSession,
  previewMemoryContext,
  resumeSessionTurn,
  retrySessionTurn,
  saveNote,
  submitMessage,
} from "../../lib/api";
import { buildChatRoute } from "../../lib/chatRoutes";
import { showError } from "../../lib/notifications";
import { streamEvents } from "../../lib/sse";
import type { MemoryContextLayers, Note, PendingPermission, RuntimeEvent } from "../../lib/types";
import { useSettingsStore } from "../../store/settings";

const { Title, Text, Paragraph } = Typography;

const PLAYBOOK_TAG = "workflow";

/** One recorded launch run, persisted in localStorage. */
type WorkflowRunRecord = {
  playbookId: string;
  playbookTitle: string;
  sessionKey: string;
  status: "completed" | "error";
  timestamp: string;
  /** Turn id of the launch turn — enables retry / resume from history. */
  turnId?: string;
  /** Wall-clock duration of the run in ms. */
  durationMs?: number;
  /** Tool activity summary at completion (name → status), for quick review. */
  tools?: Array<{ name: string; status: string }>;
};

const RUN_HISTORY_KEY = "godex:workflows:run-history";
const RUN_HISTORY_MAX = 20;

function loadRunHistory(): WorkflowRunRecord[] {
  if (typeof window === "undefined") {
    return [];
  }
  try {
    const raw = window.localStorage.getItem(RUN_HISTORY_KEY);
    const parsed = raw ? (JSON.parse(raw) as WorkflowRunRecord[]) : [];
    return Array.isArray(parsed) ? parsed : [];
  } catch {
    return [];
  }
}

function persistRunHistory(records: WorkflowRunRecord[]) {
  try {
    window.localStorage.setItem(RUN_HISTORY_KEY, JSON.stringify(records.slice(0, RUN_HISTORY_MAX)));
  } catch {
    // Ignore quota / private-mode errors.
  }
}

/** Built-in playbook templates offered via the “From template” dropdown. */
type PlaybookTemplate = {
  title: string;
  summary: string;
  content: string;
  /** “AI 工号卡”六问示例（保存时写入 front-matter）。 */
  six?: Partial<Record<CardQuestionKey, string>>;
};

const PLAYBOOK_TEMPLATES: PlaybookTemplate[] = [
  {
    title: "测试失败排查",
    summary: "按步骤定位测试失败根因并给出修复建议",
    six: {
      serviceWhom: "开发团队",
      canRead: "工作区源码、测试文件、CI 日志",
      canCall: "bash（测试命令）、read_file、grep",
      cannotDo: "修改生产配置、推送代码",
      deliverable: "根因分析 + 最小修复 diff + 验证结果",
      reviewer: "提交人 / 模块负责人",
    },
    content: `# 目标
定位当前工作区测试失败（或指定范围）的根因，并给出最小修复建议。

# 步骤
1. 先用合适的测试命令复现失败，记录失败用例与报错摘要。
2. 读取失败用例对应源码与相关文件（read_file），确认断言与预期行为。
3. 缩小范围：判断是逻辑错误、环境差异还是测试本身问题。
4. 给出根因分析 + 最小修复方案；若涉及改动，先说明再实施。
5. 用与项目一致的验证命令复跑相关测试，确认通过。

# 输出
根因、修复 diff、验证结果。若失败与工作区无关，明确说明证据。`,
  },
  {
    title: "发布前检查清单",
    summary: "发布前的版本、构建、测试、文档全量检查",
    six: {
      serviceWhom: "发布负责人",
      canRead: "工作区代码、Makefile/package.json、release notes、git 状态",
      canCall: "bash（构建/测试命令）、read_file、git",
      cannotDo: "实际执行发布/推送、修改版本号",
      deliverable: "逐项 ✅/❌ 清单 + 阻塞项 + 建议动作",
      reviewer: "发布负责人",
    },
    content: `# 目标
对当前工作区执行发布前检查，输出一份可执行清单。

# 步骤
1. 确认版本号引用点（Makefile / 代码内 version 字符串）并核对是否一致。
2. 检查构建：运行项目真实构建命令（make / package.json scripts），确认通过。
3. 检查测试：运行相关测试套件，记录失败项。
4. 检查文档：release notes / README / CHANGELOG 是否与本版本变更一致。
5. 检查未提交改动与未推送提交，确认发布范围。

# 输出
逐项 ✅/❌ 清单 + 阻塞项列表 + 建议动作。`,
  },
  {
    title: "新环境接入",
    summary: "把当前仓库/服务接入新环境（部署、配置、验证）",
    six: {
      serviceWhom: "部署工程师",
      canRead: "部署文档、配置模板、环境变量说明",
      canCall: "bash（只读检查）、read_file",
      cannotDo: "修改生产配置、重启生产服务",
      deliverable: "接入步骤摘要 + 配置变更 + 验证结果 + 遗留问题",
      reviewer: "部署负责人",
    },
    content: `# 目标
把当前项目接入新环境（新机器 / 新服务 / 新节点），完成配置与验证。

# 步骤
1. 明确目标环境信息（地址、凭据位置、配置文件）。
2. 检查项目对环境的要求（依赖、端口、环境变量），对照现有配置。
3. 按项目文档执行接入步骤；必要时先读部署文档再动手。
4. 验证连通性与基础功能，记录验证命令与结果。

# 输出
接入步骤摘要、配置变更、验证结果、遗留问题。`,
  },
  {
    title: "问题复盘报告",
    summary: "对一次故障/问题做结构化复盘",
    six: {
      serviceWhom: "值班团队",
      canRead: "故障相关日志/事件、记忆、历史会话",
      canCall: "read_file、grep、memory",
      cannotDo: "回滚/恢复操作、向外部发通知",
      deliverable: "问题概述 / 时间线 / 根因 / 改进项 / 沉淀动作",
      reviewer: "值班负责人",
    },
    content: `# 目标
对指定问题做一次结构化复盘，输出可复用的结论。

# 步骤
1. 收集问题事实：发生时间、影响范围、现象、相关日志/事件。
2. 定位根因：结合记忆、历史会话与代码/配置证据。
3. 提炼改进：防再发生措施、检测手段、流程补丁。
4. 沉淀：把关键结论写入记忆（memory remember），供后续会话召回。

# 输出
问题概述 / 时间线 / 根因 / 改进项 / 沉淀动作。`,
  },
];

type PlaybookFormValues = {
  id?: string;
  title: string;
  summary?: string;
  content: string;
  /** “AI 工号卡”六问（Step 5）：服务谁 / 可读什么 / 可调什么 / 不能做什么 / 交付什么 / 谁验收 */
  serviceWhom?: string;
  canRead?: string;
  canCall?: string;
  cannotDo?: string;
  deliverable?: string;
  reviewer?: string;
};

/** 六问字段清单，用于 front-matter 序列化/解析与编辑器渲染。 */
const CARD_SIX_QUESTIONS = [
  { key: "serviceWhom", frontMatter: "service_whom", label: "serviceWhom" },
  { key: "canRead", frontMatter: "can_read", label: "canRead" },
  { key: "canCall", frontMatter: "can_call", label: "canCall" },
  { key: "cannotDo", frontMatter: "cannot_do", label: "cannotDo" },
  { key: "deliverable", frontMatter: "deliverable", label: "deliverable" },
  { key: "reviewer", frontMatter: "reviewer", label: "reviewer" },
] as const;

type CardQuestionKey = (typeof CARD_SIX_QUESTIONS)[number]["key"];

/** 把六问序列化为 YAML front-matter 拼到 content 前（无六问则原样返回）。 */
function withCardFrontMatter(values: Pick<PlaybookFormValues, CardQuestionKey> & { content: string }): string {
  const meta: Record<string, string> = {};
  for (const q of CARD_SIX_QUESTIONS) {
    const value = values[q.key]?.trim();
    if (value) {
      meta[q.frontMatter] = value;
    }
  }
  if (Object.keys(meta).length === 0) {
    return values.content;
  }
  const fm = `---\n${toYaml(meta).trimEnd()}\n---\n`;
  return `${fm}${values.content}`;
}

/** 从 content 解析 front-matter，返回 { six, body }（无 front-matter 时 six 为空、body 为原 content）。 */
function parseCardFrontMatter(content: string): { six: Partial<Record<CardQuestionKey, string>>; body: string } {
  const six: Partial<Record<CardQuestionKey, string>> = {};
  const match = /^---\n([\s\S]*?)\n---\n?/.exec(content);
  if (!match) {
    return { six, body: content };
  }
  try {
    const parsed = parse(match[1]) as Record<string, unknown>;
    for (const q of CARD_SIX_QUESTIONS) {
      const value = parsed[q.frontMatter];
      if (typeof value === "string" && value.trim()) {
        six[q.key] = value;
      }
    }
  } catch {
    return { six, body: content };
  }
  return { six, body: content.slice(match[0].length) };
}

/** 从剧本 front-matter 生成「AI 工号卡」六问段落，注入启动 prompt。 */
function buildCardSixSection(playbook: Note): string {
  const { six } = parseCardFrontMatter(playbook.content);
  const rows = CARD_SIX_QUESTIONS.filter((q) => six[q.key]?.trim())
    .map((q) => `- ${q.frontMatter}: ${six[q.key]?.trim()}`)
    .join("\n");
  if (!rows) {
    return "";
  }
  return `**AI 工号卡（边界与验收）：**\n${rows}`;
}

type LaunchFormValues = {
  playbookId: string;
  instructions?: string;
};

/** One tool invocation observed during a launch run. */
type ToolActivity = {
  name: string;
  status: "running" | "finished" | "failed";
};

/** A parsed ui_card emitted by the agent during a launch run. */
type UiCardEmission = {
  id: string;
  card: UiCardData;
};

function makeSessionKey() {
  return crypto.randomUUID();
}

function splitTags(value?: string | string[]): string[] {
  if (Array.isArray(value)) {
    return value;
  }
  if (!value) {
    return [];
  }
  return value
    .split(",")
    .map((tag) => tag.trim())
    .filter(Boolean);
}

/** Format a wall-clock duration (ms) as a compact human string. */
function formatDuration(ms: number): string {
  if (ms < 1000) {
    return `${ms}ms`;
  }
  const seconds = Math.round(ms / 1000);
  if (seconds < 60) {
    return `${seconds}s`;
  }
  const minutes = Math.floor(seconds / 60);
  const rest = seconds % 60;
  return rest ? `${minutes}m ${rest}s` : `${minutes}m`;
}

export function WorkflowsPage() {
  const { t } = useI18n();
  const { message: antMessage } = AntApp.useApp();
  const navigate = useNavigate();
  const queryClient = useQueryClient();
  const token = useSettingsStore((state) => state.token);
  const [playbookForm] = Form.useForm<PlaybookFormValues>();
  const [launchForm] = Form.useForm<LaunchFormValues>();
  const [search, setSearch] = useState("");
  const [editingPlaybook, setEditingPlaybook] = useState<Note | null>(null);
  const [playbookEditorOpen, setPlaybookEditorOpen] = useState(false);
  const [kbQuery, setKbQuery] = useState("");
  const [kbSearch, setKbSearch] = useState("");
  const [activeTab, setActiveTab] = useState("playbooks");
  const [runHistory, setRunHistory] = useState<WorkflowRunRecord[]>(() => loadRunHistory());

  // Launch state
  const [launching, setLaunching] = useState(false);
  const [streamOutput, setStreamOutput] = useState("");
  const [streamStatus, setStreamStatus] = useState<"idle" | "running" | "completed" | "error">("idle");
  const [launchSessionKey, setLaunchSessionKey] = useState("");
  const [toolActivity, setToolActivity] = useState<ToolActivity[]>([]);
  const toolActivityRef = useRef<ToolActivity[]>([]);
  const [pendingPermissions, setPendingPermissions] = useState<PendingPermission[]>([]);
  const [uiCards, setUiCards] = useState<UiCardEmission[]>([]);
  const streamBufferRef = useRef("");
  const streamAbortRef = useRef<AbortController | null>(null);
  const launchSessionRef = useRef<{ sessionId: string; turnId: string } | null>(null);
  const activeSessionIdRef = useRef("");

  const metaQuery = useQuery({ queryKey: ["meta"], queryFn: getMeta });
  const authRequired = metaQuery.data?.auth_required ?? false;
  const canReach = !authRequired || !!token;

  const playbooksQuery = useQuery({
    queryKey: ["notes", token, "workflows"],
    enabled: canReach,
    queryFn: () => listNotes(token || null, { tag: PLAYBOOK_TAG }),
  });
  const playbooks = useMemo(() => playbooksQuery.data ?? [], [playbooksQuery.data]);

  const knowledgeQuery = useQuery({
    queryKey: ["workflows-knowledge", token, kbQuery],
    enabled: canReach && kbQuery.trim().length > 0,
    queryFn: () => previewMemoryContext(token || null, kbQuery),
  });
  const browseQuery = useQuery({
    queryKey: ["memory", token, "workflows-browse", kbSearch],
    enabled: canReach,
    queryFn: () => listMemory(token || null, { query: kbSearch, limit: 100 }),
  });

  const saveMutation = useMutation({
    mutationFn: (values: PlaybookFormValues) =>
      saveNote(token || null, {
        id: values.id,
        title: values.title,
        summary: values.summary,
        tags: [PLAYBOOK_TAG],
        content: withCardFrontMatter(values),
      }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["notes", token, "workflows"] });
      antMessage.success(t("workflows.playbookSaved"));
      setPlaybookEditorOpen(false);
      setEditingPlaybook(null);
      playbookForm.resetFields();
    },
    onError: (error: Error) => showError(antMessage, error, t("workflows.playbookSaveFailed")),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteNote(token || null, id),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["notes", token, "workflows"] });
      antMessage.success(t("workflows.playbookDeleted"));
    },
    onError: (error: Error) => showError(antMessage, error, t("workflows.playbookDeleteFailed")),
  });

  const handleNewPlaybook = (template?: PlaybookTemplate) => {
    setEditingPlaybook(null);
    if (template) {
      playbookForm.setFieldsValue({
        title: template.title,
        summary: template.summary,
        content: template.content,
        serviceWhom: template.six?.serviceWhom ?? "",
        canRead: template.six?.canRead ?? "",
        canCall: template.six?.canCall ?? "",
        cannotDo: template.six?.cannotDo ?? "",
        deliverable: template.six?.deliverable ?? "",
        reviewer: template.six?.reviewer ?? "",
      });
    } else {
      playbookForm.resetFields();
    }
    setPlaybookEditorOpen(true);
  };

  const handleEditPlaybook = (playbook: Note) => {
    setEditingPlaybook(playbook);
    const { six, body } = parseCardFrontMatter(playbook.content);
    playbookForm.setFieldsValue({
      id: playbook.id,
      title: playbook.title,
      summary: playbook.summary ?? "",
      content: body,
      serviceWhom: six.serviceWhom ?? "",
      canRead: six.canRead ?? "",
      canCall: six.canCall ?? "",
      cannotDo: six.cannotDo ?? "",
      deliverable: six.deliverable ?? "",
      reviewer: six.reviewer ?? "",
    });
    setPlaybookEditorOpen(true);
  };

  const handleLaunchPlaybook = (playbook: Note) => {
    launchForm.setFieldsValue({ playbookId: playbook.id, instructions: "" });
    setStreamOutput("");
    setStreamStatus("idle");
    launchSessionRef.current = null;
    const sessionKey = makeSessionKey();
    setLaunchSessionKey(sessionKey);
    setActiveTab("launch");
  };

  const selectedPlaybookId = Form.useWatch("playbookId", launchForm);
  const selectedPlaybook = useMemo(
    () => playbooks.find((item) => item.id === selectedPlaybookId) ?? null,
    [playbooks, selectedPlaybookId],
  );

  const handleRun = async (values: LaunchFormValues) => {
    if (!values.playbookId) {
      antMessage.warning(t("workflows.launchPickFirst"));
      return;
    }
    const playbook = playbooks.find((item) => item.id === values.playbookId);
    if (!playbook) {
      antMessage.warning(t("workflows.launchPickFirst"));
      return;
    }
    if (streamAbortRef.current) {
      streamAbortRef.current.abort();
    }
    const controller = new AbortController();
    streamAbortRef.current = controller;
    streamBufferRef.current = "";
    setStreamOutput("");
    setStreamStatus("running");
    setLaunching(true);
    setToolActivity([]);
    toolActivityRef.current = [];
    setPendingPermissions([]);
    setUiCards([]);
    activeSessionIdRef.current = "";

    const sessionKey = launchSessionKey || makeSessionKey();
    setLaunchSessionKey(sessionKey);
    const startedAt = Date.now();

    const prompt = [
      `## ${playbook.title}`,
      playbook.content,
      "",
      buildCardSixSection(playbook),
      values.instructions?.trim() ? `**附加指令：**\n${values.instructions.trim()}` : "",
    ]
      .filter(Boolean)
      .join("\n");

    try {
      const opened = await openSession(token || null, { channel: "web", key: sessionKey });
      const sessionId = opened.session_id;
      activeSessionIdRef.current = sessionId;
      const submitted = await submitMessage(
        token || null,
        sessionId,
        { source: "workflows", text: prompt },
        { queueMode: "steering" },
      );
      launchSessionRef.current = { sessionId, turnId: submitted.turn_id };

      await streamEvents(
        sessionId,
        token || null,
        controller.signal,
        (event: RuntimeEvent) => {
          if (event.type === "assistant_text_delta") {
            const payload = event.payload as { text?: string } | undefined;
            if (payload?.text) {
              streamBufferRef.current += payload.text;
              setStreamOutput(streamBufferRef.current);
            }
          }
          if (event.type === "tool_call_started") {
            const payload = (event.payload ?? {}) as Record<string, unknown>;
            const name = String(payload.name ?? "tool");
            toolActivityRef.current = [...toolActivityRef.current, { name, status: "running" }];
            setToolActivity(toolActivityRef.current);
          }
          if (event.type === "tool_call_finished") {
            const payload = (event.payload ?? {}) as Record<string, unknown>;
            const name = String(payload.name ?? "tool");
            toolActivityRef.current = toolActivityRef.current.map((item) =>
              item.name === name && item.status === "running"
                ? { ...item, status: payload.error ? "failed" : "finished" }
                : item,
            );
            setToolActivity(toolActivityRef.current);
            if (name === "ui_card") {
              const output = String(payload.output ?? "").trim();
              if (output) {
                try {
                  const parsed = JSON.parse(output) as UiCardData;
                  if (parsed && (parsed.kind === "form" || parsed.kind === "button_group" || parsed.kind === "card")) {
                    setUiCards((current) => [...current, { id: crypto.randomUUID(), card: parsed }]);
                  }
                } catch {
                  // Not a ui_card JSON payload; ignore.
                }
              }
            }
          }
          if (event.type === "snapshot_ready") {
            void refreshPermissions(sessionId);
          }
          if (event.type === "turn_completed") {
            setStreamStatus("completed");
            recordRun(playbook, sessionKey, "completed", {
              turnId: submitted.turn_id,
              durationMs: Date.now() - startedAt,
              tools: toolActivityRef.current,
            });
          }
        },
      );
      if (!controller.signal.aborted) {
        setStreamStatus((current) => (current === "running" ? "completed" : current));
      }
    } catch (error) {
      if (!controller.signal.aborted) {
        setStreamStatus("error");
        setLaunching(false);
        recordRun(playbook, sessionKey, "error", {
          turnId: launchSessionRef.current?.turnId,
          durationMs: Date.now() - startedAt,
          tools: toolActivityRef.current,
        });
        showError(antMessage, error, t("workflows.launchRunFailed"));
      }
    } finally {
      if (!controller.signal.aborted) {
        setLaunching(false);
      }
    }
  };

  const handleCancelLaunch = () => {
    streamAbortRef.current?.abort();
    streamAbortRef.current = null;
    setLaunching(false);
    setStreamStatus("idle");
  };

  const handleOpenInChat = () => {
    const session = launchSessionRef.current;
    const key = session?.sessionId ? launchSessionKey : "";
    if (!key) {
      return;
    }
    navigate(buildChatRoute({ channel: "web", key }));
  };

  const refreshPermissions = async (sessionId: string) => {
    try {
      const list = await getSessionPermissions(token || null, sessionId);
      setPendingPermissions(Array.isArray(list) ? list : []);
    } catch {
      setPendingPermissions([]);
    }
  };

  const approvePermissionMutation = useMutation({
    mutationFn: async ({ sessionId, requestId, scope }: { sessionId: string; requestId: string; scope: "once" | "session" }) =>
      approveSessionPermission(token || null, sessionId, requestId, scope),
    onSuccess: async (_data, variables) => {
      await refreshPermissions(variables.sessionId);
      antMessage.success(t("workflows.permissionApproved"));
    },
    onError: (error: Error) => showError(antMessage, error, t("workflows.permissionFailed")),
  });

  const denyPermissionMutation = useMutation({
    mutationFn: async ({ sessionId, requestId }: { sessionId: string; requestId: string }) =>
      denySessionPermission(token || null, sessionId, requestId),
    onSuccess: async (_data, variables) => {
      await refreshPermissions(variables.sessionId);
      antMessage.success(t("workflows.permissionDenied"));
    },
    onError: (error: Error) => showError(antMessage, error, t("workflows.permissionFailed")),
  });

  /** Retry the launch turn in the same session (Step 6). */
  const retryRunMutation = useMutation({
    mutationFn: async ({ sessionId, turnId }: { sessionId: string; turnId: string }) =>
      retrySessionTurn(token || null, sessionId, turnId),
    onSuccess: () => {
      antMessage.success(t("workflows.retryQueued"));
      if (activeSessionIdRef.current) {
        void refreshPermissions(activeSessionIdRef.current);
      }
    },
    onError: (error: Error) => showError(antMessage, error, t("workflows.retryFailed")),
  });

  /** Resume the launch turn (recover from a paused/interrupted state) (Step 6). */
  const resumeRunMutation = useMutation({
    mutationFn: async ({ sessionId, turnId }: { sessionId: string; turnId: string }) =>
      resumeSessionTurn(token || null, sessionId, turnId),
    onSuccess: () => {
      antMessage.success(t("workflows.resumeQueued"));
      if (activeSessionIdRef.current) {
        void refreshPermissions(activeSessionIdRef.current);
      }
    },
    onError: (error: Error) => showError(antMessage, error, t("workflows.resumeFailed")),
  });

  useEffect(() => {
    return () => {
      streamAbortRef.current?.abort();
    };
  }, []);

  useEffect(() => {
    persistRunHistory(runHistory);
  }, [runHistory]);

  const recordRun = (
    playbook: Note,
    sessionKey: string,
    status: "completed" | "error",
    extra?: { turnId?: string; durationMs?: number; tools?: ToolActivity[] },
  ) => {
    setRunHistory((current) =>
      [
        {
          playbookId: playbook.id,
          playbookTitle: playbook.title,
          sessionKey,
          status,
          timestamp: new Date().toISOString(),
          turnId: extra?.turnId,
          durationMs: extra?.durationMs,
          tools: extra?.tools?.length ? extra.tools : undefined,
        },
        ...current,
      ].slice(0, RUN_HISTORY_MAX),
    );
  };

  const knowledgeLayers: MemoryContextLayers = {
    identity: Array.isArray(knowledgeQuery.data?.identity) ? knowledgeQuery.data.identity : [],
    core: Array.isArray(knowledgeQuery.data?.core) ? knowledgeQuery.data.core : [],
    relevant: Array.isArray(knowledgeQuery.data?.relevant) ? knowledgeQuery.data.relevant : [],
  };
  const browseEntries = Array.isArray(browseQuery.data) ? browseQuery.data : [];

  const tabItems = [
    {
      key: "playbooks",
      label: (
        <span>
          <BookOutlined /> {t("workflows.tabPlaybooks")}
        </span>
      ),
      children: (
        <div className="workflows-tab-body">
          <Space style={{ marginBottom: 16 }} wrap>
            <Button type="primary" icon={<PlusOutlined />} onClick={() => handleNewPlaybook()}>
              {t("workflows.newPlaybook")}
            </Button>
            <Dropdown
              menu={{
                items: PLAYBOOK_TEMPLATES.map((template) => ({
                  key: template.title,
                  label: (
                    <div>
                      <div>{template.title}</div>
                      <Typography.Text type="secondary" style={{ fontSize: 12 }}>
                        {template.summary}
                      </Typography.Text>
                    </div>
                  ),
                  onClick: () => handleNewPlaybook(template),
                })),
              }}
            >
              <Button icon={<BookOutlined />}>{t("workflows.templateFrom")}</Button>
            </Dropdown>
            <Button icon={<ReloadOutlined />} onClick={() => void playbooksQuery.refetch()}>
              {t("workflows.refresh")}
            </Button>
          </Space>
          <Input.Search
            placeholder={t("workflows.searchPlaceholder")}
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            allowClear
            style={{ marginBottom: 16, maxWidth: 420 }}
          />
          {runHistory.length > 0 ? (
            <Card
              size="small"
              title={
                <Space size={6}>
                  <HistoryOutlined />
                  {t("workflows.recentRuns")}
                </Space>
              }
              style={{ marginBottom: 16 }}
              extra={
                <Button type="text" size="small" onClick={() => setRunHistory([])}>
                  {t("workflows.clearRuns")}
                </Button>
              }
            >
              <List
                size="small"
                dataSource={runHistory.slice(0, 6)}
                renderItem={(run) => (
                  <List.Item
                    actions={[
                      <Button
                        key="open"
                        type="link"
                        size="small"
                        icon={<MessageOutlined />}
                        onClick={() => navigate(buildChatRoute({ channel: "web", key: run.sessionKey }))}
                      >
                        {t("workflows.openInChat")}
                      </Button>,
                    ]}
                  >
                    <List.Item.Meta
                      title={
                        <Space size={6} wrap>
                          <span>{run.playbookTitle}</span>
                          <Tag color={run.status === "completed" ? "green" : "red"}>
                            {run.status === "completed" ? t("workflows.runCompleted") : t("workflows.runFailed")}
                          </Tag>
                          {run.durationMs ? <Text type="secondary" style={{ fontSize: 12 }}>{formatDuration(run.durationMs)}</Text> : null}
                        </Space>
                      }
                      description={
                        <Space direction="vertical" size={2}>
                          <Text type="secondary" style={{ fontSize: 12 }}>
                            {new Date(run.timestamp).toLocaleString()}
                          </Text>
                          {run.tools?.length ? (
                            <Space size={[4, 4]} wrap>
                              {run.tools.slice(0, 8).map((tool, index) => (
                                <Tag
                                  key={`${tool.name}-${index}`}
                                  color={tool.status === "running" ? "processing" : tool.status === "failed" ? "red" : "green"}
                                  style={{ fontSize: 11 }}
                                >
                                  {tool.name}
                                </Tag>
                              ))}
                              {run.tools.length > 8 ? <Text type="secondary" style={{ fontSize: 11 }}>+{run.tools.length - 8}</Text> : null}
                            </Space>
                          ) : null}
                        </Space>
                      }
                    />
                  </List.Item>
                )}
              />
            </Card>
          ) : null}
          {playbooks.length === 0 ? (
            <Empty description={t("workflows.playbookEmpty")} />
          ) : (
            <List
              grid={{ gutter: 16, xs: 1, sm: 2, lg: 3 }}
              dataSource={playbooks.filter((item) => {
                const q = search.trim().toLowerCase();
                if (!q) {
                  return true;
                }
                return (
                  item.title.toLowerCase().includes(q) ||
                  (item.summary ?? "").toLowerCase().includes(q) ||
                  item.content.toLowerCase().includes(q)
                );
              })}
              renderItem={(playbook) => (
                <List.Item>
                  <Card
                    size="small"
                    title={playbook.title}
                    extra={
                      <Space size={4}>
                        <Button
                          type="text"
                          size="small"
                          icon={<PlayCircleOutlined />}
                          aria-label={t("workflows.run")}
                          onClick={() => handleLaunchPlaybook(playbook)}
                        />
                        <Button
                          type="text"
                          size="small"
                          icon={<EditOutlined />}
                          aria-label={t("workflows.edit")}
                          onClick={() => handleEditPlaybook(playbook)}
                        />
                        <Popconfirm
                          title={t("workflows.deleteConfirm")}
                          onConfirm={() => deleteMutation.mutate(playbook.id)}
                        >
                          <Button type="text" size="small" danger icon={<DeleteOutlined />} aria-label={t("workflows.delete")} />
                        </Popconfirm>
                      </Space>
                    }
                  >
                    <Paragraph type="secondary" ellipsis={{ rows: 2 }} style={{ marginBottom: 8 }}>
                      {playbook.summary || playbook.content}
                    </Paragraph>
                    <Tag color="teal">{PLAYBOOK_TAG}</Tag>
                  </Card>
                </List.Item>
              )}
            />
          )}
        </div>
      ),
    },
    {
      key: "knowledge",
      label: (
        <span>
          <DatabaseOutlined /> {t("workflows.tabKnowledge")}
        </span>
      ),
      children: (
        <div className="workflows-tab-body">
          <Space direction="vertical" style={{ width: "100%" }}>
            <Input.Search
              placeholder={t("workflows.kbSearchPlaceholder")}
              enterButton={<SearchOutlined />}
              value={kbSearch}
              onChange={(event) => setKbSearch(event.target.value)}
              onSearch={(value) => setKbQuery(value)}
              allowClear
              style={{ maxWidth: 480 }}
            />
            <Card size="small" title={t("workflows.kbRecall")}>
              {kbQuery.trim() ? (
                knowledgeQuery.isLoading ? (
                  <Spin />
                ) : (
                  <RenderContextLayers layers={knowledgeLayers} t={t} />
                )
              ) : (
                <Paragraph type="secondary">{t("workflows.kbRecallHint")}</Paragraph>
              )}
            </Card>
            <Divider plain>
              {t("workflows.kbBrowse")}
            </Divider>
            <List
              size="small"
              dataSource={browseEntries}
              locale={{ emptyText: t("workflows.kbBrowseEmpty") }}
              renderItem={(entry) => (
                <List.Item>
                  <List.Item.Meta
                    title={
                      <Space size={6}>
                        <span>{entry.title}</span>
                        <Tag color="blue">{entry.type}</Tag>
                        {entry.tags?.map((tag) => (
                          <Tag key={tag}>{tag}</Tag>
                        ))}
                      </Space>
                    }
                    description={entry.summary}
                  />
                </List.Item>
              )}
            />
          </Space>
        </div>
      ),
    },
    {
      key: "launch",
      label: (
        <span>
          <ThunderboltOutlined /> {t("workflows.tabLaunch")}
        </span>
      ),
      children: (
        <div className="workflows-tab-body" style={{ maxWidth: 760 }}>
          <Form form={launchForm} layout="vertical" onFinish={handleRun}>
            <Form.Item
              name="playbookId"
              label={t("workflows.launchPlaybook")}
              rules={[{ required: true, message: t("workflows.launchPickFirst") }]}
            >
              <Select
                placeholder={t("workflows.launchPlaybookPlaceholder")}
                options={playbooks.map((playbook) => ({
                  value: playbook.id,
                  label: playbook.title,
                }))}
                showSearch
                optionFilterProp="label"
              />
            </Form.Item>
            {selectedPlaybook ? (
              <Card size="small" title={t("workflows.launchPreview")} style={{ marginBottom: 16 }}>
                <MarkdownContent content={selectedPlaybook.content} />
              </Card>
            ) : null}
            <Form.Item name="instructions" label={t("workflows.launchInstructions")}>
              <Input.TextArea rows={3} placeholder={t("workflows.launchInstructionsPlaceholder")} />
            </Form.Item>
            <Space>
              <Button type="primary" htmlType="submit" icon={<PlayCircleOutlined />} loading={launching}>
                {t("workflows.launchRun")}
              </Button>
              {streamStatus === "running" ? (
                <Button icon={<ReloadOutlined />} onClick={handleCancelLaunch}>
                  {t("workflows.launchCancel")}
                </Button>
              ) : null}
              {streamStatus === "completed" && launchSessionKey ? (
                <Button icon={<MessageOutlined />} onClick={handleOpenInChat}>
                  {t("workflows.openInChat")}
                </Button>
              ) : null}
            </Space>
          </Form>
          {streamStatus !== "idle" ? (
            <Card
              size="small"
              title={t("workflows.launchResult")}
              style={{ marginTop: 16 }}
              extra={
                <Tag color={streamStatus === "running" ? "processing" : streamStatus === "error" ? "red" : "green"}>
                  {streamStatus}
                </Tag>
              }
            >
              {streamStatus === "running" && !streamOutput ? <Spin size="small" /> : null}
              {streamOutput ? <MarkdownContent content={streamOutput} /> : null}
              {streamStatus === "error" ? <Alert type="error" message={t("workflows.launchError")} showIcon /> : null}
              {streamStatus === "error" && launchSessionRef.current ? (
                <Space style={{ marginTop: 12 }}>
                  <Button
                    size="small"
                    icon={<ReloadOutlined />}
                    loading={retryRunMutation.isPending}
                    onClick={() =>
                      retryRunMutation.mutate({
                        sessionId: launchSessionRef.current!.sessionId,
                        turnId: launchSessionRef.current!.turnId,
                      })
                    }
                  >
                    {t("workflows.retryRun")}
                  </Button>
                  <Button
                    size="small"
                    icon={<PlayCircleOutlined />}
                    loading={resumeRunMutation.isPending}
                    onClick={() =>
                      resumeRunMutation.mutate({
                        sessionId: launchSessionRef.current!.sessionId,
                        turnId: launchSessionRef.current!.turnId,
                      })
                    }
                  >
                    {t("workflows.resumeRun")}
                  </Button>
                </Space>
              ) : null}
            </Card>
          ) : null}
          {uiCards.length > 0 ? (
            <Space direction="vertical" style={{ width: "100%", marginTop: 16 }}>
              {uiCards.map((emission) => (
                <UiCardView
                  key={emission.id}
                  card={emission.card}
                  onSubmitCard={(action) => {
                    if (!activeSessionIdRef.current) {
                      return;
                    }
                    void submitMessage(
                      token || null,
                      activeSessionIdRef.current,
                      { source: "workflows", text: action },
                      { queueMode: "follow_up" },
                    );
                  }}
                  labels={{
                    cardTitle: t("workflows.cardTitle"),
                    cardSubmit: t("workflows.cardSubmit"),
                    cardFieldRequired: t("workflows.cardFieldRequired"),
                  }}
                />
              ))}
            </Space>
          ) : null}
          {pendingPermissions.length > 0 ? (
            <Card size="small" title={t("workflows.launchApprovals")} style={{ marginTop: 16 }}>
              <List
                size="small"
                dataSource={pendingPermissions}
                renderItem={(permission) => (
                  <List.Item
                    actions={[
                      <Button
                        key="allow"
                        type="primary"
                        size="small"
                        loading={approvePermissionMutation.isPending}
                        onClick={() =>
                          approvePermissionMutation.mutate({
                            sessionId: activeSessionIdRef.current,
                            requestId: permission.id,
                            scope: "once",
                          })
                        }
                      >
                        {t("workflows.approve")}
                      </Button>,
                      <Button
                        key="deny"
                        danger
                        size="small"
                        loading={denyPermissionMutation.isPending}
                        onClick={() =>
                          denyPermissionMutation.mutate({
                            sessionId: activeSessionIdRef.current,
                            requestId: permission.id,
                          })
                        }
                      >
                        {t("workflows.deny")}
                      </Button>,
                    ]}
                  >
                    <List.Item.Meta
                      title={permission.request.tool_name}
                      description={permission.request.command || permission.request.paths?.join(", ") || permission.reason}
                    />
                  </List.Item>
                )}
              />
            </Card>
          ) : null}
          {toolActivity.length > 0 ? (
            <Card size="small" title={t("workflows.launchTools")} style={{ marginTop: 16 }}>
              <Space size={[6, 6]} wrap>
                {toolActivity.map((activity, index) => (
                  <Tag
                    key={`${activity.name}-${index}`}
                    color={activity.status === "running" ? "processing" : activity.status === "failed" ? "red" : "green"}
                  >
                    {activity.name}
                  </Tag>
                ))}
              </Space>
            </Card>
          ) : null}
        </div>
      ),
    },
  ];

  return (
    <div className="workflows-page">
      <Title level={3}>{t("workflows.pageTitle")}</Title>
      <Paragraph type="secondary">{t("workflows.pageSubtitle")}</Paragraph>
      <Tabs activeKey={activeTab} onChange={setActiveTab} items={tabItems} />
      <Modal
        open={playbookEditorOpen}
        title={
          <Space>
            <EditOutlined />
            {editingPlaybook ? t("workflows.editPlaybook") : t("workflows.newPlaybook")}
          </Space>
        }
        onCancel={() => {
          setPlaybookEditorOpen(false);
          setEditingPlaybook(null);
        }}
        footer={null}
        destroyOnHidden
        width={680}
      >
        <Form form={playbookForm} layout="vertical" onFinish={(values) => saveMutation.mutate(values)}>
          <Form.Item name="id" hidden>
            <Input />
          </Form.Item>
          <Form.Item
            name="title"
            label={t("workflows.playbookTitle")}
            rules={[{ required: true, message: t("workflows.playbookTitleRequired") }]}
          >
            <Input placeholder={t("workflows.playbookTitlePlaceholder")} />
          </Form.Item>
          <Form.Item name="summary" label={t("workflows.playbookSummary")}>
            <Input placeholder={t("workflows.playbookSummaryPlaceholder")} />
          </Form.Item>
          <Form.Item
            name="content"
            label={t("workflows.playbookContent")}
            rules={[{ required: true, message: t("workflows.playbookContentRequired") }]}
          >
            <Input.TextArea rows={12} placeholder={t("workflows.playbookContentPlaceholder")} />
          </Form.Item>
          <Divider plain>{t("workflows.cardSixTitle")}</Divider>
          <Paragraph type="secondary">{t("workflows.cardSixHint")}</Paragraph>
          <Form.Item name="serviceWhom" label={t("workflows.cardServiceWhom")}>
            <Input placeholder={t("workflows.cardServiceWhomPlaceholder")} />
          </Form.Item>
          <Form.Item name="canRead" label={t("workflows.cardCanRead")}>
            <Input placeholder={t("workflows.cardCanReadPlaceholder")} />
          </Form.Item>
          <Form.Item name="canCall" label={t("workflows.cardCanCall")}>
            <Input placeholder={t("workflows.cardCanCallPlaceholder")} />
          </Form.Item>
          <Form.Item name="cannotDo" label={t("workflows.cardCannotDo")}>
            <Input placeholder={t("workflows.cardCannotDoPlaceholder")} />
          </Form.Item>
          <Form.Item name="deliverable" label={t("workflows.cardDeliverable")}>
            <Input placeholder={t("workflows.cardDeliverablePlaceholder")} />
          </Form.Item>
          <Form.Item name="reviewer" label={t("workflows.cardReviewer")}>
            <Input placeholder={t("workflows.cardReviewerPlaceholder")} />
          </Form.Item>
          <Space>
            <Button type="primary" htmlType="submit" icon={<SaveOutlined />} loading={saveMutation.isPending}>
              {t("workflows.savePlaybook")}
            </Button>
            <Button
              onClick={() => {
                setPlaybookEditorOpen(false);
                setEditingPlaybook(null);
              }}
            >
              {t("workflows.cancel")}
            </Button>
          </Space>
        </Form>
      </Modal>
    </div>
  );
}

function RenderContextLayers({
  layers,
  t,
}: {
  layers: MemoryContextLayers;
  t: (key: string) => string;
}) {
  const sections = [
    { key: "identity" as const, label: t("workflows.kbLayerIdentity"), items: layers.identity },
    { key: "core" as const, label: t("workflows.kbLayerCore"), items: layers.core },
    { key: "relevant" as const, label: t("workflows.kbLayerRelevant"), items: layers.relevant },
  ];
  const hasAny = sections.some((section) => section.items.length > 0);
  if (!hasAny) {
    return <Empty description={t("workflows.kbNoMatch")} />;
  }
  return (
    <Space direction="vertical" style={{ width: "100%" }} size="middle">
      {sections
        .filter((section) => section.items.length > 0)
        .map((section) => (
          <div key={section.key}>
            <Tag color={section.key === "identity" ? "gold" : section.key === "core" ? "geekblue" : "green"}>
              {section.label}
            </Tag>
            <List
              size="small"
              dataSource={section.items}
              renderItem={(item) => (
                <List.Item>
                  <List.Item.Meta
                    title={item.title}
                    description={
                      <Space direction="vertical" size={0}>
                        <Text type="secondary" style={{ fontSize: 12 }}>
                          {item.file}
                        </Text>
                        {item.content ? <MarkdownContent content={item.content} className="workflows-kb-content" /> : null}
                      </Space>
                    }
                  />
                </List.Item>
              )}
            />
          </div>
        ))}
    </Space>
  );
}

