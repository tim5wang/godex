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
import { ConfigSectionFields } from "./SettingsConfigFields";
import {
  ApplyReportView,
  ConfigYamlCard,
  DoctorPanel,
  NotificationsCard,
  ProvidersPanel,
  RuntimeServiceCard,
  SecurityPanel,
  WeixinPanel,
  formatTimestamp,
  renderAccessDecision,
  renderChannelCapabilities,
  renderDeliveryStatus,
} from "./SettingsStatusPanels";
import {
  buildSaveValues,
  formValuesFromConfig,
  llmModelOptions,
  mergeDiscoveredModels,
  parseJSONValue,
  providersConfigToForm,
  sameConfigValue,
  type ConfigFormValues,
} from "./settingsConfigModel";

type LocalFormValues = {
  locale: "en" | "zh";
  token: string;
  defaultSessionKey: string;
};

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
