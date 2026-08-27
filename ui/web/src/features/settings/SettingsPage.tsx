import { useEffect, useMemo, useState, type ReactNode } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Collapse,
  Descriptions,
  Form,
  Image,
  Input,
  InputNumber,
  QRCode,
  Select,
  Space,
  Switch,
  Table,
  Tabs,
  Tag,
  Typography,
} from "antd";
import { ArrowDownOutlined, ArrowUpOutlined, BellOutlined, CopyOutlined, DeleteOutlined, EyeOutlined, LogoutOutlined, PlusOutlined, QrcodeOutlined, ReloadOutlined, SaveOutlined, SendOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { showError } from "../../lib/notifications";
import { writeClipboardText } from "../../lib/clipboard";
import { stringify as toYaml } from "yaml";
import {
  discoverProviderModels,
  getChannelsStatus,
  getConfigDoctor,
  getConfigMeta,
  getConfigSchema,
  getConfigView,
  getMeta,
  getPackageQuality,
  getRuntimeServiceStatus,
  getSecurityAudit,
  getSecuritySummary,
  getWeixinAuthStatus,
  listProviders,
  logoutWeixinAuth,
  reloadConfigFromDisk,
  revealConfigSecret,
  restartRuntimeService,
  startWeixinAuth,
  testProvider,
  updateConfig,
} from "../../lib/api";
import type { ApplyReport, ChannelStatus, CIKSummary, ConfigFieldSchema, ConfigFieldState, ConfigSectionSchema, DoctorCheck, PackageQualityReport, ProviderModelInfo, ProviderStatus, RuntimeServiceStatus, SecurityEvent, WeixinAuthStatus } from "../../lib/types";
import { useSettingsStore } from "../../store/settings";
import { ensurePushSubscription, pushSupported, sendTestPush } from "../../lib/push";
import { MCPSettingsPanel } from "./MCPSettingsPanel";

type LocalFormValues = {
  locale: "en" | "zh";
  token: string;
  defaultSessionKey: string;
};

type ConfigFormValues = Record<string, unknown>;

const SECRET_MASK = "********";
const API_HIDDEN_PATHS = new Set([
  "api.default_profile",
  "api.auto_fallback_enabled",
  "api.timeout_seconds",
]);

type LLMProvidersFormValue = {
  items: LLMProviderFormItem[];
};

type LLMProviderFormItem = {
  id: string;
  name?: string;
  type?: string;
  base_url?: string;
  api_key?: string;
  api_key_env?: string;
  credential_kind?: string;
  timeout_seconds?: number;
  models: LLMModelFormItem[];
};

type LLMModelFormItem = {
  id: string;
  name?: string;
  model?: string;
  max_tokens?: number;
  context_window_tokens?: number;
  supports_streaming?: boolean;
  supports_vision?: boolean;
  reasoning_effort?: string;
  tags?: string;
};

type LLMStrategyFormValue = {
  type: "primary" | "fallback" | "round_robin";
  candidates: string[];
};

type ModelOption = {
  value: string;
  label: string;
};

const reasoningEffortOptions = [
  { value: "none", label: "None" },
  { value: "minimal", label: "Minimal" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "xhigh", label: "X High" },
];

export function SettingsPage() {
  const { message } = AntApp.useApp();
  const { locale, setLocale, t } = useI18n();
  const queryClient = useQueryClient();
  const token = useSettingsStore((state) => state.token);
  const defaultSessionKey = useSettingsStore((state) => state.defaultSessionKey);
  const setToken = useSettingsStore((state) => state.setToken);
  const clearToken = useSettingsStore((state) => state.clearToken);
  const setDefaultSessionKey = useSettingsStore((state) => state.setDefaultSessionKey);
  const [localForm] = Form.useForm<LocalFormValues>();
  const [configForm] = Form.useForm<ConfigFormValues>();
  const [clearSecrets, setClearSecrets] = useState<Record<string, boolean>>({});
  const [backendDirty, setBackendDirty] = useState(false);

  const metaQuery = useQuery({ queryKey: ["meta"], queryFn: getMeta });
  const authRequired = metaQuery.data?.auth_required ?? false;
  const canReachConfig = !authRequired || !!token;
  const configMetaQuery = useQuery({
    queryKey: ["config-meta", token],
    enabled: canReachConfig,
    queryFn: async () => getConfigMeta(token || null),
  });
  const configSchemaQuery = useQuery({
    queryKey: ["config-schema", token],
    enabled: canReachConfig,
    queryFn: async () => getConfigSchema(token || null),
  });
  const configViewQuery = useQuery({
    queryKey: ["config-view", token],
    enabled: canReachConfig,
    queryFn: async () => getConfigView(token || null),
  });
  const runtimeServiceQuery = useQuery({
    queryKey: ["runtime-service", token],
    enabled: canReachConfig,
    queryFn: async () => getRuntimeServiceStatus(token || null),
    refetchInterval: canReachConfig ? 10000 : false,
  });
  const doctorQuery = useQuery({
    queryKey: ["config-doctor", token],
    enabled: canReachConfig,
    queryFn: async () => getConfigDoctor(token || null),
  });
  const providersQuery = useQuery({
    queryKey: ["providers", token],
    enabled: canReachConfig,
    queryFn: async () => listProviders(token || null),
    refetchInterval: canReachConfig ? 10000 : false,
  });
  const channelsQuery = useQuery({
    queryKey: ["channels-status", token],
    enabled: canReachConfig,
    queryFn: async () => getChannelsStatus(token || null),
    refetchInterval: canReachConfig ? 5000 : false,
  });
  const securityQuery = useQuery({
    queryKey: ["security-summary", token],
    enabled: canReachConfig,
    queryFn: async () => getSecuritySummary(token || null),
    refetchInterval: canReachConfig ? 10000 : false,
  });
  const auditQuery = useQuery({
    queryKey: ["security-audit", token],
    enabled: canReachConfig,
    queryFn: async () => getSecurityAudit(token || null, 50),
    refetchInterval: canReachConfig ? 10000 : false,
  });
  const packageQualityQuery = useQuery({
    queryKey: ["packages-quality", token],
    enabled: canReachConfig,
    queryFn: async () => getPackageQuality(token || null),
    refetchInterval: canReachConfig ? 10000 : false,
  });
  const weixinAuthQuery = useQuery({
    queryKey: ["weixin-auth", token],
    enabled: canReachConfig,
    queryFn: async () => getWeixinAuthStatus(token || null),
    refetchInterval: canReachConfig ? 5000 : false,
  });

  useEffect(() => {
    localForm.setFieldsValue({ locale, token, defaultSessionKey });
  }, [defaultSessionKey, locale, localForm, token]);

  useEffect(() => {
    if (configViewQuery.data) {
      configForm.setFieldsValue(formValuesFromConfig(configViewQuery.data.stored_values, configSchemaQuery.data ?? []));
    }
  }, [configForm, configSchemaQuery.data, configViewQuery.data]);

  const saveConfigMutation = useMutation({
    mutationFn: async (values: ConfigFormValues) =>
      updateConfig(token || null, {
        values: buildSaveValues(values, configSchemaQuery.data ?? []),
        clear_secrets: Object.entries(clearSecrets).filter(([, enabled]) => enabled).map(([path]) => path),
      }),
    onSuccess: async (_view, values) => {
      const rotatedToken = String(values["web.token"] ?? "").trim();
      if (rotatedToken) {
        setToken(rotatedToken);
      }
      setClearSecrets({});
      setBackendDirty(false);
      void message.success(t("settings.msgConfigSaved"));
      await refreshAll(queryClient, token);
    },
    onError: (error) => showError(message, error, t("settings.msgConfigSaveFailed")),
  });

  const reloadConfigMutation = useMutation({
    mutationFn: async () => reloadConfigFromDisk(token || null),
    onSuccess: async () => {
      setBackendDirty(false);
      void message.success(t("settings.msgConfigReloaded"));
      await refreshAll(queryClient, token);
    },
    onError: (error) => showError(message, error, t("settings.msgConfigReloadFailed")),
  });

  const restartServiceMutation = useMutation({
    mutationFn: async () => restartRuntimeService(token || null),
    onSuccess: async () => {
      void message.success(t("settings.msgRestartRequested"));
      await queryClient.invalidateQueries({ queryKey: ["runtime-service", token] });
    },
    onError: (error) => showError(message, error, t("settings.msgRestartFailed")),
  });

  const revealMutation = useMutation({
    mutationFn: async (path: string) => revealConfigSecret(token || null, path),
    onSuccess: ({ path, value }) => {
      configForm.setFieldValue(path, path === "api.providers" ? providersConfigToForm(parseJSONValue(value)) : value);
      setClearSecrets((current) => ({ ...current, [path]: false }));
    },
    onError: (error) => showError(message, error, t("settings.msgRevealFailed")),
  });

  const testProviderMutation = useMutation({
    mutationFn: async (id: string) => testProvider(token || null, id),
    onSuccess: async () => {
      await queryClient.invalidateQueries({ queryKey: ["providers", token] });
    },
    onError: (error) => showError(message, error, t("settings.msgProviderTestFailed")),
  });

  const discoverModelsMutation = useMutation({
    mutationFn: async (id: string) => discoverProviderModels(token || null, id),
    onSuccess: (result) => {
      const providerID = result.provider_id;
      const current = providersConfigToForm(configForm.getFieldValue("api.providers"));
      const providerIndex = current.items.findIndex((provider) => provider.id === providerID);
      if (providerIndex < 0) {
        message.warning(t("settings.msgProviderNotFound"));
        return;
      }
      const nextItems = current.items.map((provider, index) => index === providerIndex ? {
        ...provider,
        models: mergeDiscoveredModels(provider.models, result.models ?? []),
      } : provider);
      configForm.setFieldValue("api.providers", { items: nextItems });
      message.success(t("settings.msgModelsFetched", { count: (result.models ?? []).length }));
    },
    onError: (error) => showError(message, error, t("settings.msgModelDiscoverFailed")),
  });

  const startWeixinMutation = useMutation({
    mutationFn: async () => startWeixinAuth(token || null, weixinAuthQuery.data?.account_id),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["weixin-auth", token] }),
        queryClient.invalidateQueries({ queryKey: ["channels-status", token] }),
      ]);
    },
    onError: (error) => showError(message, error, t("settings.msgWeixinStartFailed")),
  });

  const logoutWeixinMutation = useMutation({
    mutationFn: async () => logoutWeixinAuth(token || null, weixinAuthQuery.data?.account_id),
    onSuccess: async () => {
      await Promise.all([
        queryClient.invalidateQueries({ queryKey: ["weixin-auth", token] }),
        queryClient.invalidateQueries({ queryKey: ["channels-status", token] }),
      ]);
    },
    onError: (error) => showError(message, error, t("settings.msgWeixinLogoutFailed")),
  });

  const sections = configSchemaQuery.data ?? [];
  const fields = configViewQuery.data?.fields ?? {};
  const effectiveValues = configViewQuery.data?.effective_values ?? {};
  const providerFormValue = Form.useWatch("api.providers", configForm);
  const modelOptions = useMemo(() => llmModelOptions(providerFormValue), [providerFormValue]);
  const configInSync = useMemo(
    () => sameConfigValue(configViewQuery.data?.stored_values, configViewQuery.data?.effective_values),
    [configViewQuery.data?.effective_values, configViewQuery.data?.stored_values],
  );

  return (
    <div className="page-pad">
      <div className="page-action-row">
        <Button icon={<ReloadOutlined />} onClick={() => { setBackendDirty(false); void refreshAll(queryClient, token); }}>{t("settings.refresh")}</Button>
      </div>

      <Tabs
        items={[
          {
            key: "client",
            label: t("settings.webClientTitle"),
            children: (
              <>
                <Card>
                  <Form
                    form={localForm}
                  layout="vertical"
                  onFinish={(values) => {
                    setLocale(values.locale);
                    setToken(values.token.trim());
                    setDefaultSessionKey(values.defaultSessionKey.trim());
                    void message.success(t("settings.msgLocalSaved"));
                  }}
                >
                  <Form.Item name="locale" label={t("locale.label")}>
                    <Select options={[{ value: "en", label: t("locale.en") }, { value: "zh", label: t("locale.zh") }]} />
                  </Form.Item>
                  <Form.Item name="token" label={t("settings.sharedBearerToken")}>
                    <Input.Password placeholder={t("settings.sharedBearerTokenPlaceholder")} />
                  </Form.Item>
                  <Form.Item name="defaultSessionKey" label={t("settings.defaultWebSessionKey")}>
                    <Input />
                  </Form.Item>
                  <Space wrap>
                    <Button type="primary" htmlType="submit" icon={<SaveOutlined />}>{t("settings.saveLocalSettings")}</Button>
                    <Button
                      onClick={() => {
                        clearToken();
                        localForm.setFieldValue("token", "");
                      }}
                    >
                      {t("settings.clearToken")}
                    </Button>
                  </Space>
                </Form>
              </Card>
                  <Card title={t("settings.pushTitle")} style={{ marginTop: 16 }}>
                    <NotificationsCard token={token} t={t} />
                  </Card>
                </>
              ),
            },
            {
              key: "backend",
            label: t("settings.backendConfigTitle"),
            children: authRequired && !token ? (
              <Alert type="warning" showIcon message={t("settings.authRequired", { area: t("settings.authAreaBackend") })} />
            ) : (
              <Space direction="vertical" size={16} style={{ width: "100%" }}>
                <Card>
                  <Descriptions
                    bordered
                    size="small"
                    column={{ xs: 1, md: 2 }}
                    items={[
                      { key: "file", label: t("settings.configFile"), children: configMetaQuery.data?.file_path ?? "Loading..." },
                      { key: "env", label: t("settings.envFile"), children: configMetaQuery.data?.env_file ?? "Loading..." },
                      { key: "home", label: t("settings.home"), children: configMetaQuery.data?.home_dir ?? "-" },
                      { key: "project", label: t("settings.project"), children: configMetaQuery.data?.project_dir ?? "-" },
                      { key: "home-config", label: t("settings.homeConfig"), children: configMetaQuery.data?.home_config_file ?? "-" },
                      { key: "project-config", label: t("settings.projectConfig"), children: configMetaQuery.data?.project_config_file ?? "-" },
                      { key: "home-env", label: t("settings.homeEnv"), children: configMetaQuery.data?.home_env_file ?? "-" },
                      { key: "project-env", label: t("settings.projectEnv"), children: configMetaQuery.data?.project_env_file ?? "-" },
                      { key: "revision", label: t("settings.revision"), children: configMetaQuery.data?.revision ?? "-" },
                      { key: "version", label: t("settings.version"), children: metaQuery.data?.version?.version ?? "-" },
                      { key: "sync", label: t("settings.configSync"), children: configInSync ? t("settings.storedEqualsEffective") : t("settings.storedDiffersEffective") },
                    ]}
                  />
                  <ApplyReportView report={configMetaQuery.data?.last_apply} configInSync={configInSync} />
                </Card>
                <RuntimeServiceCard
                  status={runtimeServiceQuery.data}
                  loading={runtimeServiceQuery.isLoading}
                  reloading={reloadConfigMutation.isPending}
                  restarting={restartServiceMutation.isPending}
                  onReload={() => reloadConfigMutation.mutate()}
                  onRestart={() => restartServiceMutation.mutate()}
                />
                <ProvidersPanel
                  providers={providersQuery.data?.providers ?? []}
                  loading={providersQuery.isLoading}
                  testingID={testProviderMutation.variables}
                  testing={testProviderMutation.isPending}
                  onTest={(id) => testProviderMutation.mutate(id)}
                />
                <Form form={configForm} layout="vertical" onValuesChange={() => setBackendDirty(true)} onFinish={(values) => saveConfigMutation.mutate(values)}>
                  {backendDirty ? (
                    <Alert type="warning" showIcon style={{ marginBottom: 16 }} message={t("settings.unsavedBackendConfig")} />
                  ) : null}
                  <Collapse
                    defaultActiveKey={sections.slice(0, 2).map((section) => section.id)}
                    items={sections.map((section) => ({
                      key: section.id,
                      label: section.label,
                      children: (
                        <Space direction="vertical" size={12} style={{ width: "100%" }}>
                          {section.description ? <Typography.Text type="secondary">{section.description}</Typography.Text> : null}
                          <ConfigSectionFields
                            section={section}
                            fields={fields}
                            effectiveValues={effectiveValues}
                            clearSecrets={clearSecrets}
                            revealMutation={revealMutation}
                            modelOptions={modelOptions}
                            discoveringProviderID={discoverModelsMutation.variables}
                            discoveringModels={discoverModelsMutation.isPending}
                            onDiscoverModels={(id) => discoverModelsMutation.mutate(id)}
                            onClearSecret={(path) => {
                              configForm.setFieldValue(path, "");
                              setClearSecrets((current) => ({ ...current, [path]: true }));
                            }}
                          />
                        </Space>
                      ),
                    }))}
                  />
                  <Card style={{ marginTop: 16 }}>
                    <Button type="primary" htmlType="submit" loading={saveConfigMutation.isPending} icon={<SaveOutlined />}>{t("settings.saveBackendConfig")}</Button>
                  </Card>
                </Form>
              </Space>
            ),
          },
          {
            key: "config-yaml",
            label: t("settings.configYamlTitle"),
            children: authRequired && !token ? (
              <Alert type="warning" showIcon message={t("settings.authRequired", { area: t("settings.authAreaYaml") })} />
            ) : (
              <ConfigYamlCard
                storedValues={configViewQuery.data?.stored_values ?? {}}
                effectiveValues={configViewQuery.data?.effective_values ?? {}}
                loading={configViewQuery.isLoading}
              />
            ),
          },
          {
            key: "security",
            label: t("settings.securityTitle"),
            children: authRequired && !token ? (
              <Alert type="warning" showIcon message={t("settings.authRequired", { area: t("settings.authAreaSecurity") })} />
            ) : (
              <SecurityPanel
                summary={securityQuery.data}
                audit={auditQuery.data ?? []}
                packageQuality={packageQualityQuery.data}
                loading={securityQuery.isLoading || auditQuery.isLoading || packageQualityQuery.isLoading}
              />
            ),
          },
          {
            key: "runtime",
            label: t("settings.runtimeTitle"),
            children: (
              <Space direction="vertical" size={16} style={{ width: "100%" }}>
                <WeixinPanel
                  auth={weixinAuthQuery.data}
                  runtime={channelsQuery.data?.channels?.find((channel) => channel.name === "weixin")}
                  pending={weixinAuthQuery.isLoading}
                  starting={startWeixinMutation.isPending}
                  loggingOut={logoutWeixinMutation.isPending}
                  onStart={() => startWeixinMutation.mutate()}
                  onLogout={() => logoutWeixinMutation.mutate()}
                />
                <Card title={t("settings.runtimeChannelsTitle")}>
                  <Table<ChannelStatus>
                    rowKey="name"
                    size="small"
                    dataSource={channelsQuery.data?.channels ?? []}
                    loading={channelsQuery.isLoading}
                    scroll={{ x: 900 }}
                    columns={[
                      { title: t("settings.channelCol"), dataIndex: "name" },
                      { title: t("settings.stateCol"), render: (_value, channel) => <Tag color={channel.enabled ? (channel.running ? "green" : "gold") : "default"}>{channel.enabled ? channel.state || (channel.running ? t("settings.channelRunning") : t("settings.channelIdle")) : t("settings.channelDisabled")}</Tag> },
                      { title: t("settings.capabilitiesCol"), render: (_value, channel) => renderChannelCapabilities(channel.capabilities) },
                      { title: t("settings.deliveryCol"), render: (_value, channel) => renderDeliveryStatus(channel.last_delivery) },
                      { title: t("settings.accessCol"), render: (_value, channel) => renderAccessDecision(channel.last_access) },
                      { title: t("settings.detailCol"), dataIndex: "detail", render: (value) => value || "-" },
                      { title: t("settings.updatedCol"), dataIndex: "updated_at", render: formatTimestamp },
                      { title: t("settings.lastErrorCol"), dataIndex: "last_error", render: (value) => value ? <Typography.Text type="danger">{value}</Typography.Text> : "-" },
                    ]}
                  />
                </Card>
                <DoctorPanel checks={doctorQuery.data?.checks ?? []} loading={doctorQuery.isLoading} />
              </Space>
            ),
          },
          {
            key: "mcp",
            label: t("settings.mcpTabTitle"),
            children: authRequired && !token ? (
              <Alert type="warning" showIcon message={t("settings.authRequired", { area: t("settings.mcpTabTitle") })} />
            ) : (
              <MCPSettingsPanel token={token || null} />
            ),
          },
        ]}
      />
    </div>
  );
}

