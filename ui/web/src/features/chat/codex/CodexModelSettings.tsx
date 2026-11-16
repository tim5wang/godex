import { InfoCircleOutlined } from "@ant-design/icons";
import { Alert, Button, Select, Space, Tooltip } from "antd";
import { useQuery } from "@tanstack/react-query";
import { useEffect, useRef, useState } from "react";
import { useI18n } from "../../../i18n";
import { codexModels, codexUpdateSettings, type CodexModelSettings as Settings, type CodexThread } from "./codexApi";

interface Props {
  token: string | null;
  nodeId?: string | null;
  thread?: CodexThread;
  enabled: boolean;
  writable: boolean;
  onUpdated: (thread: CodexThread) => void;
  onBusyChange: (busy: boolean) => void;
}

const effortKeys: Record<string, string> = { none: "effortNone", minimal: "effortMinimal", low: "effortLow", medium: "effortMedium", high: "effortHigh", xhigh: "effortXhigh", max: "effortMax", ultra: "effortUltra" };

export function CodexModelSettings({ token, nodeId, thread, enabled, writable, onUpdated, onBusyChange }: Props) {
  const { t } = useI18n();
  const [draft, setDraft] = useState<Settings | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState("");
  const mounted = useRef(true);
  useEffect(() => { mounted.current = true; return () => { mounted.current = false; }; }, []);
  // A native-client settings notification invalidates any local un-applied
  // selection. Never silently re-apply an old draft over another client's edit.
  useEffect(() => { setDraft(null); }, [thread?.model, thread?.reasoningEffort]);
  const catalog = useQuery({
    queryKey: ["codex-models", token, nodeId, thread?.id, thread?.modelProvider],
    queryFn: ({ signal }) => codexModels(token, thread!.id, signal),
    enabled: enabled && !!thread, retry: false, staleTime: 60_000,
  });
  const models = catalog.data?.data ?? [];
  const selection = draft ?? { model: thread?.model || "", effort: thread?.reasoningEffort || "" };
  const selectedModel = models.find((model) => model.model === selection.model);
  const efforts = selectedModel?.supportedReasoningEfforts ?? [];
  const valid = !!selectedModel && (efforts.length ? efforts.some((effort) => effort.reasoningEffort === selection.effort) : selection.effort === "");
  const dirty = selection.model !== (thread?.model || "") || selection.effort !== (thread?.reasoningEffort || "");
  const modelOptions: Array<{ value: string; label: string; disabled?: boolean }> = models.map((model) => ({ value: model.model, label: model.displayName || model.model }));
  if (selection.model && !selectedModel) modelOptions.unshift({ value: selection.model, label: selection.model, disabled: true });
  const effortLabel = (value: string) => effortKeys[value] ? t(`codex.${effortKeys[value]}`) : value;
  const effortOptions: Array<{ value: string; label: string; disabled?: boolean }> = efforts.map((effort) => ({ value: effort.reasoningEffort, label: effortLabel(effort.reasoningEffort) }));
  if (selection.effort && !effortOptions.some((option) => option.value === selection.effort)) effortOptions.unshift({ value: selection.effort, label: effortLabel(selection.effort), disabled: true });
  const apply = async () => {
    if (!thread || !writable || !valid || !dirty || saving) return;
    setSaving(true); onBusyChange(true); setError("");
    try {
      const updated = await codexUpdateSettings(token, thread.id, selection);
      if (mounted.current) { onUpdated(updated); setDraft(null); }
    } catch (caught) {
      if (mounted.current) setError(caught instanceof Error ? caught.message : String(caught));
    } finally {
      if (mounted.current) { setSaving(false); onBusyChange(false); }
    }
  };
  return <div className="codex-model-settings" data-testid="codex-model-settings">
    <Space wrap size="small">
      <Select size="small" aria-label={t("codex.model")} placeholder={t("codex.selectModel")} value={selection.model || undefined} options={modelOptions} loading={catalog.isLoading} disabled={!writable || saving || !models.length} onChange={(model) => {
        const option = models.find((entry) => entry.model === model);
        if (!option) return;
        const supported = option.supportedReasoningEfforts ?? [];
        const effort = supported.length ? supported.some((entry) => entry.reasoningEffort === selection.effort) ? selection.effort : option.defaultReasoningEffort || "" : "";
        setDraft({ model, effort }); setError("");
      }} />
      <Select size="small" aria-label={t("codex.effort")} placeholder={t("codex.nativeDefault")} value={selection.effort || undefined} options={effortOptions} disabled={!writable || saving || !efforts.length} onChange={(effort) => { setDraft({ ...selection, effort }); setError(""); }} />
      <Button size="small" loading={saving} disabled={!writable || !valid || !dirty} onClick={() => void apply()}>{t("codex.applySettings")}</Button>
      <Tooltip title={t("codex.settingsHint")}><InfoCircleOutlined aria-label={t("codex.settingsHint")} tabIndex={0} /></Tooltip>
    </Space>
    {catalog.error ? <Alert type="warning" title={t("codex.settingsUnavailable")} description={catalog.error.message} action={<Button size="small" onClick={() => void catalog.refetch()}>{t("codex.retry")}</Button>} /> : null}
    {error ? <Alert type="error" title={t("codex.settingsFailed", { error })} /> : null}
  </div>;
}
