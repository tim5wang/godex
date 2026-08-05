import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, Button, Card, Empty, Space, Table, Tag, Typography } from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { useNavigate, useParams } from "react-router-dom";
import { useI18n } from "../../i18n";
import { getMeta, listControlNodes } from "../../lib/api";
import type { ControlNode } from "../../lib/types";
import { useSettingsStore } from "../../store/settings";
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

  if (!canReachNodes) {
    return (
      <main className="page-shell">
        <Alert type="warning" showIcon message={t("nodes.authRequired")} />
      </main>
    );
  }

  return (
    <main className="page-shell">
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
        <Table<ControlNode>
          rowKey="id"
          loading={nodesQuery.isLoading}
          dataSource={nodesQuery.data ?? []}
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
              render: (values?: string[]) => (
                <Space size={[4, 4]} wrap>
                  {(values ?? []).slice(0, 6).map((item) => (
                    <Tag key={item}>{item}</Tag>
                  ))}
                  {(values?.length ?? 0) > 6 ? <Tag>+{(values?.length ?? 0) - 6}</Tag> : null}
                </Space>
              ),
            },
          ]}
        />
      </Card>
    </main>
  );
}