function NotificationsCard({ token, t }: { token: string; t: (key: string) => string }) {
  const { message } = AntApp.useApp();
  const [pushState, setPushState] = useState<{ supported: boolean; permission: string; subscribed: boolean } | null>(null);
  const [busy, setBusy] = useState(false);
  const supported = pushSupported();

  const enable = async () => {
    if (!supported) {
      void message.warning(t("settings.pushUnsupported"));
      return;
    }
    setBusy(true);
    try {
      const state = await ensurePushSubscription(token || null);
      setPushState(state);
      void message.success(state.subscribed ? t("settings.pushEnabled") : t("settings.pushDenied"));
    } catch (e: any) {
      void message.error(e?.message || t("settings.pushFailed"));
    } finally {
      setBusy(false);
    }
  };

  const test = async () => {
    setBusy(true);
    try {
      const notified = await sendTestPush(token || null);
      void message.success(notified > 0 ? t("settings.pushTestSent") : t("settings.pushNoSubscribers"));
    } catch (e: any) {
      void message.error(e?.message || t("settings.pushFailed"));
    } finally {
      setBusy(false);
    }
  };

  return (
    <Space direction="vertical" size={8} style={{ width: "100%" }}>
      <Typography.Text type="secondary">{t("settings.pushSubtitle")}</Typography.Text>
      {pushState ? (
        <Tag color={pushState.subscribed ? "green" : "default"}>
          {pushState.subscribed ? t("settings.pushEnabled") : t("settings.pushDenied")}
        </Tag>
      ) : null}
      <Space wrap>
        <Button type="primary" icon={<BellOutlined />} loading={busy} disabled={!supported} onClick={() => void enable()}>
          {t("settings.pushEnable")}
        </Button>
        <Button icon={<SendOutlined />} loading={busy} disabled={!supported} onClick={() => void test()}>
          {t("settings.pushTest")}
        </Button>
      </Space>
    </Space>
  );
}

