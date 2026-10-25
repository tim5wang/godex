import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, App, Button, Card, Empty, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Table, Tag, Typography } from "antd";
import { CopyOutlined, PlusOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { writeClipboardText } from "../../lib/clipboard";
import {
  createJoinToken,
  listAdminMembers,
  listJoinTokens,
  revokeJoinToken,
  type JoinToken,
} from "../../lib/apiAdmin";

/** AdminJoinTokensCard issues and revokes one-time tenant enrolment codes
 *  (design §3.5).
 *
 *  This is the UI half of replacing "give the tenant the center's web token":
 *  the operator issues a code scoped to ONE tenant, and the code is worth exactly
 *  one node enrolment. The plaintext is shown once in a dedicated dialog, because
 *  the server stores only a hash and a closed dialog means the code must be
 *  reissued rather than recovered. */
export function AdminJoinTokensCard({
  token,
  tenant,
  onClose,
}: {
  token: string | null;
  tenant: { id: string; name?: string } | null;
  onClose: () => void;
}) {
  const { t } = useI18n();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const tenantID = tenant?.id ?? "";

  const tokensQuery = useQuery({
    queryKey: ["admin-join-tokens", tenantID, token],
    enabled: tenantID !== "",
    queryFn: () => listJoinTokens(token, tenantID),
  });
  const membersQuery = useQuery({
    queryKey: ["admin-members", tenantID, token],
    enabled: tenantID !== "",
    queryFn: () => listAdminMembers(token, tenantID),
  });

  const [form] = Form.useForm<{ owner_user_id?: string; label?: string; ttl_hours?: number }>();
  const [plaintext, setPlaintext] = useState<string | null>(null);

  const refresh = () => queryClient.invalidateQueries({ queryKey: ["admin-join-tokens", tenantID, token] });

  const createMutation = useMutation({
    mutationFn: (values: { owner_user_id?: string; label?: string; ttl_hours?: number }) =>
      createJoinToken(token, tenantID, {
        ownerUserID: values.owner_user_id,
        label: values.label,
        ttlHours: values.ttl_hours,
      }),
    onSuccess: async (result) => {
      setPlaintext(result.token);
      form.resetFields();
      await refresh();
    },
    onError: (err) => {
      // The server refuses an owner outside the tenant; surfacing its message is
      // what tells the operator to pick a member rather than a stale id.
      void message.error(err instanceof Error ? err.message : t("settings.adminJoinTokenCreateFailed"));
    },
  });

  const revokeMutation = useMutation({
    mutationFn: (id: string) => revokeJoinToken(token, id),
    onSuccess: async () => {
      void message.success(t("settings.adminJoinTokenRevoked"));
      await refresh();
    },
    onError: (err) => {
      void message.error(err instanceof Error ? err.message : t("settings.adminJoinTokenRevokeFailed"));
    },
  });

  const stateTag = (record: JoinToken) => {
    if (record.revoked_at) return <Tag>{t("settings.adminJoinTokenStateRevoked")}</Tag>;
    if (record.used_at) return <Tag color="blue">{t("settings.adminJoinTokenStateUsed")}</Tag>;
    const expires = record.expires_at ? new Date(record.expires_at) : null;
    if (expires && expires.getTime() <= Date.now()) return <Tag color="orange">{t("settings.adminJoinTokenStateExpired")}</Tag>;
    return <Tag color="green">{t("settings.adminJoinTokenStateActive")}</Tag>;
  };

  return (
    <>
      <Card
        title={t("settings.adminJoinTokensTitle", { tenant: tenant?.name || tenantID })}
        loading={tokensQuery.isLoading}
        extra={<Button size="small" onClick={onClose}>{t("settings.adminJoinTokensClose")}</Button>}
      >
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          {t("settings.adminJoinTokensSubtitle")}
        </Typography.Paragraph>

        <Form
          form={form}
          layout="inline"
          style={{ rowGap: 8, marginBottom: 16 }}
          onFinish={(values) => createMutation.mutate(values)}
        >
          <Form.Item name="owner_user_id" label={t("settings.adminNodeOwnerCol")}>
            {/* Picked from the tenant's members: the server rejects an owner that
                is not a member, and typing an id is how that mistake happens. */}
            <Select
              allowClear
              style={{ minWidth: 220 }}
              placeholder={t("settings.adminJoinTokenOwnerPlaceholder")}
              loading={membersQuery.isLoading}
              options={(membersQuery.data?.members ?? []).map((member) => ({
                value: member.user_id,
                label: member.email ? `${member.email}` : member.user_id,
              }))}
            />
          </Form.Item>
          <Form.Item name="label" label={t("settings.adminJoinTokenLabel")}>
            <Input placeholder={t("settings.adminJoinTokenLabelPlaceholder")} style={{ width: 180 }} />
          </Form.Item>
          <Form.Item name="ttl_hours" label={t("settings.adminJoinTokenTTL")}>
            <InputNumber min={0} max={720} placeholder="24" style={{ width: 110 }} />
          </Form.Item>
          <Form.Item>
            <Button type="primary" htmlType="submit" icon={<PlusOutlined />} loading={createMutation.isPending}>
              {t("settings.adminJoinTokenIssue")}
            </Button>
          </Form.Item>
        </Form>

        <Table<JoinToken>
          rowKey="id"
          size="small"
          dataSource={tokensQuery.data?.join_tokens ?? []}
          scroll={{ x: 820 }}
          locale={{ emptyText: <Empty description={t("settings.adminJoinTokensEmpty")} /> }}
          columns={[
            { title: t("settings.adminJoinTokenLabel"), dataIndex: "label", render: (value?: string) => value || "-" },
            { title: t("settings.adminJoinTokenState"), render: (_value, record) => stateTag(record) },
            {
              title: t("settings.adminNodeOwnerCol"),
              dataIndex: "owner_user_id",
              render: (value?: string) => (value ? <Typography.Text>{value}</Typography.Text> : t("settings.adminJoinTokenOwnerFallback")),
            },
            {
              title: t("settings.adminJoinTokenCreated"),
              dataIndex: "created_at",
              render: (value?: string) => (value ? new Date(value).toLocaleString() : "-"),
            },
            {
              title: t("settings.adminJoinTokenExpires"),
              dataIndex: "expires_at",
              render: (value?: string) => (value ? new Date(value).toLocaleString() : "-"),
            },
            { title: t("settings.adminJoinTokenUsedBy"), dataIndex: "used_by_node", render: (value?: string) => value || "-" },
            {
              title: "",
              width: 90,
              render: (_value, record) =>
                record.used_at || record.revoked_at ? null : (
                  <Popconfirm
                    title={t("settings.adminJoinTokenRevokeConfirm")}
                    onConfirm={() => revokeMutation.mutate(record.id)}
                  >
                    <Button size="small" danger>{t("settings.adminJoinTokenRevoke")}</Button>
                  </Popconfirm>
                ),
            },
          ]}
        />
      </Card>

      <Modal
        open={plaintext !== null}
        title={t("settings.adminJoinTokenIssuedTitle")}
        onCancel={() => setPlaintext(null)}
        onOk={() => setPlaintext(null)}
        okText={t("settings.adminJoinTokensClose")}
      >
        <Alert
          type="warning"
          showIcon
          style={{ marginBottom: 12 }}
          message={t("settings.adminJoinTokenShowOnce")}
        />
        <Space direction="vertical" size={8} style={{ width: "100%" }}>
          <Typography.Text code copyable={{ text: plaintext ?? "" }} style={{ wordBreak: "break-all" }}>
            {plaintext}
          </Typography.Text>
          <Button
            icon={<CopyOutlined />}
            onClick={() => {
              void writeClipboardText(plaintext ?? "");
              void message.success(t("settings.adminJoinTokenCopied"));
            }}
          >
            {t("settings.adminJoinTokenCopy")}
          </Button>
        </Space>
      </Modal>
    </>
  );
}
