import { useEffect, useMemo, useState } from "react";
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
  Modal,
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
  getModels,
  listBizKeys,
  listMCPServers,
  listPackages,
  listSkillsCatalog,
  resetBizKey,
  revealBizKey,
  updateBizKey,
} from "../../lib/api";
import type { BizKey, ModelsView, ProviderRef } from "../../lib/types";
import { useSettingsStore } from "../../store/settings";
// Side-effect import: registers the <godex-step> custom element via
// customElements.define. The class is never referenced directly, so a named
// import would be tree-shaken away and the element would stay unregistered
// (blank embed preview).
import "../../lib/agent-step/godex-step";

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
  pin?: string;
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

  const mcpServersQuery = useQuery({
    queryKey: ["mcp-servers", token],
    enabled: canReach,
    queryFn: () => listMCPServers(token),
  });
  const mcpServerOptions = useMemo(
    () => (mcpServersQuery.data?.servers ?? []).map((s) => ({ label: s.name, value: s.name })),
    [mcpServersQuery.data],
  );

  const modelsQuery = useQuery<ModelsView>({
    queryKey: ["models", token],
    enabled: canReach,
    queryFn: () => getModels(token),
  });
  // Profile IDs are what the key stores; labels show provider/model for clarity.
  const modelOptions = useMemo(
    () => (modelsQuery.data?.profiles ?? []).map((p) => ({
      label: `${p.name || p.id} · ${p.provider}/${p.model}`,
      value: p.id,
    })),
    [modelsQuery.data],
  );

  // ---- Selection / modal state ----
  const [selectedId, setSelectedId] = useState<string | null>(null);
  const selected = useMemo(() => keys.find((k) => k.id === selectedId) ?? null, [keys, selectedId]);
  const [editorOpen, setEditorOpen] = useState(false);
  const [editing, setEditing] = useState<BizKey | null>(null);
  const [secret, setSecret] = useState<string>("");
  // Pin-verified plaintext secrets, kept in memory only (page session).
  // Keyed by biz id so switching tabs keeps the unlock; refreshing re-prompts.
  const [revealed, setRevealed] = useState<Record<string, string>>({});
  const revealedKey = (biz: BizKey | null) => (biz ? (revealed[biz.id] ?? biz.key_prefix) : "");

  const handleRevealed = (id: string, plain: string) => {
    setRevealed((prev) => ({ ...prev, [id]: plain }));
  };

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
        ...(values.pin ? { pin: values.pin } : {}),
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
  -H "Authorization: Bearer ${revealedKey(selected)}" \\
  -H "Content-Type: application/json" \\
  -d '{"prompt":"你的业务指令","inputs":{"order_id":"ORD-1"},"context":{"recall":["godex://memory"]}}'`,
        sdk: `import { createStepClient } from "godex/agent-step";

const step = createStepClient({
  baseUrl: "${baseUrl}",
  apiKey: "${revealedKey(selected)}",
});
const result = await step.createStep({
  prompt: "你的业务指令",
  inputs: { order_id: "ORD-1" },
});`,
        embed: `<script src="${baseUrl}/embed/godex-step.js"></script>
<godex-step
  base-url="${baseUrl}"
  api-key="${revealedKey(selected)}"
  prompt="分析订单 ORD-1234"
