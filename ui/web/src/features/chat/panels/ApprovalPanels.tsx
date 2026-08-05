import type { PendingPermission } from "../../../lib/types";
import { useMutation } from "@tanstack/react-query";
import { useI18n } from "../../../i18n";
import { useState, useMemo, useEffect } from "react";
import { Alert, Space, Typography, Tag, Button, Empty, List, Card } from "antd";
import { permissionRequestTitle, permissionRequestSummary, formatTimelineTime } from "../../../lib/timelineUtils";

export function ApprovalBanner({
  items,
  approving,
  denying,
}: {
  items: PendingPermission[];
  approving: ReturnType<typeof useMutation<unknown, Error, { requestId: string; scope: "once" | "session" }>>;
  denying: ReturnType<typeof useMutation<unknown, Error, { requestId: string }>>;
}) {
  const { t } = useI18n();
  const item = items[0];
  // Allow the user to fold the banner away (e.g. after handling it, or to
  // review approvals later in the Tasks dock). It reappears automatically
  // when a NEW pending request (different id set) arrives.
  const [dismissedIds, setDismissedIds] = useState<string>("");
  const currentIds = useMemo(() => items.map((p) => p.id).join(","), [items]);
  const dismissed = dismissedIds === currentIds && currentIds !== "";
  useEffect(() => {
    // Reset dismissal whenever the pending set changes (new request arrived,
    // or queue advanced to the next item after an approve/deny).
    if (dismissedIds && dismissedIds !== currentIds) {
      setDismissedIds("");
    }
  }, [currentIds, dismissedIds]);
  if (!item || dismissed) {
    return null;
  }
  const busy = approving.isPending || denying.isPending;
  const title = items.length > 1 ? `${t("chat.pendingApprovalsTitle")} (${items.length})` : t("chat.pendingApprovalsTitle");
  return (
    <div style={{ borderTop: "1px solid var(--godex-border)", padding: "8px 16px" }}>
      <Alert
        type="warning"
        showIcon
        closable
        onClose={() => setDismissedIds(currentIds)}
        message={
          <Space size={8} wrap>
            <Typography.Text strong>{title}</Typography.Text>
            <Tag color="gold">{permissionRequestTitle(item)}</Tag>
          </Space>
        }
        description={
          <Space direction="vertical" size={6} style={{ width: "100%" }}>
            <Typography.Text>{permissionRequestSummary(item)}</Typography.Text>
            {item.reason ? <Typography.Text type="secondary">{item.reason}</Typography.Text> : null}
            <Space wrap>
              <Button size="small" disabled={busy} onClick={() => approving.mutate({ requestId: item.id, scope: "once" })}>
                {t("chat.allowOnce")}
              </Button>
              <Button size="small" type="primary" disabled={busy} onClick={() => approving.mutate({ requestId: item.id, scope: "session" })}>
                {t("chat.allowSession")}
              </Button>
              <Button size="small" danger disabled={busy} onClick={() => denying.mutate({ requestId: item.id })}>
                {t("chat.deny")}
              </Button>
            </Space>
          </Space>
        }
      />
    </div>
  );
}

export function ApprovalList({
  items,
  approving,
  denying,
}: {
  items: PendingPermission[];
  approving: ReturnType<typeof useMutation<unknown, Error, { requestId: string; scope: "once" | "session" }>>;
  denying: ReturnType<typeof useMutation<unknown, Error, { requestId: string }>>;
}) {
  const { t } = useI18n();
  if (items.length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("chat.noPendingApprovals")} />;
  }
  return (
    <List
      dataSource={items}
      renderItem={(item) => {
        const busy = approving.isPending || denying.isPending;
        return (
          <List.Item>
            <Card size="small" style={{ width: "100%" }}>
              <Space direction="vertical" size={8} style={{ width: "100%" }}>
                <Space style={{ justifyContent: "space-between", width: "100%" }}>
                  <Typography.Text strong>{permissionRequestTitle(item)}</Typography.Text>
                  <Typography.Text type="secondary">{formatTimelineTime(item.created_at)}</Typography.Text>
                </Space>
                <Typography.Text type="secondary">{permissionRequestSummary(item)}</Typography.Text>
                {item.reason ? <Alert type="info" message={item.reason} /> : null}
                <Space wrap>
                  <Button size="small" disabled={busy} onClick={() => approving.mutate({ requestId: item.id, scope: "once" })}>
                    {t("chat.allowOnce")}
                  </Button>
                  <Button size="small" type="primary" disabled={busy} onClick={() => approving.mutate({ requestId: item.id, scope: "session" })}>
                    {t("chat.allowSession")}
                  </Button>
                  <Button size="small" danger disabled={busy} onClick={() => denying.mutate({ requestId: item.id })}>
                    {t("chat.deny")}
                  </Button>
                </Space>
              </Space>
            </Card>
          </List.Item>
        );
      }}
    />
  );
}
