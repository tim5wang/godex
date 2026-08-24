import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Drawer,
  Empty,
  Form,
  Input,
  InputNumber,
  List,
  Popconfirm,
  Select,
  Space,
  Switch,
  Tabs,
  Tag,
  Typography,
} from "antd";
import {
  ApiOutlined,
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
  EyeOutlined,
  PlusOutlined,
  ReloadOutlined,
  SaveOutlined,
} from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { showError } from "../../lib/notifications";
import { writeClipboardText } from "../../lib/clipboard";
import {
  createBizKey,
  deleteBizKey,
  listBizKeys,
  listPackages,
  listSkillsCatalog,
  updateBizKey,
} from "../../lib/api";
import type { BizKey, ProviderRef } from "../../lib/types";
import { useSettingsStore } from "../../store/settings";
import { GodexStepElement } from "../../lib/agent-step/godex-step";

const { Title, Text, Paragraph } = Typography;

interface BizFormValues {
  name: string;
  description?: string;
  default_prompt?: string;
  mcp_servers?: string[];
  sandbox_tools?: string[];
  skills?: string[];
  packages?: string[];
  models?: string[];
  project_dir?: string;
  budget_credits?: number;
  warning_threshold?: number;
}

const SANDBOX_TOOL_OPTIONS = [
  "read_file",
  "write_file",
  "edit_file",
  "bash",
  "grep",
  "glob",
  "find",
  "todo_write",
  "memory",
  "web_search",
  "web_fetch",
  "skill",
];

function firstSecret(raw?: BizKey | null): string {
  return raw?.key_prefix ?? "";
}

