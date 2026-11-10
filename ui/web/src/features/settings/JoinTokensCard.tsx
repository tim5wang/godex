import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, App, Button, Card, Empty, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Table, Tag, Typography } from "antd";
import { CopyOutlined, PlusOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { writeClipboardText } from "../../lib/clipboard";
import {
  createEnrolmentCode,
  listEnrolmentCodes,
  listMembers,
  revokeEnrolmentCode,
  type EnrolmentCode,
  type MemberScope,
} from "../../lib/apiAccount";

/** JoinTokensCard issues and revokes one-time device enrolment codes (§3.5, §3.5.1).
 *
 *  ONE card serves both scopes, deliberately. The operator and the tenant admin ask
 *  the exact same question — "give me a code that enrols one device into this
 *  tenant" — so the difference is only WHICH tenant the caller may name, and that is
 *  what `scope` carries (same shape as the members surface). Two near-identical
 *  cards would have drifted on the state tags and the revoke rules, which is where
 *  the subtle bugs live.
 *
 *  The tenant scope is what makes this round's feature reachable at all: a self-serve
 *  user owns their tenant, so they must be able to issue their own second-device code
 *  without going through the operator. Before it, the endpoint existed but the only
 *  UI was behind the platform-admin tab, so the user had no way to obtain a code.
 *
 *  The plaintext is shown once in a dedicated dialog: the server keeps only a hash,
 *  so a closed dialog means the code must be reissued rather than recovered. */
export function JoinTokensCard({
  token,
  scope,
  tenant,
  onClose,
}: {
  token: string | null;
  scope: MemberScope;
  /** Only used to label the card; the tenant is derived from the scope server-side. */
  tenant?: { id: string; name?: string } | null;
  onClose?: () => void;
}) {
  const { t } = useI18n();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const tenantID = scope.kind === "admin" ? scope.tenantID : "";
  const enabled = scope.kind === "tenant" || tenantID !== "";

  const tokensQuery = useQuery({
    queryKey: ["join-tokens", scope.kind, tenantID, token],
    enabled,
    queryFn: () => listEnrolmentCodes(token, scope),
  });
  const membersQuery = useQuery({
    queryKey: ["join-token-owners", scope.kind, tenantID, token],
    enabled,
    queryFn: () => listMembers(token, scope),
  });

  const [form] = Form.useForm<{ owner_user_id?: string; label?: string; ttl_hours?: number }>();
  const [plaintext, setPlaintext] = useState<string | null>(null);

  const refresh = () => queryClient.invalidateQueries({ queryKey: ["join-tokens", scope.kind, tenantID, token] });

  const createMutation = useMutation({
    mutationFn: (values: { owner_user_id?: string; label?: string; ttl_hours?: number }) =>
      createEnrolmentCode(token, scope, {
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
      // The server refuses a code that cannot be redeemed (over the node allowance)
      // and an owner outside the tenant; surfacing its message is what tells the user
      // to delete a device, raise the allowance, or pick a member.
      void message.error(err instanceof Error ? err.message : t("settings.adminJoinTokenCreateFailed"));
    },
  });

  const revokeMutation = useMutation({
    mutationFn: (id: string) => revokeEnrolmentCode(token, scope, id),
    onSuccess: async () => {
      void message.success(t("settings.adminJoinTokenRevoked"));
      await refresh();
    },
    onError: (err) => {
      void message.error(err instanceof Error ? err.message : t("settings.adminJoinTokenRevokeFailed"));
    },
  });

  const stateTag = (record: EnrolmentCode) => {
    if (record.revoked_at) return <Tag>{t("settings.adminJoinTokenStateRevoked")}</Tag>;
    if (record.used_at) return <Tag color="blue">{t("settings.adminJoinTokenStateUsed")}</Tag>;
    const expires = record.expires_at ? new Date(record.expires_at) : null;
    if (expires && expires.getTime() <= Date.now()) return <Tag color="orange">{t("settings.adminJoinTokenStateExpired")}</Tag>;
    return <Tag color="green">{t("settings.adminJoinTokenStateActive")}</Tag>;
  };

  // The allowance is shown BEFORE the user clicks: the server refuses an
  // over-allowance issue with 409, and a user who only learns that from an error has
  // already spent the click. Both numbers are advisory — enrolment re-checks
  // atomically — so this only ever produces a better message.
  const maxNodes = tokensQuery.data?.max_nodes ?? 0;
  const usedNodes = tokensQuery.data?.used_nodes ?? 0;
  const atLimit = maxNodes > 0 && usedNodes >= maxNodes;
  const title =
    scope.kind === "tenant"
      ? t("settings.joinTokensTitleTenant")
      : t("settings.adminJoinTokensTitle", { tenant: tenant?.name || tenantID });

  return (
    <>
      <Card
        title={title}
        loading={tokensQuery.isLoading}
        extra={
          onClose ? (
            <Button size="small" onClick={onClose}>
              {t("settings.adminJoinTokensClose")}
            </Button>
          ) : undefined
        }
      >
        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          {scope.kind === "tenant" ? t("settings.joinTokensSubtitleTenant") : t("settings.adminJoinTokensSubtitle")}
        </Typography.Paragraph>

        <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
          {maxNodes > 0
            ? t("settings.joinTokensAllowanceUsed", { used: String(usedNodes), limit: String(maxNodes) })
            : t("settings.joinTokensAllowanceUnlimited", { used: String(usedNodes) })}
        </Typography.Paragraph>

        {atLimit ? (
          <Alert
            type="warning"
            showIcon
            style={{ marginBottom: 16 }}
            message={t("settings.joinTokensAtLimit", { limit: String(maxNodes) })}
          />
        ) : null}

        <Form form={form} layout="inline" style={{ rowGap: 8, marginBottom: 16 }} onFinish={(values) => createMutation.mutate(values)}>
          <Form.Item name="owner_user_id" label={t("settings.adminNodeOwnerCol")}>
            {/* Picked from the tenant's members: the server rejects an owner that is
                not a member, and typing an id is how that mistake happens. Left empty
                it defaults to the CALLER, which is what a user adding their own
                second device wants. */}
            <Select
              allowClear
              style={{ minWidth: 220 }}
              placeholder={t("settings.joinTokensOwnerPlaceholder")}
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
            <Button
              type="primary"
              htmlType="submit"
              icon={<PlusOutlined />}
              loading={createMutation.isPending}
              disabled={atLimit}
            >
              {t("settings.adminJoinTokenIssue")}
            </Button>
          </Form.Item>
        </Form>

        <Table<EnrolmentCode>
          rowKey="id"
          size="small"
          dataSource={tokensQuery.data?.join_tokens ?? []}
          scroll={{ x: 820 }}
          locale={{ emptyText: <Empty description={t("settings.joinTokensEmpty")} /> }}
          columns={[
            { title: t("settings.adminJoinTokenLabel"), dataIndex: "label", render: (value?: string) => value || "-" },
            { title: t("settings.adminJoinTokenState"), render: (_value, record) => stateTag(record) },
            {
              title: t("settings.adminNodeOwnerCol"),
              dataIndex: "owner_user_id",
              render: (value?: string) =>
                value ? <Typography.Text>{value}</Typography.Text> : t("settings.joinTokensOwnerCaller"),
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
                  <Popconfirm title={t("settings.adminJoinTokenRevokeConfirm")} onConfirm={() => revokeMutation.mutate(record.id)}>
                    <Button size="small" danger>
                      {t("settings.adminJoinTokenRevoke")}
                    </Button>
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
        <Alert type="warning" showIcon message={t("settings.adminJoinTokenShowOnce")} style={{ marginBottom: 12 }} />
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
