import type { ReactNode } from "react";
import {
  Alert,
  Button,
  Card,
  Collapse,
  Descriptions,
  Form,
  Input,
  InputNumber,
  Select,
  Space,
  Switch,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import {
  ArrowDownOutlined,
  ArrowUpOutlined,
  DeleteOutlined,
  EyeOutlined,
  PlusOutlined,
  ReloadOutlined,
} from "@ant-design/icons";
import { useI18n } from "../../i18n";
import type { ConfigFieldSchema, ConfigFieldState, ConfigSectionSchema } from "../../lib/types";
import { ACPAgentsEditor, LabelledControl, LLMProvidersEditor } from "./SettingsConfigEditors";
import {
  API_HIDDEN_PATHS,
  asOptionalString,
  modelOptionsWithCurrent,
  strategyConfigToForm,
  type ACPAgentsFormValue,
  type LLMProvidersFormValue,
  type LLMStrategyFormValue,
  type ModelOption,
} from "./settingsConfigModel";

export function ConfigSectionFields(props: {
  section: ConfigSectionSchema;
  fields: Record<string, ConfigFieldState>;
  effectiveValues: Record<string, unknown>;
  clearSecrets: Record<string, boolean>;
  revealMutation: { isPending: boolean; variables?: string; mutate: (path: string) => void };
  modelOptions: ModelOption[];
  discoveringProviderID?: string;
  discoveringModels: boolean;
  onDiscoverModels: (id: string) => void;
  testingProviderID?: string;
  testingProvider?: boolean;
  canTestProviders?: boolean;
  onTestProvider?: (id: string) => void;
  onClearSecret: (path: string) => void;
}) {
  const { section, fields, effectiveValues, clearSecrets, revealMutation, modelOptions, discoveringProviderID, discoveringModels, onDiscoverModels, testingProviderID, testingProvider, canTestProviders, onTestProvider, onClearSecret } = props;
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
          testingProviderID={testingProviderID}
          testingProvider={testingProvider}
          canTestProviders={canTestProviders}
          onTestProvider={onTestProvider}
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
  testingProviderID?: string;
  testingProvider?: boolean;
  canTestProviders?: boolean;
  onTestProvider?: (id: string) => void;
  onReveal: () => void;
  onClearSecret: () => void;
}) {
  const { field, fieldState, effectiveValue, clearSecret, revealPending, modelOptions, discoveringProviderID, discoveringModels, onDiscoverModels, testingProviderID, testingProvider, canTestProviders, onTestProvider, onReveal, onClearSecret } = props;
  const { t } = useI18n();
  const isProvidersField = field.path === "api.providers";
  const isStructuredEditorField = isProvidersField || field.path === "acp.agents";
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
          testingProviderID={testingProviderID}
          testingProvider={testingProvider}
          canTestProviders={canTestProviders}
          onTestProvider={onTestProvider}
        />
      </Form.Item>
      {field.secret ? (
        <Space wrap size={8}>
          <Button size="small" icon={<EyeOutlined />} loading={revealPending} onClick={onReveal}>{t("settings.reveal")}</Button>
          {!isStructuredEditorField ? <Button size="small" danger onClick={onClearSecret}>{t("settings.clear")}</Button> : null}
          {clearSecret && !isStructuredEditorField ? <Tag color="red">{t("settings.willClearOnSave")}</Tag> : null}
        </Space>
      ) : null}
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
  const isStructuredEditorField = isProvidersField || field.path === "acp.agents";
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
          {!isStructuredEditorField ? <Button size="small" danger onClick={onClearSecret}>{t("settings.clear")}</Button> : null}
          {clearSecret && !isStructuredEditorField ? <Tag color="red">{t("settings.willClearOnSave")}</Tag> : null}
        </Space>
      ) : null}
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
  testingProviderID?: string;
  testingProvider?: boolean;
  canTestProviders?: boolean;
  onTestProvider?: (id: string) => void;
}) {
  const { field, fieldState, effectiveValue, clearSecret, compact, value, checked, onChange, modelOptions, discoveringProviderID, discoveringModels, onDiscoverModels, testingProviderID, testingProvider, canTestProviders, onTestProvider } = props;
  const { t } = useI18n();
  const isProvidersField = field.path === "api.providers";
  if (isProvidersField) {
    return (
      <LLMProvidersEditor
        value={value}
        onChange={onChange as (value: LLMProvidersFormValue) => void}
        discoveringProviderID={discoveringProviderID}
        discoveringModels={discoveringModels}
        onDiscoverModels={onDiscoverModels}
        testingProviderID={testingProviderID}
        testingProvider={testingProvider}
        canTestProviders={canTestProviders}
        onTestProvider={onTestProvider}
      />
    );
  }
  if (field.path === "acp.agents") {
    return <ACPAgentsEditor value={value} onChange={onChange as (value: ACPAgentsFormValue) => void} />;
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
