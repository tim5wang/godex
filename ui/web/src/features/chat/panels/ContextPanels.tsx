import { useState } from "react";
import type { SessionContextInspector, SkillActivation, ProtocolMessage } from "../../../lib/types";
import { useI18n } from "../../../i18n";
import { Tag, Popover, Alert, Space, Descriptions, Card, Typography, List, Popconfirm, Button, Progress, Empty, Modal } from "antd";
import { useMutation, useQuery } from "@tanstack/react-query";
import { DeleteOutlined, HistoryOutlined } from "@ant-design/icons";
import { getSessionTranscript } from "../../../lib/api";
import { useSettingsStore } from "../../../store/settings";
import { type ContextStatusSummary, formatCompactNumber } from "../../../lib/timelineUtils";

export function ContextStatusInline({ summary, inspector }: { summary: ContextStatusSummary; inspector?: SessionContextInspector | null }) {
  const { t } = useI18n();
  const color = summary.suggestCompact || summary.budgetPercent >= 85 ? "gold" : summary.budgetPercent >= 65 ? "blue" : "default";
  const ctx = inspector?.context;
  const breakdown = ctx?.token_breakdown;
  const cache = ctx?.prefix_cache;
  const cacheStableSystem = cache?.stable_system_tokens ?? 0;
  const cacheStableTools = cache?.stable_tool_schema_tokens ?? 0;
  const cacheStableMemory = cache?.stable_memory_index_tokens ?? 0;
  const cacheStable = cacheStableSystem + cacheStableTools + cacheStableMemory;
  const cacheDynamic = cache?.dynamic_runtime_tokens ?? 0;
  const hasCache = cacheStable > 0 || cacheDynamic > 0;
  // Cacheable estimate = stable prefix + conversation history. History is
  // append-only, so it stays prefix-cacheable between turns; only the small
  // volatile tail (todos/inbox/notifications, appended after history) is
  // uncacheable. Ratio is measured against the whole prompt so the number
  // tracks the real hit rate instead of only the non-history slice.
  const historyTokens = breakdown?.history ?? 0;
  const promptTotal = breakdown?.total ?? 0;
  const cacheable = cacheStable + historyTokens;
  const cacheRatio = promptTotal > 0 ? ((cacheable / promptTotal) * 100) : 0;
  const dynamicSections = Object.entries(cache?.dynamic_section_tokens ?? {}).sort((a, b) => b[1] - a[1]);
  const realUsage = ctx?.cache_usage;
  const hasRealUsage = (realUsage?.calls ?? 0) > 0;
  const sectionLabel = (key: string) => {
    const i18nKey = "chat.ctxPopoverSection" + key.split("_").map((part) => part.charAt(0).toUpperCase() + part.slice(1)).join("");
    const translated = t(i18nKey);
    return translated === i18nKey ? key.replace(/_/g, " ") : translated;
  };

  const content = (
    <div className="ctx-popover">
      <div className="ctx-popover-group">
        <div className="ctx-popover-group-title">{t("chat.ctxPopoverContext")}</div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.ctxPopoverTokens")}</span>
          <span className="ctx-popover-value">
            {ctx ? `${formatCompactNumber(ctx.total_token_estimate ?? ctx.token_estimate ?? 0)} / ${formatCompactNumber(ctx.compress_threshold ?? 0)}` : "—"}
          </span>
        </div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.ctxPopoverOccupancy")}</span>
          <span className="ctx-popover-value">{summary.budgetPercent}%</span>
        </div>
      </div>
      <div className="ctx-popover-group">
        <div className="ctx-popover-group-title">{t("chat.ctxPopoverBreakdown")}</div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.ctxPopoverSystem")}</span>
          <span className="ctx-popover-value">{formatCompactNumber(breakdown?.system ?? 0)}</span>
        </div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.ctxPopoverHistory")}</span>
          <span className="ctx-popover-value">{formatCompactNumber(breakdown?.history ?? 0)}</span>
        </div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.ctxPopoverMemory")}</span>
          <span className="ctx-popover-value">{formatCompactNumber(breakdown?.memory ?? 0)}</span>
        </div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.ctxPopoverRuntime")}</span>
          <span className="ctx-popover-value">{formatCompactNumber(breakdown?.runtime ?? 0)}</span>
        </div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.ctxPopoverToolSchemas")}</span>
          <span className="ctx-popover-value">{formatCompactNumber(breakdown?.tool_schemas ?? 0)}</span>
        </div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.ctxPopoverToolResults")}</span>
          <span className="ctx-popover-value">{formatCompactNumber(breakdown?.tool_results ?? 0)}</span>
        </div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.ctxPopoverAttachments")}</span>
          <span className="ctx-popover-value">{formatCompactNumber(breakdown?.attachments ?? 0)}</span>
        </div>
        {breakdown ? (
          <div className="ctx-popover-row ctx-popover-row-total">
            <span className="ctx-popover-label">{t("chat.ctxPopoverTotal")}</span>
            <span className="ctx-popover-value">{formatCompactNumber(breakdown.total)}</span>
          </div>
        ) : null}
      </div>
      <div className="ctx-popover-group">
        <div className="ctx-popover-group-title">{t("chat.ctxPopoverSession")}</div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.ctxPopoverMessages")}</span>
          <span className="ctx-popover-value">{ctx?.message_count ?? "—"}</span>
        </div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.ctxPopoverSkills")}</span>
          <span className="ctx-popover-value">{ctx?.active_skill_count ?? "—"}</span>
        </div>
        <div className="ctx-popover-row">
          <span className="ctx-popover-label">{t("chat.ctxPopoverApprovals")}</span>
          <span className="ctx-popover-value">{ctx?.pending_permission_count ?? "—"}</span>
        </div>
        {ctx?.suggest_compact ? (
          <div className="ctx-popover-row">
            <span className="ctx-popover-label">{t("chat.ctxPopoverCompaction")}</span>
            <span className="ctx-popover-value"><Tag color="gold" style={{ margin: 0 }}>{t("chat.ctxPopoverCompactionSuggested")}</Tag></span>
          </div>
        ) : null}
      </div>
      {hasCache ? (
        <div className="ctx-popover-group">
          <div className="ctx-popover-group-title">{t("chat.ctxPopoverPrefixCache")}</div>
          {hasRealUsage ? (
            <>
              <div className="ctx-popover-row">
                <span className="ctx-popover-label">{t("chat.ctxPopoverRealHitRate")}</span>
                <span className="ctx-popover-value">
                  {realUsage!.hit_rate_percent.toFixed(1)}%
                  <span className="ctx-popover-hint"> · {t("chat.ctxPopoverRealHitRateNote")}</span>
                </span>
              </div>
              <div className="ctx-popover-row ctx-popover-row-sub">
                <span className="ctx-popover-label">{t("chat.ctxPopoverCacheRead")}</span>
                <span className="ctx-popover-value">{formatCompactNumber(realUsage!.cache_read_tokens)}</span>
              </div>
              <div className="ctx-popover-row ctx-popover-row-sub">
                <span className="ctx-popover-label">{t("chat.ctxPopoverCacheWrite")}</span>
                <span className="ctx-popover-value">{formatCompactNumber(realUsage!.cache_write_tokens)}</span>
              </div>
              <div className="ctx-popover-row ctx-popover-row-sub">
                <span className="ctx-popover-label">{t("chat.ctxPopoverCacheCalls")}</span>
                <span className="ctx-popover-value">{realUsage!.calls}</span>
              </div>
            </>
          ) : null}
          <div className="ctx-popover-row">
            <span className="ctx-popover-label">{t("chat.ctxPopoverStablePrefix")}</span>
            <span className="ctx-popover-value">
              {formatCompactNumber(cacheable)} ({Math.round(cacheRatio)}%)
              <span className="ctx-popover-hint"> · {t("chat.ctxPopoverStablePrefixNote")}</span>
            </span>
          </div>
          <div className="ctx-popover-row ctx-popover-row-sub">
            <span className="ctx-popover-label">{t("chat.ctxPopoverCacheSystem")}</span>
            <span className="ctx-popover-value">{formatCompactNumber(cacheStableSystem)}</span>
          </div>
          <div className="ctx-popover-row ctx-popover-row-sub">
            <span className="ctx-popover-label">{t("chat.ctxPopoverCacheToolSchemas")}</span>
            <span className="ctx-popover-value">{formatCompactNumber(cacheStableTools)}</span>
          </div>
          <div className="ctx-popover-row ctx-popover-row-sub">
            <span className="ctx-popover-label">{t("chat.ctxPopoverCacheMemoryIndex")}</span>
            <span className="ctx-popover-value">{formatCompactNumber(cacheStableMemory)}</span>
          </div>
          <div className="ctx-popover-row ctx-popover-row-sub">
            <span className="ctx-popover-label">{t("chat.ctxPopoverHistory")}</span>
            <span className="ctx-popover-value">{formatCompactNumber(historyTokens)}</span>
          </div>
          <div className="ctx-popover-row">
            <span className="ctx-popover-label">{t("chat.ctxPopoverDynamicRuntime")}</span>
            <span className="ctx-popover-value">{formatCompactNumber(cacheDynamic)}</span>
          </div>
          {dynamicSections.map(([key, tokens]) => (
            <div className="ctx-popover-row ctx-popover-row-sub" key={key}>
              <span className="ctx-popover-label">{sectionLabel(key)}</span>
              <span className="ctx-popover-value">{formatCompactNumber(tokens)}</span>
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );

  return (
    <Popover content={content} trigger="hover" placement="top" overlayStyle={{ maxWidth: 360 }}>
      <Tag color={color} className="chat-context-status" style={{ cursor: "pointer" }}>
        {summary.text}
      </Tag>
    </Popover>
  );
}

export function ContextRecallPanel({
  inspector,
  loading,
  activeSkills,
  activeSkillsLoading,
  unloadingSkill,
  onUnloadSkill,
  sessionId,
}: {
  inspector: SessionContextInspector | null;
  loading: boolean;
  activeSkills: SkillActivation[];
  activeSkillsLoading: boolean;
  unloadingSkill: ReturnType<typeof useMutation<SkillActivation, Error, string>>;
  onUnloadSkill: (skillId: string) => void;
  sessionId: string;
}) {
  const { t } = useI18n();
  const token = useSettingsStore((state) => state.token);
  const [archiveRef, setArchiveRef] = useState<string | null>(null);
  if (loading && !inspector) {
    return <Alert type="info" showIcon message={t("chat.contextInspectorLoading")} />;
  }
  const context = inspector?.context;
  const breakdown = context?.token_breakdown;
  const totalTokens = context?.total_token_estimate ?? context?.token_estimate ?? breakdown?.total ?? 0;
  const historyTokens = context?.history_token_estimate ?? breakdown?.history ?? 0;
  const compressionReasons = context?.compression_reasons ?? [];
  const largestSources = context?.largest_context_sources ?? [];
  const toolRefs = context?.tool_result_references ?? [];
  const budgetPercent =
    context && context.compress_threshold > 0 ? Math.min(100, Math.round((totalTokens / context.compress_threshold) * 100)) : 0;
  const memoryPreview = {
    identity: inspector?.memory_preview?.identity ?? [],
    core: inspector?.memory_preview?.core ?? [],
    relevant: inspector?.memory_preview?.relevant ?? [],
  };
  const breakdownItems = [
    { key: "system", label: t("chat.contextInspectorBreakdownSystem"), value: breakdown?.system ?? 0 },
    { key: "history", label: t("chat.contextInspectorBreakdownHistory"), value: breakdown?.history ?? historyTokens },
    { key: "memory", label: t("chat.contextInspectorBreakdownMemory"), value: breakdown?.memory ?? 0 },
    { key: "runtime", label: t("chat.contextInspectorBreakdownRuntime"), value: breakdown?.runtime ?? 0 },
    { key: "tool_schemas", label: t("chat.contextInspectorBreakdownToolSchemas"), value: breakdown?.tool_schemas ?? 0 },
    { key: "tool_results", label: t("chat.contextInspectorBreakdownToolResults"), value: breakdown?.tool_results ?? 0 },
    { key: "attachments", label: t("chat.contextInspectorBreakdownAttachments"), value: breakdown?.attachments ?? 0 },
  ];
  return (
    <Space direction="vertical" size={14} style={{ width: "100%" }}>
      <Descriptions
        bordered
        size="small"
        column={1}
        items={[
          { key: "messages", label: t("chat.contextInspectorMessages"), children: context?.message_count ?? 0 },
          { key: "tokens", label: t("chat.contextInspectorTokenEstimate"), children: totalTokens },
          { key: "history_tokens", label: t("chat.contextInspectorHistoryTokens"), children: historyTokens },
          { key: "threshold", label: t("chat.contextInspectorThreshold"), children: context?.compress_threshold ?? 0 },
          { key: "skills", label: t("chat.contextInspectorActiveSkills"), children: context?.active_skill_count ?? 0 },
          { key: "approvals", label: t("chat.contextInspectorPendingPermissions"), children: context?.pending_permission_count ?? 0 },
        ]}
      />
      <Card size="small" title={t("chat.activeSkillsTitle")} loading={activeSkillsLoading && activeSkills.length === 0}>
        {activeSkills.length === 0 ? (
          <Typography.Text type="secondary">{t("chat.noActiveSkills")}</Typography.Text>
        ) : (
          <List
            size="small"
            dataSource={activeSkills}
            renderItem={(item) => (
              <List.Item
                actions={[
                  <Popconfirm
                    key="unload"
                    title={t("chat.unloadSkillConfirm")}
                    onConfirm={() => onUnloadSkill(item.id)}
                  >
                    <Button
                      danger
                      size="small"
                      icon={<DeleteOutlined />}
                      loading={unloadingSkill.isPending && unloadingSkill.variables === item.id}
                    >
                      {t("chat.unloadSkill")}
                    </Button>
                  </Popconfirm>,
                ]}
              >
                <List.Item.Meta
                  title={<Typography.Text strong>{item.name || item.id}</Typography.Text>}
                  description={
                    <Space direction="vertical" size={4}>
                      {item.description ? <Typography.Text type="secondary">{item.description}</Typography.Text> : null}
                      {item.loaded_sections?.length ? (
                        <Space wrap size={4}>
                          {item.loaded_sections.map((section) => (
                            <Tag key={section}>{section}</Tag>
                          ))}
                        </Space>
                      ) : null}
                    </Space>
                  }
                />
              </List.Item>
            )}
          />
        )}
      </Card>
      <Card size="small" title={t("chat.contextInspectorStatusTitle")}>
        <Space direction="vertical" size={8} style={{ width: "100%" }}>
          <Typography.Text>{context?.suggest_compact ? t("chat.contextInspectorSuggestCompact") : t("chat.contextInspectorNoCompact")}</Typography.Text>
          <Progress percent={budgetPercent} size="small" status={budgetPercent > 85 ? "exception" : "active"} />
          <Typography.Text type="secondary">
            {context
              ? t("chat.contextInspectorBudgetUsage", {
                  used: totalTokens,
                  limit: context.compress_threshold,
                  percent: budgetPercent,
                })
              : t("chat.contextInspectorNoArchive")}
          </Typography.Text>
          {compressionReasons.length > 0 ? (
            <Space wrap size={4}>
              {compressionReasons.map((reason) => (
                <Tag key={reason}>{reason}</Tag>
              ))}
            </Space>
          ) : null}
          {context?.compaction_mode ? (
            <Typography.Text type="secondary">
              {t("chat.contextInspectorCompactionDiagnostics", {
                mode: context.compaction_mode,
                before: context.pre_compaction_total ?? totalTokens,
                after: context.post_compaction_total ?? totalTokens,
                latency: context.compaction_latency_ms ?? 0,
              })}
            </Typography.Text>
          ) : null}
        </Space>
      </Card>
      <Card size="small" title={t("chat.contextInspectorBreakdownTitle")}>
        <Descriptions
          size="small"
          column={1}
          items={breakdownItems.map((item) => ({
            key: item.key,
            label: item.label,
            children: item.value,
          }))}
        />
        {largestSources.length > 0 ? (
          <Space direction="vertical" size={4} style={{ width: "100%", marginTop: 10 }}>
            <Typography.Text type="secondary">{t("chat.contextInspectorLargestSources")}</Typography.Text>
            <Space wrap size={4}>
              {largestSources.map((item) => (
                <Tag key={item.source}>
                  {item.source}: {item.tokens}
                </Tag>
              ))}
            </Space>
          </Space>
        ) : null}
      </Card>
      <Card size="small" title={t("chat.contextInspectorToolResultTitle")}>
        <Space direction="vertical" size={8} style={{ width: "100%" }}>
          <Typography.Text type="secondary">
            {t("chat.contextInspectorToolResultSummary", {
              count: context?.large_tool_result_reference_count ?? toolRefs.length,
            })}
          </Typography.Text>
          {toolRefs.length > 0 ? (
            <List
              size="small"
              dataSource={toolRefs}
              renderItem={(item) => (
                <List.Item>
                  <Space direction="vertical" size={2}>
                    <Typography.Text strong>{item.tool_name || item.tool_use_id || t("chat.contextInspectorToolResultUnknown")}</Typography.Text>
                    <Typography.Text type="secondary">
                      {[item.artifact_path, item.bytes ? `${item.bytes} bytes` : "", item.sha256 ? item.sha256.slice(0, 12) : ""]
                        .filter(Boolean)
                        .join(" · ")}
                    </Typography.Text>
                  </Space>
                </List.Item>
              )}
            />
          ) : (
            <Typography.Text type="secondary">{t("chat.contextInspectorToolResultEmpty")}</Typography.Text>
          )}
        </Space>
      </Card>
      <Card size="small" title={t("chat.contextInspectorRecallTitle")}>
        <Space direction="vertical" size={6}>
          <Typography.Text type="secondary">{t("chat.contextInspectorQueryLabel")}</Typography.Text>
          <Typography.Text>{inspector?.recall_query?.trim() || t("chat.contextInspectorNoQuery")}</Typography.Text>
          {inspector?.history_recall ? (
            <Tag color={inspector.history_recall.allow_tool ? "green" : "default"}>
              {inspector.history_recall.allow_tool ? t("chat.contextInspectorHistoryAllowed") : t("chat.contextInspectorHistoryBlocked")}
            </Tag>
          ) : (
            <Typography.Text type="secondary">{t("chat.contextInspectorNoHistoryDecision")}</Typography.Text>
          )}
        </Space>
      </Card>
      <MemoryPreviewSection title={t("chat.contextInspectorMemoryIdentity")} items={memoryPreview.identity} />
      <MemoryPreviewSection title={t("chat.contextInspectorMemoryCore")} items={memoryPreview.core} />
      <MemoryPreviewSection title={t("chat.contextInspectorMemoryRelevant")} items={memoryPreview.relevant} />
      <Card size="small" title={t("chat.contextArchiveTitle")}>
        {inspector?.transcript_refs?.length ? (
          <List
            size="small"
            dataSource={inspector.transcript_refs}
            renderItem={(ref) => (
              <List.Item
                actions={[
                  <Button key="view" size="small" icon={<HistoryOutlined />} onClick={() => setArchiveRef(ref)}>
                    {t("chat.contextArchiveView")}
                  </Button>,
                ]}
              >
                <Typography.Text code ellipsis style={{ maxWidth: 320 }}>
                  {ref}
                </Typography.Text>
              </List.Item>
            )}
          />
        ) : (
          <Typography.Text type="secondary">{t("chat.contextInspectorNoArchive")}</Typography.Text>
        )}
      </Card>
      <TranscriptArchiveModal ref={archiveRef} sessionId={sessionId} token={token} onClose={() => setArchiveRef(null)} />
    </Space>
  );
}

function TranscriptArchiveModal({
  ref: archiveRef,
  sessionId,
  token,
  onClose,
}: {
  ref: string | null;
  sessionId: string;
  token: string | null;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const archiveQuery = useQuery({
    queryKey: ["session-transcript", token, sessionId, archiveRef],
    enabled: !!archiveRef && !!sessionId,
    queryFn: async () => getSessionTranscript(token || null, sessionId, archiveRef!),
  });
  const messages = archiveQuery.data?.messages ?? [];
  return (
    <Modal
      open={!!archiveRef}
      onCancel={onClose}
      footer={null}
      width={760}
      title={
        <Space size={8}>
          <HistoryOutlined />
          {t("chat.contextArchiveModalTitle")}
          {archiveRef ? <Typography.Text code>{archiveRef}</Typography.Text> : null}
        </Space>
      }
    >
      {archiveQuery.isLoading ? (
        <Alert type="info" showIcon message={t("chat.contextArchiveLoading")} />
      ) : archiveQuery.isError ? (
        <Alert type="error" showIcon message={t("chat.contextArchiveError")} />
      ) : messages.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("chat.contextArchiveEmpty")} />
      ) : (
        <List
          size="small"
          dataSource={messages}
          renderItem={(msg) => <ArchiveMessageRow msg={msg} />}
          style={{ maxHeight: "60vh", overflowY: "auto" }}
        />
      )}
    </Modal>
  );
}

function ArchiveMessageRow({ msg }: { msg: ProtocolMessage }) {
  const text = msg.metadata?.text ?? msg.content?.filter((block) => block.type === "text").map((block) => block.text || "").join("") ?? "";
  const toolNames = msg.content?.filter((block) => block.type === "tool_use").map((block) => block.name || "tool") ?? [];
  const roleLabel = msg.role === "user" ? "You" : msg.role === "assistant" ? "GoDex" : msg.role || "message";
  return (
    <List.Item style={{ alignItems: "flex-start" }}>
      <Space direction="vertical" size={2} style={{ width: "100%" }}>
        <Space size={8}>
          <Tag color={msg.role === "user" ? "blue" : "green"}>{roleLabel}</Tag>
          {toolNames.length > 0 ? <Tag>tool: {toolNames.join(", ")}</Tag> : null}
        </Space>
        {text.trim() ? (
          <Typography.Paragraph style={{ marginBottom: 0, whiteSpace: "pre-wrap" }}>{text}</Typography.Paragraph>
        ) : toolNames.length === 0 ? (
          <Typography.Text type="secondary">—</Typography.Text>
        ) : null}
      </Space>
    </List.Item>
  );
}

function MemoryPreviewSection({
  title,
  items,
}: {
  title: string;
  items: SessionContextInspector["memory_preview"]["identity"];
}) {
  return (
    <Card size="small" title={title} extra={<Tag>{items.length}</Tag>}>
      {items.length === 0 ? (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
      ) : (
        <List
          size="small"
          dataSource={items}
          renderItem={(item) => (
            <List.Item>
              <Space direction="vertical" size={2}>
                <Typography.Text strong>{item.title}</Typography.Text>
                <Typography.Text type="secondary">{item.summary}</Typography.Text>
                <Space wrap>
                  {item.score ? <Tag>score {item.score}</Tag> : null}
                  {(item.tags ?? []).map((tag) => (
                    <Tag key={tag}>{tag}</Tag>
                  ))}
                </Space>
              </Space>
            </List.Item>
          )}
        />
      )}
    </Card>
  );
}
