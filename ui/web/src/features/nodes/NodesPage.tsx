import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, App, Button, Card, Empty, Grid, Popconfirm, Space, Spin, Table, Tag, Typography } from "antd";
import { DeleteOutlined, ReloadOutlined } from "@ant-design/icons";
import { useNavigate, useParams } from "react-router-dom";
import { useI18n } from "../../i18n";
import { deleteControlNode, getMeta, listControlNodes } from "../../lib/api";
import type { ControlNode } from "../../lib/types";
import { useSettingsStore } from "../../store/settings";
import { JoinNodeCard } from "./JoinNodeCard";
import { NodeDetailPage } from "./NodeDetailPage";

export function NodesPage() {
  const { id } = useParams();
  if (id) {
    return <NodeDetailPage />;
  }
  return <NodesListPage />;
}

function NodesListPage() {
  const { t } = useI18n();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const token = useSettingsStore((state) => state.token);
  const metaQuery = useQuery({ queryKey: ["meta"], queryFn: getMeta });
  const authRequired = metaQuery.data?.auth_required ?? false;
  const canReachNodes = !authRequired || !!token;
  const nodesQuery = useQuery({
    queryKey: ["control-nodes", token],
    enabled: canReachNodes,
    refetchInterval: 15_000,
    queryFn: async () => listControlNodes(token || null),
  });
  const screens = Grid.useBreakpoint();
  const isNarrow = !screens.md;

  const removeNode = async (node: ControlNode) => {
    try {
      await deleteControlNode(token || null, node.id);
      void message.success(t("nodes.deleteSuccess"));
      await queryClient.invalidateQueries({ queryKey: ["control-nodes", token] });
    } catch (err) {
      void message.error(err instanceof Error ? err.message : String(err));
    }
  };

  const renderCapabilities = (values?: string[]) => (
    <Space size={[4, 4]} wrap>
      {(values ?? []).slice(0, 6).map((item) => (
        <Tag key={item}>{item}</Tag>
      ))}
      {(values?.length ?? 0) > 6 ? <Tag>+{(values?.length ?? 0) - 6}</Tag> : null}
    </Space>
  );

  const renderDelete = (node: ControlNode) => (
    <Popconfirm
      title={t("nodes.deleteTitle")}
      description={t("nodes.deleteConfirm")}
      okText={t("nodes.deleteTitle")}
      cancelText={t("common.cancel")}
      onConfirm={() => void removeNode(node)}
      onPopupClick={(e) => e.stopPropagation()}
    >
      <Button size="small" danger icon={<DeleteOutlined />} onClick={(e) => e.stopPropagation()} />
    </Popconfirm>
  );

  if (!canReachNodes) {
    return (
      <main className="page-shell">
        <Alert type="warning" showIcon message={t("nodes.authRequired")} />
      </main>
    );
  }

  const nodes = nodesQuery.data ?? [];

  // Narrow screens: card list instead of the wide table.
  const nodeCards = nodes.map((node) => (
    <Card
      key={node.id}
      size="small"
      hoverable
      onClick={() => navigate(`/nodes/${encodeURIComponent(node.id)}`)}
      title={
        <Space direction="vertical" size={2}>
          <Typography.Text strong>{node.name || node.id}</Typography.Text>
          <Typography.Text type="secondary" copyable={{ text: node.id }}>
            {node.id}
          </Typography.Text>
        </Space>
      }
      extra={renderDelete(node)}
      style={{ cursor: "pointer" }}
    >
      <Space direction="vertical" size={8} style={{ width: "100%" }}>
        <Space wrap>
          <Tag color={node.status === "online" ? "green" : "default"}>{node.status || "offline"}</Tag>
          <Tag color={node.relay_status === "connected" ? "blue" : "default"}>
            {node.relay_status || "disconnected"}
          </Tag>
        </Space>
        <div>
          <Typography.Text type="secondary">{t("nodes.workspace")}: </Typography.Text>
          <Typography.Text title={node.workspace_dir}>{node.workspace_dir || "-"}</Typography.Text>
        </div>
        <div>
          <Typography.Text type="secondary">{t("nodes.endpoint")}: </Typography.Text>
          <Typography.Text title={node.endpoint}>{node.endpoint || "-"}</Typography.Text>
        </div>
        <div>
          <Typography.Text type="secondary">{t("nodes.version")}: </Typography.Text>
          <Typography.Text>{node.version || "-"}</Typography.Text>
          <Typography.Text type="secondary" style={{ marginLeft: 12 }}>
            {t("nodes.lastSeen")}:{" "}
          </Typography.Text>
          <Typography.Text>{node.last_seen ? new Date(node.last_seen).toLocaleString() : "-"}</Typography.Text>
        </div>
        {renderCapabilities(node.capabilities)}
      </Space>
    </Card>
  ));

  return (
    <main className="page-shell">
      <JoinNodeCard />
      <Card
        title={t("nodes.tableTitle")}
        extra={
          <Button
            icon={<ReloadOutlined />}
            onClick={() => void queryClient.invalidateQueries({ queryKey: ["control-nodes", token] })}
          >
            {t("nodes.refresh")}
          </Button>
        }
      >
        {isNarrow ? (
          <Spin spinning={nodesQuery.isLoading}>
            <Space direction="vertical" size={12} style={{ width: "100%" }}>
              {nodeCards}
              {nodes.length === 0 ? <Empty description={t("nodes.empty")} /> : null}
            </Space>
          </Spin>
        ) : (
          <Table<ControlNode>
            rowKey="id"
            loading={nodesQuery.isLoading}
            dataSource={nodes}
            locale={{ emptyText: <Empty description={t("nodes.empty")} /> }}
            pagination={{ pageSize: 20, showSizeChanger: true }}
            onRow={(node) => ({
              onClick: () => navigate(`/nodes/${encodeURIComponent(node.id)}`),
              style: { cursor: "pointer" },
            })}
            columns={[
              {
                title: t("nodes.name"),
                dataIndex: "name",
                render: (_value, node) => (
                  <Space direction="vertical" size={2}>
                    <Typography.Text strong>{node.name || node.id}</Typography.Text>
                    <Typography.Text type="secondary" copyable={{ text: node.id }}>
                      {node.id}
                    </Typography.Text>
                  </Space>
                ),
              },
              {
                title: t("nodes.status"),
                dataIndex: "status",
                width: 120,
                render: (status: ControlNode["status"]) => (
                  <Tag color={status === "online" ? "green" : "default"}>{status || "offline"}</Tag>
                ),
              },
              {
                title: t("nodes.workspace"),
                dataIndex: "workspace_dir",
                ellipsis: true,
                render: (value?: string) => <Typography.Text title={value}>{value || "-"}</Typography.Text>,
              },
              {
                title: t("nodes.endpoint"),
                dataIndex: "endpoint",
                ellipsis: true,
                render: (value?: string) => <Typography.Text title={value}>{value || "-"}</Typography.Text>,
              },
              {
                title: t("nodes.version"),
                dataIndex: "version",
                width: 120,
                render: (value?: string) => value || "-",
              },
              {
                title: t("nodes.lastSeen"),
                dataIndex: "last_seen",
                width: 190,
                render: (value?: string) => (value ? new Date(value).toLocaleString() : "-"),
              },
              {
                title: t("nodes.capabilities"),
                dataIndex: "capabilities",
                render: (_value, node) => renderCapabilities(node.capabilities),
              },
              {
                title: t("nodes.deleteTitle"),
                width: 90,
                render: (_value, node) => renderDelete(node),
              },
            ]}
          />
        )}
      </Card>
    </main>
  );
}
