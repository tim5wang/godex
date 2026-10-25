import { useQuery } from "@tanstack/react-query";
import { Button, Card, Empty, Space, Table, Tag, Typography } from "antd";
import { ReloadOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { APIError } from "../../lib/apiClient";
import { getMyDelegations, type TenantDelegation } from "../../lib/apiDelegation";

/** OperatorGrantsCard lists the grants issued TO the current operator (design §3.6).
 *
 *  The operator half of "no silent access": it can see exactly which tenants it may
 *  read and until when, instead of having to guess whether a grant is still live.
 *
 *  Scoped to the caller by the server, so this never becomes a map of who else is
 *  inspecting whom — which is also why it is not a tenant-selectable list. */
export function OperatorGrantsCard({ token }: { token: string | null }) {
  const { t } = useI18n();
  const query = useQuery({
    queryKey: ["delegations-mine", token],
    queryFn: () => getMyDelegations(token),
    retry: false,
    refetchInterval: 60_000,
  });

  // A tenant user asking "what may I read" is a different question with a
  // different answer (nothing — it reads its own tenant directly). Saying so beats
  // an empty table that looks like a failed load.
  if (query.error instanceof APIError && query.error.status === 400) {
    return null;
  }

  return (
    <Card
      title={t("delegation.mineTitle")}
      loading={query.isLoading}
      extra={<Button size="small" icon={<ReloadOutlined />} onClick={() => void query.refetch()} />}
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {t("delegation.mineSubtitle")}
      </Typography.Paragraph>
      <Table<TenantDelegation>
        rowKey="id"
        size="small"
        dataSource={query.data?.delegations ?? []}
        pagination={false}
        scroll={{ x: 720 }}
        locale={{ emptyText: <Empty description={t("delegation.mineEmpty")} /> }}
        columns={[
          {
            title: t("delegation.mineTenantCol"),
            dataIndex: "tenant_id",
            render: (value: string) => <Typography.Text copyable={{ text: value }}>{value}</Typography.Text>,
          },
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
          {
            title: t("delegation.expiresCol"),
            dataIndex: "expires_at",
            render: (value?: string) => (value ? new Date(value).toLocaleString() : "-"),
          },
          {
            title: t("delegation.reasonCol"),
            dataIndex: "reason",
            render: (value?: string) => value || "-",
          },
          {
            title: t("delegation.auditDelegationCol"),
            dataIndex: "id",
            render: (value: string) => (
              <Space>
                <Typography.Text code>{value}</Typography.Text>
              </Space>
            ),
          },
        ]}
      />
    </Card>
  );
}
