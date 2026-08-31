import { DeleteOutlined, ReloadOutlined } from "@ant-design/icons";
import { Button, Card, Empty, Popconfirm, Space, Tag, Typography } from "antd";
import { ResponsiveTable } from "../../components/ResponsiveTable";
import type {
  PackageCommandEntry,
  PackageEntry,
  PackageQuality,
  PackageQualityReport,
  PackageRoleEntry,
  PromptEntry,
} from "../../lib/types";

export function PackageQualityPanel({ report, loading }: { report?: PackageQualityReport; loading: boolean }) {
  const items = report?.packages ?? [];
  return (
    <Card
      title="Quality & Security"
      loading={loading}
      style={{ marginBottom: 16 }}
    >
      <div className="stat-grid" style={{ marginBottom: 16 }}>
        <Metric title="Package risk" value={report?.high_risk_packages ?? 0} />
        <Metric title="Skills" value={report?.skill_count ?? 0} />
        <Metric title="Prompts" value={report?.prompt_count ?? 0} />
        <Metric title="Commands" value={report?.command_count ?? 0} />
      </div>
      <ResponsiveTable<PackageQuality>
        rowKey="name"
        size="small"
        dataSource={items}
        pagination={items.length > 6 ? { pageSize: 6, size: "small" } : false}
        columns={[
          {
            title: "Package",
            render: (_value, item) => (
              <Space direction="vertical" size={4}>
                <Space wrap>
                  <Typography.Text strong>{item.name}</Typography.Text>
                  {item.version ? <Tag>{item.version}</Tag> : null}
                  <Tag color={riskColor(item.risk_level)}>{item.risk_level}</Tag>
                  <Tag>{item.score}</Tag>
                </Space>
                <Typography.Text type="secondary" copyable>{item.source}</Typography.Text>
              </Space>
            ),
          },
          {
            title: "Resources",
            render: (_value, item) => <ResourceCountTags counts={item.resource_counts} />,
          },
          {
            title: "App",
            render: (_value, item) => <PackageAppTag app={item.app} issues={item.app_issues} />,
          },
          {
            title: "Contract",
            render: (_value, item) => (
              <Space direction="vertical" size={4}>
                {item.capabilities?.length ? <Space wrap>{item.capabilities.map((capability) => <Tag key={capability}>{capability}</Tag>)}</Space> : null}
                {item.requires?.length ? <Space wrap>{item.requires.map((req) => <Tag color="orange" key={req}>requires {req}</Tag>)}</Space> : null}
                {item.provides?.length ? <Space wrap>{item.provides.map((provided) => <Tag color="cyan" key={provided}>provides {provided}</Tag>)}</Space> : null}
                {item.tool_policy?.length ? <Space wrap>{item.tool_policy.map((policy) => <Tag color="blue" key={policy}>{policy}</Tag>)}</Space> : null}
                <Space wrap>
                  {item.command_diagnostics?.length ? <Tag>commands: {item.command_diagnostics.length}</Tag> : null}
                  {item.role_diagnostics?.length ? <Tag>roles: {item.role_diagnostics.length}</Tag> : null}
                </Space>
              </Space>
            ),
          },
          {
            title: "Issues",
            render: (_value, item) => {
              const issues = [
                ...(item.manifest_issues ?? []),
                ...(item.resource_issues ?? []),
                ...(item.permission_issues ?? []),
                ...(item.capability_issues ?? []),
                ...(item.tool_policy_issues ?? []),
                ...(item.dependency_issues ?? []),
                ...(item.app_issues ?? []),
                ...(item.command_diagnostics ?? []).flatMap((diag) => diag.issues ?? []),
                ...(item.role_diagnostics ?? []).flatMap((diag) => diag.issues ?? []),
                ...(item.smoke_checks ?? []).flatMap((check) => check.issues ?? []),
                ...(item.unknown_bundles ?? []).map((bundle) => `unknown bundle: ${bundle}`),
              ];
              return issues.length ? (
                <Space direction="vertical" size={2}>
                  {issues.slice(0, 3).map((issue) => <Typography.Text key={issue} type="danger">{issue}</Typography.Text>)}
                  {issues.length > 3 ? <Typography.Text type="secondary">+{issues.length - 3} more</Typography.Text> : null}
                </Space>
              ) : <Tag color="green">healthy</Tag>;
            },
          },
          {
            title: "Permissions",
            render: (_value, item) => item.permissions?.length ? (
              <Space wrap>{item.permissions.map((permission) => <Tag color="gold" key={permission}>{permission}</Tag>)}</Space>
            ) : "-",
          },
          {
            title: "Health",
            render: (_value, item) => (
              <Space direction="vertical" size={4}>
                {item.install_health ? <Tag>{item.install_health}</Tag> : null}
                {item.reinstall_available_hint ? <Typography.Text type="secondary">{item.reinstall_available_hint}</Typography.Text> : null}
                {item.smoke_checks?.length ? (
                  <Space wrap>
                    {item.smoke_checks.map((check) => <Tag key={check.name} color={smokeColor(check.status)}>{check.name}: {check.status}</Tag>)}
                  </Space>
                ) : null}
              </Space>
            ),
          },
        ]}
      />
    </Card>
  );
}

