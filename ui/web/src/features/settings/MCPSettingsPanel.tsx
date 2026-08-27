import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { App as AntApp, Button, Card, Form, Input, Popconfirm, Select, Space, Switch, Table, Tag, Tooltip, Typography } from "antd";
import { CloseOutlined, DeleteOutlined, EditOutlined, PlusOutlined, ReloadOutlined, SaveOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { showError } from "../../lib/notifications";
import {
  listMCPServers,
  createMCPServer,
  updateMCPServer,
  deleteMCPServer,
  testMCPServer,
  getMCPStatuses,
} from "../../lib/api";
import type { MCPServerConfig, MCPServerStatus, MCPServerType } from "../../lib/types";

type MCPFormValues = {
  name: string;
  type: MCPServerType;
  root?: string;
  command?: string;
  args?: string;
  env?: string;
  url?: string;
  headers?: string;
  session_required?: boolean;
};

const TYPE_OPTIONS: { value: MCPServerType; label: string }[] = [
  { value: "stdio", label: "stdio" },
  { value: "streamable-http", label: "Streamable HTTP" },
  { value: "filesystem", label: "Filesystem" },
];

function typeColor(type: MCPServerType) {
  switch (type) {
    case "stdio":
      return "blue";
    case "streamable-http":
      return "purple";
    case "filesystem":
      return "green";
    default:
      return "default";
  }
}

// parseLines converts a textarea of "key=value" lines into a map (or the raw
// JSON for headers/args). Empty input yields undefined so the server is
// unchanged when the field is left blank.
function parseKV(text?: string): Record<string, string> | undefined {
  if (!text?.trim()) return undefined;
  const out: Record<string, string> = {};
  for (const line of text.split("\n")) {
    const trimmed = line.trim();
    if (!trimmed) continue;
    const idx = trimmed.indexOf("=");
    if (idx <= 0) continue;
    out[trimmed.slice(0, idx).trim()] = trimmed.slice(idx + 1).trim();
  }
  return Object.keys(out).length ? out : undefined;
}

function parseArgs(text?: string): string[] | undefined {
  if (!text?.trim()) return undefined;
  return text.split("\n").map((s) => s.trim()).filter(Boolean);
}

function kvToText(map?: Record<string, string>): string {
  if (!map) return "";
  return Object.entries(map)
    .map(([k, v]) => `${k}=${v}`)
    .join("\n");
}

function argsToText(args?: string[]): string {
  return (args ?? []).join("\n");
}

export function MCPSettingsPanel({ token }: { token: string | null }) {
  const { message } = AntApp.useApp();
  const { t } = useI18n();
  const queryClient = useQueryClient();
  const [editing, setEditing] = useState<{ name?: string; values?: MCPServerConfig } | null>(null);
  const [form] = Form.useForm<MCPFormValues>();

  const serversQuery = useQuery({
    queryKey: ["mcp-servers", token],
    queryFn: () => listMCPServers(token),
    enabled: Boolean(token),
  });

  const statusesQuery = useQuery({
    queryKey: ["mcp-statuses", token],
    queryFn: () => getMCPStatuses(token),
    enabled: Boolean(token),
  });

  const invalidate = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["mcp-servers", token] }),
      queryClient.invalidateQueries({ queryKey: ["mcp-statuses", token] }),
    ]);
  };

  const saveMutation = useMutation({
    mutationFn: async (values: MCPFormValues) => {
      const server: MCPServerConfig = {
        name: values.name.trim(),
        type: values.type,
        root: values.root?.trim() || undefined,
        command: values.command?.trim() || undefined,
        args: parseArgs(values.args),
        env: parseKV(values.env),
        url: values.url?.trim() || undefined,
        headers: parseKV(values.headers),
        session_required: values.session_required,
      };
      if (editing?.name) {
        return updateMCPServer(token, editing.name, server);
      }
      return createMCPServer(token, server);
    },
    onSuccess: async () => {
      message.success(editing?.name ? t("settings.mcpServerUpdated") : t("settings.mcpServerCreated"));
      setEditing(null);
      form.resetFields();
      await invalidate();
    },
    onError: (error: unknown) => {
      showError(message, error, t("settings.mcpSaveFailed"));
    },
  });

  const deleteMutation = useMutation({
    mutationFn: (name: string) => deleteMCPServer(token, name),
    onSuccess: async () => {
      message.success(t("settings.mcpServerDeleted"));
      await invalidate();
    },
    onError: (error: unknown) => {
      showError(message, error, t("settings.mcpDeleteFailed"));
    },
  });

  const testMutation = useMutation({
    mutationFn: (name: string) => testMCPServer(token, name),
    onSuccess: (status: MCPServerStatus) => {
      if (status.online) {
        message.success(t("settings.mcpTestOk", { name: status.name, tools: status.tools ?? 0 }));
      } else {
        message.warning(status.error || t("settings.mcpTestFailed"));
      }
      void invalidate();
    },
    onError: (error: unknown) => {
      showError(message, error, t("settings.mcpTestFailed"));
    },
  });

  const openCreate = () => {
    setEditing({});
    form.resetFields();
  };

  const openEdit = (server: MCPServerConfig) => {
    setEditing({ name: server.name, values: server });
    form.setFieldsValue({
      name: server.name,
      type: server.type,
      root: server.root,
      command: server.command,
      args: argsToText(server.args),
      env: kvToText(server.env),
      url: server.url,
      headers: kvToText(server.headers),
      session_required: server.session_required,
    });
  };

  const statusByName = new Map<string, MCPServerStatus>((statusesQuery.data?.statuses ?? []).map((s: MCPServerStatus) => [s.name, s]));
  const servers = serversQuery.data?.servers ?? [];

  const columns = [
    { title: t("settings.mcpNameCol"), dataIndex: "name" },
    { title: t("settings.mcpTypeCol"), dataIndex: "type", render: (v: MCPServerType) => <Tag color={typeColor(v)}>{v}</Tag> },
    { title: t("settings.mcpTargetCol"), render: (_: unknown, server: MCPServerConfig) => server.command || server.url || server.root || "-" },
    {
      title: t("settings.mcpStatusCol"),
      render: (_: unknown, server: MCPServerConfig) => {
        const status = statusByName.get(server.name);
        if (!status) return <Tag>{t("settings.mcpUnknown")}</Tag>;
        return status.online ? (
          <Tag color="green">{t("settings.mcpOnline")}{status.tools !== undefined ? ` · ${status.tools}` : ""}</Tag>
        ) : (
          <Tooltip title={status.error}><Tag color="red">{t("settings.mcpOffline")}</Tag></Tooltip>
        );
      },
    },
    {
      title: t("settings.mcpActionsCol"),
      render: (_: unknown, server: MCPServerConfig) => (
        <Space size={4}>
          <Button size="small" type="text" icon={<ReloadOutlined />} aria-label={t("settings.mcpTest")} loading={testMutation.isPending} onClick={() => testMutation.mutate(server.name)} />
          <Button size="small" type="text" icon={<EditOutlined />} aria-label={t("settings.mcpEdit")} onClick={() => openEdit(server)} />
          <Popconfirm title={t("settings.mcpDeleteConfirm", { name: server.name })} onConfirm={() => deleteMutation.mutate(server.name)}>
            <Button size="small" type="text" danger icon={<DeleteOutlined />} aria-label={t("settings.mcpDelete")} />
          </Popconfirm>
        </Space>
      ),
    },
  ];

  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      <Card
        title={t("settings.mcpTitle")}
        extra={
          <Space>
            <Button icon={<ReloadOutlined />} onClick={() => void invalidate()} loading={serversQuery.isFetching}>
              {t("settings.mcpRefresh")}
            </Button>
            <Button type="primary" icon={<PlusOutlined />} onClick={openCreate}>
              {t("settings.mcpAdd")}
            </Button>
          </Space>
        }
      >
        <Table
          rowKey="name"
          size="small"
          dataSource={servers}
          loading={serversQuery.isLoading}
          pagination={false}
          columns={columns}
          locale={{ emptyText: t("settings.mcpEmpty") }}
        />
      </Card>

      {editing ? (
        <Card title={editing.name ? t("settings.mcpEditTitle", { name: editing.name }) : t("settings.mcpAddTitle")}>
          <Form
            form={form}
            layout="vertical"
            onFinish={() => saveMutation.mutate(form.getFieldsValue())}
            initialValues={{ type: "stdio" }}
          >
            <Form.Item name="name" label={t("settings.mcpNameCol")} rules={[{ required: true, message: t("settings.mcpNameRequired") }]}>
              <Input disabled={Boolean(editing.name)} />
            </Form.Item>
            <Form.Item name="type" label={t("settings.mcpTypeCol")}>
              <Select options={TYPE_OPTIONS} />
            </Form.Item>
            <Form.Item noStyle shouldUpdate={(prev, curr) => prev.type !== curr.type}>
              {({ getFieldValue }) => {
                const type = getFieldValue("type") as MCPServerType;
                if (type === "stdio") {
                  return (
                    <>
                      <Form.Item name="command" label={t("settings.mcpCommand")} rules={[{ required: true, message: t("settings.mcpCommandRequired") }]}>
                        <Input placeholder="/path/to/server" />
                      </Form.Item>
                      <Form.Item name="args" label={t("settings.mcpArgs")} extra={t("settings.mcpLinesHint")}>
                        <Input.TextArea rows={3} />
                      </Form.Item>
                      <Form.Item name="env" label={t("settings.mcpEnv")} extra={t("settings.mcpKVHint")}>
                        <Input.TextArea rows={3} />
                      </Form.Item>
                    </>
                  );
                }
                if (type === "streamable-http") {
                  return (
                    <>
                      <Form.Item name="url" label={t("settings.mcpUrl")} rules={[{ required: true, message: t("settings.mcpUrlRequired") }]}>
                        <Input placeholder="https://mcp.example.com/mcp" />
                      </Form.Item>
                      <Form.Item name="headers" label={t("settings.mcpHeaders")} extra={t("settings.mcpKVHint")}>
                        <Input.TextArea rows={3} />
                      </Form.Item>
                      <Form.Item name="session_required" label={t("settings.mcpSessionRequired")} valuePropName="checked">
                        <Switch />
                      </Form.Item>
                    </>
                  );
                }
                return (
                  <Form.Item name="root" label={t("settings.mcpRoot")} rules={[{ required: true, message: t("settings.mcpRootRequired") }]}>
                    <Input placeholder="/path/to/docs" />
                  </Form.Item>
                );
              }}
            </Form.Item>
            <Space>
              <Button type="primary" htmlType="submit" icon={<SaveOutlined />} loading={saveMutation.isPending}>
                {t("settings.mcpSave")}
              </Button>
              <Button icon={<CloseOutlined />} onClick={() => { setEditing(null); form.resetFields(); }}>
                {t("settings.mcpCancel")}
              </Button>
            </Space>
          </Form>
        </Card>
      ) : null}
    </Space>
  );
}
