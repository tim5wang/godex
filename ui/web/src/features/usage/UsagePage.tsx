import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  App as AntApp,
  AutoComplete,
  Button,
  Card,
  DatePicker,
  Form,
  Input,
  InputNumber,
  Modal,
  Select,
  Space,
  Switch,
  Table,
  Tabs,
  Tag,
  Typography,
} from "antd";
import {
  CopyOutlined,
  DeleteOutlined,
  EditOutlined,
  EyeOutlined,
  PlusOutlined,
  ReloadOutlined,
  SaveOutlined,
} from "@ant-design/icons";
import dayjs from "dayjs";
import { useI18n } from "../../i18n";
import { showError } from "../../lib/notifications";
import { writeClipboardText } from "../../lib/clipboard";
import {
  createUsageKey,
  createUsageModel,
  getModels,
  getUsageSummary,
  listUsageCalls,
  listUsageKeys,
  listUsageModels,
  resetUsageKey,
  updateUsageKey,
  updateUsageModel,
} from "../../lib/api";
import type { ModelsView, UsageCall, UsageKey, UsageModelMapping, UsageSummary } from "../../lib/types";
import { useSettingsStore } from "../../store/settings";
import { OverviewTab } from "./overview/OverviewTab";
import { SessionTab } from "./sessions/SessionTab";
import { CacheStatsTab } from "./cache/CacheStatsTab";

const { Title, Text, Paragraph } = Typography;

function maskKeyPrefix(prefix: string): string {
  if (prefix.length > 8) {
    return prefix.slice(0, 8) + "****";
  }
  return prefix;
}