function ResourceCountTags({ counts }: { counts: Record<string, number> }) {
  const entries = Object.entries(counts).filter(([, value]) => value > 0);
  return entries.length ? (
    <Space wrap>{entries.map(([key, value]) => <Tag key={key}>{key}: {value}</Tag>)}</Space>
  ) : <>-</>;
}

function riskColor(level: string) {
  if (level === "high") {
    return "red";
  }
  if (level === "medium") {
    return "gold";
  }
  return "green";
}

function smokeColor(status: string) {
  if (status === "passed" || status === "ready") {
    return "green";
  }
  if (status === "pending_approval" || status === "running") {
    return "gold";
  }
  return "red";
}

export function PackageTable({
  items,
  loading,
  removingPackage,
  reinstallingPackage,
  runningSmoke,
  onRemove,
  onReinstall,
  onRunSmoke,
}: {
  items: PackageEntry[];
  loading: boolean;
  removingPackage: string | null;
  reinstallingPackage: string | null;
  runningSmoke: string | null;
  onRemove: (item: PackageEntry) => void;
  onReinstall: (item: PackageEntry) => void;
  onRunSmoke: (item: PackageEntry, smokeName: string) => void;
}) {
  return (
    <Card title="Installed packages">
      <ResponsiveTable<PackageEntry>
        rowKey="name"
        loading={loading}
        dataSource={items}
        pagination={{ pageSize: 8 }}
        locale={{
          emptyText: (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No packages installed yet.">
              <Typography.Text type="secondary">Use the install form above to add your first skill package.</Typography.Text>
            </Empty>
          ),
        }}
        columns={[
          {
            title: "Package",
            render: (_value, item) => (
              <Space direction="vertical" size={4}>
                <Space wrap>
                  <Typography.Text strong>{item.name}</Typography.Text>
                  {item.version ? <Tag>{item.version}</Tag> : null}
                  <Tag color={item.trust === "local" ? "green" : "gold"}>{item.trust}</Tag>
                </Space>
                {item.description ? <Typography.Text type="secondary">{item.description}</Typography.Text> : null}
                <Typography.Text type="secondary" copyable>{item.source}</Typography.Text>
              </Space>
            ),
          },
          {
            title: "Resources",
            render: (_value, item) => <ResourceTags resources={item.resources ?? {}} />,
          },
          {
            title: "App",
            render: (_value, item) => <PackageAppTag app={item.app} />,
          },
          {
            title: "Permissions",
            render: (_value, item) => item.permissions?.length ? (
              <Space wrap>{item.permissions.map((permission) => <Tag color="gold" key={permission}>{permission}</Tag>)}</Space>
            ) : "-",
          },
          {
            title: "Quality",
            render: (_value, item) => (
              <Space direction="vertical" size={4}>
                {item.capabilities?.length ? <Space wrap>{item.capabilities.map((capability) => <Tag key={capability}>{capability}</Tag>)}</Space> : null}
                {item.requires?.length ? <Space wrap>{item.requires.map((req) => <Tag color="orange" key={req}>requires {req}</Tag>)}</Space> : null}
                {item.provides?.length ? <Space wrap>{item.provides.map((provided) => <Tag color="cyan" key={provided}>provides {provided}</Tag>)}</Space> : null}
                {item.tool_policy?.length ? <Space wrap>{item.tool_policy.map((policy) => <Tag color="blue" key={policy}>{policy}</Tag>)}</Space> : null}
                {item.smoke_tests?.length ? (
                  <Space wrap>
                    {item.smoke_tests.map((smoke) => {
                      const key = `${item.name}:${smoke.name}`;
                      return (
                        <Button
                          key={key}
                          size="small"
                          loading={runningSmoke === key}
                          onClick={() => onRunSmoke(item, smoke.name)}
                        >
                          Smoke {smoke.name}
                        </Button>
                      );
                    })}
                  </Space>
                ) : null}
              </Space>
            ),
          },
          {
            title: "Installed",
            dataIndex: "installed_at",
            render: (value) => formatTime(value),
          },
          {
            key: "actions",
            title: "Action",
            width: 220,
            render: (_value, item) => (
              <Space wrap>
                <Button
                  icon={<ReloadOutlined />}
                  loading={reinstallingPackage === item.name}
                  onClick={() => onReinstall(item)}
                >
                  Reinstall
                </Button>
                <Popconfirm title={`Remove ${item.name}?`} onConfirm={() => onRemove(item)}>
                  <Button
                    danger
                    icon={<DeleteOutlined />}
                    loading={removingPackage === item.name}
                  >
                    Remove
                  </Button>
                </Popconfirm>
              </Space>
            ),
          },
        ]}
      />
    </Card>
  );
}

