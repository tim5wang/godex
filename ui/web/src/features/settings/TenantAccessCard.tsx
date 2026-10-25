import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  App,
  Button,
  Card,
  Empty,
  Form,
  Input,
  InputNumber,
  Popconfirm,
  Space,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import { PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { APIError } from "../../lib/apiClient";
import {
  createTenantDelegation,
  listConsentRecords,
  listDelegationAudit,
  listTenantDelegations,
  recordConsent,
  revokeTenantDelegation,
  type DelegationAuditEntry,
  type TenantDelegation,
} from "../../lib/apiDelegation";

/** TenantAccessCard is the tenant's own operating-access surface (design §3.6).
 *
 *  This is where the tenant authorizes being looked at, sees every use, and
 *  withdraws at any time. The grant lives on the tenant's side of the API on
 *  purpose: the whole model is "the tenant decides", so putting the create action
 *  behind an operator route would turn explicit consent into self-service access.
 *
 *  Shown to every signed-in tenant member; the server refuses a non-admin's write
 *  with 403 and the card explains it rather than hiding the surface. */
export function TenantAccessCard({ token }: { token: string | null }) {
  const { t } = useI18n();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const [form] = Form.useForm<{ operator_user_id: string; ttl_hours?: number; reason?: string }>();

  const delegationsQuery = useQuery({
    queryKey: ["tenant-delegations", token],
    queryFn: () => listTenantDelegations(token),
    retry: false,
  });
  const auditQuery = useQuery({
    queryKey: ["tenant-delegation-audit", token],
    queryFn: () => listDelegationAudit(token, 100),
    retry: false,
  });
  const consentQuery = useQuery({
    queryKey: ["tenant-consent", token],
    queryFn: () => listConsentRecords(token),
    retry: false,
  });

  // The operator holds no tenant, and this instance may have no account store at
  // all. Both are "this surface does not apply to you", which is worth saying
  // plainly instead of rendering an empty list that looks like "no grants".
  if (delegationsQuery.error instanceof APIError && delegationsQuery.error.status === 400) {
    return <Alert type="info" showIcon message={t("delegation.tenantNoTenant")} />;
  }
  if (delegationsQuery.error instanceof APIError && delegationsQuery.error.status === 503) {
    return <Alert type="warning" showIcon message={t("delegation.tenantUnavailable")} />;
  }

  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ["tenant-delegations", token] }),
      queryClient.invalidateQueries({ queryKey: ["tenant-delegation-audit", token] }),
      queryClient.invalidateQueries({ queryKey: ["delegations-mine", token] }),
    ]);

  const createMutation = useMutation({
    mutationFn: (values: { operator_user_id: string; ttl_hours?: number; reason?: string }) =>
      createTenantDelegation(token, {
        operatorUserID: values.operator_user_id,
        ttlHours: values.ttl_hours,
        reason: values.reason,
      }),
    onSuccess: async () => {
      void message.success(t("delegation.grantCreated"));
      form.resetFields();
      await refresh();
    },
    onError: (err) => {
      void message.error(err instanceof Error ? err.message : t("delegation.grantCreateFailed"));
    },
  });

  const revokeMutation = useMutation({
    mutationFn: (id: string) => revokeTenantDelegation(token, id),
    onSuccess: async () => {
      void message.success(t("delegation.grantRevoked"));
      await refresh();
    },
    onError: (err) => {
      void message.error(err instanceof Error ? err.message : t("delegation.grantRevokeFailed"));
    },
  });

  const consentMutation = useMutation({
    mutationFn: (version: string) => recordConsent(token, version),
    onSuccess: async () => {
      void message.success(t("delegation.consentRecorded"));
      await queryClient.invalidateQueries({ queryKey: ["tenant-consent", token] });
    },
    onError: (err) => {
      void message.error(err instanceof Error ? err.message : t("delegation.consentRecordFailed"));
    },
  });

  const rows = delegationsQuery.data?.delegations ?? [];
  const maxTTL = delegationsQuery.data?.max_ttl_hours ?? 24;

  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      <Alert type="info" showIcon message={t("delegation.tenantSubtitle")} />

      <Card
        title={t("delegation.grantTitle")}
        extra={<Button size="small" icon={<ReloadOutlined />} onClick={() => void refresh()} />}
      >
        <Form
          form={form}
          layout="inline"
          onFinish={(values) => createMutation.mutate(values)}
          style={{ marginBottom: 16, rowGap: 8 }}
        >
          <Form.Item
            name="operator_user_id"
            label={t("delegation.operatorLabel")}
            rules={[{ required: true, message: t("delegation.operatorRequired") }]}
          >
            <Input placeholder="local-operator" style={{ width: 220 }} />
          </Form.Item>
          <Form.Item name="ttl_hours" label={t("delegation.ttlLabel")} initialValue={2}>
            {/* The cap comes from the server's own reported maximum, so the form
                cannot offer a window the server would refuse. */}
            <InputNumber min={1} max={maxTTL} style={{ width: 120 }} />
          </Form.Item>
          <Form.Item name="reason" label={t("delegation.reasonLabel")}>
            <Input placeholder={t("delegation.reasonPlaceholder")} style={{ width: 220 }} />
          </Form.Item>
          <Form.Item>
            <Button type="primary" htmlType="submit" icon={<PlusOutlined />} loading={createMutation.isPending}>
              {t("delegation.grantCreate")}
            </Button>
          </Form.Item>
        </Form>
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          {t("delegation.grantHint", { hours: maxTTL })}
        </Typography.Paragraph>

        <Table<TenantDelegation>
          rowKey="id"
          size="small"
          dataSource={rows}
          loading={delegationsQuery.isLoading}
          pagination={false}
          scroll={{ x: 860 }}
          locale={{ emptyText: <Empty description={t("delegation.grantEmpty")} /> }}
          columns={[
            { title: t("delegation.operatorCol"), dataIndex: "operator_user_id" },
            {
              title: t("delegation.stateCol"),
              dataIndex: "active",
              width: 120,
              render: (active: boolean, row) => (
                <Tag color={row.revoked_at ? "default" : active ? "green" : "orange"}>
                  {row.revoked_at
                    ? t("delegation.stateRevoked")
                    : active
                      ? t("delegation.stateActive")
                      : t("delegation.stateExpired")}
                </Tag>
              ),
            },
            { title: t("delegation.scopeCol"), dataIndex: "scope", render: (v?: string) => v || "-" },
            {
              title: t("delegation.expiresCol"),
              dataIndex: "expires_at",
              render: (v?: string) => (v ? new Date(v).toLocaleString() : "-"),
            },
            {
              title: t("delegation.reasonCol"),
              dataIndex: "reason",
              render: (v?: string) => v || "-",
            },
            {
              title: t("delegation.actionsCol"),
              width: 110,
              render: (_value, row) =>
                // A revoked or expired grant keeps its row (the record matters) but
                // offers no action: there is nothing left to withdraw.
                row.active ? (
                  <Popconfirm
                    title={t("delegation.grantRevokeConfirm")}
                    onConfirm={() => revokeMutation.mutate(row.id)}
                  >
                    <Button size="small" danger loading={revokeMutation.isPending}>
                      {t("delegation.grantRevoke")}
                    </Button>
                  </Popconfirm>
                ) : (
                  <Typography.Text type="secondary">-</Typography.Text>
                ),
            },
          ]}
        />
      </Card>

      {/* The audit trail is the tenant's guarantee that operating access is not
          silent: the rows are written at the authorization seam, so they exist
          whether or not anyone tells the tenant about the access. */}
      <Card title={t("delegation.auditTitle")} loading={auditQuery.isLoading}>
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          {t("delegation.auditSubtitle")}
        </Typography.Paragraph>
        <Table<DelegationAuditEntry>
          rowKey={(_row, index) => String(index ?? 0)}
          size="small"
          dataSource={auditQuery.data?.entries ?? []}
          pagination={{ pageSize: 10, hideOnSinglePage: true }}
          scroll={{ x: 860 }}
          locale={{ emptyText: <Empty description={t("delegation.auditEmpty")} /> }}
          columns={[
            {
              title: t("delegation.auditAtCol"),
              dataIndex: "at",
              width: 190,
              render: (v?: string) => (v ? new Date(v).toLocaleString() : "-"),
            },
            { title: t("delegation.operatorCol"), dataIndex: "operator_user_id", render: (v?: string) => v || "-" },
            { title: t("delegation.auditActionCol"), dataIndex: "action", render: (v?: string) => v || "-" },
            { title: t("delegation.auditTargetCol"), dataIndex: "target", render: (v?: string) => v || "-" },
            {
              title: t("delegation.auditDelegationCol"),
              dataIndex: "delegation_id",
              render: (v?: string) => (v ? <Typography.Text code>{v}</Typography.Text> : "-"),
            },
            {
              title: t("delegation.auditResultCol"),
              dataIndex: "allowed",
              width: 100,
              render: (allowed: boolean) => (
                <Tag color={allowed ? "green" : "red"}>
                  {allowed ? t("delegation.auditAllowed") : t("delegation.auditDenied")}
                </Tag>
              ),
            },
          ]}
        />
      </Card>

      <Card title={t("delegation.consentTitle")} loading={consentQuery.isLoading}>
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          {t("delegation.consentSubtitle")}
        </Typography.Paragraph>
        <Space direction="vertical" size={12} style={{ width: "100%" }}>
          {/* An empty list is reported as empty rather than filled in with the
              current revision: inventing an acceptance is the one thing a consent
              trail must not do. */}
          {(consentQuery.data?.records?.length ?? 0) === 0 ? (
            <Alert type="warning" showIcon message={t("delegation.consentNone")} />
          ) : (
            <Table
              rowKey={(row) => `${row.terms_version}-${row.accepted_at}`}
              size="small"
              dataSource={consentQuery.data?.records ?? []}
              pagination={false}
              scroll={{ x: 640 }}
              columns={[
                { title: t("delegation.consentVersionCol"), dataIndex: "terms_version" },
                {
                  title: t("delegation.consentAcceptedCol"),
                  dataIndex: "accepted_at",
                  render: (v?: string) => (v ? new Date(v).toLocaleString() : "-"),
                },
                { title: t("delegation.consentSourceCol"), dataIndex: "source_ip", render: (v?: string) => v || "-" },
              ]}
            />
          )}
          <Tooltip title={t("delegation.consentAgreeHint")}>
            <Button
              onClick={() => consentMutation.mutate(consentQuery.data?.current_terms_version ?? "")}
              loading={consentMutation.isPending}
            >
              {t("delegation.consentAgree", { version: consentQuery.data?.current_terms_version ?? "" })}
            </Button>
          </Tooltip>
        </Space>
      </Card>
    </Space>
  );
}
