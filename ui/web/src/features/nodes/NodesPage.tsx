import { useMemo, useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, App, Button, Empty, Input, Segmented, Space, Spin, Typography } from "antd";
import { ReloadOutlined, SettingOutlined } from "@ant-design/icons";
import { useNavigate, useParams } from "react-router-dom";
import { useI18n } from "../../i18n";
import { deleteControlNode, getMeta, listControlNodes } from "../../lib/api";
import type { ControlNode } from "../../lib/types";
import { useSettingsStore } from "../../store/settings";
import { NodeCard } from "./NodeCard";
import { NodeDetailPage } from "./NodeDetailPage";
import { NodeSettingsPanel } from "./NodeSettingsPanel";
import { matchesNodeQuery, nodeVisualStatus, sortNodesByStatus, type NodeVisualStatus } from "./nodeStatus";

export function NodesPage() {
  const { id } = useParams();
  if (id) {
    return <NodeDetailPage />;
  }
  return <NodesListPage />;
}

type StatusFilter = "all" | NodeVisualStatus;

const SETTINGS_KEY = "settings";

function NodesListPage() {
  const { t } = useI18n();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const token = useSettingsStore((state) => state.token);
  const [query, setQuery] = useState("");
  const [filter, setFilter] = useState<StatusFilter>("all");
  // null = not touched yet, so the section follows its default; once the operator
  // toggles it, their choice wins (including "keep it collapsed" on an empty list).
  const [settingsOpen, setSettingsOpen] = useState<string[] | null>(null);

  const metaQuery = useQuery({ queryKey: ["meta"], queryFn: getMeta });
  const authRequired = metaQuery.data?.auth_required ?? false;
  const canReachNodes = !authRequired || !!token;
  const nodesQuery = useQuery({
    queryKey: ["control-nodes", token],
    enabled: canReachNodes,
    refetchInterval: 15_000,
    queryFn: async () => listControlNodes(token || null),
  });

  const nodes = useMemo(() => nodesQuery.data ?? [], [nodesQuery.data]);

  // Relative timestamps are pinned to the last data refresh, so a re-render from
  // typing in the search box cannot make the same node read "2 分钟前" then "3 分钟前".
  const now = nodesQuery.dataUpdatedAt || Date.now();

  const removeNode = async (node: ControlNode) => {
    try {
      await deleteControlNode(token || null, node.id);
      void message.success(t("nodes.deleteSuccess"));
      await queryClient.invalidateQueries({ queryKey: ["control-nodes", token] });
    } catch (err) {
      void message.error(err instanceof Error ? err.message : String(err));
    }
  };

  const counts = useMemo(() => {
    const base: Record<StatusFilter, number> = { all: nodes.length, healthy: 0, degraded: 0, offline: 0 };
    for (const node of nodes) {
      base[nodeVisualStatus(node)] += 1;
    }
    return base;
  }, [nodes]);

  // Status order first (healthy → degraded → offline), not registry order: the
  // first card should be one you can actually open.
  const visible = useMemo(
    () =>
      sortNodesByStatus(nodes).filter(
        (node) => (filter === "all" || nodeVisualStatus(node) === filter) && matchesNodeQuery(node, query),
      ),
    [nodes, filter, query],
  );

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["control-nodes", token] });

  if (!canReachNodes) {
    return (
      <main className="page-shell">
        <Alert type="warning" showIcon message={t("nodes.authRequired")} />
      </main>
    );
  }

  const isLoading = nodesQuery.isLoading;
  // Onboarding is not the daily task, so the panel ships collapsed — unless the
  // list is loaded AND empty, where adding the first node IS the task.
  //
  // Deliberately keyed on isSuccess rather than `nodes.length === 0`: the length is
  // 0 while the query is still loading too, so an unqualified check would flash the
  // panel open on every visit to a center that has nodes.
  const empty = nodesQuery.isSuccess && nodes.length === 0;
  const expandedSettings = settingsOpen ?? (empty ? [SETTINGS_KEY] : []);

  return (
    <main className="page-shell">
      <div className="node-toolbar">
        <div className="node-toolbar-filters">
          <Input.Search
            allowClear
            value={query}
            onChange={(e) => setQuery(e.target.value)}
            placeholder={t("nodes.searchPlaceholder")}
            style={{ width: 240 }}
          />
          <Segmented
            value={filter}
            onChange={(value) => setFilter(value as StatusFilter)}
            options={[
              { value: "all", label: `${t("nodes.filterAll")} ${counts.all}` },
              { value: "healthy", label: `${t("nodes.filterHealthy")} ${counts.healthy}` },
              { value: "degraded", label: `${t("nodes.filterDegraded")} ${counts.degraded}` },
              { value: "offline", label: `${t("nodes.filterOffline")} ${counts.offline}` },
            ]}
          />
        </div>
        <Space>
          <Button icon={<ReloadOutlined />} loading={nodesQuery.isFetching} onClick={refresh}>
            {t("nodes.refresh")}
          </Button>
          <Button
            icon={<SettingOutlined />}
            type={expandedSettings.length > 0 ? "primary" : "default"}
            onClick={() => setSettingsOpen(expandedSettings.length > 0 ? [] : [SETTINGS_KEY])}
          >
            {t("nodes.settingsTitle")}
          </Button>
        </Space>
      </div>

      {/* Onboarding is not the daily task: keep it collapsed until asked for —
          except on a brand-new center, where adding the first node IS the task. */}
      <NodeSettingsPanel activeKey={expandedSettings} onChange={(keys) => setSettingsOpen(keys)} />

      <Spin spinning={isLoading}>
        {!isLoading && nodes.length === 0 ? (
          <Empty description={t("nodes.empty")} />
        ) : visible.length === 0 ? (
          <Empty description={t("nodes.noMatch")} />
        ) : (
          <div className="node-card-grid">
            {visible.map((node) => (
              <NodeCard
                key={node.id}
                node={node}
                now={now}
                onOpen={() => navigate(`/nodes/${encodeURIComponent(node.id)}`)}
                onDelete={() => void removeNode(node)}
              />
            ))}
          </div>
        )}
      </Spin>

      {visible.length > 0 ? (
        <Typography.Text type="secondary" className="node-count-hint">
          {t("nodes.showingCount", { shown: visible.length, total: nodes.length })}
        </Typography.Text>
      ) : null}
    </main>
  );
}