export function BusinessAgentsPage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { message: antMessage } = AntApp.useApp();
  const token = useSettingsStore((state) => state.token);
  const canReach = !!token;

  // ---- List ----
  const keysQuery = useQuery({
    queryKey: ["biz-keys", token],
    enabled: canReach,
    queryFn: () => listBizKeys(token),
  });
  const keys = useMemo(() => keysQuery.data ?? [], [keysQuery.data]);

  const skillsQuery = useQuery({
    queryKey: ["skills-catalog", token],
    enabled: canReach,
    queryFn: () => listSkillsCatalog(token),
  });
  const skillNames = useMemo(
    () => (skillsQuery.data ?? []).map((s) => ({ label: s.name, value: s.name })),
    [skillsQuery.data],
  );

  const packagesQuery = useQuery({
    queryKey: ["packages", token],
    enabled: canReach,
    queryFn: () => listPackages(token),
  });
  const packageNames = useMemo(
    () => (packagesQuery.data ?? []).map((p) => ({ label: p.name, value: p.name })),
    [packagesQuery.data],
  );

  // ---- Selection / modal state ----
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const selected = useMemo(() => keys.find((k) => k.id === selectedId) ?? null, [keys, selectedId]);
  const [editorOpen, setEditorOpen] = useState(false);
  const [editing, setEditing] = useState<BizKey | null>(null);
  const [secret, setSecret] = useState<string>("");
  const [previewOpen, setPreviewOpen] = useState(false);

  const [form] = Form.useForm<BizFormValues>();

  const invalidate = () => {
    void queryClient.invalidateQueries({ queryKey: ["biz-keys", token] });
  };

  const saveMutation = useMutation({
    mutationFn: async (values: BizFormValues) => {
      const body = {
        name: values.name,
        description: values.description ?? "",
        default_prompt: values.default_prompt ?? "",
        mcp_servers: values.mcp_servers ?? [],
        providers: [] as ProviderRef[],
        sandbox_tools: values.sandbox_tools ?? [],
        skills: values.skills ?? [],
        packages: values.packages ?? [],
        models: values.models ?? [],
        project_dir: values.project_dir ?? "",
        budget_credits: values.budget_credits ?? 0,
        warning_threshold: values.warning_threshold ?? 0,
      };
      if (editing) {
        return updateBizKey(token, editing.id, body);
      }
      return createBizKey(token, body);
    },
    onSuccess: (res) => {
      invalidate();
      setEditorOpen(false);
      setEditing(null);
      const created = res && "secret" in res ? (res as { secret: string; key: BizKey }).secret : "";
      if (created) {
        setSecret(created);
        setSelectedId((res as { key: BizKey }).key.id);
      } else {
        antMessage.success(t("businessAgents.saved"));
      }
    },
    onError: (error: Error) => showError(antMessage, error, t("businessAgents.saveFailed")),
  });

  const deleteMutation = useMutation({
    mutationFn: (id: string) => deleteBizKey(token, id),
    onSuccess: () => {
      invalidate();
      if (selectedId) {
        setSelectedId(null);
      }
      antMessage.success(t("businessAgents.deleted"));
    },
    onError: (error: Error) => showError(antMessage, error, t("businessAgents.deleteFailed")),
  });

  const handleEdit = (key: BizKey) => {
    setEditing(key);
    setSecret("");
    form.setFieldsValue({
      name: key.name,
      description: key.description ?? "",
      default_prompt: key.default_prompt ?? "",
      mcp_servers: key.mcp_servers ?? [],
      sandbox_tools: key.sandbox_tools ?? [],
      skills: key.skills ?? [],
      packages: key.packages ?? [],
      models: key.models ?? [],
      project_dir: key.project_dir ?? "",
      budget_credits: key.budget_credits,
      warning_threshold: key.warning_threshold,
    });
    setEditorOpen(true);
  };

  const handleNew = () => {
    setEditing(null);
    setSecret("");
    form.resetFields();
    setEditorOpen(true);
  };

  // ---- 接入指南代码片段 ----
  const baseUrl = window.location.origin;
  const snippets = selected
    ? {
        curl: `curl -X POST ${baseUrl}/v1/agent-steps \\
  -H "Authorization: Bearer ${firstSecret(selected)}****" \\
  -H "Content-Type: application/json" \\
  -d '{"prompt":"你的业务指令","inputs":{"order_id":"ORD-1"},"context":{"recall":["godex://memory"]}}'`,
        sdk: `import { createStepClient } from "godex/agent-step";

const step = createStepClient({
  baseUrl: "${baseUrl}",
  apiKey: "${firstSecret(selected)}****",
});
const result = await step.createStep({
  prompt: "你的业务指令",
  inputs: { order_id: "ORD-1" },
});`,
        embed: `<script src="${baseUrl}/embed/godex-step.js"></script>
<godex-step
  base-url="${baseUrl}"
  api-key="${firstSecret(selected)}****"
  prompt="分析订单 ORD-1234"
></godex-step>`,
      }
    : null;

  const copy = (text: string) => {
    void writeClipboardText(text).then(() => antMessage.success(t("businessAgents.copied")));
  };

  return (
    <div style={{ display: "flex", gap: 16, padding: 16 }}>
      {/* ---- 左：列表 ---- */}
      <Card
        style={{ width: 320, flexShrink: 0 }}
        title={<Text strong>{t("businessAgents.listTitle")}</Text>}
        extra={
          <Button type="primary" size="small" icon={<PlusOutlined />} onClick={handleNew}>
            {t("businessAgents.new")}
          </Button>
        }
      >
        {keysQuery.isLoading ? (
          <Text type="secondary">{t("businessAgents.loading")}</Text>
        ) : keys.length === 0 ? (
          <Empty description={t("businessAgents.noKeys")} />
        ) : (
          <List
            dataSource={keys}
            renderItem={(item) => (
              <List.Item
                onClick={() => setSelectedId(item.id)}
                style={{ cursor: "pointer", padding: "8px 4px", background: item.id === selectedId ? "#e6f4ff" : undefined }}
                actions={[
                  <Button key="edit" type="text" size="small" icon={<EditOutlined />} onClick={() => handleEdit(item)} />,
                  <Popconfirm key="del" title={t("businessAgents.deleteConfirm")} onConfirm={() => deleteMutation.mutate(item.id)}>
                    <Button type="text" size="small" danger icon={<DeleteOutlined />} />
                  </Popconfirm>,
                ]}
              >
                <List.Item.Meta
                  title={
                    <Space>
                      {item.name}
                      {!item.enabled ? <Tag color="default">{t("businessAgents.disabled")}</Tag> : null}
                    </Space>
                  }
                  description={
                    <Text type="secondary" style={{ fontSize: 12 }}>
                      {item.description || firstSecret(item)}
                    </Text>
                  }
                />
              </List.Item>
            )}
          />
        )}
      </Card>

      {/* ---- 右：详情/编辑 ---- */}
      <div style={{ flex: 1, minWidth: 0 }}>
        {!selected ? (
          <Card>
            <Empty description={t("businessAgents.selectHint")} />
          </Card>
        ) : (
          <Tabs
            items={[
              {
                key: "overview",
                label: t("businessAgents.tabOverview"),
                children: <OverviewTab key={selected.id} biz={selected} onEdit={() => handleEdit(selected)} />,
              },
              {
                key: "capabilities",
                label: t("businessAgents.tabCapabilities"),
                children: <CapabilitiesTab key={selected.id} biz={selected} />,
              },
              {
                key: "access",
                label: t("businessAgents.tabAccess"),
                children: snippets ? <AccessTab snippets={snippets} /> : <Empty />,
              },
              {
                key: "preview",
                label: t("businessAgents.tabPreview"),
                children: <PreviewTab key={selected.id} biz={selected} onOpen={() => setPreviewOpen(true)} />,
              },
            ]}
          />
        )}
      </div>

      {/* ---- 编辑器 ---- */}
      <Drawer
        title={editing ? t("businessAgents.edit") : t("businessAgents.new")}
        width={520}
        open={editorOpen}
        onClose={() => setEditorOpen(false)}
        destroyOnClose
        extra={
          <Button type="primary" icon={<SaveOutlined />} loading={saveMutation.isPending} onClick={() => form.submit()}>
            {t("businessAgents.save")}
          </Button>
        }
      >
        <Form form={form} layout="vertical" onFinish={(v) => saveMutation.mutate(v)}>
          <Form.Item name="name" label={t("businessAgents.name")} rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="description" label={t("businessAgents.description")}>
            <Input.TextArea rows={2} />
          </Form.Item>
          <Form.Item name="default_prompt" label={t("businessAgents.defaultPrompt")}>
            <Input.TextArea rows={3} placeholder={t("businessAgents.defaultPromptPlaceholder")} />
          </Form.Item>
          <Form.Item name="mcp_servers" label={t("businessAgents.mcpServers")}>
            <Select mode="tags" placeholder={t("businessAgents.mcpPlaceholder")} />
          </Form.Item>
          <Form.Item name="sandbox_tools" label={t("businessAgents.sandboxTools")}>
            <Select mode="multiple" options={SANDBOX_TOOL_OPTIONS.map((o) => ({ label: o, value: o }))} />
          </Form.Item>
          <Form.Item name="skills" label={t("businessAgents.skills")}>
            <Select mode="multiple" options={skillNames} allowClear />
          </Form.Item>
          <Form.Item name="packages" label={t("businessAgents.packages")}>
            <Select mode="multiple" options={packageNames} allowClear />
          </Form.Item>
          <Form.Item name="models" label={t("businessAgents.models")}>
            <Select mode="tags" placeholder={t("businessAgents.modelsPlaceholder")} />
          </Form.Item>
          <Form.Item name="project_dir" label={t("businessAgents.projectDir")}>
            <Input placeholder="~/work/sales-crm" />
          </Form.Item>
          <Space size="large">
            <Form.Item name="budget_credits" label={t("businessAgents.budgetCredits")}>
              <InputNumber min={0} step={1} />
            </Form.Item>
            <Form.Item name="warning_threshold" label={t("businessAgents.warningThreshold")}>
              <InputNumber min={0} step={1} />
            </Form.Item>
          </Space>
        </Form>
        {secret ? (
          <Alert
            type="success"
            showIcon
            message={t("businessAgents.secretOnce")}
            description={
              <Space direction="vertical" style={{ width: "100%" }}>
                <Text code copyable={{ text: secret }}>
                  {secret}
                </Text>
                <Text type="secondary" style={{ fontSize: 12 }}>
                  {t("businessAgents.secretOnceHint")}
                </Text>
              </Space>
            }
          />
        ) : null}
      </Drawer>
    </div>
  );
}