export function UsagePage() {
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const { message: antMessage } = AntApp.useApp();
  const token = useSettingsStore((state) => state.token);

  // ---- Modal state ----
  const [createKeyOpen, setCreateKeyOpen] = useState(false);
  const [editKeyOpen, setEditKeyOpen] = useState(false);
  const [editingKey, setEditingKey] = useState<UsageKey | null>(null);
  const [createModelOpen, setCreateModelOpen] = useState(false);
  const [editModelOpen, setEditModelOpen] = useState(false);
  const [editingModel, setEditingModel] = useState<UsageModelMapping | null>(null);
  const [resetKeyOpen, setResetKeyOpen] = useState(false);
  const [resetKeyName, setResetKeyName] = useState<string>("");
  const [resetKeySecret, setResetKeySecret] = useState<string>("");
  const [resettingKey, setResettingKey] = useState(false);

  // ---- Form instances ----
  const [createKeyForm] = Form.useForm();
  const [editKeyForm] = Form.useForm();
  const [createModelForm] = Form.useForm();
  const [editModelForm] = Form.useForm();
  const [submitting, setSubmitting] = useState(false);
  const [selectedProfileId, setSelectedProfileId] = useState<string>("");

  // ---- Queries ----
  const keysQuery = useQuery<UsageKey[]>({
    queryKey: ["usage", "keys"],
    queryFn: () => listUsageKeys(token),
  });

  const modelsQuery = useQuery<UsageModelMapping[]>({
    queryKey: ["usage", "models"],
    queryFn: () => listUsageModels(token),
  });

  const [summaryRange, setSummaryRange] = useState<"day" | "week">("day");
  const [summaryKeyId, setSummaryKeyId] = useState<string>("");

  const summaryQuery = useQuery<UsageSummary[]>({
    queryKey: ["usage", "summary", summaryRange, summaryKeyId],
    queryFn: () => getUsageSummary(token, summaryRange, summaryKeyId || undefined),
  });

  const [callsDate, setCallsDate] = useState(() => dayjs().format("YYYY-MM-DD"));
  const [callsKeyId, setCallsKeyId] = useState<string>("");

  // ---- Model profiles (target_profile_id maps to a configured model profile) ----
  const allModelsQuery = useQuery<ModelsView>({
    queryKey: ["models", token],
    queryFn: () => getModels(token),
  });

  const profileByID = useMemo(() => {
    const map = new Map<string, ModelsView["profiles"][number]>();
    for (const profile of allModelsQuery.data?.profiles ?? []) {
      map.set(profile.id, profile);
    }
    return map;
  }, [allModelsQuery.data]);

  const profileSelectOptions = useMemo(
    () => (allModelsQuery.data?.profiles ?? []).map((p) => ({
      value: p.id,
      label: `${p.name || p.id} · ${p.provider}/${p.model}`,
    })),
    [allModelsQuery.data],
  );

  // ---- Model mapping option lists (unique values from existing mappings) ----
  const publicModelOptions = useMemo(
    () => [...new Set((modelsQuery.data ?? []).map((m) => m.public_model))].map((v) => ({ value: v })),
    [modelsQuery.data],
  );
  const allowedModelOptions = useMemo(
    () => [...new Set((modelsQuery.data ?? []).map((m) => m.public_model))].map((v) => ({ value: v })),
    [modelsQuery.data],
  );
  const usageKeyFilterOptions = useMemo(
    () => {
      const keys = keysQuery.data ?? [];
      const proxyKeys = keys
        .filter((k) => !k.id.startsWith("system:"))
        .map((k) => ({ value: k.id, label: k.name }));
      const systemKeys = keys
        .filter((k) => k.id.startsWith("system:"))
        .map((k) => ({ value: k.id, label: k.name }));
      return [
        { label: "Proxy Keys", options: proxyKeys },
        { label: "System Entries", options: systemKeys },
      ].filter((group) => group.options.length > 0);
    },
    [keysQuery.data],
  );
  const proxyKeys = useMemo(() => (keysQuery.data ?? []).filter((k) => !k.id.startsWith("system:")), [keysQuery.data]);

  const callsQuery = useQuery<UsageCall[]>({
    queryKey: ["usage", "calls", callsDate, callsKeyId],
    queryFn: () => listUsageCalls(token, callsDate, callsKeyId || undefined),
  });

  // ---- Handlers ----
  const handleCreateKey = async (values: { name: string; budget_credits: number; warning_threshold: number; allowed_models: string[] }) => {
    setSubmitting(true);
    try {
      const res = await createUsageKey(token, {
        name: values.name,
        budget_credits: values.budget_credits || 0,
        warning_threshold: values.warning_threshold || 0,
        allowed_models: values.allowed_models ?? [],
      });
      antMessage.success(`Key created! Secret: ${res.secret}. Copy it now - it won't be shown again.`);
      queryClient.invalidateQueries({ queryKey: ["usage", "keys"] });
      setCreateKeyOpen(false);
      createKeyForm.resetFields();
    } catch (err) {
      showError(antMessage, err, "Failed to create key");
    } finally {
      setSubmitting(false);
    }
  };

  const handleEditKey = async (values: { name: string; budget_credits: number; warning_threshold: number; allowed_models: string[] }) => {
    if (!editingKey) return;
    setSubmitting(true);
    try {
      await updateUsageKey(token, editingKey.id, {
        name: values.name,
        budget_credits: values.budget_credits,
        warning_threshold: values.warning_threshold,
        allowed_models: values.allowed_models,
      });
      antMessage.success("Key updated");
      queryClient.invalidateQueries({ queryKey: ["usage", "keys"] });
      setEditKeyOpen(false);
      setEditingKey(null);
    } catch (err) {
      showError(antMessage, err, "Failed to update key");
    } finally {
      setSubmitting(false);
    }
  };

  const handleToggleKey = async (id: string, enabled: boolean) => {
    try {
      await updateUsageKey(token, id, { enabled });
      queryClient.invalidateQueries({ queryKey: ["usage", "keys"] });
    } catch (err) {
      showError(antMessage, err, "Failed to update key");
    }
  };

  const handleResetKey = async (record: UsageKey) => {
    setResetKeyName(record.name);
    setResetKeySecret("");
    setResetKeyOpen(true);
    setResettingKey(true);
    try {
      const resp = await resetUsageKey(token, record.id);
      setResetKeySecret(resp.secret);
      antMessage.success("Key reset — copy the new secret now");
      queryClient.invalidateQueries({ queryKey: ["usage", "keys"] });
    } catch (err) {
      showError(antMessage, err, "Failed to reset key");
      setResetKeyOpen(false);
    } finally {
      setResettingKey(false);
    }
  };

  const handleCreateModel = async (values: { public_model: string; target_profile_id: string; target_model: string; credit_weight: number }) => {
    setSubmitting(true);
    try {
      const profile = profileByID.get(values.target_profile_id);
      await createUsageModel(token, {
        ...values,
        target_model: values.target_model || profile?.model || "",
      });
      antMessage.success("Model mapping created");
      queryClient.invalidateQueries({ queryKey: ["usage", "models"] });
      setCreateModelOpen(false);
      createModelForm.resetFields();
    } catch (err) {
      showError(antMessage, err, "Failed to create model");
    } finally {
      setSubmitting(false);
    }
  };

  const handleEditModel = async (values: { public_model: string; target_profile_id: string; target_model: string; credit_weight: number }) => {
    if (!editingModel) return;
    setSubmitting(true);
    try {
      const profile = profileByID.get(values.target_profile_id);
      await updateUsageModel(token, editingModel.id, {
        ...values,
        target_model: values.target_model || profile?.model || "",
      });
      antMessage.success("Model mapping updated");
      queryClient.invalidateQueries({ queryKey: ["usage", "models"] });
      setEditModelOpen(false);
      setEditingModel(null);
    } catch (err) {
      showError(antMessage, err, "Failed to update model");
    } finally {
      setSubmitting(false);
    }
  };

  const handleToggleModel = async (id: string, enabled: boolean) => {
    try {
      await updateUsageModel(token, id, { enabled });
      queryClient.invalidateQueries({ queryKey: ["usage", "models"] });
    } catch (err) {
      showError(antMessage, err, "Failed to update model");
    }
  };

  // ---- Key columns ----
  const keyColumns = [
    { title: "Name", dataIndex: "name", key: "name" },
    {
      title: "Key",
      dataIndex: "key_prefix",
      key: "key_prefix",
      render: (v: string, record: UsageKey) => (
        <Space size={4}>
          <Text code>{maskKeyPrefix(v)}</Text>
          <EyeOutlined
            aria-label={`Reveal new secret for ${record.name}`}
            title="Rotate this key and reveal the new plaintext secret — the masked prefix above is not the full key"
            style={{ cursor: "pointer", color: "#1677ff", fontSize: 13 }}
            onClick={() => handleResetKey(record)}
          />
        </Space>
      ),
    },
    {
      title: "Enabled",
      dataIndex: "enabled",
      key: "enabled",
      render: (_: boolean, record: UsageKey) => (
        <Switch
          checked={record.enabled}
          onChange={(checked) => handleToggleKey(record.id, checked)}
        />
      ),
    },
    { title: "Budget", dataIndex: "budget_credits", key: "budget_credits", render: (v: number) => v.toLocaleString() },
    { title: "Warning", dataIndex: "warning_threshold", key: "warning_threshold", render: (v: number) => v.toLocaleString() },
    {
      title: "Allowed Models",
      dataIndex: "allowed_models",
      key: "allowed_models",
      render: (v: string[]) => v?.map((m) => <Tag key={m}>{m}</Tag>) ?? <Text type="secondary">all</Text>,
    },
    {
      title: "",
      key: "actions",
      width: 88,
      render: (_: unknown, record: UsageKey) => (
        <Space size={0}>
          <Button
            type="text"
            size="small"
            icon={<EditOutlined />}
            aria-label={`Edit key ${record.name}`}
            onClick={() => {
              setEditingKey(record);
              editKeyForm.setFieldsValue({
                name: record.name,
                budget_credits: record.budget_credits,
                warning_threshold: record.warning_threshold,
                allowed_models: record.allowed_models ?? [],
              });
              setEditKeyOpen(true);
            }}
          />
          <Button
            type="text"
            size="small"
            danger
            icon={<ReloadOutlined />}
            aria-label={`Reset key ${record.name}`}
            onClick={() => handleResetKey(record)}
          />
        </Space>
      ),
    },
  ];

  // ---- Model columns ----
  const modelColumns = [
    { title: "Public Model", dataIndex: "public_model", key: "public_model" },
    { title: "Target Profile", dataIndex: "target_profile_id", key: "target_profile_id" },
    { title: "Target Model", dataIndex: "target_model", key: "target_model" },
    { title: "Weight", dataIndex: "credit_weight", key: "credit_weight" },
    {
      title: "Enabled",
      dataIndex: "enabled",
      key: "enabled",
      render: (_: boolean, record: UsageModelMapping) => (
        <Switch
          checked={record.enabled}
          onChange={(checked) => handleToggleModel(record.id, checked)}
        />
      ),
    },
    {
      title: "",
      key: "actions",
      width: 48,
      render: (_: unknown, record: UsageModelMapping) => (
        <Button
          type="text"
          size="small"
          icon={<EditOutlined />}
          aria-label={`Edit mapping ${record.public_model}`}
          onClick={() => {
            setEditingModel(record);
            setSelectedProfileId(record.target_profile_id);
            editModelForm.setFieldsValue({
              public_model: record.public_model,
              target_profile_id: record.target_profile_id,
              target_model: record.target_model,
              credit_weight: record.credit_weight,
            });
            setEditModelOpen(true);
          }}
        />
      ),
    },
  ];

  // ---- Summary columns ----
  const summaryColumns = [
    { title: "Period", dataIndex: "period", key: "period", render: (v?: string) => v || "all" },
    { title: "Key ID", dataIndex: "api_key_id", key: "api_key_id", render: (v?: string) => v ? <Text code>{v.slice(0, 12)}...</Text> : <Text type="secondary">all</Text> },
    { title: "Calls", dataIndex: "call_count", key: "call_count" },
    { title: "Errors", dataIndex: "error_count", key: "error_count" },
    { title: "Input", dataIndex: "input_tokens", key: "input_tokens" },
    { title: "Output", dataIndex: "output_tokens", key: "output_tokens" },
    { title: "Cache R", dataIndex: "cache_read_tokens", key: "cache_read_tokens" },
    { title: "Cache W", dataIndex: "cache_write_tokens", key: "cache_write_tokens" },
    { title: "Billable", dataIndex: "billable_tokens", key: "billable_tokens" },
    { title: "Credits", dataIndex: "credits", key: "credits", render: (v: number) => v.toFixed(2) },
  ];

  // ---- Calls columns ----
  const callsColumns = [
    { title: "Time", dataIndex: "timestamp", key: "timestamp", render: (v: string) => dayjs(v).format("HH:mm:ss") },
    { title: "Key", dataIndex: "api_key_id", key: "api_key_id", render: (v: string) => <Text code>{v.slice(0, 8)}...</Text> },
    { title: "Model", dataIndex: "public_model", key: "public_model" },
    { title: "Target", dataIndex: "target_model", key: "target_model", render: (_: string, r: UsageCall) => `${r.target_profile_id}/${r.target_model}` },
    { title: "Source", dataIndex: "source_channel", key: "source_channel", render: (v?: string) => v || <Text type="secondary">proxy</Text> },
    { title: "In", dataIndex: "input_tokens", key: "input_tokens" },
    { title: "Out", dataIndex: "output_tokens", key: "output_tokens" },
    { title: "Cache R", dataIndex: "cache_read_tokens", key: "cache_read_tokens" },
    { title: "Cache W", dataIndex: "cache_write_tokens", key: "cache_write_tokens" },
    { title: "Weight", dataIndex: "credit_weight", key: "credit_weight" },
    { title: "Credits", dataIndex: "credits", key: "credits", render: (v: number) => v.toFixed(2) },
    {
      title: "Status",
      dataIndex: "status",
      key: "status",
      render: (v: string) => v === "success" ? <Tag color="green">success</Tag> : <Tag color="red">{v}</Tag>,
    },
  ];

  const baseUrl = typeof window !== "undefined" ? `${window.location.origin}` : "";

  return (
    <div style={{ padding: 24 }}>
      <Title level={3}>Usage Gateway</Title>

      <Tabs defaultActiveKey="overview" items={[
        {
          key: "overview",
          label: t("usage.overview"),
          children: <OverviewTab token={token} />,
        },
        {
          key: "keys",
          label: t("usage.apiKeys"),
          children: (
            <>
              <Card size="small" style={{ marginBottom: 16 }}>
                <Space size={16} wrap>
                  <div>
                    <Text strong>OpenAI Compatible</Text>
                    <div style={{ marginTop: 4 }}>
                      <Text code copyable={{ text: `${baseUrl}/v1/chat/completions` }}>
                        {baseUrl}/v1/chat/completions
                      </Text>
                    </div>
                  </div>
                  <div>
                    <Text strong>Anthropic Compatible</Text>
                    <div style={{ marginTop: 4 }}>
                      <Text code copyable={{ text: `${baseUrl}/v1/messages` }}>
                        {baseUrl}/v1/messages
                      </Text>
                    </div>
                  </div>
                  <div style={{ marginLeft: 'auto' }}>
                    <Text type="secondary">Send POST requests with header</Text>
                    <div style={{ marginTop: 4 }}>
                      <Text code>Authorization: Bearer gdx_&lt;your-key&gt;</Text>
                    </div>
                  </div>
                </Space>
              </Card>

              <Card
                title="Proxy API Keys"
                extra={
                  <Button
                    type="primary"
                    icon={<PlusOutlined />}
                    onClick={() => setCreateKeyOpen(true)}
                  >
                    Create Key
                  </Button>
                }
              >
                <Table
                  dataSource={proxyKeys}
                  columns={keyColumns}
                  rowKey="id"
                  loading={keysQuery.isLoading}
                  size="small"
                />
              </Card>
            </>
          ),
        },
        {
          key: "models",
          label: t("usage.modelMappings"),
          children: (
            <Card
              title="Proxy Model Mappings"
              extra={
                <Button
                  type="primary"
                  icon={<PlusOutlined />}
                  onClick={() => setCreateModelOpen(true)}
                >
                  Add Model
                </Button>
              }
            >
              <Table
                dataSource={modelsQuery.data ?? []}
                columns={modelColumns}
                rowKey="id"
                loading={modelsQuery.isLoading}
                size="small"
              />
            </Card>
          ),
        },
        {
          key: "summary",
          label: t("usage.summary"),
          children: (
            <Card
              title="Token & Credit Summary"
              extra={
                <Space>
                  <Select
                    value={summaryRange}
                    onChange={setSummaryRange}
                    options={[
                      { value: "day", label: "Day" },
                      { value: "week", label: "Week" },
                    ]}
                    style={{ width: 100 }}
                  />
                  <Select
                    value={summaryKeyId}
                    onChange={setSummaryKeyId}
                    allowClear
                    placeholder="All keys"
                    style={{ width: 200 }}
                    options={usageKeyFilterOptions}
                  />
                  <Button icon={<ReloadOutlined />} onClick={() => summaryQuery.refetch()} />
                </Space>
              }
            >
              <Table
                dataSource={summaryQuery.data ?? []}
                columns={summaryColumns}
                rowKey={(r) => `${r.period ?? "all"}:${r.api_key_id ?? "all"}`}
                loading={summaryQuery.isLoading}
                size="small"
              />
            </Card>
          ),
        },
        {
          key: "sessions",
          label: t("usage.sessions"),
          children: <SessionTab token={token} />,
        },
        {
          key: "calls",
          label: t("usage.dailyCalls"),
          children: (
            <Card
              title="Usage Calls"
              extra={
                <Space>
                  <DatePicker
                    value={dayjs(callsDate)}
                    onChange={(d) => setCallsDate(d?.format("YYYY-MM-DD") ?? dayjs().format("YYYY-MM-DD"))}
                  />
                  <Select
                    value={callsKeyId}
                    onChange={setCallsKeyId}
                    allowClear
                    placeholder="All keys"
                    style={{ width: 200 }}
                    options={usageKeyFilterOptions}
                  />
                  <Button icon={<ReloadOutlined />} onClick={() => callsQuery.refetch()} />
                </Space>
              }
            >
              <Table
                dataSource={callsQuery.data ?? []}
                columns={callsColumns}
                rowKey="id"
                loading={callsQuery.isLoading}
                size="small"
              />
            </Card>
          ),
        },
        {
          key: "cache",
          label: t("usage.cacheStats"),
          children: <CacheStatsTab token={token} />,
        },
      ]} />

      {/* ===== Create Key Modal ===== */}
      <Modal
        title="Create API Key"
        open={createKeyOpen}
        confirmLoading={submitting}
        onOk={() => createKeyForm.submit()}
        onCancel={() => { setCreateKeyOpen(false); createKeyForm.resetFields(); }}
        destroyOnClose
      >
        <Form form={createKeyForm} layout="vertical" onFinish={handleCreateKey}>
          <Form.Item name="name" label="Name" rules={[{ required: true, message: "Key name is required" }]}>
            <Input placeholder="e.g. my-app-key" />
          </Form.Item>
          <Form.Item name="budget_credits" label="Budget Credits" initialValue={0}>
            <InputNumber min={0} style={{ width: "100%" }} placeholder="0 = unlimited" />
          </Form.Item>
          <Form.Item name="warning_threshold" label="Warning Threshold" initialValue={0}>
            <InputNumber min={0} style={{ width: "100%" }} placeholder="0 = no warning" />
          </Form.Item>
          <Form.Item name="allowed_models" label="Allowed Models" initialValue={[]}>
            <Select mode="tags" placeholder="Leave empty for all models" options={allowedModelOptions} />
          </Form.Item>
        </Form>
      </Modal>

      {/* ===== Reset Key Modal (shows new plaintext secret once) ===== */}
      <Modal
        title={resetKeySecret ? "New API Key — copy now" : "Resetting API Key…"}
        open={resetKeyOpen}
        footer={
          <Space>
            <Button onClick={() => { setResetKeyOpen(false); setResetKeySecret(""); setResetKeyName(""); }}>Close</Button>
            {resetKeySecret ? (
              <Button
                type="primary"
                icon={<CopyOutlined />}
                onClick={async () => {
                  try {
                    await writeClipboardText(resetKeySecret);
                    antMessage.success("New secret copied to clipboard");
                  } catch (err) {
                    showError(antMessage, err, "Failed to copy");
                  }
                }}
              >
                Copy Secret
              </Button>
            ) : null}
          </Space>
        }
        onCancel={() => { setResetKeyOpen(false); setResetKeySecret(""); setResetKeyName(""); }}
        destroyOnClose
      >
        <Paragraph>
          Resetting <Text strong>{resetKeyName}</Text> will invalidate the previous secret immediately.
          The new secret below is shown <Text strong>once</Text> — store it now, or reset again.
        </Paragraph>
        {resetKeySecret ? (
          <Paragraph>
            <Text strong>New secret:</Text>
            <div style={{ marginTop: 8 }}>
              <Text code copyable={{ text: resetKeySecret }} style={{ wordBreak: "break-all" }}>
                {resetKeySecret}
              </Text>
            </div>
          </Paragraph>
        ) : (
          <Paragraph type="secondary">Waiting for the gateway to issue a new secret…</Paragraph>
        )}
      </Modal>

      {/* ===== Edit Key Modal ===== */}
      <Modal
        title="Edit API Key"
        open={editKeyOpen}
        confirmLoading={submitting}
        onOk={() => editKeyForm.submit()}
        onCancel={() => { setEditKeyOpen(false); setEditingKey(null); }}
        destroyOnClose
      >
        <Form form={editKeyForm} layout="vertical" onFinish={handleEditKey}>
          <Form.Item name="name" label="Name" rules={[{ required: true }]}>
            <Input />
          </Form.Item>
          <Form.Item name="budget_credits" label="Budget Credits">
            <InputNumber min={0} style={{ width: "100%" }} />
          </Form.Item>
          <Form.Item name="warning_threshold" label="Warning Threshold">
            <InputNumber min={0} style={{ width: "100%" }} />
          </Form.Item>
          <Form.Item name="allowed_models" label="Allowed Models">
            <Select mode="tags" placeholder="Leave empty for all models" options={allowedModelOptions} />
          </Form.Item>
        </Form>
      </Modal>

      {/* ===== Create Model Mapping Modal ===== */}
      <Modal
        title="Add Model Mapping"
        open={createModelOpen}
        confirmLoading={submitting}
        onOk={() => createModelForm.submit()}
        onCancel={() => { setCreateModelOpen(false); createModelForm.resetFields(); setSelectedProfileId(""); }}
        destroyOnClose
      >
        <Form form={createModelForm} layout="vertical" onFinish={handleCreateModel}>
          <Form.Item name="public_model" label="Public Model" rules={[{ required: true }]}>
            <AutoComplete options={publicModelOptions} placeholder="e.g. gpt-4" />
          </Form.Item>
          <Form.Item name="target_profile_id" label="Target Profile" rules={[{ required: true }]}>
            <Select
              showSearch
              placeholder="Select a model profile"
              options={profileSelectOptions}
              filterOption={(input, option) =>
                (option?.label ?? "").toLowerCase().includes(input.toLowerCase())
              }
              onChange={(value) => {
                setSelectedProfileId(value);
                createModelForm.setFieldValue("target_model", profileByID.get(value)?.model ?? "");
              }}
            />
          </Form.Item>
          <Form.Item name="target_model" label="Target Model Override">
            <Input placeholder={selectedProfileId ? profileByID.get(selectedProfileId)?.model : "Defaults to selected profile model"} />
          </Form.Item>
          <Form.Item name="credit_weight" label="Credit Weight" initialValue={1}>
            <InputNumber min={0.1} step={0.1} style={{ width: "100%" }} />
          </Form.Item>
        </Form>
      </Modal>

      {/* ===== Edit Model Mapping Modal ===== */}
      <Modal
        title="Edit Model Mapping"
        open={editModelOpen}
        confirmLoading={submitting}
        onOk={() => editModelForm.submit()}
        onCancel={() => { setEditModelOpen(false); setEditingModel(null); setSelectedProfileId(""); }}
        destroyOnClose
      >
        <Form form={editModelForm} layout="vertical" onFinish={handleEditModel}>
          <Form.Item name="public_model" label="Public Model" rules={[{ required: true }]}>
            <AutoComplete options={publicModelOptions} />
          </Form.Item>
          <Form.Item name="target_profile_id" label="Target Profile" rules={[{ required: true }]}>
            <Select
              showSearch
              placeholder="Select a model profile"
              options={profileSelectOptions}
              filterOption={(input, option) =>
                (option?.label ?? "").toLowerCase().includes(input.toLowerCase())
              }
              onChange={(value) => {
                setSelectedProfileId(value);
                editModelForm.setFieldValue("target_model", profileByID.get(value)?.model ?? "");
              }}
            />
          </Form.Item>
          <Form.Item name="target_model" label="Target Model Override">
            <Input placeholder={selectedProfileId ? profileByID.get(selectedProfileId)?.model : "Defaults to selected profile model"} />
          </Form.Item>
          <Form.Item name="credit_weight" label="Credit Weight">
            <InputNumber min={0.1} step={0.1} style={{ width: "100%" }} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
