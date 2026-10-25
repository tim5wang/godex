// godex-feature: settings-admin
import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { App, Button, InputNumber, Space, Tag, Tooltip, Typography } from "antd";
import { useI18n } from "../../i18n";
import { setTenantMaxNodes } from "../../lib/apiAdmin";

/** TenantNodeLimitCell is one tenant's node allowance, editable in place
 *  (design §7, basic tier).
 *
 *  A cell rather than a modal because the operator's task is "scan every tenant
 *  and adjust a few": a modal per row turns a two-second pass into a sequence of
 *  dialogs, and the used/limit pair is only useful next to the other tenants.
 *
 *  The draft is local state so a half-typed number is never written to the
 *  server, and Save applies it. Cancel restores the server value, which matters
 *  when the operator clears the field to retype and then changes their mind:
 *  an empty box must not read as "0 = unlimited". */
export function TenantNodeLimitCell({
  token,
  tenantID,
  maxNodes,
  nodeCount,
}: {
  token: string | null;
  tenantID: string;
  maxNodes: number;
  nodeCount: number;
}) {
  const { t } = useI18n();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<number | null>(maxNodes);
  const [editing, setEditing] = useState(false);

  const mutation = useMutation({
    mutationFn: (next: number) => setTenantMaxNodes(token, tenantID, next),
    onSuccess: async () => {
      void message.success(t("settings.adminNodesLimitUpdated"));
      setEditing(false);
      await queryClient.invalidateQueries({ queryKey: ["admin-tenants", token] });
    },
    onError: (err) => {
      void message.error(
        err instanceof Error
          ? `${t("settings.adminNodesLimitUpdateFailed")}: ${err.message}`
          : t("settings.adminNodesLimitUpdateFailed"),
      );
    },
  });

  // Over the allowance is shown, not blocked: lowering a limit never evicts
  // existing nodes, so an operator can legitimately be looking at "6 of 5"
  // after tightening a policy. Rendering that as an error state would suggest
  // something must be fixed immediately, when the choice is theirs.
  const overLimit = maxNodes > 0 && nodeCount > maxNodes;

  if (!editing) {
    return (
      <Space size={8} wrap>
        <Tooltip title={t("settings.adminNodesLimitHint")}>
          <Tag color={overLimit ? "orange" : "default"}>
            {maxNodes > 0
              ? t("settings.adminNodesLimitUsed", { used: String(nodeCount), limit: String(maxNodes) })
              : `${t("settings.adminNodesLimitUnlimited")} (${nodeCount})`}
          </Tag>
        </Tooltip>
        <Button
          size="small"
          onClick={() => {
            setDraft(maxNodes);
            setEditing(true);
          }}
        >
          {t("settings.adminNodesLimitCol")}
        </Button>
      </Space>
    );
  }

  return (
    <Space size={4}>
      <Tooltip title={t("settings.adminNodesLimitHint")}>
        <InputNumber
          size="small"
          min={0}
          value={draft}
          onChange={(value) => setDraft(typeof value === "number" ? value : null)}
          style={{ width: 76 }}
        />
      </Tooltip>
      <Button
        size="small"
        type="primary"
        loading={mutation.isPending}
        disabled={draft === null}
        onClick={() => {
          if (draft === null || draft < 0) {
            void message.error(t("settings.adminNodesLimitInvalid"));
            return;
          }
          mutation.mutate(draft);
        }}
      >
        {t("settings.adminNodesLimitSave")}
      </Button>
      <Button size="small" onClick={() => setEditing(false)}>
        <Typography.Text type="secondary">✕</Typography.Text>
      </Button>
    </Space>
  );
}
