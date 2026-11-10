// godex-feature: settings-admin
import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { App, Button, InputNumber, Space, Tag, Tooltip, Typography } from "antd";
import { useI18n } from "../../i18n";
import { setTenantMaxDevicesPerUser, setTenantMaxUsers } from "../../lib/apiAdmin";

/** TenantLimitCell is one of a tenant's allowances, editable in place.
 *
 *  A cell rather than a modal because the operator's task is "scan every tenant and
 *  adjust a few": a modal per row turns a two-second pass into a sequence of dialogs,
 *  and the used/limit pair is only useful next to the other tenants.
 *
 *  One component serves both numbers because the interaction is identical — only the
 *  endpoint, the label and the tooltip differ. Duplicating it would let the two drift
 *  on the draft/cancel rules, which is where the subtle bugs live.
 *
 *  The draft is local state so a half-typed number is never written to the server, and
 *  Save applies it. Cancel restores the server value, which matters when the operator
 *  clears the field to retype and then changes their mind: an empty box must not read
 *  as "0 = unlimited".
 *
 *  `used` is only meaningful where the server can count it against this same number.
 *  The per-user device limit is counted per account while the console lists devices
 *  per TENANT, so no used-count is passed for it: showing "3 of 2 devices" from a
 *  tenant-wide count would be a number nobody can act on. */
export function TenantLimitCell({
  token,
  tenantID,
  kind,
  limit,
  used,
}: {
  token: string | null;
  tenantID: string;
  kind: "devices_per_user" | "users";
  limit: number;
  used?: number;
}) {
  const { t } = useI18n();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const [draft, setDraft] = useState<number | null>(limit);
  const [editing, setEditing] = useState(false);

  const labels = {
    devices_per_user: {
      col: t("settings.adminDevicesPerUserCol"),
      hint: t("settings.adminDevicesPerUserHint"),
      updated: t("settings.adminDevicesPerUserUpdated"),
      failed: t("settings.adminDevicesPerUserUpdateFailed"),
      save: t("settings.adminDevicesPerUserSave"),
      invalid: t("settings.adminDevicesPerUserInvalid"),
      unlimited: t("settings.adminDevicesPerUserUnlimited"),
      usedFmt: t("settings.adminDevicesPerUserUsed"),
    },
    users: {
      col: t("settings.adminUsersLimitCol"),
      hint: t("settings.adminUsersLimitHint"),
      updated: t("settings.adminUsersLimitUpdated"),
      failed: t("settings.adminUsersLimitUpdateFailed"),
      save: t("settings.adminUsersLimitSave"),
      invalid: t("settings.adminUsersLimitInvalid"),
      unlimited: t("settings.adminUsersLimitUnlimited"),
      usedFmt: t("settings.adminUsersLimitUsed"),
    },
  }[kind];

  const mutation = useMutation({
    // Awaited rather than returned: the two endpoints answer with DIFFERENT shapes
    // (max_devices_per_user vs max_users), and a union return would make the shared
    // mutation's generic unassignable. The caller only cares that it succeeded — the
    // refreshed list is the source of truth for the new value.
    mutationFn: async (next: number) => {
      if (kind === "devices_per_user") {
        await setTenantMaxDevicesPerUser(token, tenantID, next);
        return;
      }
      await setTenantMaxUsers(token, tenantID, next);
    },
    onSuccess: async () => {
      void message.success(labels.updated);
      setEditing(false);
      await queryClient.invalidateQueries({ queryKey: ["admin-tenants", token] });
    },
    onError: (err) => {
      void message.error(err instanceof Error ? `${labels.failed}: ${err.message}` : labels.failed);
    },
  });

  // Over the allowance is shown, not blocked: lowering a limit never evicts what
  // already exists, so an operator can legitimately be looking at "5 of 2" after
  // tightening a policy. Rendering that as an error would suggest something must be
  // fixed immediately, when the choice is theirs.
  const overLimit = limit > 0 && used !== undefined && used > limit;

  if (!editing) {
    return (
      <Space size={8} wrap>
        <Tooltip title={labels.hint}>
          <Tag color={overLimit ? "orange" : "default"}>
            {limit > 0
              ? used === undefined
                ? `${limit}`
                : labels.usedFmt.replace("{used}", String(used)).replace("{limit}", String(limit))
              : used === undefined
                ? labels.unlimited
                : `${labels.unlimited} (${used})`}
          </Tag>
        </Tooltip>
        <Button
          size="small"
          onClick={() => {
            setDraft(limit);
            setEditing(true);
          }}
        >
          {labels.col}
        </Button>
      </Space>
    );
  }

  return (
    <Space size={4}>
      <Tooltip title={labels.hint}>
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
            void message.error(labels.invalid);
            return;
          }
          mutation.mutate(draft);
        }}
      >
        {labels.save}
      </Button>
      <Button size="small" onClick={() => setEditing(false)}>
        <Typography.Text type="secondary">✕</Typography.Text>
      </Button>
    </Space>
  );
}
