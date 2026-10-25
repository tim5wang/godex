import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  App,
  Button,
  Card,
  Empty,
  Form,
  Modal,
  Select,
  Space,
  Switch,
  Table,
  Tag,
  Tooltip,
  Typography,
} from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { APIError } from "../../lib/apiClient";
import {
  claimNode,
  getAdminPolicy,
  listAdminMembers,
  listAdminNodes,
  listAdminTenants,
  setNodeVisibility,
  setTenantSharing,
  type AdminMember,
  type AdminNode,
  type AdminTenant,
} from "../../lib/apiAdmin";

import { AdminJoinTokensCard } from "./AdminJoinTokensCard";

/** AdminSettingsPanel is the superadmin's tenant management surface (design §3.6).
 *
 *  Every endpoint behind it crosses tenant boundaries by design, so the server
 *  answers 403 for a non-operator. The panel still distinguishes 403 from 503:
 *  "you may not" and "this instance has no account store" are different problems
 *  and the operator needs to know which one they are looking at. */
export function AdminSettingsPanel({ token }: { token: string | null }) {
  const { t } = useI18n();
  const { message } = App.useApp();
  const queryClient = useQueryClient();

  const tenantsQuery = useQuery({
    queryKey: ["admin-tenants", token],
    queryFn: () => listAdminTenants(token),
  });
  const policyQuery = useQuery({
    queryKey: ["admin-policy", token],
    queryFn: () => getAdminPolicy(token),
  });
  const nodesQuery = useQuery({
    queryKey: ["admin-nodes", token],
    queryFn: () => listAdminNodes(token),
  });

  // One members query key serves both the members card and the claim dialog's
  // owner picker, so selecting a tenant twice does not fetch twice.
  const [selectedTenant, setSelectedTenant] = useState("");
  // The join-token card needs the tenant's NAME as well as its id (the id alone
  // makes an unreadable card title), so it keeps the whole row rather than just
  // the id like the members card does.
  const [joinTokenTenant, setJoinTokenTenant] = useState<AdminTenant | null>(null);
  const membersQuery = useQuery({
    queryKey: ["admin-members", selectedTenant, token],
    enabled: selectedTenant !== "",
    queryFn: () => listAdminMembers(token, selectedTenant),
  });

  const [claimTarget, setClaimTarget] = useState<AdminNode | null>(null);
  const [claimForm] = Form.useForm<{ tenant_id: string; owner_user_id: string; visibility: string }>();
  const claimTenant = Form.useWatch("tenant_id", claimForm);
  const claimMembersQuery = useQuery({
    queryKey: ["admin-members", claimTenant ?? "", token],
    enabled: claimTarget !== null && !!claimTenant,
    queryFn: () => listAdminMembers(token, claimTenant ?? ""),
  });

  const refreshAdmin = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ["admin-tenants", token] }),
      queryClient.invalidateQueries({ queryKey: ["admin-nodes", token] }),
      queryClient.invalidateQueries({ queryKey: ["admin-policy", token] }),
    ]);

  const sharingMutation = useMutation({
    mutationFn: ({ tenantID, enabled }: { tenantID: string; enabled: boolean }) =>
      setTenantSharing(token, tenantID, enabled),
    onSuccess: async () => {
      void message.success(t("settings.adminSharingUpdated"));
      await refreshAdmin();
    },
    onError: (err) => {
      void message.error(
        err instanceof Error ? `${t("settings.adminSharingUpdateFailed")}: ${err.message}` : t("settings.adminSharingUpdateFailed"),
      );
      // The switch is bound to server state and re-renders from the query, so a
      // failed write simply snaps back once the list refetches.
      void refreshAdmin();
    },
  });

  const visibilityMutation = useMutation({
    mutationFn: ({ nodeID, visibility }: { nodeID: string; visibility: "private" | "tenant" }) =>
      setNodeVisibility(token, nodeID, visibility),
    onSuccess: async () => {
      void message.success(t("nodes.visibilityUpdateSuccess"));
      await refreshAdmin();
      await queryClient.invalidateQueries({ queryKey: ["control-nodes", token] });
    },
    onError: (err) => {
      void message.error(err instanceof Error ? err.message : t("nodes.visibilityUpdateFailed"));
    },
  });

  const claimMutation = useMutation({
    mutationFn: (input: { nodeID: string; tenantID: string; ownerUserID: string; visibility: string }) =>
      claimNode(token, input.nodeID, {
        tenantID: input.tenantID,
        ownerUserID: input.ownerUserID,
        visibility: input.visibility,
      }),
    onSuccess: async () => {
      void message.success(t("settings.adminClaimSuccess"));
      setClaimTarget(null);
      claimForm.resetFields();
      await refreshAdmin();
      await queryClient.invalidateQueries({ queryKey: ["control-nodes", token] });
    },
    onError: (err) => {
      void message.error(
        err instanceof Error ? `${t("settings.adminClaimFailed")}: ${err.message}` : t("settings.adminClaimFailed"),
      );
    },
  });

  // A 403 here is not an error the operator can fix by retrying, so it renders as
  // a plain explanation rather than a red failure state.
  if (tenantsQuery.isError && tenantsQuery.error instanceof APIError && tenantsQuery.error.status === 403) {
    return <Alert type="warning" showIcon message={t("settings.adminForbidden")} />;
  }
  if (tenantsQuery.isError && tenantsQuery.error instanceof APIError && tenantsQuery.error.status === 503) {
    return <Alert type="info" showIcon message={t("settings.adminUnavailable")} />;
  }

  const unclaimed = (node: AdminNode) => !node.tenant_id || !node.owner_user_id;

  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      <Alert type="info" showIcon message={t("settings.adminTabSubtitle")} />

      <Card
        title={t("settings.adminPolicyTitle")}
        loading={policyQuery.isLoading}
        extra={<Button size="small" icon={<ReloadOutlined />} onClick={() => void refreshAdmin()} />}
      >
        <Space direction="vertical" size={4}>
          <Typography.Text type="secondary">{t("settings.adminDefaultVisibility")}</Typography.Text>
          <Tag color={policyQuery.data?.default_visibility === "tenant" ? "blue" : "default"}>
            {policyQuery.data?.default_visibility === "tenant" ? t("nodes.visibilityTenant") : t("nodes.visibilityPrivate")}
          </Tag>
        </Space>
      </Card>

      <Card title={t("settings.adminNodesTitle")} loading={nodesQuery.isLoading}>
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          {t("settings.adminNodesSubtitle")}
        </Typography.Paragraph>
        <Table<AdminNode>
          rowKey="id"
          size="small"
          dataSource={nodesQuery.data?.nodes ?? []}
          scroll={{ x: 860 }}
          columns={[
            {
              title: t("settings.adminNodeCol"),
              dataIndex: "id",
              render: (value: string, node) => (
                <Space direction="vertical" size={0}>
                  <Typography.Text strong>{node.name || value}</Typography.Text>
                  <Typography.Text type="secondary" copyable={{ text: value }}>
                    {value}
                  </Typography.Text>
                </Space>
              ),
            },
            {
              title: t("nodes.status"),
              dataIndex: "status",
              render: (value?: string) => <Tag color={value === "online" ? "green" : "default"}>{value || "offline"}</Tag>,
            },
            {
              title: t("settings.adminNodeTenantCol"),
              dataIndex: "tenant_id",
              render: (value: string | undefined, node) =>
                unclaimed(node) ? <Tag>{t("nodes.unclaimed")}</Tag> : <Typography.Text>{value}</Typography.Text>,
            },
            {
              title: t("settings.adminNodeOwnerCol"),
              dataIndex: "owner_user_id",
              render: (value?: string) => (value ? <Typography.Text>{value}</Typography.Text> : "-"),
            },
            {
              title: t("settings.adminNodeVisibilityCol"),
              dataIndex: "visibility",
              render: (value: string | undefined, node) => (
                <Tooltip title={unclaimed(node) ? t("settings.adminNodesSubtitle") : t("nodes.visibilityHint")}>
                  <Select<"private" | "tenant">
                    size="small"
                    value={value === "tenant" ? "tenant" : "private"}
                    // Visibility is enforced against ownership, so offer it only
                    // once the node has been claimed: an unclaimed node has no
                    // tenant to share within.
                    disabled={unclaimed(node) || visibilityMutation.isPending}
                    style={{ minWidth: 150 }}
                    onChange={(next) => visibilityMutation.mutate({ nodeID: node.id, visibility: next })}
                    options={[
                      { value: "private", label: t("nodes.visibilityPrivate") },
                      { value: "tenant", label: t("nodes.visibilityTenant") },
                    ]}
                  />
                </Tooltip>
              ),
            },
            {
              title: "",
              width: 100,
              render: (_value, node) => (
                <Button
                  size="small"
                  onClick={() => {
                    claimForm.setFieldsValue({
                      tenant_id: node.tenant_id ?? "",
                      owner_user_id: node.owner_user_id ?? "",
                      visibility: node.visibility === "tenant" ? "tenant" : "private",
                    });
                    setClaimTarget(node);
                  }}
                >
                  {t("settings.adminClaim")}
                </Button>
              ),
            },
          ]}
        />
      </Card>

      <Card title={t("settings.adminTabTitle")} loading={tenantsQuery.isLoading}>
        <Table<AdminTenant>
          rowKey="id"
          size="small"
          dataSource={tenantsQuery.data?.tenants ?? []}
          scroll={{ x: 860 }}
          columns={[
            {
              title: t("settings.adminTenantCol"),
              dataIndex: "name",
              render: (value: string, tenant) => (
                <Space direction="vertical" size={0}>
                  <Typography.Text strong>{value || tenant.id}</Typography.Text>
                  <Typography.Text type="secondary" copyable={{ text: tenant.id }}>
                    {tenant.id}
                  </Typography.Text>
                </Space>
              ),
            },
            { title: t("settings.adminPlanCol"), dataIndex: "plan", render: (value?: string) => value || "-" },
            { title: t("settings.adminMembersCol"), dataIndex: "member_count" },
            {
              title: t("settings.adminSharingCol"),
              dataIndex: "sharing_enabled",
              render: (enabled: boolean, tenant) => (
                <Space size={8}>
                  <Tooltip title={t("settings.adminSharingToggleHint")}>
                    <Switch
                      size="small"
                      checked={enabled}
                      loading={sharingMutation.isPending && sharingMutation.variables?.tenantID === tenant.id}
                      onChange={(next) => sharingMutation.mutate({ tenantID: tenant.id, enabled: next })}
                    />
                  </Tooltip>
                  <Tag color={enabled ? "green" : "default"}>
                    {enabled ? t("settings.adminSharingOn") : t("settings.adminSharingOff")}
                  </Tag>
                </Space>
              ),
            },
            {
              title: t("settings.adminCreatedCol"),
              dataIndex: "created_at",
              render: (value?: string) => (value ? new Date(value).toLocaleString() : "-"),
            },
            {
              title: "",
              width: 170,
              render: (_value, tenant) => (
                <Space size={4}>
                  <Button size="small" onClick={() => setSelectedTenant(tenant.id)}>
                    {t("settings.adminMembersCol")}
                  </Button>
                  <Button size="small" onClick={() => setJoinTokenTenant(tenant)}>
                    {t("settings.adminJoinTokensButton")}
                  </Button>
                </Space>
              ),
            },
          ]}
        />
      </Card>

      {joinTokenTenant ? (
        <AdminJoinTokensCard
          token={token}
          tenant={{ id: joinTokenTenant.id, name: joinTokenTenant.name }}
          onClose={() => setJoinTokenTenant(null)}
        />
      ) : null}

      {selectedTenant ? (
        <Card
          title={t("settings.adminMembersTitle", { tenant: selectedTenant })}
          loading={membersQuery.isLoading}
          extra={<Button size="small" onClick={() => setSelectedTenant("")}>{t("settings.adminMembersCol")} ✕</Button>}
        >
          <Table<AdminMember>
            rowKey="user_id"
            size="small"
            dataSource={membersQuery.data?.members ?? []}
            locale={{ emptyText: <Empty description={t("settings.adminMembersEmpty")} /> }}
            columns={[
              { title: t("settings.adminMemberEmailCol"), dataIndex: "email", render: (value?: string) => value || "-" },
              { title: t("settings.adminMemberRoleCol"), dataIndex: "role" },
              { title: t("settings.adminMemberJoinedCol"), dataIndex: "joined_at", render: (value?: string) => (value ? new Date(value).toLocaleString() : "-") },
            ]}
          />
        </Card>
      ) : null}

      <Modal
        open={claimTarget !== null}
        title={t("settings.adminClaimTitle", { node: claimTarget?.id ?? "" })}
        okText={t("settings.adminClaim")}
        confirmLoading={claimMutation.isPending}
        onCancel={() => {
          setClaimTarget(null);
          claimForm.resetFields();
        }}
        onOk={() => {
          void claimForm
            .validateFields()
            .then((values) => {
              if (!claimTarget) return;
              claimMutation.mutate({
                nodeID: claimTarget.id,
                tenantID: values.tenant_id,
                ownerUserID: values.owner_user_id,
                visibility: values.visibility,
              });
            })
            .catch(() => undefined);
        }}
      >
        <Form form={claimForm} layout="vertical" initialValues={{ visibility: "private" }}>
          <Form.Item
            name="tenant_id"
            label={t("settings.adminNodeTenantCol")}
            rules={[{ required: true, message: t("settings.adminClaimTenantRequired") }]}
          >
            <Select
              placeholder="-"
              onChange={() => claimForm.setFieldValue("owner_user_id", "")}
              options={(tenantsQuery.data?.tenants ?? []).map((tenant) => ({
                value: tenant.id,
                label: tenant.name ? `${tenant.name} (${tenant.id})` : tenant.id,
              }))}
            />
          </Form.Item>
          <Form.Item
            name="owner_user_id"
            label={t("settings.adminNodeOwnerCol")}
            rules={[{ required: true, message: t("settings.adminClaimOwnerRequired") }]}
            extra={t("settings.adminClaimOwnerRequired")}
          >
            {/* Owner is picked from the tenant's members rather than typed: an
                owner id that matches no user would create a node nobody can
                claim back. */}
            <Select
              placeholder="-"
              loading={claimMembersQuery.isLoading}
              disabled={!claimTenant}
              options={(claimMembersQuery.data?.members ?? []).map((member) => ({
                value: member.user_id,
                label: member.email ? `${member.email} (${member.user_id})` : member.user_id,
              }))}
            />
          </Form.Item>
          <Form.Item name="visibility" label={t("nodes.visibility")}>
            <Select
              options={[
                { value: "private", label: t("nodes.visibilityPrivate") },
                { value: "tenant", label: t("nodes.visibilityTenant") },
              ]}
            />
          </Form.Item>
        </Form>
      </Modal>
    </Space>
  );
}