function ConfigSectionFields(props: {
  section: ConfigSectionSchema;
  fields: Record<string, ConfigFieldState>;
  effectiveValues: Record<string, unknown>;
  clearSecrets: Record<string, boolean>;
  revealMutation: { isPending: boolean; variables?: string; mutate: (path: string) => void };
  modelOptions: ModelOption[];
  discoveringProviderID?: string;
  discoveringModels: boolean;
  onDiscoverModels: (id: string) => void;
  onClearSecret: (path: string) => void;
}) {
  const { section, fields, effectiveValues, clearSecrets, revealMutation, modelOptions, discoveringProviderID, discoveringModels, onDiscoverModels, onClearSecret } = props;
  if (section.id === "tools-web-search") {
    return (
      <WebSearchConfigFields
        section={section}
        fields={fields}
        effectiveValues={effectiveValues}
        clearSecrets={clearSecrets}
        revealMutation={revealMutation}
        modelOptions={modelOptions}
        discoveringProviderID={discoveringProviderID}
        discoveringModels={discoveringModels}
        onDiscoverModels={onDiscoverModels}
        onClearSecret={onClearSecret}
      />
    );
  }
  if (section.id === "tools-subagent") {
    return (
      <SubagentConfigFields
        section={section}
        fields={fields}
        effectiveValues={effectiveValues}
        clearSecrets={clearSecrets}
        revealMutation={revealMutation}
        modelOptions={modelOptions}
        discoveringProviderID={discoveringProviderID}
        discoveringModels={discoveringModels}
        onDiscoverModels={onDiscoverModels}
        onClearSecret={onClearSecret}
      />
    );
  }
  if (section.id !== "api") {
    return (
      <>
        {section.fields.map((field) => (
          <FieldEditor
            key={field.path}
            field={field}
            fieldState={fields[field.path]}
            effectiveValue={effectiveValues[field.path]}
            clearSecret={clearSecrets[field.path] ?? false}
            revealPending={revealMutation.isPending && revealMutation.variables === field.path}
            modelOptions={modelOptions}
            discoveringProviderID={discoveringProviderID}
            discoveringModels={discoveringModels}
            onDiscoverModels={onDiscoverModels}
            onReveal={() => revealMutation.mutate(field.path)}
            onClearSecret={() => onClearSecret(field.path)}
          />
        ))}
      </>
    );
  }
  const primaryFields = section.fields.filter((field) => !API_HIDDEN_PATHS.has(field.path));
  return (
    <>
      {primaryFields.map((field) => (
        <FieldEditor
          key={field.path}
          field={field}
          fieldState={fields[field.path]}
          effectiveValue={effectiveValues[field.path]}
          clearSecret={clearSecrets[field.path] ?? false}
          revealPending={revealMutation.isPending && revealMutation.variables === field.path}
          modelOptions={modelOptions}
          discoveringProviderID={discoveringProviderID}
          discoveringModels={discoveringModels}
          onDiscoverModels={onDiscoverModels}
          onReveal={() => revealMutation.mutate(field.path)}
          onClearSecret={() => onClearSecret(field.path)}
        />
      ))}
    </>
  );
}

function SubagentConfigFields(props: {
  section: ConfigSectionSchema;
  fields: Record<string, ConfigFieldState>;
  effectiveValues: Record<string, unknown>;
  clearSecrets: Record<string, boolean>;
  revealMutation: { isPending: boolean; variables?: string; mutate: (path: string) => void };
  modelOptions: ModelOption[];
  discoveringProviderID?: string;
  discoveringModels: boolean;
  onDiscoverModels: (id: string) => void;
  onClearSecret: (path: string) => void;
}) {
  const runtime = props.section.fields.filter((field) => [
    "tools.subagent.default_max_turns",
    "tools.subagent.max_batch_size",
    "tools.subagent.max_concurrent_jobs",
    "tools.subagent.max_job_timeout_ms",
  ].includes(field.path));
  const workspace = props.section.fields.filter((field) => [
    "tools.subagent.readonly_isolation",
    "tools.subagent.git_dirty_isolation",
    "tools.subagent.non_git_write_isolation",
    "tools.subagent.workspace_ttl_hours",
  ].includes(field.path));
  const other = props.section.fields.filter((field) => !runtime.includes(field) && !workspace.includes(field));
  const { t } = useI18n();
  const editorProps = {
    fieldStates: props.fields,
    effectiveValues: props.effectiveValues,
    clearSecrets: props.clearSecrets,
    revealMutation: props.revealMutation,
    modelOptions: props.modelOptions,
    discoveringProviderID: props.discoveringProviderID,
    discoveringModels: props.discoveringModels,
    onDiscoverModels: props.onDiscoverModels,
    onClearSecret: props.onClearSecret,
  };
  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      <CompactConfigGroup
        title={t("settings.groupRuntimeBudgets")}
        items={runtime}
        {...editorProps}
      />
      <CompactConfigGroup
        title={t("settings.groupWorkspaceIsolation")}
        items={workspace}
        {...editorProps}
      />
      <CompactConfigGroup
        title={t("settings.groupOther")}
        items={other}
        {...editorProps}
      />
    </Space>
  );
}

function WebSearchConfigFields(props: {
  section: ConfigSectionSchema;
  fields: Record<string, ConfigFieldState>;
  effectiveValues: Record<string, unknown>;
  clearSecrets: Record<string, boolean>;
  revealMutation: { isPending: boolean; variables?: string; mutate: (path: string) => void };
  modelOptions: ModelOption[];
  discoveringProviderID?: string;
  discoveringModels: boolean;
  onDiscoverModels: (id: string) => void;
  onClearSecret: (path: string) => void;
}) {
  const { section } = props;
  const core = section.fields.filter((field) => [
    "tools.web_search.enabled",
    "tools.web_search.provider_order",
    "tools.web_search.cache_ttl_seconds",
  ].includes(field.path));
  const browserRuntime = section.fields.filter((field) =>
    field.path.startsWith("tools.web_search.browser.") &&
    !field.path.startsWith("tools.web_search.browser.engines."),
  );
  const browserEngineNames = ["duckduckgo", "bing", "brave", "custom"];
  const browserEngines = browserEngineNames.map((engine) => ({
    engine,
    fields: section.fields.filter((field) => field.path.startsWith(`tools.web_search.browser.engines.${engine}.`)),
  }));
  const browser = section.fields.filter((field) => field.path.startsWith("tools.web_search.browser."));
  const api = section.fields.filter((field) => !core.includes(field) && !browser.includes(field));
  const { t } = useI18n();
  const editorProps = {
    fieldStates: props.fields,
    effectiveValues: props.effectiveValues,
    clearSecrets: props.clearSecrets,
    revealMutation: props.revealMutation,
    modelOptions: props.modelOptions,
    discoveringProviderID: props.discoveringProviderID,
    discoveringModels: props.discoveringModels,
    onDiscoverModels: props.onDiscoverModels,
    onClearSecret: props.onClearSecret,
  };
  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      <CompactConfigGroup
        title={t("settings.groupCoreSearch")}
        items={core}
        {...editorProps}
      />
      <CompactConfigGroup
        title={t("settings.groupBrowserRuntime")}
        items={browserRuntime}
        {...editorProps}
      />
      <div className="config-compact-panel">
        <Space direction="vertical" size={2}>
          <Typography.Text strong>{t("settings.groupBrowserSelectors")}</Typography.Text>
          <Typography.Text type="secondary" className="config-compact-description">
            {t("settings.groupBrowserSelectorsHint")}
          </Typography.Text>
        </Space>
        <div className="browser-engine-grid">
          {browserEngines.map(({ engine, fields }) => (
            <BrowserEngineConfigPanel
              key={engine}
              engine={engine}
              fields={fields}
              {...editorProps}
            />
          ))}
        </div>
      </div>
      <CompactConfigGroup
        title={t("settings.groupApiProviders")}
        items={api}
        {...editorProps}
      />
    </Space>
  );
}

function BrowserEngineConfigPanel(props: {
  engine: string;
  fields: ConfigFieldSchema[];
  fieldStates: Record<string, ConfigFieldState>;
  effectiveValues: Record<string, unknown>;
  clearSecrets: Record<string, boolean>;
  revealMutation: { isPending: boolean; variables?: string; mutate: (path: string) => void };
  modelOptions: ModelOption[];
  discoveringProviderID?: string;
  discoveringModels: boolean;
  onDiscoverModels: (id: string) => void;
  onClearSecret: (path: string) => void;
}) {
  const { engine, fields, fieldStates, effectiveValues, clearSecrets, revealMutation, modelOptions, discoveringProviderID, discoveringModels, onDiscoverModels, onClearSecret } = props;
  const { t } = useI18n();
  const demo = browserEngineDemo(engine);
  if (fields.length === 0) {
    return null;
  }
  return (
    <div className="browser-engine-panel">
      <Space direction="vertical" size={2}>
        <Typography.Text strong>{demo.title}</Typography.Text>
        <Typography.Text type="secondary" className="config-compact-description">{demo.description}</Typography.Text>
      </Space>
      <div className="config-compact-grid browser-engine-field-grid">
        {fields.map((field) => (
          <CompactFieldEditor
            key={field.path}
            field={field}
            fieldState={fieldStates[field.path]}
            effectiveValue={effectiveValues[field.path]}
            clearSecret={clearSecrets[field.path] ?? false}
            revealPending={revealMutation.isPending && revealMutation.variables === field.path}
            modelOptions={modelOptions}
            discoveringProviderID={discoveringProviderID}
            discoveringModels={discoveringModels}
            onDiscoverModels={onDiscoverModels}
            onReveal={() => revealMutation.mutate(field.path)}
            onClearSecret={() => onClearSecret(field.path)}
          />
        ))}
      </div>
      <div className="browser-selector-demo">
        <Typography.Text strong>{t("settings.selectorDemo")}</Typography.Text>
        <Typography.Text type="secondary">{demo.selectorSummary}</Typography.Text>
        <pre>{demo.markup}</pre>
      </div>
    </div>
  );
}