export function PromptTable({ items, loading }: { items: PromptEntry[]; loading: boolean }) {
  return (
    <Card title="Prompt templates">
      <ResponsiveTable<PromptEntry>
        rowKey={(item) => `${item.package_name}:${item.name}:${item.path}`}
        loading={loading}
        dataSource={items}
        pagination={{ pageSize: 10 }}
        columns={[
          { title: "Prompt", dataIndex: "name", render: (value) => <Typography.Text strong>{value}</Typography.Text> },
          { title: "Package", dataIndex: "package_name", render: (value) => <Tag>{value}</Tag> },
          { title: "Path", dataIndex: "path", render: (value) => <Typography.Text type="secondary" copyable>{value}</Typography.Text> },
        ]}
      />
    </Card>
  );
}

export function PackageCommandTable({ items, loading }: { items: PackageCommandEntry[]; loading: boolean }) {
  return (
    <Card title="Package commands">
      <ResponsiveTable<PackageCommandEntry>
        rowKey={(item) => `${item.package_name}:${item.namespace}:${item.name}:${item.path}`}
        loading={loading}
        dataSource={items}
        pagination={{ pageSize: 10 }}
        columns={[
          {
            title: "Command",
            render: (_value, item) => (
              <Space direction="vertical" size={2}>
                <Space wrap>
                  <Typography.Text strong>/{item.namespace || item.package_name} {item.name}</Typography.Text>
                  {item.mode ? <Tag>{item.mode}</Tag> : null}
                </Space>
                {item.description ? <Typography.Text type="secondary">{item.description}</Typography.Text> : null}
              </Space>
            ),
          },
          { title: "Package", dataIndex: "package_name", render: (value) => <Tag>{value}</Tag> },
          {
            title: "Roles",
            render: (_value, item) => item.roles?.length ? <Space wrap>{item.roles.map((role) => <Tag key={role}>{role}</Tag>)}</Space> : "-",
          },
          {
            title: "Bundles",
            render: (_value, item) => item.recommended_bundles?.length ? (
              <Space wrap>{item.recommended_bundles.map((bundle) => <Tag key={bundle}>{bundle}</Tag>)}</Space>
            ) : "-",
          },
          {
            title: "Policy",
            render: (_value, item) => (
              <Space direction="vertical" size={4}>
                {item.capabilities?.length ? <Space wrap>{item.capabilities.map((capability) => <Tag key={capability}>{capability}</Tag>)}</Space> : null}
                {item.tool_policy?.length ? <Space wrap>{item.tool_policy.map((policy) => <Tag color="blue" key={policy}>{policy}</Tag>)}</Space> : null}
              </Space>
            ),
          },
          { title: "Path", dataIndex: "path", render: (value) => <Typography.Text type="secondary" copyable>{value}</Typography.Text> },
        ]}
      />
    </Card>
  );
}