></godex-step>`,
      }
    : null;

  const copy = (text: string) => {
    void writeClipboardText(text).then(() => antMessage.success(t("businessAgents.copied")));
  };

  return (
    <div className="business-agents-layout">
      {/* ---- 左：列表 ---- */}
      <Card
        className="business-agents-list"
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
                children: <OverviewTab key={selected.id} biz={selected} onEdit={() => handleEdit(selected)} revealed={revealed} onRevealed={handleRevealed} />,
              },
              {
                key: "capabilities",
                label: t("businessAgents.tabCapabilities"),
                children: <CapabilitiesTab key={selected.id} biz={selected} />,
              },
              {
                key: "access",
                label: t("businessAgents.tabAccess"),
                children: snippets ? (
                  <AccessTab snippets={snippets} biz={selected} revealed={revealed} onRevealed={handleRevealed} />
                ) : (
                  <Empty />
                ),
              },
              {
                key: "preview",
                label: t("businessAgents.tabPreview"),
                children: <PreviewTab key={selected.id} biz={selected} revealed={revealed} onRevealed={handleRevealed} />,
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
          <Space>
            <Button onClick={() => setEditorOpen(false)}>{t("businessAgents.cancel")}</Button>
            <Button type="primary" icon={<SaveOutlined />} loading={saveMutation.isPending} onClick={() => form.submit()}>
              {t("businessAgents.save")}
            </Button>
          </Space>
        }
        footer={
          <Space style={{ width: "100%", justifyContent: "flex-end" }}>
            <Button onClick={() => setEditorOpen(false)}>{t("businessAgents.cancel")}</Button>
            <Button type="primary" icon={<SaveOutlined />} loading={saveMutation.isPending} onClick={() => form.submit()}>
              {t("businessAgents.save")}
            </Button>
          </Space>
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
          <Form.Item name="mcp_servers" label={t("businessAgents.mcpServers")} extra={t("businessAgents.mcpExtra")}>
            <Select mode="multiple" options={mcpServerOptions} placeholder={t("businessAgents.mcpPlaceholder")} allowClear showSearch optionFilterProp="label" loading={mcpServersQuery.isLoading} />
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
          <Form.Item name="models" label={t("businessAgents.models")} extra={t("businessAgents.modelsExtra")}>
            <Select
              mode="multiple"
              options={modelOptions}
              placeholder={t("businessAgents.modelsPlaceholder")}
              loading={modelsQuery.isLoading}
              allowClear
              filterOption={(input, option) => String(option?.label ?? "").toLowerCase().includes(input.toLowerCase())}
            />
          </Form.Item>
          <Form.Item name="project_dir" label={t("businessAgents.projectDir")}>
            <Input placeholder="~/work/sales-crm" />
          </Form.Item>
          <Form.Item
            name="pin"
            label={t("businessAgents.pinLabel")}
            rules={
              editing
                ? [{ pattern: /^\d{6}$/, message: t("businessAgents.pinRule") }]
                : [
                    { required: true, message: t("businessAgents.pinRule") },
                    { pattern: /^\d{6}$/, message: t("businessAgents.pinRule") },
                  ]
            }
            extra={t("businessAgents.pinExtra")}
          >
            <Input.Password maxLength={6} placeholder="123456" />
          </Form.Item>
          <Space size="large" align="start">
            <Form.Item name="budget_credits" label={t("businessAgents.budgetCredits")} extra={t("businessAgents.budgetExtra")}>
              <InputNumber min={0} step={1} placeholder="0" addonAfter={t("businessAgents.creditsUnit")} />
            </Form.Item>
            <Form.Item name="warning_threshold" label={t("businessAgents.warningThreshold")} extra={t("businessAgents.warningExtra")}>
              <InputNumber min={0} step={1} placeholder="0" addonAfter={t("businessAgents.creditsUnit")} />
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

// ---- Pin 解锁组件 ----
function PinUnlock({ biz, revealed, onRevealed }: { biz: BizKey; revealed: Record<string, string>; onRevealed: (id: string, plain: string) => void }) {
  const { t } = useI18n();
  const { message: antMessage } = AntApp.useApp();
  const token = useSettingsStore((state) => state.token);
  const [open, setOpen] = useState(false);
  const [pin, setPin] = useState("");
  const plain = revealed[biz.id];

  const revealMutation = useMutation({
    mutationFn: () => revealBizKey(token, biz.id, pin),
    onSuccess: (res) => {
      onRevealed(biz.id, res.secret);
      setOpen(false);
      setPin("");
      antMessage.success(t("businessAgents.revealDone"));
    },
    onError: (error: Error) => showError(antMessage, error, t("businessAgents.revealFailed")),
  });

  // Already unlocked this page session → show the full key, copyable.
  if (plain) {
    return (
      <Space direction="vertical" size={4} style={{ width: "100%" }}>
        <Text strong>{t("businessAgents.keyPrefix")}: </Text>
        <Text code copyable={{ text: plain }}>
          {plain}
        </Text>
      </Space>
    );
  }
  return (
    <>
      <Button size="small" icon={<EyeOutlined />} onClick={() => setOpen(true)}>
        {t("businessAgents.revealKey")}
      </Button>
      <Modal
        title={t("businessAgents.pinPrompt")}
        open={open}
        onCancel={() => setOpen(false)}
        footer={
          <Space>
            <Button onClick={() => setOpen(false)}>{t("businessAgents.cancel")}</Button>
            <Button
              type="primary"
              loading={revealMutation.isPending}
              disabled={!/^\d{6}$/.test(pin)}
              onClick={() => revealMutation.mutate()}
            >
              {t("businessAgents.unlock")}
            </Button>
          </Space>
        }
        destroyOnClose
      >
        <Input.Password maxLength={6} value={pin} onChange={(e) => setPin(e.target.value)} placeholder="123456" />
      </Modal>
    </>
  );
}

// ---- 概览 Tab ----
function OverviewTab({ biz, onEdit, revealed, onRevealed }: { biz: BizKey; onEdit: () => void; revealed: Record<string, string>; onRevealed: (id: string, plain: string) => void }) {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { message: antMessage } = AntApp.useApp();
  const token = useSettingsStore((state) => state.token);
  const [resetSecret, setResetSecret] = useState("");

  const toggleMutation = useMutation({
    mutationFn: (enabled: boolean) => updateBizKey(token, biz.id, { enabled }),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: ["biz-keys", token] });
      antMessage.success(t("businessAgents.saved"));
    },
    onError: (error: Error) => showError(antMessage, error, t("businessAgents.saveFailed")),
  });

  const resetMutation = useMutation({
    mutationFn: () => resetBizKey(token, biz.id),
    onSuccess: (res) => {
      void queryClient.invalidateQueries({ queryKey: ["biz-keys", token] });
      setResetSecret(res.secret);
      antMessage.success(t("businessAgents.secretResetDone"));
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
        <PinUnlock biz={biz} revealed={revealed} onRevealed={onRevealed} />
      </Paragraph>
      <Paragraph>
        <Button
          size="small"
          icon={<ReloadOutlined />}
          loading={resetMutation.isPending}
          onClick={() => resetMutation.mutate()}
        >
          {t("businessAgents.resetKey")}
        </Button>
      </Paragraph>
      {resetSecret ? (
        <Alert
          type="success"
          showIcon
          style={{ marginBottom: 12 }}
          message={t("businessAgents.secretOnce")}
          description={
            <Space direction="vertical" style={{ width: "100%" }}>
              <Text code copyable={{ text: resetSecret }}>
                {resetSecret}
              </Text>
              <Text type="secondary" style={{ fontSize: 12 }}>
                {t("businessAgents.secretOnceHint")}
              </Text>
            </Space>
          }
        />
      ) : null}
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
function AccessTab({ snippets, biz, revealed, onRevealed }: { snippets: Record<string, string>; biz: BizKey; revealed: Record<string, string>; onRevealed: (id: string, plain: string) => void }) {
  const { t } = useI18n();
  const { message: antMessage } = AntApp.useApp();

  const copy = (text: string) => {
    void writeClipboardText(text).then(() => antMessage.success(t("businessAgents.copied")));
  };

  return (
    <Card>
      <Paragraph>
        <PinUnlock biz={biz} revealed={revealed} onRevealed={onRevealed} />
      </Paragraph>
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
function PreviewTab({ biz, revealed, onRevealed }: { biz: BizKey; revealed: Record<string, string>; onRevealed: (id: string, plain: string) => void }) {
  const { t } = useI18n();
  // key_prefix is masked (e.g. biz_ab12****); once unlocked via pin the full
  // secret is used for live runs.
  const [apiKey, setApiKey] = useState(revealed[biz.id] ?? biz.key_prefix);
  const [fullOpen, setFullOpen] = useState(false);
  // Unlocking here (or in another tab) fills the live-run key automatically.
  useEffect(() => {
    if (revealed[biz.id]) {
      setApiKey(revealed[biz.id]);
    }
  }, [revealed, biz.id]);
  const renderStep = () => <godex-step base-url={window.location.origin} api-key={apiKey} prompt="" />;
  return (
    <Card
      extra={
        <Button icon={<EyeOutlined />} onClick={() => setFullOpen(true)}>
          {t("businessAgents.previewFull")}
        </Button>
      }
    >
      <Alert type="info" showIcon style={{ marginBottom: 12 }} message={t("businessAgents.previewHint")} />
      <Paragraph style={{ marginBottom: 12 }}>
        <PinUnlock biz={biz} revealed={revealed} onRevealed={onRevealed} />
      </Paragraph>
      <Space direction="vertical" style={{ width: "100%" }} size="small">
        <Input
          addonBefore={t("businessAgents.keyPrefix")}
          value={apiKey}
          onChange={(e) => setApiKey(e.target.value)}
          placeholder={t("businessAgents.previewKeyPlaceholder")}
          allowClear
        />
        {renderStep()}
      </Space>
      <Modal
        title={t("businessAgents.previewFull")}
        open={fullOpen}
        onCancel={() => setFullOpen(false)}
        footer={null}
        width={760}
        destroyOnClose
      >
        {renderStep()}
      </Modal>
    </Card>
  );
}