function browserEngineDemo(engine: string) {
  switch (engine) {
    case "bing":
      return {
        title: "Bing",
        description: "Defaults to https://www.bing.com/search?q={{query}}. Selectors can target Bing's result blocks when auto link scanning is noisy.",
        selectorSummary: "container: li.b_algo · link: h2 a · snippet: .b_caption p",
        markup: `<li class="b_algo">
  <h2><a href="https://example.com">Result title</a></h2>
  <div class="b_caption"><p>Snippet text</p></div>
</li>`,
      };
    case "brave":
      return {
        title: "Brave Search",
        description: "Defaults to Brave Search. Brave markup changes more often, so selectors are optional and fallback link scanning remains available.",
        selectorSummary: "container: div[data-testid=\"web-result\"] · link: a[href] · snippet: .snippet",
        markup: `<div data-testid="web-result">
  <a href="https://example.com">Result title</a>
  <div class="snippet">Snippet text</div>
</div>`,
      };
    case "custom":
      return {
        title: "Custom",
        description: "Use this for another search page. The URL template must produce an http/https page and include {{query}} or {{query_raw}}.",
        selectorSummary: "container: .result · link: h2 a · snippet: .summary",
        markup: `<article class="result">
  <h2><a href="https://example.com">Result title</a></h2>
  <p class="summary">Snippet text</p>
</article>`,
      };
    default:
      return {
        title: "DuckDuckGo",
        description: "Defaults to DuckDuckGo web search. Empty selectors usually work; explicit selectors can improve snippets.",
        selectorSummary: "container: .result · link: .result__a · snippet: .result__snippet",
        markup: `<div class="result">
  <a class="result__a" href="https://example.com">Result title</a>
  <a class="result__snippet">Snippet text</a>
</div>`,
      };
  }
}

function CompactConfigGroup(props: {
  title: string;
  items: ConfigFieldSchema[];
  fieldStates: Record<string, ConfigFieldState>;
  effectiveValues: Record<string, unknown>;
  clearSecrets: Record<string, boolean>;
  revealMutation: { isPending: boolean; variables?: string; mutate: (path: string) => void };
  modelOptions: ModelOption[];
  discoveringProviderID?: string;
  discoveringModels: boolean;
  onDiscoverModels: (id: string) => void;
  onClearSecret: (path: string) => void;
}) {
  const { title, items, fieldStates, effectiveValues, clearSecrets, revealMutation, modelOptions, discoveringProviderID, discoveringModels, onDiscoverModels, onClearSecret } = props;
  if (items.length === 0) {
    return null;
  }
  return (
    <div className="config-compact-panel">
      <Typography.Text strong>{title}</Typography.Text>
      <div className="config-compact-grid">
        {items.map((field) => (
          <CompactFieldEditor
            key={field.path}
            field={field}
            fieldState={fieldStates[field.path]}
            effectiveValue={effectiveValues[field.path]}
            clearSecret={clearSecrets[field.path] ?? false}
            revealPending={revealMutation.isPending && revealMutation.variables === field.path}
            modelOptions={modelOptions}
            discoveringProviderID={discoveringProviderID}
            discoveringModels={discoveringModels}
            onDiscoverModels={onDiscoverModels}
            onReveal={() => revealMutation.mutate(field.path)}
            onClearSecret={() => onClearSecret(field.path)}
          />
        ))}
      </div>
    </div>
  );
}

/** 把 description 中的 URL 渲染成可点击链接（voice-engine 仓库地址等）。 */
function DescriptionWithLinks({ text }: { text: string }) {
  // 仅匹配 http(s):// 开头的完整 URL，避免误伤其他文本。
  const parts = text.split(/(https?:\/\/[^\s]+)/g);
  return (
    <>
      {parts.map((part, i) =>
        /^https?:\/\//.test(part) ? (
          <a key={i} href={part} target="_blank" rel="noreferrer">
            {part}
          </a>
        ) : (
          <span key={i}>{part}</span>
        ),
      )}
    </>
  );
}

function FieldEditor(props: {
  field: ConfigFieldSchema;
  fieldState?: ConfigFieldState;
  effectiveValue?: unknown;
  clearSecret: boolean;
  revealPending: boolean;
  modelOptions: ModelOption[];
  discoveringProviderID?: string;
  discoveringModels: boolean;
  onDiscoverModels: (id: string) => void;
  onReveal: () => void;
  onClearSecret: () => void;
}) {
  const { field, fieldState, effectiveValue, clearSecret, revealPending, modelOptions, discoveringProviderID, discoveringModels, onDiscoverModels, onReveal, onClearSecret } = props;
  const { t } = useI18n();
  const isProvidersField = field.path === "api.providers";
  return (
    <Card size="small" title={field.label} extra={<FieldTags field={field} state={fieldState} />}>
      <Typography.Paragraph type="secondary">
        <DescriptionWithLinks text={field.description} />
      </Typography.Paragraph>
      <Form.Item name={field.path} valuePropName={field.type === "bool" ? "checked" : "value"} style={{ marginBottom: 8 }}>
        <ConfigFieldInput
          field={field}
          fieldState={fieldState}
          effectiveValue={effectiveValue}
          clearSecret={clearSecret}
          modelOptions={modelOptions}
          discoveringProviderID={discoveringProviderID}
          discoveringModels={discoveringModels}
          onDiscoverModels={onDiscoverModels}
        />
      </Form.Item>
      <Space direction="vertical" size={8} style={{ width: "100%" }}>
        {field.secret ? (
          <Space wrap>
            <Button size="small" icon={<EyeOutlined />} loading={revealPending} onClick={onReveal}>{t("settings.reveal")}</Button>
            {!isProvidersField ? <Button size="small" danger onClick={onClearSecret}>{t("settings.clear")}</Button> : null}
            {clearSecret && !isProvidersField ? <Tag color="red">{t("settings.willClearOnSave")}</Tag> : null}
          </Space>
        ) : null}
        <Typography.Text type="secondary">{t("settings.effectivePrefix")}{formatValue(effectiveValue)}</Typography.Text>
      </Space>
    </Card>
  );
}

function CompactFieldEditor(props: {
  field: ConfigFieldSchema;
  fieldState?: ConfigFieldState;
  effectiveValue?: unknown;
  clearSecret: boolean;
  revealPending: boolean;
  modelOptions: ModelOption[];
  discoveringProviderID?: string;
  discoveringModels: boolean;
  onDiscoverModels: (id: string) => void;
  onReveal: () => void;
  onClearSecret: () => void;
}) {
  const { field, fieldState, effectiveValue, clearSecret, revealPending, modelOptions, discoveringProviderID, discoveringModels, onDiscoverModels, onReveal, onClearSecret } = props;
  const { t } = useI18n();
  const isProvidersField = field.path === "api.providers";
  const wide = field.type === "json" || field.path.endsWith("search_url_template") || field.path.includes("selector");
  return (
    <div className={wide ? "config-compact-field config-compact-field-wide" : "config-compact-field"}>
      <div className="config-compact-field-header">
        <Typography.Text strong>{field.label}</Typography.Text>
        <FieldTags field={field} state={fieldState} />
      </div>
      <Typography.Text type="secondary" className="config-compact-description">
        <DescriptionWithLinks text={field.description} />
      </Typography.Text>
      <Form.Item name={field.path} valuePropName={field.type === "bool" ? "checked" : "value"} style={{ marginBottom: 4 }}>
        <ConfigFieldInput
          field={field}
          fieldState={fieldState}
          effectiveValue={effectiveValue}
          clearSecret={clearSecret}
          compact
          modelOptions={modelOptions}
          discoveringProviderID={discoveringProviderID}
          discoveringModels={discoveringModels}
          onDiscoverModels={onDiscoverModels}
        />
      </Form.Item>
      {field.secret ? (
        <Space wrap size={6}>
          <Button size="small" icon={<EyeOutlined />} loading={revealPending} onClick={onReveal}>{t("settings.reveal")}</Button>
          {!isProvidersField ? <Button size="small" danger onClick={onClearSecret}>{t("settings.clear")}</Button> : null}
          {clearSecret && !isProvidersField ? <Tag color="red">{t("settings.willClearOnSave")}</Tag> : null}
        </Space>
      ) : null}
      <Typography.Text type="secondary" className="config-compact-effective">{t("settings.effectivePrefix")}{formatValue(effectiveValue)}</Typography.Text>
    </div>
  );
}

function ConfigFieldInput(props: {
  field: ConfigFieldSchema;
  fieldState?: ConfigFieldState;
  effectiveValue?: unknown;
  clearSecret: boolean;
  compact?: boolean;
  value?: unknown;
  checked?: boolean;
  onChange?: (value: unknown) => void;
  modelOptions: ModelOption[];
  discoveringProviderID?: string;
  discoveringModels: boolean;
  onDiscoverModels: (id: string) => void;
}) {
  const { field, fieldState, effectiveValue, clearSecret, compact, value, checked, onChange, modelOptions, discoveringProviderID, discoveringModels, onDiscoverModels } = props;
  const { t } = useI18n();
  const isProvidersField = field.path === "api.providers";
  if (isProvidersField) {
    return <LLMProvidersEditor value={value} onChange={onChange as (value: LLMProvidersFormValue) => void} discoveringProviderID={discoveringProviderID} discoveringModels={discoveringModels} onDiscoverModels={onDiscoverModels} />;
  }
  if (field.path === "api.default_model") {
    return (
      <Select
        showSearch
        allowClear
        value={value as string | undefined}
        onChange={onChange}
        placeholder={t("settings.placeholderProviderModel")}
        options={modelOptionsWithCurrent(modelOptions, [asOptionalString(effectiveValue) ?? ""])}
        optionFilterProp="label"
      />
    );
  }
  if (field.path === "api.model_strategy") {
    return <LLMStrategyEditor value={value} onChange={onChange as (value: LLMStrategyFormValue) => void} modelOptions={modelOptions} />;
  }
  if (field.type === "json") {
    return (
      <Input.TextArea
        value={value as string | undefined}
        onChange={onChange}
        autoSize={{ minRows: field.path === "api.providers" || field.path === "api.model_strategy" || field.path === "acp.agents" ? 10 : 4, maxRows: 18 }}
        spellCheck={false}
        style={{ fontFamily: "ui-monospace, SFMono-Regular, Menlo, Monaco, Consolas, monospace" }}
      />
    );
  }
  if (field.secret) {
    return <Input.Password value={value as string | undefined} onChange={onChange} placeholder={fieldState?.configured ? t("settings.configuredReplace") : t("settings.notConfigured")} disabled={clearSecret} />;
  }
  if (field.type === "bool") {
    return <Switch checked={!!checked} onChange={onChange} />;
  }
  if (field.options?.length) {
    return <Select value={value as string | undefined} onChange={onChange} options={field.options.map((option) => ({ value: option, label: option }))} />;
  }
  if (field.type === "int") {
    return <InputNumber value={value as number | undefined} onChange={onChange} style={{ width: "100%" }} />;
  }
  if (field.type === "float") {
    return <InputNumber step={0.05} value={value as number | undefined} onChange={onChange} style={{ width: "100%" }} />;
  }
  if (field.type === "string_list") {
    const values = Array.isArray(value) ? value.map(String) : String(value ?? "").split(",").map((part) => part.trim()).filter(Boolean);
    return <Select mode="tags" value={values} onChange={onChange} tokenSeparators={[","]} open={compact ? false : undefined} placeholder={t("settings.placeholderCommaValues")} />;
  }
  return <Input value={value as string | undefined} onChange={onChange} />;
}