// ---- 概览 Tab ----
function OverviewTab({ biz, onEdit }: { biz: BizKey; onEdit: () => void }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { message: antMessage } = AntApp.useApp();
  const token = useSettingsStore((state) => state.token);

  const toggleMutation = useMutation({
    mutationFn: (enabled: boolean) => updateBizKey(token, biz.id, { enabled }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["biz-keys", token] });
      antMessage.success(t("businessAgents.saved"));
    },
    onError: (error: Error) => showError(antMessage, error, t("businessAgents.saveFailed")),
  });

  return (
    <Card
      title={biz.name}
      extra={
        <Space>
          <Switch
            checked={biz.enabled}
            checkedChildren={t("businessAgents.enabled")}
            unCheckedChildren={t("businessAgents.disabled")}
            loading={toggleMutation.isPending}
            onChange={(v) => toggleMutation.mutate(v)}
          />
          <Button icon={<EditOutlined />} onClick={onEdit}>
            {t("businessAgents.edit")}
          </Button>
        </Space>
      }
    >
      <Paragraph type="secondary">{biz.description || t("businessAgents.noDescription")}</Paragraph>
      <Paragraph>
        <Text strong>{t("businessAgents.keyPrefix")}: </Text>
        <Text code>{biz.key_prefix}</Text>
      </Paragraph>
      <Paragraph>
        <Text strong>{t("businessAgents.budgetCredits")}: </Text>
        <Text>{biz.budget_credits}</Text>
        <Text type="secondary" style={{ marginLeft: 12 }}>
          {t("businessAgents.warningThreshold")}: {biz.warning_threshold}
        </Text>
      </Paragraph>
      {biz.default_prompt ? (
        <Paragraph>
          <Text strong>{t("businessAgents.defaultPrompt")}:</Text>
          <pre style={{ background: "#f6f8fa", padding: 8, borderRadius: 6, whiteSpace: "pre-wrap" }}>{biz.default_prompt}</pre>
        </Paragraph>
      ) : null}
      {biz.project_dir ? (
        <Paragraph>
          <Text strong>{t("businessAgents.projectDir")}: </Text>
          <Text code>{biz.project_dir}</Text>
        </Paragraph>
      ) : null}
    </Card>
  );
}

