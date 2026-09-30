import { useState, type ReactNode } from "react";
import { Alert, Button, Collapse, Input, InputNumber, Select, Space, Switch, Tag, Tooltip, Typography } from "antd";
import { DeleteOutlined, PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { discoverACPAgentModels, type ACPModelOption } from "../../lib/api";
import { useSettingsStore } from "../../store/settings";
import {
  acpAgentsConfigToForm,
  nextUniqueID,
  numberOrUndefined,
  providersConfigToForm,
  reasoningEffortOptions,
  SECRET_MASK,
  stringsPresent,
  type ACPAgentFormItem,
  type ACPAgentsFormValue,
  type LLMModelFormItem,
  type LLMProviderFormItem,
  type LLMProvidersFormValue,
} from "./settingsConfigModel";

export function LLMProvidersEditor({
  value,
  onChange,
  discoveringProviderID,
  discoveringModels,
  onDiscoverModels,
  testingProviderID,
  testingProvider,
  canTestProviders,
  onTestProvider,
}: {
  value?: unknown;
  onChange?: (value: LLMProvidersFormValue) => void;
  discoveringProviderID?: string;
  discoveringModels: boolean;
  onDiscoverModels: (id: string) => void;
  testingProviderID?: string;
  testingProvider?: boolean;
  canTestProviders?: boolean;
  onTestProvider?: (id: string) => void;
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
            // Use the stable index as the panel key: provider.id is editable
            // and changes on every keystroke, so keying on it would remount
            // the panel and reset its expand state mid-typing.
            key: String(providerIndex),
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
              <Space size={6}>
                <Tooltip title={!canTestProviders ? t("settings.providerTestSaveFirst") : undefined}>
                  <span>
                    <Button
                      size="small"
                      disabled={!canTestProviders || !stringsPresent(provider.id)}
                      loading={testingProvider && testingProviderID === provider.id}
                      onClick={(event) => {
                        event.stopPropagation();
                        onTestProvider?.(provider.id);
                      }}
                    >
                      {t("settings.testAction")}
                    </Button>
                  </span>
                </Tooltip>
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
              </Space>
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
                        { value: "laya_jev", label: "Laya / Jev (本地决策模型)" },
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

export function ACPAgentsEditor({ value, onChange }: { value?: unknown; onChange?: (value: ACPAgentsFormValue) => void }) {
  const agents = acpAgentsConfigToForm(value);
  const { t } = useI18n();
  const token = useSettingsStore((state) => state.token);
  const [discovering, setDiscovering] = useState<number | null>(null);
  const [discovered, setDiscovered] = useState<Record<number, ACPModelOption[]>>({});
  const [discoverError, setDiscoverError] = useState<string | undefined>(undefined);
  const emit = (items: ACPAgentFormItem[]) => onChange?.({ items });
  const updateAgent = (index: number, patch: Partial<ACPAgentFormItem>) => {
    emit(agents.items.map((item, itemIndex) => (itemIndex === index ? { ...item, ...patch } : item)));
  };
  const removeAgent = (index: number) => emit(agents.items.filter((_, itemIndex) => itemIndex !== index));
  const addAgent = () => {
    emit([
      ...agents.items,
      {
        id: nextUniqueID("codex", agents.items.map((item) => item.id)),
        command: "",
        args: "",
        env: "",
        timeout_seconds: 120,
        description: "",
        model: "",
      },
    ]);
  };
  const discoverModels = async (index: number) => {
    const agent = agents.items[index];
    if (!agent.id) {
      setDiscoverError(t("settings.acpDiscoverNeedsID"));
      return;
    }
    setDiscovering(index);
    setDiscoverError(undefined);
    try {
      const resp = await discoverACPAgentModels(token, agent.id);
      setDiscovered((prev) => ({ ...prev, [index]: resp.models ?? [] }));
    } catch (err) {
      setDiscoverError(err instanceof Error ? err.message : String(err));
    } finally {
      setDiscovering(null);
    }
  };

  return (
    <Space direction="vertical" size={12} style={{ width: "100%" }}>
      {agents.items.length === 0 ? <Alert type="info" showIcon message={t("settings.noACPAgents")} /> : null}
      <Collapse
        defaultActiveKey={[]}
        items={agents.items.map((agent, index) => ({
          key: String(index),
          label: (
            <span className="llm-provider-collapse-label">
              <Typography.Text strong>{agent.id || t("settings.unnamedACPAgent")}</Typography.Text>
              {agent.command ? <Typography.Text type="secondary">{agent.command}</Typography.Text> : null}
            </span>
          ),
          extra: (
            <Button
              danger
              size="small"
              icon={<DeleteOutlined />}
              onClick={(event) => {
                event.stopPropagation();
                removeAgent(index);
              }}
            >
              {t("settings.remove")}
            </Button>
          ),
          children: (
            <div className="llm-provider-panel">
              <div className="llm-form-grid">
                <LabelledControl label={t("settings.acpAgentIDLabel")}>
                  <Input value={agent.id} placeholder="codex" onChange={(event) => updateAgent(index, { id: event.target.value })} />
                </LabelledControl>
                <LabelledControl label={t("settings.acpCommandLabel")}>
                  <Input value={agent.command} placeholder="codex" onChange={(event) => updateAgent(index, { command: event.target.value })} />
                </LabelledControl>
                <LabelledControl label={t("settings.acpArgsLabel")}>
                  <Input value={agent.args} placeholder="acp" onChange={(event) => updateAgent(index, { args: event.target.value })} />
                </LabelledControl>
                <LabelledControl label={t("settings.acpTimeoutLabel")}>
                  <InputNumber
                    min={1}
                    style={{ width: "100%" }}
                    value={agent.timeout_seconds}
                    onChange={(timeout) => updateAgent(index, { timeout_seconds: numberOrUndefined(timeout) })}
                  />
                </LabelledControl>
                <LabelledControl label={t("settings.acpModelLabel")} wide>
                  <Space.Compact style={{ width: "100%" }}>
                    <Select
                      allowClear
                      showSearch
                      value={agent.model || undefined}
                      placeholder={t("settings.acpModelPlaceholder")}
                      onChange={(model) => updateAgent(index, { model: model ?? "" })}
                      options={(discovered[index] ?? []).map((option) => ({ value: option.value, label: option.name || option.value }))}
                      optionFilterProp="label"
                      style={{ flex: 1 }}
                    />
                    <Button icon={<ReloadOutlined />} loading={discovering === index} onClick={() => void discoverModels(index)}>
                      {t("settings.acpDiscoverModels")}
                    </Button>
                  </Space.Compact>
                  {discoverError ? (
                    <Typography.Text type="danger" style={{ fontSize: 12, display: "block", marginTop: 4 }}>
                      {discoverError}
                    </Typography.Text>
                  ) : null}
                </LabelledControl>
                <LabelledControl label={t("settings.acpEnvLabel")} wide>
                  <Input.TextArea
                    rows={3}
                    value={agent.env}
                    placeholder={"KEY=VALUE\nANOTHER=value"}
                    spellCheck={false}
                    onChange={(event) => updateAgent(index, { env: event.target.value })}
                  />
                </LabelledControl>
                <LabelledControl label={t("settings.acpDescriptionLabel")} wide>
                  <Input value={agent.description} placeholder="OpenAI Codex" onChange={(event) => updateAgent(index, { description: event.target.value })} />
                </LabelledControl>
              </div>
            </div>
          ),
        }))}
      />
      <Button icon={<PlusOutlined />} onClick={addAgent}>{t("settings.addACPAgent")}</Button>
    </Space>
  );
}

export function LabelledControl({ label, wide, children }: { label: string; wide?: boolean; children: ReactNode }) {
  return (
    <label className={wide ? "llm-form-field llm-form-field-wide" : "llm-form-field"}>
      <Typography.Text type="secondary">{label}</Typography.Text>
      {children}
    </label>
  );
}