function FieldTags({ field, state }: { field: ConfigFieldSchema; state?: ConfigFieldState }) {
  const { t } = useI18n();
  return (
    <Space wrap size={4}>
      <Tag>{state?.source || t("settings.sourceUnknown")}</Tag>
      {state?.overridden_by ? <Tag color="gold">{t("settings.overriddenBy", { name: state.overridden_by })}</Tag> : null}
      {field.live_apply ? <Tag color="green">{t("settings.liveApply")}</Tag> : <Tag>{t("settings.saveOnly")}</Tag>}
      {field.secret ? <Tag color={state?.configured ? "green" : "default"}>{state?.configured ? t("settings.configuredTag") : t("settings.notSetTag")}</Tag> : null}
    </Space>
  );
}

function LLMProvidersEditor({
  value,
  onChange,
  discoveringProviderID,
  discoveringModels,
  onDiscoverModels,
}: {
  value?: unknown;
  onChange?: (value: LLMProvidersFormValue) => void;
  discoveringProviderID?: string;
  discoveringModels: boolean;
  onDiscoverModels: (id: string) => void;
}) {
  const providers = providersConfigToForm(value);
  const { t } = useI18n();
  const emit = (items: LLMProviderFormItem[]) => onChange?.({ items });
  const updateProvider = (index: number, patch: Partial<LLMProviderFormItem>) => {
    emit(providers.items.map((item, itemIndex) => itemIndex === index ? { ...item, ...patch } : item));
  };
  const updateModel = (providerIndex: number, modelIndex: number, patch: Partial<LLMModelFormItem>) => {
    emit(providers.items.map((provider, itemIndex) => {
      if (itemIndex !== providerIndex) {
        return provider;
      }
      return {
        ...provider,
        models: provider.models.map((model, idx) => idx === modelIndex ? { ...model, ...patch } : model),
      };
    }));
  };
  const removeProvider = (index: number) => emit(providers.items.filter((_, itemIndex) => itemIndex !== index));
  const removeModel = (providerIndex: number, modelIndex: number) => {
    emit(providers.items.map((provider, itemIndex) => itemIndex === providerIndex ? {
      ...provider,
      models: provider.models.filter((_, idx) => idx !== modelIndex),
    } : provider));
  };
  const addProvider = () => {
    emit([...providers.items, {
      id: nextUniqueID("provider", providers.items.map((item) => item.id)),
      name: "",
      type: "anthropic_compatible",
      base_url: "",
      api_key: "",
      api_key_env: "",
      credential_kind: "api-key",
      timeout_seconds: 600,
      models: [],
    }]);
  };
  const addModel = (providerIndex: number) => {
    const provider = providers.items[providerIndex];
    updateProvider(providerIndex, {
      models: [...provider.models, {
        id: nextUniqueID("model", provider.models.map((model) => model.id)),
        name: "",
        model: "",
        max_tokens: 4096,
        supports_streaming: true,
        supports_vision: false,
        tags: "",
      }],
    });
  };

  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      {providers.items.length === 0 ? <Alert type="info" showIcon message={t("settings.noProviders")} /> : null}
      <Collapse
        className="llm-provider-collapse"
        defaultActiveKey={[]}
        items={providers.items.map((provider, providerIndex) => {
          const apiKeyConfigured = stringsPresent(provider.api_key) || provider.api_key === SECRET_MASK;
          return {
            key: provider.id || String(providerIndex),
            label: (
              <span className="llm-provider-collapse-label">
                <Typography.Text strong>{provider.id || t("settings.unnamedProvider")}</Typography.Text>
                <Typography.Text type="secondary">{provider.type || "anthropic_compatible"}</Typography.Text>
                <Tag color={provider.models.length > 0 ? "blue" : "default"}>
                  {t("settings.modelsLabel")} {provider.models.length}
                </Tag>
                <Tag color={apiKeyConfigured ? "green" : "gold"}>
                  {apiKeyConfigured ? t("settings.credentialPresent") : t("settings.credentialMissing")}
                </Tag>
              </span>
            ),
            extra: (
              <Button
                danger
                size="small"
                icon={<DeleteOutlined />}
                onClick={(event) => {
                  event.stopPropagation();
                  removeProvider(providerIndex);
                }}
              >
                {t("settings.remove")}
              </Button>
            ),
            children: (
              <div className="llm-provider-panel">
                <div className="llm-form-grid">
                  <LabelledControl label={t("settings.providerIDLabel")}>
                    <Input value={provider.id} placeholder="anthropic" onChange={(event) => updateProvider(providerIndex, { id: event.target.value })} />
                  </LabelledControl>
                  <LabelledControl label={t("settings.name")}>
                    <Input value={provider.name} placeholder="Anthropic" onChange={(event) => updateProvider(providerIndex, { name: event.target.value })} />
                  </LabelledControl>
                  <LabelledControl label={t("settings.protocolType")}>
                    <Select
                      value={provider.type || "anthropic_compatible"}
                      options={[
                        { value: "anthropic_compatible", label: "Anthropic compatible" },
                        { value: "openai_compatible", label: "OpenAI compatible" },
                        { value: "openai_responses", label: "OpenAI Responses" },
                        { value: "openai_codex", label: "OpenAI Codex OAuth" },
                      ]}
                      onChange={(type) => updateProvider(providerIndex, { type })}
                    />
                  </LabelledControl>
                  <LabelledControl label={t("settings.timeoutSeconds")}>
                    <InputNumber min={1} style={{ width: "100%" }} value={provider.timeout_seconds} onChange={(timeout) => updateProvider(providerIndex, { timeout_seconds: numberOrUndefined(timeout) })} />
                  </LabelledControl>
                  <LabelledControl label={t("settings.baseURL")} wide>
                    <Input value={provider.base_url} placeholder="https://api.example.com" onChange={(event) => updateProvider(providerIndex, { base_url: event.target.value })} />
                  </LabelledControl>
                  <LabelledControl label={t("settings.apiKeyEnv")}>
                    <Input value={provider.api_key_env} placeholder="OPENAI_API_KEY" onChange={(event) => updateProvider(providerIndex, { api_key_env: event.target.value })} />
                  </LabelledControl>
                  <LabelledControl label={t("settings.credentialKind")}>
                    <Select
                      value={provider.credential_kind || "api-key"}
                      options={[
                        { value: "api-key", label: "API key" },
                        { value: "codex-oauth", label: "Codex OAuth" },
                        { value: "oauth-token", label: "OAuth token" },
                      ]}
                      onChange={(credential_kind) => updateProvider(providerIndex, { credential_kind })}
                    />
                  </LabelledControl>
                  <LabelledControl label={t("settings.apiKey")} wide>
                    <Space.Compact style={{ width: "100%" }}>
                      <Input.Password
                        value={provider.api_key === SECRET_MASK ? "" : provider.api_key}
                        placeholder={apiKeyConfigured ? t("settings.configuredReplace") : t("settings.notConfigured")}
                        onChange={(event) => updateProvider(providerIndex, { api_key: event.target.value })}
                      />
                      <Button danger onClick={() => updateProvider(providerIndex, { api_key: "" })}>{t("settings.clearKey")}</Button>
                    </Space.Compact>
                  </LabelledControl>
                </div>
                <div className="llm-models-block">
                  <div className="llm-panel-header">
                    <Typography.Text strong>{t("settings.modelsLabel")}</Typography.Text>
                    <Space size={8}>
                      <Button
                        size="small"
                        icon={<ReloadOutlined />}
                        disabled={!stringsPresent(provider.id)}
                        loading={discoveringModels && discoveringProviderID === provider.id}
                        onClick={() => onDiscoverModels(provider.id)}
                      >
                        {t("settings.fetchModels")}
                      </Button>
                      <Button size="small" icon={<PlusOutlined />} onClick={() => addModel(providerIndex)}>{t("settings.addModel")}</Button>
                    </Space>
                  </div>
                  <Space direction="vertical" size={8} style={{ width: "100%" }}>
                    {provider.models.length === 0 ? <Typography.Text type="secondary">{t("settings.noModels")}</Typography.Text> : null}
                    {provider.models.map((model, modelIndex) => (
                      <div className="llm-model-row" key={`provider-${providerIndex}-model-${modelIndex}`}>
                        <LabelledControl label={t("settings.modelIDLabel")}>
                          <Input value={model.id} placeholder="sonnet" onChange={(event) => updateModel(providerIndex, modelIndex, { id: event.target.value })} />
                        </LabelledControl>
                        <LabelledControl label={t("settings.name")}>
                          <Input value={model.name} placeholder="Claude Sonnet" onChange={(event) => updateModel(providerIndex, modelIndex, { name: event.target.value })} />
                        </LabelledControl>
                        <LabelledControl label={t("settings.actualModelLabel")}>
                          <Input value={model.model} placeholder="claude-sonnet-4-20250514" onChange={(event) => updateModel(providerIndex, modelIndex, { model: event.target.value })} />
                        </LabelledControl>
                        <LabelledControl label={t("settings.maxTokensLabel")}>
                          <InputNumber min={1} style={{ width: "100%" }} value={model.max_tokens} onChange={(tokens) => updateModel(providerIndex, modelIndex, { max_tokens: numberOrUndefined(tokens) })} />
                        </LabelledControl>
                        <LabelledControl label={t("settings.contextWindowLabel")}>
                          <InputNumber
                            min={1}
                            style={{ width: "100%" }}
                            placeholder={t("settings.defaultContextPlaceholder")}
                            value={model.context_window_tokens}
                            onChange={(tokens) => updateModel(providerIndex, modelIndex, { context_window_tokens: numberOrUndefined(tokens) })}
                          />
                        </LabelledControl>
                        <LabelledControl label={t("settings.streamingLabel")}>
                          <Switch checked={model.supports_streaming !== false} onChange={(supports_streaming) => updateModel(providerIndex, modelIndex, { supports_streaming })} />
                        </LabelledControl>
                        <LabelledControl label={t("settings.visionLabel")}>
                          <Switch checked={!!model.supports_vision} onChange={(supports_vision) => updateModel(providerIndex, modelIndex, { supports_vision })} />
                        </LabelledControl>
                        <LabelledControl label={t("settings.reasoningEffortLabel")}>
                          <Select
                            allowClear
                            placeholder="default"
                            value={model.reasoning_effort || undefined}
                            onChange={(reasoning_effort) => updateModel(providerIndex, modelIndex, { reasoning_effort })}
                            options={reasoningEffortOptions}
                          />
                        </LabelledControl>
                        <Button danger icon={<DeleteOutlined />} onClick={() => removeModel(providerIndex, modelIndex)}>{t("settings.removeModel")}</Button>
                        <LabelledControl label={t("settings.tagsLabel")} wide>
                          <Input value={model.tags} placeholder="coding,fast" onChange={(event) => updateModel(providerIndex, modelIndex, { tags: event.target.value })} />
                        </LabelledControl>
                      </div>
                    ))}
                  </Space>
                </div>
              </div>
            ),
          };
        })}
      />
      <Button icon={<PlusOutlined />} onClick={addProvider}>{t("settings.addProvider")}</Button>
    </Space>
  );
}

