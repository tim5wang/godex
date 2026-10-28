import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  Alert,
  App as AntApp,
  Button,
  Card,
  Descriptions,
  Empty,
  Form,
  Input,
  Popconfirm,
  Space,
  Table,
  Tag,
  Typography,
} from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import dayjs from "dayjs";
import { useI18n } from "../../i18n";
import { changePassword, getAccountProfile, listAccountSessions, listLoginEvents, revokeAccountSession, revokeOtherSessions, type AccountSession, type LoginEvent } from "../../lib/apiAccount";
import { eventLabel, roleLabel } from "../../lib/accountLabels";
import { useSettingsStore } from "../../store/settings";

/** AccountPage is the user centre (design §3.7): the caller's own profile, their
 *  signed-in devices, and their sign-in history.
 *
 *  Every call is self-scoped by the server — the user id comes from the
 *  authenticated principal, never from the request — so this page has no id to
 *  pass and cannot address another account. That is also why it is safe to show
 *  it to any signed-in user.
 *
 *  The shared web token is NOT an account: it has no user row, so the
 *  device/history surfaces answer 400 and this page says why instead of rendering
 *  an empty list, which would read as "nothing is signed in". */
export function AccountPage() {
  const { t } = useI18n();
  const { message } = AntApp.useApp();
  const queryClient = useQueryClient();
  const token = useSettingsStore((state) => state.token);
  const [passwordForm] = Form.useForm<{ currentPassword: string; newPassword: string }>();
  const [showPassword, setShowPassword] = useState(false);

  const profileQuery = useQuery({
    queryKey: ["account-profile", token],
    queryFn: () => getAccountProfile(token),
    retry: false,
  });
  // Enabled only for a real account: for the shared token the server answers 400,
  // and firing it anyway would surface a red error for a legitimate situation.
  const isAccount = profileQuery.data ? !profileQuery.data.shared_token : false;
  const sessionsQuery = useQuery({
    queryKey: ["account-sessions", token],
    queryFn: () => listAccountSessions(token),
    enabled: isAccount,
    retry: false,
  });
  const eventsQuery = useQuery({
    queryKey: ["account-login-events", token],
    queryFn: () => listLoginEvents(token),
    enabled: isAccount,
    retry: false,
  });

  const revokeMutation = useMutation({
    mutationFn: (id: number) => revokeAccountSession(token, id),
    onSuccess: async () => {
      void message.success(t("account.sessionRevoked"));
      await queryClient.invalidateQueries({ queryKey: ["account-sessions", token] });
    },
    onError: (err) => {
      void message.error(errorText(err, t("account.sessionRevokeFailed")));
    },
  });

  const revokeOthersMutation = useMutation({
    mutationFn: () => revokeOtherSessions(token),
    onSuccess: async (result) => {
      void message.success(t("account.revokeOthersDone", { count: result.revoked }));
      await queryClient.invalidateQueries({ queryKey: ["account-sessions", token] });
      await queryClient.invalidateQueries({ queryKey: ["account-login-events", token] });
    },
    onError: (err) => {
      void message.error(errorText(err, t("account.revokeOthersFailed")));
    },
  });

  const passwordMutation = useMutation({
    mutationFn: (values: { currentPassword: string; newPassword: string }) =>
      changePassword(token, { currentPassword: values.currentPassword, newPassword: values.newPassword }),
    onSuccess: async () => {
      void message.success(t("account.passwordChanged"));
      passwordForm.resetFields();
      setShowPassword(false);
      // Other sessions were revoked server-side, so the device list is stale.
      await queryClient.invalidateQueries({ queryKey: ["account-sessions", token] });
      await queryClient.invalidateQueries({ queryKey: ["account-login-events", token] });
    },
    onError: (err) => {
      void message.error(errorText(err, t("account.passwordChangeFailed")));
    },
  });

  const profile = profileQuery.data;
  const sessions = sessionsQuery.data?.sessions ?? [];

  return (
    <div className="godex-page">
      <Space direction="vertical" size={16} style={{ width: "100%" }}>
        <div>
          <Typography.Title level={4} style={{ marginBottom: 4 }}>
            {t("account.title")}
          </Typography.Title>
          <Typography.Text type="secondary">{t("account.subtitle")}</Typography.Text>
        </div>

        {profileQuery.isError ? (
          <Alert type="error" showIcon message={errorText(profileQuery.error, t("account.loadFailed"))} />
        ) : null}

        {profile?.shared_token ? (
          <Alert type="info" showIcon message={t("account.sharedTokenNotice")} />
        ) : null}

        <Card title={t("account.profileTitle")} loading={profileQuery.isLoading}>
          <Descriptions column={2} size="small" bordered>
            <Descriptions.Item label={t("account.email")}>{profile?.email || "-"}</Descriptions.Item>
            <Descriptions.Item label={t("account.userID")}>
              <Typography.Text copyable code>
                {profile?.user_id || "-"}
              </Typography.Text>
            </Descriptions.Item>
            <Descriptions.Item label={t("account.tenant")}>
              {profile?.tenant_name || profile?.tenant_id || "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("account.role")}>{roleLabel(t, profile?.role)}</Descriptions.Item>
            <Descriptions.Item label={t("account.platformRole")}>
              {profile?.platform_role || "-"}
            </Descriptions.Item>
            <Descriptions.Item label={t("account.createdAt")}>
              {profile?.created_at ? dayjs(profile.created_at).format("YYYY-MM-DD HH:mm") : "-"}
            </Descriptions.Item>
          </Descriptions>
        </Card>

        {/* Password change is offered only to a real account: the shared token has
            no password to change. */}
        {isAccount ? (
          <Card
            title={t("account.passwordTitle")}
            extra={
              <Button size="small" onClick={() => setShowPassword((value) => !value)}>
                {t("account.passwordTitle")}
              </Button>
            }
          >
            {showPassword ? (
              <Form
                form={passwordForm}
                layout="vertical"
                requiredMark={false}
                onFinish={(values) => passwordMutation.mutate(values)}
                style={{ maxWidth: 420 }}
              >
                <Form.Item
                  name="currentPassword"
                  label={t("account.currentPassword")}
                  rules={[{ required: true, message: t("account.currentPasswordRequired") }]}
                >
                  <Input.Password autoComplete="current-password" />
                </Form.Item>
                <Form.Item
                  name="newPassword"
                  label={t("account.newPassword")}
                  extra={t("account.passwordHint")}
                  rules={[{ required: true, message: t("account.newPasswordRequired") }]}
                >
                  <Input.Password autoComplete="new-password" />
                </Form.Item>
                <Button type="primary" htmlType="submit" loading={passwordMutation.isPending}>
                  {t("account.passwordTitle")}
                </Button>
              </Form>
            ) : (
              <Typography.Text type="secondary">{t("account.passwordHint")}</Typography.Text>
            )}
          </Card>
        ) : null}

        {isAccount ? (
          <Card
            title={t("account.sessionsTitle")}
            loading={sessionsQuery.isLoading}
            extra={
              <Space>
                <Button size="small" icon={<ReloadOutlined />} onClick={() => void sessionsQuery.refetch()} />
                <Popconfirm
                  title={t("account.revokeOthersConfirm")}
                  onConfirm={() => revokeOthersMutation.mutate()}
                  disabled={sessions.length <= 1}
                >
                  <Button size="small" danger disabled={sessions.length <= 1} loading={revokeOthersMutation.isPending}>
                    {t("account.revokeOthers")}
                  </Button>
                </Popconfirm>
              </Space>
            }
          >
            <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
              {t("account.sessionsSubtitle")}
            </Typography.Paragraph>
            <Table<AccountSession>
              rowKey="id"
              size="small"
              dataSource={sessions}
              pagination={false}
              locale={{ emptyText: <Empty description={t("account.sessionsEmpty")} /> }}
              columns={[
                {
                  title: t("account.sessionCreated"),
                  dataIndex: "created_at",
                  render: (value: string, row) => (
                    <Space size={6}>
                      <span>{value ? dayjs(value).format("YYYY-MM-DD HH:mm") : "-"}</span>
                      {row.current ? <Tag color="green">{t("account.sessionCurrent")}</Tag> : null}
                    </Space>
                  ),
                },
                {
                  title: t("account.sessionExpires"),
                  dataIndex: "expires_at",
                  render: (value: string) => (value ? dayjs(value).format("YYYY-MM-DD HH:mm") : "-"),
                },
                { title: t("account.sessionTenant"), dataIndex: "tenant_id", render: (v?: string) => v || "-" },
                {
                  title: t("account.role"),
                  dataIndex: "role",
                  render: (value?: string) => roleLabel(t, value),
                },
                {
                  title: "",
                  width: 110,
                  render: (_value, row) =>
                    // No revoke button on the current session: doing so would sign
                    // the user out with no way to undo it from this screen.
                    row.current ? (
                      <Typography.Text type="secondary">-</Typography.Text>
                    ) : (
                      <Popconfirm
                        title={t("account.sessionRevokeConfirm")}
                        onConfirm={() => revokeMutation.mutate(row.id)}
                      >
                        <Button size="small" danger loading={revokeMutation.isPending}>
                          {t("account.sessionRevoke")}
                        </Button>
                      </Popconfirm>
                    ),
                },
              ]}
            />
          </Card>
        ) : null}

        {isAccount ? (
          <Card title={t("account.eventsTitle")} loading={eventsQuery.isLoading}>
            <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
              {t("account.eventsSubtitle")}
            </Typography.Paragraph>
            <Table<LoginEvent>
              rowKey="id"
              size="small"
              dataSource={eventsQuery.data?.events ?? []}
              pagination={{ pageSize: 10, hideOnSinglePage: true }}
              locale={{ emptyText: <Empty description={t("account.eventsEmpty")} /> }}
              columns={[
                {
                  title: t("account.eventAt"),
                  dataIndex: "at",
                  width: 190,
                  render: (value: string) => (value ? dayjs(value).format("YYYY-MM-DD HH:mm:ss") : "-"),
                },
                {
                  title: t("account.eventKind"),
                  dataIndex: "event",
                  render: (value: string) => <Tag>{eventLabel(t, value)}</Tag>,
                },
                { title: t("account.sessionTenant"), dataIndex: "tenant_id", render: (v?: string) => v || "-" },
                { title: t("account.eventIP"), dataIndex: "source_ip", render: (v?: string) => v || "-" },
              ]}
            />
          </Card>
        ) : null}
      </Space>
    </div>
  );
}

function errorText(error: unknown, fallback: string): string {
  return error instanceof Error && error.message ? error.message : fallback;
}
