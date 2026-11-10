// godex-feature: settings-admin
import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { App, Button, InputNumber, Space, Tag, Tooltip, Typography } from "antd";
import { useI18n } from "../../i18n";
import {
  bytesToMB,
  setTenantMaxDailyMB,
  setTenantMaxDevicesPerUser,
  setTenantMaxUsers,
} from "../../lib/apiAdmin";
import { formatBytes } from "../../lib/byteSize";

/** LimitKind is which allowance this cell edits. Three of them, because the plan has
 *  three: how many people may join, how many devices each of them may have, and how much
 *  traffic the tenant may move per day. */
export type LimitKind = "devices_per_user" | "users" | "daily_traffic";

/** TenantLimitCell is one of a tenant's allowances, editable in place.
 *
 *  A cell rather than a modal because the operator's task is "scan every tenant and
 *  adjust a few": a modal per row turns a two-second pass into a sequence of dialogs,
 *  and the used/limit pair is only useful next to the other tenants.
 *
 *  ONE component serves all three numbers because the interaction is identical — a
 *  draft, a Save, a Cancel, an optimistic-free refresh. Three copies would drift on the
 *  draft/cancel rules, which is where the subtle bugs live.
 *
 *  The draft is local state so a half-typed number is never written to the server. Cancel
 *  restores the server value, which matters when the operator clears the field to retype
 *  and then changes their mind: an empty box must not read as "0 = unlimited".
 *
 *  UNITS: the traffic allowance is stored in bytes but edited in MB, because that is the
 *  unit an operator reasons in and the same unit the free-tier config uses. The
 *  conversion lives in apiAdmin (one place, and the same binary MB), so this component
 *  only decides what to show.
 *
 *  `used` is only passed where the server can count it against this same number. The
 *  per-user device limit is counted per account while the console lists devices per
 *  TENANT, so no used-count is given for it: showing "3 of 2 devices" from a tenant-wide
 *  count would be a number nobody can act on. */
export function TenantLimitCell({
  token,
  tenantID,
  kind,
  limit,
  used,
}: {
  token: string | null;
  tenantID: string;
  kind: LimitKind;
  /** The stored value: a count for the first two kinds, BYTES for daily_traffic. */
  limit: number;
  /** Server-reported usage, in the same unit as `limit`. */
  used?: number;
}) {
  const { t } = useI18n();
  const { message } = App.useApp();
  const queryClient = useQueryClient();

  const labels: Record<LimitKind, { col: string; hint: string; updated: string; failed: string; save: string; invalid: string; unlimited: string; usedFmt: string }> = {
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
    daily_traffic: {
      col: t("settings.adminDailyTrafficCol"),
      hint: t("settings.adminDailyTrafficHint"),
      updated: t("settings.adminDailyTrafficUpdated"),
      failed: t("settings.adminDailyTrafficUpdateFailed"),
      save: t("settings.adminDailyTrafficSave"),
      invalid: t("settings.adminDailyTrafficInvalid"),
      unlimited: t("settings.adminDailyTrafficUnlimited"),
      usedFmt: t("settings.adminDailyTrafficUsed"),
    },
  };
  const text = labels[kind];
  const isTraffic = kind === "daily_traffic";

  // The edit box works in the operator's unit, not the stored one.
  const toDraft = (stored: number) => (isTraffic ? bytesToMB(stored) : stored);
  const [draft, setDraft] = useState<number | null>(toDraft(limit));
  const [editing, setEditing] = useState(false);

  const mutation = useMutation({
    // Awaited rather than returned: the three endpoints answer with DIFFERENT shapes, and
    // a union return would make the shared mutation's generic unassignable. The caller
    // only cares that it succeeded — the refreshed list is the source of truth.
    mutationFn: async (next: number) => {
      switch (kind) {
        case "devices_per_user":
          await setTenantMaxDevicesPerUser(token, tenantID, next);
          return;
        case "users":
          await setTenantMaxUsers(token, tenantID, next);
          return;
        default:
          await setTenantMaxDailyMB(token, tenantID, next);
      }
    },
    onSuccess: async () => {
      void message.success(text.updated);
      setEditing(false);
      await queryClient.invalidateQueries({ queryKey: ["admin-tenants", token] });
    },
    onError: (err) => {
      void message.error(err instanceof Error ? `${text.failed}: ${err.message}` : text.failed);
    },
  });

  // Over the allowance is shown, not blocked: lowering a limit never evicts what already
  // exists (or disconnects anything), so an operator can legitimately be looking at
  // "5 of 2" after tightening a policy. Rendering that as an error would suggest
  // something must be fixed immediately, when the choice is theirs.
  const overLimit = limit > 0 && used !== undefined && used > limit;

  // Bytes only read well formatted; counts are plain numbers. Both go through the same
  // template so the three cells keep one shape.
  const show = (value: number) => (isTraffic ? formatBytes(value) : String(value));
  const summary =
    limit > 0
      ? used === undefined
        ? show(limit)
        : text.usedFmt.replace("{used}", show(used)).replace("{limit}", show(limit))
      : used === undefined
        ? text.unlimited
        : `${text.unlimited} (${show(used)})`;

  if (!editing) {
    return (
      <Space size={8} wrap>
        <Tooltip title={text.hint}>
          <Tag color={overLimit ? "orange" : "default"}>{summary}</Tag>
        </Tooltip>
        <Button
          size="small"
          onClick={() => {
            setDraft(toDraft(limit));
            setEditing(true);
          }}
        >
          {text.col}
        </Button>
      </Space>
    );
  }

  return (
    <Space size={4}>
      <Tooltip title={text.hint}>
        <InputNumber
          size="small"
          min={0}
          value={draft}
          suffix={isTraffic ? "MB" : undefined}
          onChange={(value) => setDraft(typeof value === "number" ? value : null)}
          style={{ width: isTraffic ? 104 : 76 }}
        />
      </Tooltip>
      <Button
        size="small"
        type="primary"
        loading={mutation.isPending}
        disabled={draft === null}
        onClick={() => {
          if (draft === null || draft < 0) {
            void message.error(text.invalid);
            return;
          }
          mutation.mutate(draft);
        }}
      >
        {text.save}
      </Button>
      <Button size="small" onClick={() => setEditing(false)}>
        <Typography.Text type="secondary">✕</Typography.Text>
      </Button>
    </Space>
  );
}
