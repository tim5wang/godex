import { useQuery } from "@tanstack/react-query";
import { Alert, App, Button, Card, Descriptions, Empty, Progress, Space, Table, Tag, Tooltip, Typography } from "antd";
import { ArrowLeftOutlined, CheckOutlined, CodeOutlined, FileTextOutlined, ReloadOutlined, CommentOutlined, StopOutlined } from "@ant-design/icons";
import { useNavigate, useParams } from "react-router-dom";
import { useI18n } from "../../i18n";
import { approveNodePermission, denyNodePermission, getNodeOverview } from "../../lib/api";
import type { NodeApprovalInfo, NodeJobInfo, NodeSessionInfo, NodeStoredEvent } from "../../lib/types";
import { useSettingsStore } from "../../store/settings";
import { useNodeContextStore } from "../../store/nodeContext";
import { ForwardTunnelsCard } from "./ForwardTunnelsCard";

export function NodeDetailPage() {
  const { t } = useI18n();
  const navigate = useNavigate();
  const { id = "" } = useParams();
  const token = useSettingsStore((state) => state.token);
  const setNode = useNodeContextStore((state) => state.setNode);
  const { message } = App.useApp();
  const query = useQuery({
    queryKey: ["node-overview", id, token],
    queryFn: () => getNodeOverview(id, token || null),
  });

  const data = query.data;
  const node = data?.node;
  const overview = data?.overview;

  const resolveApproval = async (approval: NodeApprovalInfo, approve: boolean) => {
    if (!approval.session_id) {
      void message.warning(t("nodes.approvalMissingSession"));
      return;
    }
    try {
      if (approve) {
        await approveNodePermission(id, token || null, approval.session_id, approval.id, "once");
        void message.success(t("nodes.approvalApproved"));
      } else {
        await denyNodePermission(id, token || null, approval.session_id, approval.id);
        void message.success(t("nodes.approvalDenied"));
      }
      void query.refetch();
    } catch (err) {
      void message.error(t("nodes.approvalFailed"));
    }
  };

  const openRemote = (page: "chat" | "terminal" | "files") => {
    setNode(id, node?.name);
    if (page === "files") {
      navigate("/files");
      return;
    }
    navigate("/chat");
  };

  return (
    <main className="page-shell">
      <Space direction="vertical" size={16} style={{ width: "100%" }}>
        <Space wrap>
          <Button icon={<ArrowLeftOutlined />} onClick={() => navigate("/nodes")}>
            {t("nodes.back")}
          </Button>
          <Button icon={<ReloadOutlined />} onClick={() => void query.refetch()}>
            {t("nodes.refresh")}
          </Button>
          <Tooltip title={node?.relay_status !== "connected" ? t("nodes.remoteDisabledHint") : undefined}>
            <Button icon={<CommentOutlined />} disabled={node?.relay_status !== "connected"} onClick={() => openRemote("chat")}>
              {t("nodes.openChat")}
            </Button>
          </Tooltip>
          <Tooltip title={node?.relay_status !== "connected" ? t("nodes.remoteDisabledHint") : undefined}>
            <Button icon={<CodeOutlined />} disabled={node?.relay_status !== "connected"} onClick={() => openRemote("terminal")}>
              {t("nodes.openTerminal")}
            </Button>
          </Tooltip>
          <Tooltip title={node?.relay_status !== "connected" ? t("nodes.remoteDisabledHint") : undefined}>
            <Button icon={<FileTextOutlined />} disabled={node?.relay_status !== "connected"} onClick={() => openRemote("files")}>
              {t("nodes.openFiles")}
            </Button>
          </Tooltip>
        </Space>

        {query.isError ? <Alert type="error" showIcon message={t("nodes.detailLoadError")} /> : null}

        <Card
          loading={query.isLoading}
          title={
            <Space direction="vertical" size={2}>
              <Typography.Text strong>{node?.name || id}</Typography.Text>
              <Typography.Text type="secondary" copyable={{ text: id }}>
                {id}
              </Typography.Text>
            </Space>
          }
        >
          <Descriptions
            column={{ xs: 1, sm: 2, md: 3 }}
            size="small"
            items={[
              {
                key: "status",
                label: t("nodes.status"),
                children: (
                  <Tag color={node?.status === "online" ? "green" : "default"}>{node?.status || "offline"}</Tag>
                ),
              },
              {
                key: "relay",
                label: t("nodes.relayStatus"),
                children: (
                  <Tag color={node?.relay_status === "connected" ? "blue" : "default"}>
                    {node?.relay_status || "disconnected"}
                  </Tag>
                ),
              },
              { key: "version", label: t("nodes.version"), children: node?.version || "-" },
              {
                key: "workspace",
                label: t("nodes.workspace"),
                children: <Typography.Text title={node?.workspace_dir}>{node?.workspace_dir || "-"}</Typography.Text>,
              },
              {
                key: "endpoint",
                label: t("nodes.endpoint"),
                children: <Typography.Text title={node?.endpoint}>{node?.endpoint || "-"}</Typography.Text>,
              },
              {
                key: "lastSeen",
                label: t("nodes.lastSeen"),
                children: node?.last_seen ? new Date(node.last_seen).toLocaleString() : "-",
              },
              { key: "trust", label: t("nodes.trustLevel"), children: node?.trust_level || "-" },
            ]}
          />
          <div style={{ marginTop: 12 }}>
            <Typography.Text type="secondary">{t("nodes.capabilities")}: </Typography.Text>
            <Space size={[4, 4]} wrap>
              {(overview?.capabilities ?? node?.capabilities ?? []).map((item: string) => (
                <Tag key={item}>{item}</Tag>
              ))}
            </Space>
          </div>
        </Card>

        <ForwardTunnelsCard nodeID={id} token={token} />

        <Card title={t("nodes.runningSessions")} loading={query.isLoading}>
          <Table<NodeSessionInfo>
            rowKey="id"
            dataSource={overview?.sessions ?? []}
            pagination={false}
            scroll={{ x: 560 }}
            locale={{ emptyText: <Empty description={t("nodes.noSessions")} /> }}
            columns={[
              { title: "ID", dataIndex: "id" },
              { title: t("nodes.sessionTitle"), dataIndex: "title", render: (v?: string) => v || "-" },
              {
                title: t("nodes.status"),
                dataIndex: "running",
                render: (running?: boolean) => (
                  <Tag color={running ? "green" : "default"}>{running ? t("nodes.running") : t("nodes.idle")}</Tag>
                ),
              },
              {
                title: t("nodes.updatedAt"),
                dataIndex: "updated_at",
                width: 190,
                render: (v?: string) => (v ? new Date(v).toLocaleString() : "-"),
              },
            ]}
          />
        </Card>

        <Card title={t("nodes.runningJobs")} loading={query.isLoading}>
          <Table<NodeJobInfo>
            rowKey="id"
            dataSource={overview?.jobs ?? []}
            pagination={false}
            scroll={{ x: 640 }}
            locale={{ emptyText: <Empty description={t("nodes.noJobs")} /> }}
            columns={[
              { title: "ID", dataIndex: "id" },
              { title: t("nodes.jobName"), dataIndex: "name", render: (v?: string) => v || "-" },
              { title: t("nodes.jobStatus"), dataIndex: "status", render: (v?: string) => v || "-" },
              {
                title: t("nodes.progress"),
                render: (_: unknown, job) =>
                  job.total_turns && job.total_turns > 0 ? (
                    <Progress
                      percent={Math.round(((job.turn ?? 0) / job.total_turns) * 100)}
                      size="small"
                      format={() => `${job.turn ?? 0}/${job.total_turns}`}
                    />
                  ) : (
                    "-"
                  ),
              },
              { title: t("nodes.jobPhase"), dataIndex: "phase", render: (v?: string) => v || "-" },
            ]}
          />
        </Card>

        <Card title={t("nodes.pendingApprovals")} loading={query.isLoading}>
          <Table<NodeApprovalInfo>
            rowKey="id"
            dataSource={overview?.approvals ?? []}
            pagination={false}
            scroll={{ x: 700 }}
            locale={{ emptyText: <Empty description={t("nodes.noApprovals")} /> }}
            columns={[
              { title: "ID", dataIndex: "id" },
              { title: t("nodes.sessionTitle"), dataIndex: "session_id", render: (v?: string) => v || "-" },
              { title: t("nodes.approvalIntent"), dataIndex: "intent", render: (v?: string) => v || "-" },
              {
                title: t("nodes.status"),
                dataIndex: "status",
                render: (v?: string) => <Tag color={v === "pending" ? "orange" : "default"}>{v || "pending"}</Tag>,
              },
              {
                title: t("nodes.actions"),
                width: 160,
                render: (_: unknown, approval) => (
                  <Space>
                    <Button
                      size="small"
                      type="primary"
                      icon={<CheckOutlined />}
                      disabled={approval.status !== "pending"}
                      onClick={() => void resolveApproval(approval, true)}
                    >
                      {t("nodes.approve")}
                    </Button>
                    <Button
                      size="small"
                      danger
                      icon={<StopOutlined />}
                      disabled={approval.status !== "pending"}
                      onClick={() => void resolveApproval(approval, false)}
                    >
                      {t("nodes.deny")}
                    </Button>
                  </Space>
                ),
              },
            ]}
          />
        </Card>

        <Card title={t("nodes.recentEvents")} loading={query.isLoading}>
          {!overview?.recent_events?.length ? (
            <Empty description={t("nodes.noEvents")} />
          ) : (
            <Table<NodeStoredEvent>
              rowKey={(row, index) => `${row.time}-${index}`}
              dataSource={[...(overview.recent_events ?? [])].reverse()}
              pagination={{ pageSize: 10, size: "small" }}
              scroll={{ x: 620 }}
              columns={[
                {
                  title: t("nodes.eventTime"),
                  dataIndex: "time",
                  width: 190,
                  render: (v: string) => new Date(v).toLocaleString(),
                },
                {
                  title: t("nodes.eventKind"),
                  dataIndex: "kind",
                  width: 120,
                  render: (v: string) => <Tag>{v}</Tag>,
                },
                { title: t("nodes.eventDetail"), dataIndex: "detail", render: (v?: string) => v || "-" },
              ]}
            />
          )}
        </Card>
      </Space>
    </main>
  );
}