export function PackageRoleTable({ items, loading }: { items: PackageRoleEntry[]; loading: boolean }) {
  return (
    <Card title="Named subagent roles">
      <ResponsiveTable<PackageRoleEntry>
        rowKey={(item) => `${item.package_name}:${item.id}:${item.path}`}
        loading={loading}
        dataSource={items}
        pagination={{ pageSize: 10 }}
        columns={[
          {
            title: "Role",
            render: (_value, item) => (
              <Space direction="vertical" size={2}>
                <Space wrap>
                  <Typography.Text strong>{item.name || item.id}</Typography.Text>
                  <Tag>{item.id}</Tag>
                  {item.write_enabled ? <Tag color="gold">write</Tag> : <Tag>read-only</Tag>}
                </Space>
                {item.description ? <Typography.Text type="secondary">{item.description}</Typography.Text> : null}
              </Space>
            ),
          },
          { title: "Package", dataIndex: "package_name", render: (value) => <Tag>{value}</Tag> },
          {
            title: "Tools",
            render: (_value, item) => item.tools?.length ? <Space wrap>{item.tools.map((tool) => <Tag key={tool}>{tool}</Tag>)}</Space> : "-",
          },
          {
            title: "Bundles",
            render: (_value, item) => item.default_bundles?.length ? (
              <Space wrap>{item.default_bundles.map((bundle) => <Tag key={bundle}>{bundle}</Tag>)}</Space>
            ) : "-",
          },
          {
            title: "Policy",
            render: (_value, item) => (
              <Space direction="vertical" size={4}>
                {item.capabilities?.length ? <Space wrap>{item.capabilities.map((capability) => <Tag key={capability}>{capability}</Tag>)}</Space> : null}
                {item.tool_policy?.length ? <Space wrap>{item.tool_policy.map((policy) => <Tag color="blue" key={policy}>{policy}</Tag>)}</Space> : null}
              </Space>
            ),
          },
          { title: "Path", dataIndex: "path", render: (value) => <Typography.Text type="secondary" copyable>{value}</Typography.Text> },
        ]}
      />
    </Card>
  );
}

function ResourceTags({ resources }: { resources: Record<string, string[]> }) {
  const entries = Object.entries(resources).filter(([, values]) => values.length > 0);
  if (!entries.length) {
    return <>-</>;
  }
  return (
    <Space wrap>
      {entries.map(([name, values]) => (
        <Tag key={name}>{name}: {values.length}</Tag>
      ))}
    </Space>
  );
}

function PackageAppTag({
  app,
  issues = [],
}: {
  app?: { kind?: string; id?: string; label?: string; config?: Record<string, unknown> };
  issues?: string[];
}) {
  if (!app?.id && !app?.kind && !app?.label) {
    return <>-</>;
  }
  const color = issues.length ? "orange" : "purple";
  return (
    <Space direction="vertical" size={4}>
      <Space wrap>
        <Tag color={color}>{app.kind || "builtin"}</Tag>
        {app.id ? <Tag color={color}>{app.id}</Tag> : null}
        {app.label ? <Tag>{app.label}</Tag> : null}
      </Space>
      {Object.keys(app.config ?? {}).length ? (
        <Typography.Text type="secondary">{Object.keys(app.config ?? {}).length} config keys</Typography.Text>
      ) : null}
    </Space>
  );
}

function Metric({ title, value }: { title: string; value: number }) {
  return (
    <Card size="small">
      <Typography.Text type="secondary">{title}</Typography.Text>
      <Typography.Title level={3} style={{ margin: 0 }}>{value}</Typography.Title>
    </Card>
  );
}

function formatTime(value?: string) {
  if (!value) {
    return "-";
  }
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}
