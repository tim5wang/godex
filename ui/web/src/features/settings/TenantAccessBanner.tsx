import { useQuery } from "@tanstack/react-query";
import { Alert, Button, Space } from "antd";
import { useNavigate } from "react-router-dom";
import { useI18n } from "../../i18n";
import { listTenantDelegations } from "../../lib/apiDelegation";

/** TenantAccessBanner is the tenant-side "you are being looked at" notice
 *  (design §3.6, 可见性: 代看期间租户侧显式提示，不做隐蔽访问).
 *
 *  It renders whenever this tenant has an ACTIVE grant, so operating access is
 *  visible for as long as it lasts rather than only discoverable by reading the
 *  audit trail afterwards. The design pairs the banner with audit entries; the
 *  banner is what makes it plain without the tenant having to go looking.
 *
 *  Renders nothing in every other case — including when the caller is a platform
 *  operator (no tenant: the server answers 400) or the instance has no account
 *  store. A silent banner is correct there: this is a tenant notice, and showing
 *  an error on every operator page would train people to ignore it. */
export function TenantAccessBanner({ token }: { token: string | null }) {
  const { t } = useI18n();
  const navigate = useNavigate();
  const query = useQuery({
    queryKey: ["tenant-delegations", token],
    queryFn: () => listTenantDelegations(token),
    retry: false,
    // Re-checked periodically rather than only on mount: a grant can be created
    // from another tab or by another admin, and the tenant should learn about it
    // while it is still live.
    refetchInterval: 60_000,
  });

  const active = (query.data?.delegations ?? []).filter((row) => row.active);
  if (active.length === 0) {
    return null;
  }

  // The soonest expiry is the operationally useful number: it tells the tenant when
  // the window closes on its own, so they know whether they need to act.
  const soonest = active
    .map((row) => row.expires_at ?? "")
    .filter((value) => value !== "")
    .sort()[0];

  return (
    <Alert
      type="warning"
      showIcon
      style={{ margin: 8, marginBottom: 0 }}
      message={t("delegation.bannerTitle", { count: active.length })}
      description={
        <Space direction="vertical" size={4} style={{ width: "100%" }}>
          <span>
            {t("delegation.bannerBody", {
              operators: active.map((row) => row.operator_user_id).join(", "),
              expires: soonest ? new Date(soonest).toLocaleString() : "-",
            })}
          </span>
          <Button size="small" onClick={() => navigate("/settings?tab=access")}>
            {t("delegation.bannerManage")}
          </Button>
        </Space>
      }
    />
  );
}
