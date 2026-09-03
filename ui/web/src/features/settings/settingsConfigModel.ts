import { stringify as toYaml } from "yaml";
import type { ConfigSectionSchema, ProviderModelInfo } from "../../lib/types";

export type ConfigFormValues = Record<string, unknown>;

export const SECRET_MASK = "********";
export const API_HIDDEN_PATHS = new Set([
  "api.default_profile",
  "api.auto_fallback_enabled",
  "api.timeout_seconds",
]);

export type LLMProvidersFormValue = {
  items: LLMProviderFormItem[];
};

export type LLMProviderFormItem = {
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

export type LLMModelFormItem = {
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

export type LLMStrategyFormValue = {
  type: "primary" | "fallback" | "round_robin";
  candidates: string[];
};

export type ACPAgentsFormValue = {
  items: ACPAgentFormItem[];
};

export type ACPAgentFormItem = {
  id: string;
  command?: string;
  args?: string;
  env?: string;
  timeout_seconds?: number;
  description?: string;
  model?: string;
};

export type ModelOption = {
  value: string;
  label: string;
};

export const reasoningEffortOptions = [
  { value: "none", label: "None" },
  { value: "minimal", label: "Minimal" },
  { value: "low", label: "Low" },
  { value: "medium", label: "Medium" },
  { value: "high", label: "High" },
  { value: "xhigh", label: "X High" },
];

export function buildSaveValues(values: ConfigFormValues, sections: ConfigSectionSchema[]) {
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
    if (field.path === "acp.agents") {
      result[field.path] = acpAgentsFormToConfig(value);
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

export function formValuesFromConfig(values: Record<string, unknown>, sections: ConfigSectionSchema[]) {
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
    if (field.path === "acp.agents") {
      result[field.path] = acpAgentsConfigToForm(result[field.path]);
      continue;
    }
    if (field.type === "json" && result[field.path] !== undefined && typeof result[field.path] !== "string") {
      result[field.path] = JSON.stringify(result[field.path], null, 2);
    }
  }
  return result;
}

export function providersConfigToForm(value: unknown): LLMProvidersFormValue {
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

export function mergeDiscoveredModels(existing: LLMModelFormItem[], discovered: ProviderModelInfo[]) {
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

export function strategyConfigToForm(value: unknown): LLMStrategyFormValue {
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

export function acpAgentsConfigToForm(value: unknown): ACPAgentsFormValue {
  if (isACPAgentsFormValue(value)) {
    return { items: value.items.map((item) => ({ ...item })) };
  }
  const raw = parseJSONValue(value);
  if (!raw || typeof raw !== "object" || Array.isArray(raw)) {
    return { items: [] };
  }
  const items = Object.entries(raw as Record<string, Record<string, unknown>>).map(([id, agent]) => ({
    id,
    command: asOptionalString(agent.command),
    args: Array.isArray(agent.args) ? agent.args.map(String).join(" ") : asOptionalString(agent.args),
    env: envToText(agent.env),
    timeout_seconds: asOptionalNumber(agent.timeout_seconds),
    description: asOptionalString(agent.description),
    model: asOptionalString(agent.model),
  }));
  return { items };
}

function acpAgentsFormToConfig(value: unknown) {
  const form = acpAgentsConfigToForm(value);
  return Object.fromEntries(form.items.filter((item) => stringsPresent(item.id)).map((item) => {
    return [item.id.trim(), {
      command: item.command ?? "",
      args: splitArgs(item.args),
      env: parseEnvText(item.env),
      timeout_seconds: item.timeout_seconds ?? 0,
      description: item.description ?? "",
      model: item.model ?? "",
    }];
  }));
}

function isACPAgentsFormValue(value: unknown): value is ACPAgentsFormValue {
  return !!value && typeof value === "object" && Array.isArray((value as ACPAgentsFormValue).items);
}

function envToText(env?: unknown): string | undefined {
  if (!env || typeof env !== "object" || Array.isArray(env)) {
    return undefined;
  }
  return Object.entries(env as Record<string, unknown>).map(([key, value]) => `${key}=${value ?? ""}`).join("\n");
}

function parseEnvText(text?: string): Record<string, string> {
  const out: Record<string, string> = {};
  for (const line of String(text ?? "").split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) {
      continue;
    }
    const eq = trimmed.indexOf("=");
    if (eq <= 0) {
      continue;
    }
    out[trimmed.slice(0, eq).trim()] = trimmed.slice(eq + 1).trim();
  }
  return out;
}

export function llmModelOptions(value: unknown): ModelOption[] {
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

export function modelOptionsWithCurrent(options: ModelOption[], candidates: string[]): ModelOption[] {
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

export function parseJSONValue(value: unknown): unknown {
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

export function asOptionalString(value: unknown): string | undefined {
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

export function numberOrUndefined(value: string | number | null): number | undefined {
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

/** 把 ACP agent 的 args 文本按空白拆成独立参数（shell 风格），
 *  使表单里填 "codex --profile acp" 或 "codex,--profile,acp" 都能得到
 *  ["codex","--profile","acp"]，避免把 "a b" 当成单个带空格参数传给
 *  exec.Command 导致 ACP 进程无法启动（EOF）。 */
function splitArgs(value?: string): string[] {
  return String(value ?? "").split(/[\s,]+/).map((item) => item.trim()).filter(Boolean);
}

export function nextUniqueID(base: string, ids: string[]) {
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

export function stringsPresent(value?: string) {
  return String(value ?? "").trim() !== "";
}

export function flatToNested(flat: Record<string, unknown>): Record<string, unknown> {
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

export function configToYaml(storedValues: Record<string, unknown>, effectiveValues: Record<string, unknown>): string {
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

export function formatValue(value: unknown) {
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

export function sameConfigValue(left: unknown, right: unknown) {
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

