import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import {
  Alert,
  App as AntApp,
  Avatar,
  Button,
  Card,
  Drawer,
  Empty,
  Form,
  Input,
  Popconfirm,
  Popover,
  Select,
  Space,
  Switch,
  Tag,
  Typography,
} from "antd";
import { DeleteOutlined, EditOutlined, EyeOutlined, MessageOutlined, PlusOutlined, ReloadOutlined, SaveOutlined, SmileOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { showError } from "../../lib/notifications";
import { useSettingsStore } from "../../store/settings";
import {
  createAgentTemplate,
  deleteAgentTemplate,
  getAgentTemplateOptions,
  listAgentTemplates,
  listMCPServers,
  listPackages,
  listSkillsCatalog,
  updateAgentTemplate,
  type AgentTemplate,
} from "../../lib/api";

type TemplateFormValues = {
  id: string;
  name: string;
  description?: string;
  avatar?: string;
  color?: string;
  scenarios?: string[];
  bundles?: string[];
  tools?: string[];
  skills?: string[];
  mcp_servers?: string[];
  packages?: string[];
  persona?: string;
  base_prompt?: string;
  profile?: string;
  write_enabled?: boolean;
  trim_heavy_sections?: boolean;
  memory?: string;
  engine?: string;
};

const TEMPLATE_HISTORY_PREFIX = "agent-template-history:";

/** 常用头像 emoji，供模板表单的 emoji 选择器使用。 */
const COMMON_AVATAR_EMOJIS = [
  "🤖", "🧠", "🦾", "🛠️", "📚", "🔍", "💡", "🚀",
  "🎯", "🧪", "📊", "🗂️", "🔒", "⚡", "🎨", "📝",
  "🤝", "🌐", "🧩", "💬", "⚙️", "🔄", "🏗️", "🧭",
];

/** Locally remembered free-form values per field so past entries reappear as
 *  suggestions even when the backend has no authoritative list for them. */
function useLocalOptionHistory(key: string) {
  const [history, setHistory] = useState<string[]>(() => {
    try {
      const raw = localStorage.getItem(TEMPLATE_HISTORY_PREFIX + key);
      const parsed = raw ? (JSON.parse(raw) as string[]) : [];
      return Array.isArray(parsed) ? parsed : [];
    } catch {
      return [];
    }
  });
  const remember = (values: string[]) => {
    const fresh = values.filter(Boolean);
    if (fresh.length === 0) return;
    setHistory((prev) => {
      const next = Array.from(new Set([...fresh, ...prev])).slice(0, 30);
      try {
        localStorage.setItem(TEMPLATE_HISTORY_PREFIX + key, JSON.stringify(next));
      } catch {
        // Ignore storage failures (private mode, quota).
      }
      return next;
    });
  };
  return [history, remember] as const;
}

/** Backend-sourced options first, then locally remembered custom entries. */
function mergeOptions(options: { value: string; label: string }[], history: string[]) {
  const known = new Set(options.map((o) => o.value));
  return [...history.filter((v) => !known.has(v)).map((v) => ({ value: v, label: v })), ...options];
}

function TemplateAvatar({ tpl, size }: { tpl: AgentTemplate; size: number }) {
  const avatar = tpl.avatar?.trim() ?? "";
  if (/^https?:\/\//.test(avatar)) {
    return <Avatar size={size} src={avatar} />;
  }
  if (avatar) {
    return <Avatar size={size} style={{ background: tpl.color || undefined }}>{avatar}</Avatar>;
  }
  return (
    <Avatar size={size} style={{ background: tpl.color || undefined }}>
      {(tpl.name || tpl.id || "?").slice(0, 1).toUpperCase()}
    </Avatar>
  );
}

function TemplateCard(props: {
  tpl: AgentTemplate;
  t: ReturnType<typeof useI18n>["t"];
  onEdit: (tpl: AgentTemplate) => void;
  onDelete: (id: string) => void;
  onChat: (tpl: AgentTemplate) => void;
}) {
  const { tpl, t } = props;
  const readonly = tpl.source !== "user";
  const sourceLabel =
    tpl.source === "user" ? t("agentTemplates.sourceUser") : tpl.source === "package" ? t("agentTemplates.sourcePackage") : t("agentTemplates.sourceBuiltin");
  const stats: string[] = [];
  if (tpl.bundles?.length) stats.push(`${tpl.bundles.length} bundles`);
  if (tpl.skills?.length) stats.push(`${tpl.skills.length} skills`);
  if (tpl.mcp_servers?.length) stats.push(`${tpl.mcp_servers.length} MCP`);

  return (
    <Card
      size="small"
      title={
        <Space>
          <TemplateAvatar tpl={tpl} size={28} />
          <span>{tpl.name || tpl.id}</span>
        </Space>
      }
      extra={
        <Space size={4}>
          <Tag color={tpl.source === "user" ? "blue" : tpl.source === "package" ? "purple" : "default"}>{sourceLabel}</Tag>
          {!readonly ? null : <Tag>{t("agentTemplates.readonlyBadge")}</Tag>}
        </Space>
      }
      actions={[
        <Button key="chat" size="small" type="link" icon={<MessageOutlined />} onClick={() => props.onChat(tpl)}>
          {t("agentTemplates.chatWith")}
        </Button>,
        <Button key="edit" size="small" type="link" icon={<EyeOutlined />} onClick={() => props.onEdit(tpl)}>
          {readonly ? t("agentTemplates.viewDetail") : t("agentTemplates.edit")}
        </Button>,
        readonly ? (
          <span key="delete" />
        ) : (
          <Popconfirm key="delete" title={t("agentTemplates.deleteConfirm")} onConfirm={() => props.onDelete(tpl.id)}>
            <Button size="small" type="link" danger icon={<DeleteOutlined />} />
          </Popconfirm>
        ),
      ]}
    >
      <Typography.Paragraph type="secondary" style={{ marginBottom: 8, minHeight: 40 }}>
        {tpl.description?.trim() || tpl.persona?.slice(0, 120) || tpl.id}
      </Typography.Paragraph>
      <Space size={[4, 4]} wrap>
        {(tpl.scenarios ?? []).map((s) => (
          <Tag key={s} color="geekblue">
            {s}
          </Tag>
        ))}
        {tpl.profile ? <Tag>{tpl.profile}</Tag> : null}
        {stats.map((s) => (
          <Tag key={s}>{s}</Tag>
        ))}
      </Space>
    </Card>
  );
}

export function AgentTemplatesPage() {
  const { t } = useI18n();
  const { message: antMessage } = AntApp.useApp();
  const navigate = useNavigate();
  const token = useSettingsStore((state) => state.token);
  const queryClient = useQueryClient();
  const [editorOpen, setEditorOpen] = useState(false);
  const [editing, setEditing] = useState<AgentTemplate | null>(null);
  const [form] = Form.useForm<TemplateFormValues>();

  const templatesQuery = useQuery({
    queryKey: ["agent-templates", token],
    queryFn: () => listAgentTemplates(token || null),
  });

  // Authoritative choice lists for the form pickers: bundles/tools come from
  // the live tool registration, skills/MCP/packages from their own endpoints.
  const optionsQuery = useQuery({
    queryKey: ["agent-template-options", token],
    queryFn: () => getAgentTemplateOptions(token || null),
  });
  const skillsQuery = useQuery({
    queryKey: ["skills-catalog", token],
    queryFn: () => listSkillsCatalog(token || null),
  });
  const mcpQuery = useQuery({
    queryKey: ["mcp-servers", token],
    queryFn: () => listMCPServers(token || null),
  });
  const packagesQuery = useQuery({
    queryKey: ["packages", token],
    queryFn: () => listPackages(token || null),
  });

  const [bundleHistory, rememberBundles] = useLocalOptionHistory("bundles");
  const [toolHistory, rememberTools] = useLocalOptionHistory("tools");
  const [skillHistory, rememberSkills] = useLocalOptionHistory("skills");
  const [mcpHistory, rememberMCPServers] = useLocalOptionHistory("mcp");
  const [packageHistory, rememberPackages] = useLocalOptionHistory("packages");
  const [scenarioHistory, rememberScenarios] = useLocalOptionHistory("scenarios");

  const bundleOptions = useMemo(
    () => (optionsQuery.data?.bundles ?? []).map((b) => ({ value: b.name, label: b.summary ? `${b.name} — ${b.summary}` : b.name })),
    [optionsQuery.data],
  );
  const toolOptions = useMemo(() => (optionsQuery.data?.tools ?? []).map((n) => ({ value: n, label: n })), [optionsQuery.data]);
  const skillOptions = useMemo(
    () => (skillsQuery.data ?? []).filter((s) => s.name?.trim()).map((s) => ({ value: s.name, label: s.name })),
    [skillsQuery.data],
  );
  const mcpOptions = useMemo(
    () => (mcpQuery.data?.servers ?? []).filter((s) => s.name?.trim()).map((s) => ({ value: s.name, label: s.name })),
    [mcpQuery.data],
  );
  const packageOptions = useMemo(
    () => (packagesQuery.data ?? []).filter((p) => p.name?.trim()).map((p) => ({ value: p.name, label: p.name })),
    [packagesQuery.data],
  );

  const saveMutation = useMutation({
    mutationFn: async (values: TemplateFormValues) => {
      const payload: Partial<AgentTemplate> = {
        name: values.name,
        description: values.description,
        avatar: values.avatar,
        color: values.color,
        scenarios: values.scenarios,
        bundles: values.bundles,
        tools: values.tools,
        skills: values.skills,
        mcp_servers: values.mcp_servers,
        packages: values.packages,
        persona: values.persona,
        base_prompt: values.base_prompt,
        profile: values.profile,
        write_enabled: values.write_enabled,
        trim_heavy_sections: values.trim_heavy_sections,
        memory: values.memory,
        engine: values.engine,
      };
      if (editing) {
        return updateAgentTemplate(token || null, editing.id, payload);
      }
      return createAgentTemplate(token || null, { id: values.id, ...payload });
    },
    onSuccess: (_data, variables) => {
      // Remember custom entries (values outside the authoritative lists) so
      // they reappear as suggestions next time.
      const knownBundles = new Set((optionsQuery.data?.bundles ?? []).map((b) => b.name));
      rememberBundles((variables.bundles ?? []).filter((v) => !knownBundles.has(v)));
      const knownTools = new Set(optionsQuery.data?.tools ?? []);
      rememberTools((variables.tools ?? []).filter((v) => !knownTools.has(v)));
      const knownSkills = new Set((skillsQuery.data ?? []).map((s) => s.name));
      rememberSkills((variables.skills ?? []).filter((v) => !knownSkills.has(v)));
      const knownMCP = new Set((mcpQuery.data?.servers ?? []).map((s) => s.name));
      rememberMCPServers((variables.mcp_servers ?? []).filter((v) => !knownMCP.has(v)));
      const knownPackages = new Set((packagesQuery.data ?? []).map((p) => p.name));
      rememberPackages((variables.packages ?? []).filter((v) => !knownPackages.has(v)));
      rememberScenarios(variables.scenarios ?? []);
      setEditorOpen(false);
      void queryClient.invalidateQueries({ queryKey: ["agent-templates", token] });
    },
    onError: (error) => showError(antMessage, error, t("agentTemplates.saveFailed")),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteAgentTemplate(token || null, id),
    onSuccess: () => void queryClient.invalidateQueries({ queryKey: ["agent-templates", token] }),
    onError: (error) => showError(antMessage, error, t("agentTemplates.deleteFailed")),
  });

  const groups = useMemo(() => {
    const list = templatesQuery.data ?? [];
    return {
      user: list.filter((tpl) => tpl.source === "user"),
      builtin: list.filter((tpl) => tpl.source === "builtin"),
      package: list.filter((tpl) => tpl.source === "package"),
    };
  }, [templatesQuery.data]);

  const openCreate = () => {
    setEditing(null);
    form.resetFields();
    setEditorOpen(true);
  };

  const openEdit = (tpl: AgentTemplate) => {
    setEditing(tpl);
    form.setFieldsValue({
      id: tpl.id,
      name: tpl.name,
      description: tpl.description,
      avatar: tpl.avatar,
      color: tpl.color,
      scenarios: tpl.scenarios,
      bundles: tpl.bundles,
      tools: tpl.tools,
      skills: tpl.skills,
      mcp_servers: tpl.mcp_servers,
      packages: tpl.packages,
      persona: tpl.persona,
      base_prompt: tpl.base_prompt,
      profile: tpl.profile,
      write_enabled: tpl.write_enabled,
      trim_heavy_sections: tpl.trim_heavy_sections,
      memory: tpl.memory,
      engine: tpl.engine,
    });
    setEditorOpen(true);
  };

  const chatWith = (tpl: AgentTemplate) => {
    navigate(`/chat/web/${crypto.randomUUID()}?template=${encodeURIComponent(tpl.id)}`);
  };

  const renderGroup = (title: string, items: AgentTemplate[]) =>
    items.length === 0 ? null : (
      <div key={title} style={{ marginBottom: 24 }}>
        <Typography.Title level={5}>{title}</Typography.Title>
        <div style={{ display: "grid", gridTemplateColumns: "repeat(auto-fill, minmax(320px, 1fr))", gap: 12 }}>
          {items.map((tpl) => (
            <TemplateCard
              key={tpl.id}
              tpl={tpl}
              t={t}
              onEdit={openEdit}
              onDelete={(id) => deleteMutation.mutate(id)}
              onChat={chatWith}
            />
          ))}
        </div>
      </div>
    );

  return (
    <div style={{ padding: 16 }}>
      <div style={{ display: "flex", justifyContent: "space-between", alignItems: "center", marginBottom: 16 }}>
        <Typography.Title level={4} style={{ margin: 0 }}>
          {t("app.nav.agentTemplates")}
        </Typography.Title>
        <Space>
          <Button icon={<ReloadOutlined />} onClick={() => void queryClient.invalidateQueries({ queryKey: ["agent-templates", token] })} />
          <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
            {t("agentTemplates.newTemplate")}
          </Button>
        </Space>
      </div>

      {templatesQuery.isError ? (
        <Alert type="error" showIcon message={t("agentTemplates.loadFailed")} style={{ marginBottom: 16 }} />
      ) : null}
      {templatesQuery.isLoading ? null : groups.user.length + groups.builtin.length + groups.package.length === 0 ? (
        <Empty description={t("agentTemplates.empty")} />
      ) : (
        <>
          {renderGroup(t("agentTemplates.sourceUser"), groups.user)}
          {renderGroup(t("agentTemplates.sourceBuiltin"), groups.builtin)}
          {renderGroup(t("agentTemplates.sourcePackage"), groups.package)}
        </>
      )}

      <Drawer
        title={
          editing
            ? editing.source !== "user"
              ? t("agentTemplates.viewTitle")
              : t("agentTemplates.formEditTitle")
            : t("agentTemplates.formCreateTitle")
        }
        open={editorOpen}
        onClose={() => setEditorOpen(false)}
        width={520}
        zIndex={1300}
        extra={
          editing && editing.source !== "user" ? null : (
            <Button
              type="primary"
              icon={<SaveOutlined />}
              loading={saveMutation.isPending}
              onClick={() => form.submit()}
            >
              {t("agentTemplates.save")}
            </Button>
          )
        }
      >
        <Form<TemplateFormValues>
          form={form}
          layout="vertical"
          disabled={!!editing && editing.source !== "user"}
          onFinish={(values) => saveMutation.mutate(values)}
        >
          <Form.Item name="id" label={t("agentTemplates.fieldName")} rules={[{ required: true }]} normalize={(v) => (v ?? "").toLowerCase()}>
            <Input placeholder={t("agentTemplates.fieldNamePlaceholder")} disabled={!!editing} />
          </Form.Item>
          <Form.Item name="name" label={t("agentTemplates.fieldLabel")} rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="description" label={t("agentTemplates.fieldDescription")}>
            <Input.TextArea rows={2} />
          </Form.Item>
          <Space size={12} style={{ display: "flex" }}>
            <Form.Item name="avatar" label={t("agentTemplates.fieldAvatar")} style={{ flex: 1 }}>
              <Space.Compact style={{ width: "100%" }}>
                <Input placeholder="🤖" />
                <Popover
                  trigger="click"
                  placement="bottomLeft"
                  content={
                    <Space wrap size={4} style={{ maxWidth: 280 }}>
                      {COMMON_AVATAR_EMOJIS.map((emoji) => (
                        <Button
                          key={emoji}
                          size="small"
                          type="text"
                          style={{ fontSize: 18, lineHeight: 1 }}
                          onClick={() => form.setFieldValue("avatar", emoji)}
                        >
                          {emoji}
                        </Button>
                      ))}
                    </Space>
                  }
                >
                  <Button icon={<SmileOutlined />} aria-label={t("agentTemplates.pickEmoji")} />
                </Popover>
              </Space.Compact>
            </Form.Item>
            <Form.Item name="color" label={t("agentTemplates.fieldColor")} style={{ flex: 1 }}>
              <Input placeholder="#5b8def" />
            </Form.Item>
          </Space>
          <Form.Item name="scenarios" label={t("agentTemplates.fieldScenarios")}>
            <Select
              mode="tags"
              tokenSeparators={[","]}
              allowClear
              placeholder="assistant, coding"
              options={mergeOptions([], scenarioHistory)}
            />
          </Form.Item>
          <Form.Item name="bundles" label={t("agentTemplates.fieldBundles")}>
            <Select
              mode="tags"
              tokenSeparators={[","]}
              allowClear
              loading={optionsQuery.isLoading}
              placeholder="core_code, lsp, web"
              options={mergeOptions(bundleOptions, bundleHistory)}
            />
          </Form.Item>
          <Form.Item name="tools" label={t("agentTemplates.fieldTools")}>
            <Select
              mode="tags"
              tokenSeparators={[","]}
              allowClear
              showSearch
              loading={optionsQuery.isLoading}
              options={mergeOptions(toolOptions, toolHistory)}
            />
          </Form.Item>
          <Form.Item name="skills" label={t("agentTemplates.fieldSkills")}>
            <Select
              mode="tags"
              tokenSeparators={[","]}
              allowClear
              showSearch
              loading={skillsQuery.isLoading}
              options={mergeOptions(skillOptions, skillHistory)}
            />
          </Form.Item>
          <Form.Item name="mcp_servers" label={t("agentTemplates.fieldMCPServers")}>
            <Select
              mode="tags"
              tokenSeparators={[","]}
              allowClear
              showSearch
              loading={mcpQuery.isLoading}
              options={mergeOptions(mcpOptions, mcpHistory)}
            />
          </Form.Item>
          <Form.Item name="packages" label={t("agentTemplates.fieldPackages")}>
            <Select
              mode="tags"
              tokenSeparators={[","]}
              allowClear
              showSearch
              loading={packagesQuery.isLoading}
              options={mergeOptions(packageOptions, packageHistory)}
            />
          </Form.Item>
          <Form.Item name="persona" label={t("agentTemplates.fieldPersona")}>
            <Input.TextArea rows={4} />
          </Form.Item>
          <Form.Item name="base_prompt" label={t("agentTemplates.fieldBasePrompt")}>
            <Input.TextArea rows={3} />
          </Form.Item>
          <Space size={12} style={{ display: "flex", flexWrap: "wrap", rowGap: 0 }}>
            <Form.Item name="profile" label={t("agentTemplates.fieldProfile")}>
              <Select
                allowClear
                style={{ width: 160 }}
                options={[
                  { value: "general", label: t("agentTemplates.profileGeneral") },
                  { value: "coding", label: t("agentTemplates.profileCoding") },
                ]}
              />
            </Form.Item>
            <Form.Item name="write_enabled" label={t("agentTemplates.fieldWriteEnabled")} valuePropName="checked">
              <Switch />
            </Form.Item>
            <Form.Item name="trim_heavy_sections" label={t("agentTemplates.fieldTrimHeavy")} valuePropName="checked">
              <Switch />
            </Form.Item>
          </Space>
          <Space size={12} style={{ display: "flex", flexWrap: "wrap", rowGap: 0 }}>
            <Form.Item name="memory" label={t("agentTemplates.fieldMemory")}>
              <Select
                allowClear
                placeholder={t("agentTemplates.memoryShared")}
                style={{ width: 160 }}
                options={[
                  { value: "none", label: t("agentTemplates.memoryNone") },
                  { value: "shared", label: t("agentTemplates.memoryShared") },
                  { value: "scoped", label: t("agentTemplates.memoryScoped") },
                ]}
              />
            </Form.Item>
            <Form.Item name="engine" label={t("agentTemplates.fieldEngine")}>
              <Select
                allowClear
                placeholder={t("agentTemplates.engineGodex")}
                style={{ width: 200 }}
                options={(optionsQuery.data?.engines ?? []).map((e) => ({
                  value: e,
                  label: e === "godex" ? t("agentTemplates.engineGodex") : e,
                }))}
                listHeight={280}
              />
            </Form.Item>
            <Form.Item noStyle shouldUpdate={(prev, cur) => prev.engine !== cur.engine}>
              {({ getFieldValue }) => {
                const engine = getFieldValue("engine") as string | undefined;
                return engine && engine !== "godex" ? (
                  <Typography.Text type="warning" style={{ display: "block", lineHeight: "40px" }}>
                    {t("agentTemplates.engineExternalHint")}
                  </Typography.Text>
                ) : null;
              }}
            </Form.Item>
          </Space>
        </Form>
      </Drawer>
    </div>
  );
}
