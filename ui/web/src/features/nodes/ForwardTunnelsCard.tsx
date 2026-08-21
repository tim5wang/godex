import { useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  App,
  Button,
  Card,
  Empty,
  Form,
  Input,
  InputNumber,
  Modal,
  Popconfirm,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { CheckCircleOutlined, CloseCircleOutlined, DeleteOutlined, PlusOutlined, ThunderboltOutlined } from "@ant-design/icons";
import { useState } from "react";
import { useI18n } from "../../i18n";
import { checkForward, createForward, deleteForward, listForwards } from "../../lib/api";
import type { ForwardCheckResult, ForwardStatus } from "../../lib/types";

interface Props {
  nodeID: string;
  token: string | null;
}

export function ForwardTunnelsCard({ nodeID, token }: Props) {
  const { t } = useI18n();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const [createOpen, setCreateOpen] = useState(false);
  const [checking, setChecking] = useState<string | null>(null);
  const [checkResult, setCheckResult] = useState<ForwardCheckResult | null>(null);
  const [checkError, setCheckError] = useState<string | null>(null);
  const [form] = Form.useForm();

  const forwardsQuery = useQuery({
    queryKey: ["control-forwards", token],
    refetchInterval: 15_000,
    queryFn: async () => listForwards(token),
  });
  const forwards = (forwardsQuery.data ?? []).filter((f) => f.node_id === nodeID);

  const refresh = () => void queryClient.invalidateQueries({ queryKey: ["control-forwards", token] });

  const submitCreate = async () => {
    try {
      const values = await form.validateFields();
      await createForward(token, {
        name: values.name,
        node_id: nodeID,
        local_port: values.local_port,
        target: values.target.trim(),
      });
      void message.success(t("nodes.forwardAddSuccess"));
      setCreateOpen(false);
      form.resetFields();
      await refresh();
    } catch (err) {
      if (err instanceof Error) {
        void message.error(t("nodes.forwardAddFailed") + ": " + err.message);
      }
    }
  };

  const removeForward = async (id: string) => {
    try {
      await deleteForward(token, id);
      void message.success(t("nodes.forwardDeleted"));
      await refresh();
    } catch (err) {
      void message.error(err instanceof Error ? err.message : String(err));
    }
  };

  const runCheck = async (id: string) => {
    setChecking(id);
    setCheckError(null);
    try {
      const result = await checkForward(token, id);
      setCheckResult(result);
    } catch (err) {
      setCheckResult(null);
      setCheckError(err instanceof Error ? err.message : String(err));
    } finally {
      setChecking(null);
    }
  };

  const stateTag = (state: ForwardStatus["state"]) => {
    switch (state) {
      case "running":
        return <Tag color="green">{t("nodes.forwardStateRunning")}</Tag>;
      case "error":
        return <Tag color="red">{t("nodes.forwardStateError")}</Tag>;
      default:
        return <Tag>{t("nodes.forwardStateStopped")}</Tag>;
    }
  };

  return (
    <Card
      title={t("nodes.forwardTitle")}
      extra={
        <Button size="small" type="primary" icon={<PlusOutlined />} onClick={() => setCreateOpen(true)}>
          {t("nodes.forwardAdd")}
        </Button>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: -4 }}>
        {t("nodes.forwardSubtitle")}
      </Typography.Paragraph>
      <Table<ForwardStatus>
        rowKey="id"
        size="small"
        loading={forwardsQuery.isLoading}
        dataSource={forwards}
        pagination={false}
        scroll={{ x: 640 }}
        locale={{ emptyText: <Empty description={t("nodes.forwardEmpty")} /> }}
        columns={[
          {
            title: t("nodes.forwardLocal"),
            dataIndex: "local_port",
            width: 110,
            render: (port: number) => <Typography.Text code>127.0.0.1:{port}</Typography.Text>,
          },
          {
            title: t("nodes.forwardTarget"),
            dataIndex: "target",
            width: 200,
            render: (v: string) => <Typography.Text code>{v}</Typography.Text>,
          },
          {
            title: t("nodes.forwardName"),
            dataIndex: "name",
            ellipsis: true,
            render: (v?: string, row) => v || row.id,
          },
          {
            title: t("nodes.forwardState"),
            dataIndex: "state",
            width: 90,
            render: (state: ForwardStatus["state"], row) => (
              <Space direction="vertical" size={0}>
                {stateTag(state)}
                {state === "error" && row.error ? (
                  <Typography.Text type="danger" style={{ fontSize: 12 }}>
                    {row.error}
                  </Typography.Text>
                ) : null}
              </Space>
            ),
          },
          {
            title: t("nodes.forwardConns"),
            dataIndex: "active_conns",
            width: 100,
            render: (v: number) => v ?? 0,
          },
          {
            title: t("nodes.forwardLatency"),
            dataIndex: "last_latency_ms",
            width: 110,
            render: (v?: number) =>
              v != null ? t("nodes.forwardLatencyMs", { ms: String(v) }) : "-",
          },
          {
            title: t("nodes.actions"),
            width: 150,
            render: (_: unknown, row) => (
              <Space>
                <Button
                  size="small"
                  icon={<ThunderboltOutlined />}
                  loading={checking === row.id}
                  onClick={() => void runCheck(row.id)}
                >
                  {t("nodes.forwardCheck")}
                </Button>
                <Popconfirm
                  title={t("nodes.forwardDeleteTitle")}
                  description={t("nodes.forwardDeleteConfirm")}
                  okText={t("nodes.forwardDeleteTitle")}
                  cancelText={t("common.cancel")}
                  onConfirm={() => void removeForward(row.id)}
                >
                  <Button size="small" danger icon={<DeleteOutlined />} />
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />

      <Modal
        title={t("nodes.forwardAdd")}
        open={createOpen}
        onOk={() => void submitCreate()}
        onCancel={() => {
          setCreateOpen(false);
          form.resetFields();
        }}
        okText={t("nodes.forwardAdd")}
        cancelText={t("common.cancel")}
        destroyOnClose
      >
        <Form form={form} layout="vertical" initialValues={{ local_port: 3921 }}>
          <Form.Item name="name" label={t("nodes.forwardName")}>
            <Input placeholder="e.g. node-b gateway" />
          </Form.Item>
          <Form.Item
            name="local_port"
            label={t("nodes.forwardLocalPort")}
            rules={[
              { required: true },
              {
                validator: (_rule, value) =>
                  value >= 1 && value <= 65535 ? Promise.resolve() : Promise.reject(new Error(t("nodes.forwardInvalidPort"))),
              },
            ]}
          >
            <InputNumber min={1} max={65535} style={{ width: "100%" }} />
          </Form.Item>
          <Form.Item
            name="target"
            label={t("nodes.forwardTarget")}
            rules={[{ required: true, message: t("nodes.forwardTargetRequired") }]}
          >
            <Input placeholder={t("nodes.forwardTargetPlaceholder")} />
          </Form.Item>
        </Form>
      </Modal>

      <Modal
        title={t("nodes.forwardCheckTitle")}
        open={checkResult != null || checkError != null}
        footer={null}
        onCancel={() => {
          setCheckResult(null);
          setCheckError(null);
        }}
      >
        {checkError ? <Alert type="error" showIcon message={checkError} /> : null}
        {checkResult ? (
          <Space direction="vertical" size={12} style={{ width: "100%" }}>
            <Alert
              type={checkResult.ok ? "success" : "error"}
              showIcon
              message={checkResult.ok ? t("nodes.forwardCheckOk") : t("nodes.forwardCheckFail")}
            />
            {checkResult.steps.map((step, index) => (
              <Space key={`${step.name}-${index}`} align="start" style={{ width: "100%" }}>
                {step.ok ? (
                  <CheckCircleOutlined style={{ color: "#52c41a", marginTop: 4 }} />
                ) : (
                  <CloseCircleOutlined style={{ color: "#ff4d4f", marginTop: 4 }} />
                )}
                <Space direction="vertical" size={0}>
                  <Typography.Text strong>
                    {step.name === "listener"
                      ? t("nodes.forwardStepListener")
                      : step.name === "node"
                        ? t("nodes.forwardStepNode")
                        : t("nodes.forwardStepTarget")}
                    {step.latency_ms != null ? (
                      <Typography.Text type="secondary" style={{ marginLeft: 8, fontSize: 12 }}>
                        {t("nodes.forwardLatencyMs", { ms: String(step.latency_ms) })}
                      </Typography.Text>
                    ) : null}
                  </Typography.Text>
                  <Typography.Text type={step.ok ? "secondary" : "danger"} style={{ fontSize: 12 }}>
                    {step.detail}
                  </Typography.Text>
                </Space>
              </Space>
            ))}
          </Space>
        ) : null}
      </Modal>
    </Card>
  );
}
