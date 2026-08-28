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
  Select,
  Space,
  Switch,
  Tag,
  Typography,
} from "antd";
import { DeleteOutlined, EditOutlined, MessageOutlined, PlusOutlined, ReloadOutlined, SaveOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { showError } from "../../lib/notifications";
import { useSettingsStore } from "../../store/settings";
import {
  createAgentTemplate,
  deleteAgentTemplate,
  listAgentTemplates,
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
  persona?: string;
  base_prompt?: string;
  profile?: string;
  write_enabled?: boolean;
  trim_heavy_sections?: boolean;
};

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
        readonly ? (
          <span key="edit" />
        ) : (
          <Button key="edit" size="small" type="link" icon={<EditOutlined />} onClick={() => props.onEdit(tpl)}>
            {t("agentTemplates.edit")}
          </Button>
        ),
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
        persona: values.persona,
        base_prompt: values.base_prompt,
        profile: values.profile,
        write_enabled: values.write_enabled,
        trim_heavy_sections: values.trim_heavy_sections,
      };
      if (editing) {
        return updateAgentTemplate(token || null, editing.id, payload);
      }
      return createAgentTemplate(token || null, { id: values.id, ...payload });
    },
    onSuccess: () => {
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
      persona: tpl.persona,
      base_prompt: tpl.base_prompt,
      profile: tpl.profile,
      write_enabled: tpl.write_enabled,
      trim_heavy_sections: tpl.trim_heavy_sections,
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
        title={editing ? t("agentTemplates.formEditTitle") : t("agentTemplates.formCreateTitle")}
        open={editorOpen}
        onClose={() => setEditorOpen(false)}
        width={520}
        extra={
          <Button
            type="primary"
            icon={<SaveOutlined />}
            loading={saveMutation.isPending}
            onClick={() => form.submit()}
          >
            {t("agentTemplates.save")}
          </Button>
        }
      >
        <Form<TemplateFormValues>
          form={form}
          layout="vertical"
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
              <Input placeholder="🤖" />
            </Form.Item>
            <Form.Item name="color" label={t("agentTemplates.fieldColor")} style={{ flex: 1 }}>
              <Input placeholder="#5b8def" />
            </Form.Item>
          </Space>
          <Form.Item name="scenarios" label={t("agentTemplates.fieldScenarios")}>
            <Select mode="tags" open={false} tokenSeparators={[","]} />
          </Form.Item>
          <Form.Item name="bundles" label={t("agentTemplates.fieldBundles")}>
            <Select mode="tags" open={false} tokenSeparators={[","]} placeholder="core_code, lsp, web" />
          </Form.Item>
          <Form.Item name="tools" label={t("agentTemplates.fieldTools")}>
            <Select mode="tags" open={false} tokenSeparators={[","]} />
          </Form.Item>
          <Form.Item name="skills" label={t("agentTemplates.fieldSkills")}>
            <Select mode="tags" open={false} tokenSeparators={[","]} />
          </Form.Item>
          <Form.Item name="mcp_servers" label={t("agentTemplates.fieldMCPServers")}>
            <Select mode="tags" open={false} tokenSeparators={[","]} />
          </Form.Item>
          <Form.Item name="persona" label={t("agentTemplates.fieldPersona")}>
            <Input.TextArea rows={4} />
          </Form.Item>
          <Form.Item name="base_prompt" label={t("agentTemplates.fieldBasePrompt")}>
            <Input.TextArea rows={3} />
          </Form.Item>
          <Space size={12} style={{ display: "flex" }}>
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
        </Form>
      </Drawer>
    </div>
  );
}
