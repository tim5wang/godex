import { useMemo, useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { useNavigate, useParams } from "react-router-dom";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Empty,
  InputNumber,
  Popconfirm,
  Select,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { PlusOutlined, ReloadOutlined } from "@ant-design/icons";
import dayjs from "dayjs";
import { useI18n } from "../../i18n";
import { useSettingsStore } from "../../store/settings";
import { useAuthProfile } from "../../lib/useAuthProfile";
import {
  createMemberInvite,
  listMemberInvites,
  listMembers,
  removeMember,
  revokeMemberInvite,
  setMemberRole,
  type MemberInvite,
  type MemberScope,
  type MemberView,
} from "../../lib/apiAccount";
import { roleLabel } from "../../lib/accountLabels";

/** MembersPage is tenant member management (design §3.7).
 *
 *  One component serves both surfaces: a tenant owner/admin manages their own
 *  tenant (`scope.kind === "tenant"`), and the platform superadmin manages any
 *  tenant by id (`scope.kind === "admin"`, reached at /members/:tenantID). The
 *  operations are identical and the ownership rules live server-side, so
 *  duplicating the UI would only create a second place for them to drift. */
export function MembersPage({ scope }: { scope?: MemberScope }) {
  const { t } = useI18n();
  // The admin surface is the same page at /members/:tenantID: deriving the scope
  // from the route keeps one component rather than a near-identical second one.
  const params = useParams<{ tenantID?: string }>();
  const navigate = useNavigate();
  const resolvedScope: MemberScope = scope ?? (params.tenantID ? { kind: "admin", tenantID: params.tenantID } : { kind: "tenant" });
  const { message } = AntApp.useApp();
  const queryClient = useQueryClient();
  const token = useSettingsStore((state) => state.token);
  const profileQuery = useAuthProfile(token);
  const [inviteRole, setInviteRole] = useState("member");
  const [inviteTTL, setInviteTTL] = useState(0);
  const [issuedCode, setIssuedCode] = useState("");

  // The actor's own platform role decides whether this page can work at all: the
  // tenant surface needs a tenant role, the admin surface a platform one. Said
  // plainly instead of firing calls that would 403.
  const principal = profileQuery.data?.principal;
  const isPlatformAdmin = principal?.platform_role === "admin";
  const isTenantAdmin = principal?.role === "owner" || principal?.role === "admin";
  const mayManage = resolvedScope.kind === "admin" ? isPlatformAdmin : isTenantAdmin;

  const scopeKey = resolvedScope.kind === "admin" ? resolvedScope.tenantID : "self";
  const membersQuery = useQuery({
    queryKey: ["members", scopeKey, token],
    queryFn: () => listMembers(token, resolvedScope),
    enabled: mayManage,
    retry: false,
  });
  const invitesQuery = useQuery({
    queryKey: ["member-invites", scopeKey, token],
    queryFn: () => listMemberInvites(token, resolvedScope),
    enabled: mayManage,
    retry: false,
  });

  const refresh = () =>
    Promise.all([
      queryClient.invalidateQueries({ queryKey: ["members", scopeKey, token] }),
      queryClient.invalidateQueries({ queryKey: ["member-invites", scopeKey, token] }),
    ]);

  const createInviteMutation = useMutation({
    mutationFn: () => createMemberInvite(token, resolvedScope, { role: inviteRole, ttlHours: inviteTTL }),
    onSuccess: async (result) => {
      // The plaintext code exists only in this response: the server keeps a hash,
      // so it is kept on screen until the operator dismisses it rather than shown
      // as a transient toast that can be missed.
      setIssuedCode(result.code);
      void message.success(t("members.inviteCreated"));
      await refresh();
    },
    onError: (err) => {
      void message.error(errorText(err, t("members.inviteCreateFailed")));
    },
  });

  const revokeInviteMutation = useMutation({
    mutationFn: (id: string) => revokeMemberInvite(token, resolvedScope, id),
    onSuccess: async () => {
      void message.success(t("members.inviteRevoked"));
      await refresh();
    },
    onError: (err) => {
      void message.error(errorText(err, t("members.inviteRevokeFailed")));
    },
  });

  const roleMutation = useMutation({
    mutationFn: (input: { userID: string; role: string }) =>
      setMemberRole(token, resolvedScope, input.userID, input.role),
    onSuccess: async () => {
      void message.success(t("members.roleChanged"));
      await refresh();
    },
    onError: (err) => {
      void message.error(errorText(err, t("members.roleChangeFailed")));
    },
  });

  const removeMutation = useMutation({
    mutationFn: (userID: string) => removeMember(token, resolvedScope, userID),
    onSuccess: async () => {
      void message.success(t("members.memberRemoved"));
      await refresh();
    },
    onError: (err) => {
      void message.error(errorText(err, t("members.removeFailed")));
    },
  });

  const members = membersQuery.data?.members ?? [];
  const invites = invitesQuery.data?.invites ?? [];

  const assignableRoles = useMemo(
    () => [
      { value: "admin", label: t("members.roleAdmin") },
      { value: "member", label: t("members.roleMember") },
    ],
    [t],
  );

  if (!mayManage) {
    // A platform superadmin has no TENANT role, so the tenant-scoped route is not
    // theirs to use — but telling them "only a tenant admin may manage members" is
    // wrong and a dead end. Send them where they can actually pick a tenant.
    return (
      <div className="godex-page">
        <Alert
          type="info"
          showIcon
          message={isPlatformAdmin ? t("members.platformAdminPickTenant") : t("members.tenantAdminOnly")}
          action={
            isPlatformAdmin ? (
              <Button size="small" onClick={() => navigate("/settings?tab=admin")}>
                {t("members.gotoSettings")}
              </Button>
            ) : undefined
          }
        />
      </div>
    );
  }

  return (
    <div className="godex-page">
      <Space direction="vertical" size={16} style={{ width: "100%" }}>
        <div>
          <Typography.Title level={4} style={{ marginBottom: 4 }}>
            {t("members.title")}
          </Typography.Title>
          <Typography.Text type="secondary">{t("members.subtitle")}</Typography.Text>
        </div>

        {resolvedScope.kind === "admin" ? (
          <Alert type="info" showIcon message={t("members.adminScopeNotice")} />
        ) : null}

        <Card
          title={t("members.listTitle")}
          loading={membersQuery.isLoading}
          extra={<Button size="small" icon={<ReloadOutlined />} onClick={() => void refresh()} />}
        >
          <Table<MemberView>
            rowKey="user_id"
            size="small"
            dataSource={members}
            pagination={false}
            locale={{ emptyText: <Empty description={t("members.membersEmpty")} /> }}
            columns={[
              {
                title: t("members.colEmail"),
                dataIndex: "email",
                render: (value: string) => value || "-",
              },
              {
                title: t("members.colUserID"),
                dataIndex: "user_id",
                render: (value: string) => (
                  <Typography.Text copyable code>
                    {value}
                  </Typography.Text>
                ),
              },
              {
                title: t("members.colRole"),
                dataIndex: "role",
                width: 190,
                render: (value: string, row) =>
                  // The owner's role is fixed: it cannot be removed or demoted
                  // (doing so would leave a tenant nobody can administer), so the
                  // control is a label rather than a dropdown that would fail.
                  value === "owner" ? (
                    <Tag color="gold">{t("members.roleOwner")}</Tag>
                  ) : (
                    <Select
                      size="small"
                      value={value}
                      style={{ width: 130 }}
                      options={assignableRoles}
                      onChange={(next) => roleMutation.mutate({ userID: row.user_id, role: next })}
                    />
                  ),
              },
              {
                title: t("members.colJoined"),
                dataIndex: "joined_at",
                width: 170,
                render: (value?: string) => (value ? dayjs(value).format("YYYY-MM-DD HH:mm") : "-"),
              },
              {
                title: t("members.colActions"),
                width: 110,
                render: (_value, row) =>
                  row.role === "owner" ? (
                    <Typography.Text type="secondary" title={t("members.ownerLocked")}>
                      -
                    </Typography.Text>
                  ) : (
                    <Popconfirm
                      title={t("members.removeConfirm")}
                      description={t("members.removeConfirmText")}
                      onConfirm={() => removeMutation.mutate(row.user_id)}
                    >
                      <Button size="small" danger loading={removeMutation.isPending}>
                        {t("members.removeMember")}
                      </Button>
                    </Popconfirm>
                  ),
              },
            ]}
          />
        </Card>

        <Card title={t("members.invitesTitle")} loading={invitesQuery.isLoading}>
          <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
            {t("members.invitesSubtitle")}
          </Typography.Paragraph>

          <Space wrap style={{ marginBottom: 16 }}>
            <Select
              size="small"
              value={inviteRole}
              style={{ width: 140 }}
              options={assignableRoles}
              onChange={setInviteRole}
            />
            <span>{t("members.ttlHours")}</span>
            <InputNumber size="small" min={0} value={inviteTTL} onChange={(value) => setInviteTTL(value ?? 0)} />
            <Button
              size="small"
              type="primary"
              icon={<PlusOutlined />}
              loading={createInviteMutation.isPending}
              onClick={() => createInviteMutation.mutate()}
            >
              {t("members.createInvite")}
            </Button>
          </Space>

          {issuedCode ? (
            <Alert
              type="success"
              showIcon
              closable
              onClose={() => setIssuedCode("")}
              style={{ marginBottom: 16 }}
              message={
                <Space wrap>
                  <Typography.Text code copyable={{ text: issuedCode }}>
                    {issuedCode}
                  </Typography.Text>
                  <Typography.Text type="secondary">{t("members.inviteCodeOnce")}</Typography.Text>
                </Space>
              }
            />
          ) : null}

          <Table<MemberInvite>
            rowKey="id"
            size="small"
            dataSource={invites}
            pagination={{ pageSize: 10, hideOnSinglePage: true }}
            locale={{ emptyText: <Empty description={t("members.invitesEmpty")} /> }}
            columns={[
              { title: t("members.colInviteRole"), dataIndex: "role", render: (v: string) => roleLabel(t, v) },
              {
                title: t("members.colInviteCreated"),
                dataIndex: "created_at",
                width: 170,
                render: (value?: string) => (value ? dayjs(value).format("YYYY-MM-DD HH:mm") : "-"),
              },
              {
                title: t("members.colInviteExpires"),
                dataIndex: "expires_at",
                width: 170,
                render: (value?: string) => (value ? dayjs(value).format("YYYY-MM-DD HH:mm") : "-"),
              },
              {
                title: t("members.colInviteState"),
                width: 110,
                render: (_value, row) => inviteState(t, row),
              },
              {
                title: t("members.colActions"),
                width: 110,
                render: (_value, row) => {
                  // Only an unused, unexpired, unrevoked code can be revoked: the
                  // others are already inert, so offering the action would imply it
                  // does something.
                  const usable = !row.used_at && !row.revoked_at && dayjs(row.expires_at).isAfter(dayjs());
                  return usable ? (
                    <Popconfirm
                      title={t("members.revokeInviteConfirm")}
                      onConfirm={() => revokeInviteMutation.mutate(row.id)}
                    >
                      <Button size="small" danger loading={revokeInviteMutation.isPending}>
                        {t("members.revokeInvite")}
                      </Button>
                    </Popconfirm>
                  ) : (
                    <Typography.Text type="secondary">-</Typography.Text>
                  );
                },
              },
            ]}
          />
        </Card>
      </Space>
    </div>
  );
}

/** inviteState derives the badge from the record's own timestamps.
 *
 *  Derived rather than trusting a server-side `usable` flag, which does not exist
 *  for invites: the timestamps are the facts, and showing "unused" for a code that
 *  expired yesterday would invite handing it to someone. */
function inviteState(t: (key: string) => string, invite: MemberInvite) {
  if (invite.revoked_at) {
    return <Tag>{t("members.inviteStateRevoked")}</Tag>;
  }
  if (invite.used_at) {
    return <Tag color="green">{t("members.inviteStateUsed")}</Tag>;
  }
  if (invite.expires_at && dayjs(invite.expires_at).isBefore(dayjs())) {
    return <Tag color="orange">{t("members.inviteStateExpired")}</Tag>;
  }
  return <Tag color="blue">{t("members.inviteStateUnused")}</Tag>;
}

function errorText(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}