function LLMStrategyEditor({ value, onChange, modelOptions }: { value?: unknown; onChange?: (value: LLMStrategyFormValue) => void; modelOptions: ModelOption[] }) {
  const strategy = strategyConfigToForm(value);
  const { t } = useI18n();
  const options = modelOptionsWithCurrent(modelOptions, strategy.candidates);
  const emit = (next: LLMStrategyFormValue) => onChange?.(next);
  const updateCandidate = (index: number, candidate: string) => {
    emit({ ...strategy, candidates: strategy.candidates.map((item, itemIndex) => itemIndex === index ? candidate : item) });
  };
  const removeCandidate = (index: number) => {
    emit({ ...strategy, candidates: strategy.candidates.filter((_, itemIndex) => itemIndex !== index) });
  };
  const moveCandidate = (index: number, direction: -1 | 1) => {
    const nextIndex = index + direction;
    if (nextIndex < 0 || nextIndex >= strategy.candidates.length) {
      return;
    }
    const candidates = [...strategy.candidates];
    [candidates[index], candidates[nextIndex]] = [candidates[nextIndex], candidates[index]];
    emit({ ...strategy, candidates });
  };
  const addCandidate = () => {
    emit({ ...strategy, candidates: [...strategy.candidates, options[0]?.value ?? ""] });
  };

  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      <div className="llm-form-grid">
        <LabelledControl label={t("settings.strategyType")}>
          <Select
            value={strategy.type}
            options={[
              { value: "primary", label: t("settings.primaryOnly") },
              { value: "fallback", label: t("settings.fallbackInOrder") },
              { value: "round_robin", label: t("settings.roundRobin") },
            ]}
            onChange={(type) => emit({ ...strategy, type })}
          />
        </LabelledControl>
      </div>
      <Space direction="vertical" size={8} style={{ width: "100%" }}>
        {strategy.candidates.length === 0 ? <Typography.Text type="secondary">{t("settings.noCandidates")}</Typography.Text> : null}
        {strategy.candidates.map((candidate, index) => (
          <div className="llm-strategy-row" key={`${candidate || "candidate"}-${index}`}>
            <Select
              showSearch
              value={candidate || undefined}
              placeholder={t("settings.placeholderProviderModel")}
              options={options}
              optionFilterProp="label"
              onChange={(next) => updateCandidate(index, next)}
            />
            <Button aria-label={t("settings.moveUp")} icon={<ArrowUpOutlined />} disabled={index === 0} onClick={() => moveCandidate(index, -1)} />
            <Button
              aria-label={t("settings.moveDown")}
              icon={<ArrowDownOutlined />}
              disabled={index === strategy.candidates.length - 1}
              onClick={() => moveCandidate(index, 1)}
            />
            <Button danger aria-label={t("settings.removeCandidate")} icon={<DeleteOutlined />} onClick={() => removeCandidate(index)} />
          </div>
        ))}
      </Space>
      <Button icon={<PlusOutlined />} onClick={addCandidate}>{t("settings.addCandidate")}</Button>
    </Space>
  );
}

function LabelledControl({ label, wide, children }: { label: string; wide?: boolean; children: ReactNode }) {
  return (
    <label className={wide ? "llm-form-field llm-form-field-wide" : "llm-form-field"}>
      <Typography.Text type="secondary">{label}</Typography.Text>
      {children}
    </label>
  );
}

function ProvidersPanel({
  providers,
  loading,
  testing,
  testingID,
  onTest,
}: {
  providers: ProviderStatus[];
  loading: boolean;
  testing: boolean;
  testingID?: string;
  onTest: (id: string) => void;
}) {
  const { t } = useI18n();
  return (
    <Card title={t("settings.providersTitle")}>
      <Table<ProviderStatus>
        rowKey="id"
        size="small"
        loading={loading}
        dataSource={providers}
        scroll={{ x: 820 }}
        columns={[
          { title: t("settings.providerCol"), dataIndex: "id", render: (_value, row) => <Space><Typography.Text strong>{row.id}</Typography.Text>{row.name ? <Typography.Text type="secondary">{row.name}</Typography.Text> : null}</Space> },
          { title: t("settings.typeCol"), dataIndex: "type", render: (value) => <Tag>{value}</Tag> },
          { title: t("settings.credentialCol"), render: (_value, row) => <Space><Tag color={row.has_credential ? "green" : "default"}>{row.has_credential ? t("settings.credentialPresent") : t("settings.credentialMissing")}</Tag><Typography.Text type="secondary">{row.credential_kind || "-"}</Typography.Text></Space> },
          { title: t("settings.envCol"), dataIndex: "api_key_env", render: (value) => value || "-" },
          { title: t("settings.accountCol"), dataIndex: "account_id", render: (value) => value || "-" },
          { title: t("settings.lastErrorCol"), dataIndex: "last_test_error", render: (value) => value ? <Typography.Text type="danger">{value}</Typography.Text> : "-" },
          { title: t("settings.actionCol"), render: (_value, row) => <Button size="small" loading={testing && testingID === row.id} onClick={() => onTest(row.id)}>{t("settings.testAction")}</Button> },
        ]}
      />
    </Card>
  );
}

function WeixinPanel({
  auth,
  runtime,
  pending,
  starting,
  loggingOut,
  onStart,
  onLogout,
}: {
  auth?: WeixinAuthStatus;
  runtime?: ChannelStatus;
  pending: boolean;
  starting: boolean;
  loggingOut: boolean;
  onStart: () => void;
  onLogout: () => void;
}) {
  const login = auth?.login;
  const { t } = useI18n();
  const qrInput = resolveWeixinQRInput(login?.qr_code_img_url, login?.qr_code_img_value, login?.qr_code);
  return (
    <Card
      title={t("settings.weixinTitle")}
      extra={
        <Space wrap>
          <Button icon={<QrcodeOutlined />} loading={starting} onClick={onStart}>{t("settings.startLogin")}</Button>
          <Button icon={<LogoutOutlined />} loading={loggingOut} onClick={onLogout}>{t("settings.logout")}</Button>
        </Space>
      }
    >
      <Space direction="vertical" size={16} style={{ width: "100%" }}>
        <Descriptions bordered size="small" column={{ xs: 1, md: 2 }} items={[
          { key: "account", label: t("settings.account"), children: auth?.account_id ?? "default" },
          { key: "enabled", label: t("settings.enabled"), children: auth?.enabled ? t("settings.yes") : t("settings.no") },
          { key: "configured", label: t("settings.configured"), children: auth?.configured ? t("settings.yes") : t("settings.no") },
          { key: "runtime", label: t("settings.runtimeState"), children: runtime ? `${runtime.running ? t("settings.channelRunning") : t("settings.channelIdle")} / ${runtime.state ?? "unknown"}` : pending ? "loading" : "unknown" },
          { key: "bot", label: t("settings.botID"), children: auth?.account?.ilink_bot_id ?? "-" },
          { key: "user", label: t("settings.userID"), children: auth?.account?.ilink_user_id ?? "-" },
        ]} />
        {login?.message || runtime?.detail ? <Alert type="info" showIcon message={login?.message || runtime?.detail} /> : null}
        {runtime?.last_error ? <Alert type="error" showIcon message={runtime.last_error} /> : null}
        {login?.active || qrInput ? (
          <Card size="small" title={t("settings.scanInWeixin")}>
            <div style={{ display: "grid", placeItems: "center", minHeight: 260 }}>
              {qrInput ? renderQRCode(qrInput, t) : <Typography.Text type="secondary">{t("settings.qrNotReady")}</Typography.Text>}
            </div>
          </Card>
        ) : null}
      </Space>
    </Card>
  );
}

function DoctorPanel({ checks, loading }: { checks: DoctorCheck[]; loading: boolean }) {
  const { t } = useI18n();
  return (
    <Card title={t("settings.doctorTitle")}>
      <Table<DoctorCheck>
        rowKey={(record) => `${record.code}:${record.path ?? ""}:${record.message}`}
        size="small"
        loading={loading}
        dataSource={checks}
        scroll={{ x: 720 }}
        columns={[
          { title: t("settings.severityCol"), dataIndex: "severity", render: (value) => <Tag color={value === "error" ? "red" : value === "warning" ? "gold" : "blue"}>{value}</Tag> },
          { title: t("settings.codeCol"), dataIndex: "code" },
          { title: t("settings.pathCol"), dataIndex: "path", render: (value) => value || "-" },
          { title: t("settings.messageCol"), dataIndex: "message" },
          { title: t("settings.suggestionCol"), dataIndex: "suggestion", render: (value) => value || "-" },
        ]}
      />
    </Card>
  );
}

function SecurityPanel({
  summary,
  audit,
  packageQuality,
  loading,
}: {
  summary?: CIKSummary;
  audit: SecurityEvent[];
  packageQuality?: PackageQualityReport;
  loading: boolean;
}) {
  const { t } = useI18n();
  const riskItems = summary ? [summary.capability, summary.identity, summary.knowledge] : [];
  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      <div className="stat-grid">
        {riskItems.map((item) => (
          <Card key={item.axis} loading={loading}>
            <Space direction="vertical" size={4}>
              <Typography.Text type="secondary">{item.axis.toUpperCase()}</Typography.Text>
              <Space>
                <Tag color={item.level === "high" ? "red" : item.level === "medium" ? "gold" : "green"}>{item.level}</Tag>
                <Typography.Title level={3} style={{ margin: 0 }}>{item.score}</Typography.Title>
              </Space>
              <Typography.Text type="secondary">{(item.items ?? []).slice(0, 4).join(", ") || "-"}</Typography.Text>
            </Space>
          </Card>
        ))}
      </div>
      <Card title={t("settings.effectivePolicy")} loading={loading}>
        <Descriptions
          bordered
          size="small"
          column={{ xs: 1, md: 2 }}
          items={Object.entries(summary?.policy ?? {}).map(([key, value]) => ({
            key,
            label: key,
            children: Array.isArray(value) ? value.join(", ") || "-" : String(value),
          }))}
        />
      </Card>
      <Card title={t("settings.packageRisk")} loading={loading}>
        <Descriptions
          bordered
          size="small"
          column={{ xs: 1, md: 4 }}
          items={[
            { key: "packages", label: t("settings.packages"), children: packageQuality?.package_count ?? 0 },
            { key: "high", label: t("settings.highRisk"), children: <Tag color={(packageQuality?.high_risk_packages ?? 0) > 0 ? "red" : "green"}>{packageQuality?.high_risk_packages ?? 0}</Tag> },
            { key: "runs", label: t("settings.toolRuns"), children: packageQuality?.tool_health.total_runs ?? 0 },
            { key: "rate", label: t("settings.successRate"), children: `${Math.round(packageQuality?.tool_health.success_rate ?? 0)}%` },
          ]}
        />
      </Card>
      <Card title={t("settings.recentAudit")}>
        <Table<SecurityEvent>
          rowKey="id"
          size="small"
          loading={loading}
          dataSource={audit}
          pagination={{ pageSize: 8 }}
          scroll={{ x: 760 }}
          columns={[
            { title: t("settings.timeCol"), dataIndex: "at", render: formatTimestamp },
            { title: t("settings.axisCol"), dataIndex: "category", render: (value) => <Tag>{value}</Tag> },
            { title: t("settings.actionCol"), dataIndex: "action" },
            { title: t("settings.severityCol"), dataIndex: "severity", render: (value) => <Tag color={value === "warning" ? "gold" : value === "error" ? "red" : "blue"}>{value || "info"}</Tag> },
            { title: t("settings.summaryCol"), dataIndex: "summary", render: (value) => value || "-" },
          ]}
        />
      </Card>
    </Space>
  );
}