// ---- 能力 Tab（只读回显 + 编辑入口）----
function CapabilitiesTab({ biz }: { biz: BizKey }) {
  const { t } = useI18n();
  const section = (label: string, items: string[] | undefined) =>
    items && items.length > 0 ? (
      <Paragraph>
        <Text strong>{label}: </Text>
        {items.map((i) => (
          <Tag key={i}>{i}</Tag>
        ))}
      </Paragraph>
    ) : null;

  return (
    <Card>
      {section(t("businessAgents.mcpServers"), biz.mcp_servers)}
      {section(t("businessAgents.sandboxTools"), biz.sandbox_tools)}
      {section(t("businessAgents.skills"), biz.skills)}
      {section(t("businessAgents.packages"), biz.packages)}
      {section(t("businessAgents.models"), biz.models)}
      {(!biz.mcp_servers?.length && !biz.sandbox_tools?.length && !biz.skills?.length && !biz.packages?.length) ? (
        <Empty description={t("businessAgents.noCapabilities")} />
      ) : null}
    </Card>
  );
}

// ---- 接入指南 Tab ----
function AccessTab({ snippets }: { snippets: Record<string, string> }) {
  const { t } = useI18n();
  const { message: antMessage } = AntApp.useApp();

  const copy = (text: string) => {
    void writeClipboardText(text).then(() => antMessage.success(t("businessAgents.copied")));
  };

  return (
    <Card>
      <Space direction="vertical" style={{ width: "100%" }} size="middle">
        {(["curl", "sdk", "embed"] as const).map((kind) => (
          <div key={kind}>
            <Space style={{ marginBottom: 4 }}>
              <Text strong>{t(`businessAgents.snippet.${kind}`)}</Text>
              <Button size="small" icon={<CopyOutlined />} onClick={() => copy(snippets[kind])}>
                {t("businessAgents.copy")}
              </Button>
            </Space>
            <pre
              style={{
                background: "#f6f8fa",
                padding: 10,
                borderRadius: 6,
                overflow: "auto",
                fontSize: 12,
                lineHeight: 1.5,
                margin: 0,
              }}
            >
              {snippets[kind]}
            </pre>
          </div>
        ))}
      </Space>
    </Card>
  );
}

// ---- 嵌入预览 Tab ----
function PreviewTab({ biz, onOpen }: { biz: BizKey; onOpen: () => void }) {
  const { t } = useI18n();
  // key_prefix is masked (e.g. biz_ab12****), so the live preview needs the
  // full secret pasted here — the prefix is just a placeholder.
  const [apiKey, setApiKey] = useState(biz.key_prefix);
  return (
    <Card
      extra={
        <Button icon={<EyeOutlined />} onClick={onOpen}>
          {t("businessAgents.previewFull")}
        </Button>
      }
    >
      <Alert type="info" showIcon style={{ marginBottom: 12 }} message={t("businessAgents.previewHint")} />
      <Space direction="vertical" style={{ width: "100%" }} size="small">
        <Input
          addonBefore={t("businessAgents.keyPrefix")}
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          placeholder={t("businessAgents.previewKeyPlaceholder")}
          allowClear
        />
        <godex-step base-url={window.location.origin} api-key={apiKey} prompt="" />
      </Space>
    </Card>
  );
}