function RuntimeServiceCard({
  status,
  loading,
  reloading,
  restarting,
  onReload,
  onRestart,
}: {
  status?: RuntimeServiceStatus;
  loading?: boolean;
  reloading?: boolean;
  restarting?: boolean;
  onReload: () => void;
  onRestart: () => void;
}) {
  const { t } = useI18n();
  const managed = status?.managed === true;
  return (
    <Card
      title={t("settings.runtimeServiceTitle")}
      extra={
        <Space>
          <Button icon={<ReloadOutlined />} aria-label={t("settings.reloadFromDisk")} loading={reloading} onClick={onReload}>
            {t("settings.reloadFromDisk")}
          </Button>
          <Button danger icon={<ReloadOutlined />} aria-label={t("settings.restartService")} loading={restarting} disabled={!managed} onClick={onRestart}>
            {t("settings.restartService")}
          </Button>
        </Space>
      }
    >
      <Descriptions
        bordered
        size="small"
        column={{ xs: 1, md: 2 }}
        items={[
          { key: "managed", label: t("settings.managedService"), children: loading ? "Loading..." : managed ? <Tag color="green">{t("settings.yes")}</Tag> : <Tag>{t("settings.no")}</Tag> },
          { key: "running", label: t("settings.running"), children: status?.running ? <Tag color="green">{t("settings.yes")}</Tag> : <Tag>{t("settings.unknown")}</Tag> },
          { key: "scope", label: t("settings.scope"), children: status?.scope ?? "-" },
          { key: "name", label: t("settings.name"), children: status?.name ?? "-" },
          { key: "service-file", label: t("settings.serviceFile"), children: status?.service_file ?? "-" },
          { key: "log-file", label: t("settings.logFile"), children: status?.log_file ?? "-" },
        ]}
      />
      {managed ? null : (
        <Alert
          style={{ marginTop: 12 }}
          type="info"
          showIcon
          message={status?.detail || t("settings.restartViaService")}
        />
      )}
    </Card>
  );
}

function ApplyReportView({ report, configInSync }: { report?: ApplyReport; configInSync?: boolean }) {
  const { t } = useI18n();
  if (!report && configInSync !== false) {
    return null;
  }
  return (
    <Space direction="vertical" size={8} style={{ width: "100%", marginTop: 12 }}>
      {configInSync === false ? <Alert type="warning" showIcon message={t("settings.configDrift")} /> : null}
      {report ? (
        <Alert
          type={report.runtime_status === "failed" || report.storage_status === "save_failed" ? "error" : "info"}
          showIcon
          message={report.message || t("settings.lastApplyReport")}
          description={[...(report.warnings ?? []), ...(report.errors ?? [])].join(" ")}
        />
      ) : null}
    </Space>
  );
}

function buildSaveValues(values: ConfigFormValues, sections: ConfigSectionSchema[]) {
  const result: Record<string, unknown> = {};
  const fields = sections.flatMap((section) => section.fields);
  for (const field of fields) {
    if (API_HIDDEN_PATHS.has(field.path)) {
      continue;
    }
    const value = values[field.path];
    if (value === undefined) {
      continue;
    }
    if (field.path === "api.providers") {
      result[field.path] = providersFormToConfig(value);
      continue;
    }
    if (field.path === "api.model_strategy") {
      result[field.path] = strategyFormToConfig(value);
      continue;
    }
    if (field.secret && (value === undefined || String(value).trim() === "")) {
      continue;
    }
    if (field.type === "string_list") {
      result[field.path] = Array.isArray(value) ? value : String(value ?? "").split(",").map((part) => part.trim()).filter(Boolean);
      continue;
    }
    result[field.path] = value;
  }
  return result;
}

function formValuesFromConfig(values: Record<string, unknown>, sections: ConfigSectionSchema[]) {
  const result: Record<string, unknown> = { ...(values ?? {}) };
  const fields = sections.flatMap((section) => section.fields);
  for (const field of fields) {
    if (field.path === "api.providers") {
      result[field.path] = providersConfigToForm(result[field.path]);
      continue;
    }
    if (field.path === "api.model_strategy") {
      result[field.path] = strategyConfigToForm(result[field.path]);
      continue;
    }
    if (field.type === "json" && result[field.path] !== undefined && typeof result[field.path] !== "string") {
      result[field.path] = JSON.stringify(result[field.path], null, 2);
    }
  }
  return result;
}

function providersConfigToForm(value: unknown): LLMProvidersFormValue {
  if (isProvidersFormValue(value)) {
    return {
      items: value.items.map((item) => ({
        ...item,
        models: [...(item.models ?? [])],
      })),
    };
  }
  const raw = parseJSONValue(value);
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return { items: [] };
  }
  const items = Object.entries(raw as Record<string, Record<string, unknown>>).map(([id, provider]) => {
    const rawModels = provider.models && typeof provider.models === "object" && !Array.isArray(provider.models)
      ? provider.models as Record<string, Record<string, unknown>>
      : {};
    return {
      id,
      name: asOptionalString(provider.name),
      type: asOptionalString(provider.type) || "anthropic_compatible",
      base_url: asOptionalString(provider.base_url),
      api_key: asOptionalString(provider.api_key),
      api_key_env: asOptionalString(provider.api_key_env),
      credential_kind: asOptionalString(provider.credential_kind),
      timeout_seconds: asOptionalNumber(provider.timeout_seconds),
      models: Object.entries(rawModels).map(([modelID, model]) => ({
        id: modelID,
        name: asOptionalString(model.name),
        model: asOptionalString(model.model),
        max_tokens: asOptionalNumber(model.max_tokens),
        context_window_tokens: asOptionalNumber(model.context_window_tokens),
        supports_streaming: asOptionalBool(model.supports_streaming, true),
        supports_vision: asOptionalBool(model.supports_vision, false),
        reasoning_effort: asOptionalString(model.reasoning_effort),
        tags: Array.isArray(model.tags) ? model.tags.map(String).join(",") : asOptionalString(model.tags),
      })),
    };
  });
  return { items };
}

function providersFormToConfig(value: unknown) {
  const form = providersConfigToForm(value);
  return Object.fromEntries(form.items.filter((provider) => stringsPresent(provider.id)).map((provider) => {
    const models = Object.fromEntries((provider.models ?? []).filter((model) => stringsPresent(model.id)).map((model) => [model.id.trim(), {
      name: model.name ?? "",
      model: model.model ?? "",
      max_tokens: model.max_tokens ?? 0,
      context_window_tokens: model.context_window_tokens ?? 0,
      supports_streaming: model.supports_streaming !== false,
      supports_vision: !!model.supports_vision,
      reasoning_effort: model.reasoning_effort || "",
      tags: splitTags(model.tags),
    }]));
    return [provider.id.trim(), {
      name: provider.name ?? "",
      type: provider.type || "anthropic_compatible",
      base_url: provider.base_url ?? "",
      api_key: provider.api_key ?? "",
      api_key_env: provider.api_key_env ?? "",
      credential_kind: provider.credential_kind ?? "",
      timeout_seconds: provider.timeout_seconds ?? 0,
      models,
    }];
  }));
}

function mergeDiscoveredModels(existing: LLMModelFormItem[], discovered: ProviderModelInfo[]) {
  const next = [...(existing ?? [])];
  const indexByID = new Map<string, number>();
  next.forEach((model, index) => {
    const id = (model.id || "").trim();
    if (id) {
      indexByID.set(id, index);
    }
  });
  for (const item of discovered) {
    const id = (item.id || item.model || "").trim();
    if (!id) {
      continue;
    }
    const model = (item.model || item.id || "").trim();
    const name = (item.name || id).trim();
    const existingIndex = indexByID.get(id);
    if (existingIndex === undefined) {
      indexByID.set(id, next.length);
      next.push({
        id,
        name,
        model,
        supports_streaming: item.supports_streaming !== false,
      });
      continue;
    }
    const current = next[existingIndex];
    next[existingIndex] = {
      ...current,
      name: current.name || name,
      model: current.model || model,
      supports_streaming: current.supports_streaming ?? (item.supports_streaming !== false),
    };
  }
  return next;
}

function strategyConfigToForm(value: unknown): LLMStrategyFormValue {
  if (isStrategyFormValue(value)) {
    return { type: normalizeStrategyType(value.type), candidates: value.candidates.map(modelRefText).filter(Boolean) };
  }
  const raw = parseJSONValue(value);
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return { type: "fallback", candidates: [] };
  }
  const record = raw as Record<string, unknown>;
  const type = normalizeStrategyType(record.type);
  const candidates = Array.isArray(record.candidates)
    ? record.candidates.map(modelRefText).filter(Boolean)
    : [];
  return { type, candidates };
}

function modelRefText(candidate: unknown): string {
  if (typeof candidate === "string") {
    return candidate;
  }
  if (!candidate || typeof candidate !== "object") {
    return "";
  }
  const candidateRecord = candidate as Record<string, unknown>;
  const provider = asOptionalString(candidateRecord.provider);
  const model = asOptionalString(candidateRecord.model);
  return provider && model ? `${provider}.${model}` : "";
}

function strategyFormToConfig(value: unknown) {
  const strategy = strategyConfigToForm(value);
  return {
    type: strategy.type,
    candidates: strategy.candidates.map((candidate) => parseModelRef(candidate)).filter(Boolean),
  };
}

function llmModelOptions(value: unknown): ModelOption[] {
  return providersConfigToForm(value).items.flatMap((provider) => provider.models.map((model) => {
    const value = provider.id && model.id ? `${provider.id}.${model.id}` : "";
    const modelLabel = model.name || model.model || model.id || "model";
    const providerLabel = provider.name || provider.id || "provider";
    return {
      value,
      label: `${value || "provider.model"} (${providerLabel} / ${modelLabel})`,
    };
  })).filter((option) => stringsPresent(option.value));
}

function modelOptionsWithCurrent(options: ModelOption[], candidates: string[]): ModelOption[] {
  const seen = new Set(options.map((option) => option.value));
  const out = [...options];
  for (const candidate of candidates) {
    if (stringsPresent(candidate) && !seen.has(candidate)) {
      out.push({ value: candidate, label: `${candidate} (missing)` });
      seen.add(candidate);
    }
  }
  return out;
}

function parseJSONValue(value: unknown): unknown {
  if (typeof value !== "string") {
    return value;
  }
  const trimmed = value.trim();
  if (!trimmed) {
    return undefined;
  }
  try {
    return JSON.parse(trimmed);
  } catch {
    return value;
  }
}

function isProvidersFormValue(value: unknown): value is LLMProvidersFormValue {
  return !!value && typeof value === "object" && Array.isArray((value as LLMProvidersFormValue).items);
}

function isStrategyFormValue(value: unknown): value is LLMStrategyFormValue {
  return !!value && typeof value === "object" && Array.isArray((value as LLMStrategyFormValue).candidates);
}

function normalizeStrategyType(value: unknown): LLMStrategyFormValue["type"] {
  return value === "primary" || value === "round_robin" ? value : "fallback";
}

function parseModelRef(value: string) {
  const [provider, ...modelParts] = String(value || "").split(".");
  const model = modelParts.join(".");
  if (!provider.trim() || !model.trim()) {
    return null;
  }
  return { provider: provider.trim(), model: model.trim() };
}

function asOptionalString(value: unknown): string | undefined {
  return value === undefined || value === null ? undefined : String(value);
}

function asOptionalNumber(value: unknown): number | undefined {
  if (typeof value === "number") {
    return Number.isFinite(value) ? value : undefined;
  }
  if (typeof value === "string" && value.trim() !== "") {
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : undefined;
  }
  return undefined;
}

function asOptionalBool(value: unknown, fallback: boolean): boolean {
  return typeof value === "boolean" ? value : fallback;
}

function numberOrUndefined(value: string | number | null): number | undefined {
  if (typeof value === "number") {
    return value;
  }
  if (typeof value === "string" && value.trim() !== "") {
    const parsed = Number(value);
    return Number.isFinite(parsed) ? parsed : undefined;
  }
  return undefined;
}

function splitTags(value?: string): string[] {
  return String(value ?? "").split(",").map((item) => item.trim()).filter(Boolean);
}

function nextUniqueID(base: string, ids: string[]) {
  const seen = new Set(ids.filter(Boolean));
  if (!seen.has(base)) {
    return base;
  }
  for (let index = 2; ; index += 1) {
    const candidate = `${base}_${index}`;
    if (!seen.has(candidate)) {
      return candidate;
    }
  }
}

function stringsPresent(value?: string) {
  return String(value ?? "").trim() !== "";
}

function renderQRCode(value: string, t: ReturnType<typeof useI18n>["t"]) {
  if (value.startsWith("data:image/") || value.startsWith("http://") || value.startsWith("https://")) {
    return <Image src={value} alt={t("settings.weixinQrAlt")} style={{ maxHeight: 280, objectFit: "contain" }} />;
  }
  return <QRCode value={value} size={260} />;
}

function resolveWeixinQRInput(url?: string, value?: string, token?: string) {
  const rawValue = value?.trim();
  if (rawValue) {
    if (rawValue.startsWith("data:image/")) {
      return rawValue;
    }
    if (rawValue.startsWith("<svg") || rawValue.startsWith("<?xml")) {
      return `data:image/svg+xml;utf8,${encodeURIComponent(rawValue)}`;
    }
    return rawValue;
  }
  return url?.trim() || token?.trim() || "";
}

/**
 * Convert flat dot-separated config values (e.g. {"web.token": "secret"})
 * into a nested object suitable for YAML serialization.
 * JSON-string values are parsed into objects/arrays where possible.
 */
function flatToNested(flat: Record<string, unknown>): Record<string, unknown> {
  const root: Record<string, unknown> = {};
  const keys = Object.keys(flat).sort();
  for (const dotKey of keys) {
    const parts = dotKey.split(".");
    let current = root;
    for (let i = 0; i < parts.length - 1; i++) {
      const part = parts[i];
      if (!(part in current) || typeof current[part] !== "object" || current[part] === null || Array.isArray(current[part])) {
        current[part] = {};
      }
      current = current[part] as Record<string, unknown>;
    }
    const last = parts[parts.length - 1];
    let value = flat[dotKey];
    // Try to parse JSON strings into objects/arrays
    if (typeof value === "string" && value.trim() !== "") {
      try {
        const parsed = JSON.parse(value);
        if (parsed !== null && typeof parsed === "object") {
          value = parsed;
        }
      } catch {
        // not JSON, keep as string
      }
    }
    current[last] = value;
  }
  return root;
}

function configToYaml(storedValues: Record<string, unknown>, effectiveValues: Record<string, unknown>): string {
  const storedNested = flatToNested(storedValues);
  const effectiveNested = flatToNested(effectiveValues);
  const hasDiff = JSON.stringify(storedNested) !== JSON.stringify(effectiveNested);

  if (!hasDiff) {
    // Remove empty keys from the result for cleaner output
    return toYaml(pruneEmpty(storedNested), { indent: 2, lineWidth: 120, sortMapEntries: true });
  }

  // Show stored config with effective overrides as comments
  const storedYaml = toYaml(storedNested, { indent: 2, lineWidth: 120, sortMapEntries: true });
  const effectiveYaml = toYaml(effectiveNested, { indent: 2, lineWidth: 120, sortMapEntries: true });
  return [
    "# --- stored config ---",
    storedYaml,
    "",
    "# --- effective config (with env/overrides) ---",
    effectiveYaml,
  ].join("\n");
}

function pruneEmpty(obj: Record<string, unknown>): Record<string, unknown> {
  const result: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(obj)) {
    if (value === undefined || value === null || value === "") {
      continue;
    }
    if (typeof value === "object" && !Array.isArray(value)) {
      const nested = pruneEmpty(value as Record<string, unknown>);
      if (Object.keys(nested).length > 0) {
        result[key] = nested;
      }
    } else if (Array.isArray(value) && value.length === 0) {
      continue;
    } else {
      result[key] = value;
    }
  }
  return result;
}

function ConfigYamlCard(props: {
  storedValues: Record<string, unknown>;
  effectiveValues: Record<string, unknown>;
  loading: boolean;
}) {
  const { message } = AntApp.useApp();
  const { t } = useI18n();
  const { storedValues, effectiveValues, loading } = props;
  const [copied, setCopied] = useState(false);

  const yamlText = useMemo(
    () => configToYaml(storedValues, effectiveValues),
    [effectiveValues, storedValues],
  );

  const handleCopy = async () => {
    try {
      await writeClipboardText(yamlText);
      setCopied(true);
      void message.success(t("settings.yamlCopied"));
      setTimeout(() => setCopied(false), 2000);
    } catch {
      void message.error(t("settings.yamlCopyFailed"));
    }
  };

  return (
    <Card
      title={t("settings.yamlViewTitle")}
      loading={loading}
      extra={
        <Button
          size="small"
          icon={<CopyOutlined />}
          onClick={() => void handleCopy()}
        >
          {copied ? t("settings.copied") : t("settings.copyYaml")}
        </Button>
      }
    >
      <pre
        style={{
          margin: 0,
          padding: 12,
          background: "var(--godex-panel-muted)",
          color: "var(--godex-text)",
          borderRadius: 6,
          fontSize: 13,
          lineHeight: 1.5,
          overflow: "auto",
          maxHeight: "70vh",
          whiteSpace: "pre-wrap",
          wordBreak: "break-word",
        }}
      >
        {yamlText || t("settings.noConfigData")}
      </pre>
    </Card>
  );
}

function formatValue(value: unknown) {
  if (value === undefined || value === null || value === "") {
    return "-";
  }
  if (Array.isArray(value)) {
    return value.join(", ");
  }
  if (typeof value === "object") {
    return JSON.stringify(value);
  }
  return String(value);
}

function renderChannelCapabilities(capabilities?: ChannelStatus["capabilities"]) {
  if (!capabilities) {
    return "-";
  }
  const labels: Array<[keyof NonNullable<ChannelStatus["capabilities"]>, string]> = [
    ["delivery", "delivery"],
    ["auth_login", "auth"],
    ["media", "media"],
    ["streaming", "stream"],
    ["typing", "typing"],
    ["status", "status"],
    ["allow_from", "allow_from"],
  ];
  const active = labels.filter(([key]) => Boolean(capabilities[key]));
  if (active.length === 0 && !capabilities.session_modes?.length) {
    return "-";
  }
  return (
    <Space size={[4, 4]} wrap>
      {active.map(([key, label]) => <Tag key={key}>{label}</Tag>)}
      {capabilities.session_modes?.map((mode) => <Tag key={`mode-${mode}`} color="blue">{mode}</Tag>)}
    </Space>
  );
}

function renderDeliveryStatus(delivery?: ChannelStatus["last_delivery"]) {
  if (!delivery) {
    return "-";
  }
  const color = delivery.status === "delivered" ? "green" : delivery.status === "failed" ? "red" : "gold";
  return (
    <Space direction="vertical" size={0}>
      <Tag color={color}>{delivery.status}</Tag>
      <Typography.Text type="secondary">
        {delivery.attempts} attempt{delivery.attempts === 1 ? "" : "s"}
      </Typography.Text>
      {delivery.last_error ? <Typography.Text type="danger" ellipsis={{ tooltip: delivery.last_error }}>{delivery.last_error}</Typography.Text> : null}
    </Space>
  );
}

function renderAccessDecision(access?: ChannelStatus["last_access"]) {
  if (!access) {
    return "-";
  }
  const color = access.action === "allow" ? "green" : access.action === "deny" ? "red" : "gold";
  const route = [access.platform_id, access.thread_id, access.sender_id].filter(Boolean).join(" / ");
  return (
    <Space direction="vertical" size={0}>
      <Tag color={color}>{access.action}</Tag>
      {route ? <Typography.Text type="secondary" ellipsis={{ tooltip: route }}>{route}</Typography.Text> : null}
      {access.reason ? <Typography.Text type="secondary" ellipsis={{ tooltip: access.reason }}>{access.reason}</Typography.Text> : null}
    </Space>
  );
}

function formatTimestamp(value?: string) {
  if (!value) {
    return "-";
  }
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}

function sameConfigValue(left: unknown, right: unknown) {
  return JSON.stringify(normalizeConfigValue(left)) === JSON.stringify(normalizeConfigValue(right));
}

function normalizeConfigValue(value: unknown): unknown {
  if (Array.isArray(value)) {
    return value.map((entry) => normalizeConfigValue(entry));
  }
  if (value && typeof value === "object") {
    return Object.fromEntries(Object.entries(value as Record<string, unknown>).sort(([a], [b]) => a.localeCompare(b)));
  }
  return value;
}

async function refreshAll(queryClient: ReturnType<typeof useQueryClient>, token: string) {
  await Promise.all([
    queryClient.invalidateQueries({ queryKey: ["config-meta", token] }),
    queryClient.invalidateQueries({ queryKey: ["config-schema", token] }),
    queryClient.invalidateQueries({ queryKey: ["config-view", token] }),
    queryClient.invalidateQueries({ queryKey: ["config-doctor", token] }),
    queryClient.invalidateQueries({ queryKey: ["providers", token] }),
    queryClient.invalidateQueries({ queryKey: ["runtime-service", token] }),
    queryClient.invalidateQueries({ queryKey: ["channels-status", token] }),
    queryClient.invalidateQueries({ queryKey: ["weixin-auth", token] }),
    queryClient.invalidateQueries({ queryKey: ["meta"] }),
  ]);
}
