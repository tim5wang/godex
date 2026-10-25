import { useState } from "react";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, App, Card, Radio, Space, Typography } from "antd";
import { useI18n } from "../../i18n";
import { setNodeVisibility } from "../../lib/apiAdmin";
import { getAuthProfile } from "../../lib/apiAuth";
import type { ControlNode } from "../../lib/types";

/** NodeVisibilityCard is the node OWNER's own exposure control (design §4.2,
 *  decisions ④⑥).
 *
 *  Distinct from the superadmin's claim dialog on purpose: claiming assigns
 *  ownership (platform administration), while this is "how far do I expose the
 *  machine I own". A tenant peer that can REACH a tenant-visible node must not be
 *  able to widen or narrow it, so the server checks ownership rather than
 *  reachability — and this card only offers the control to the owner (or an
 *  operator, who passes the same check for administration).
 *
 *  The control is hidden rather than shown-then-refused for a peer: a switch that
 *  always errors teaches the wrong model of who decides. */
export function NodeVisibilityCard({ node, token }: { node?: ControlNode; token: string | null }) {
  const { t } = useI18n();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  // Same query key as App's profile lookup, so this shares the cached answer
  // instead of adding a second request on every node page.
  const profileQuery = useQuery({
    queryKey: ["auth-profile", token],
    queryFn: () => getAuthProfile(token),
    retry: false,
  });
  const [pending, setPending] = useState<"private" | "tenant" | null>(null);

  const principal = profileQuery.data?.principal;
  const owner = node?.owner_user_id ?? "";
  const isOwner = owner !== "" && owner === (principal?.user_id ?? "");
  const isOperator = principal?.platform_role === "admin";
  const maySet = isOwner || isOperator;

  const mutation = useMutation({
    mutationFn: (visibility: "private" | "tenant") => setNodeVisibility(token, node?.id ?? "", visibility),
    onSuccess: async () => {
      setPending(null);
      void message.success(t("nodes.visibilityUpdateSuccess"));
      await queryClient.invalidateQueries({ queryKey: ["node-overview", node?.id] });
      await queryClient.invalidateQueries({ queryKey: ["control-nodes", token] });
    },
    onError: (err) => {
      setPending(null);
      void message.error(err instanceof Error ? err.message : t("nodes.visibilityUpdateFailed"));
    },
  });

  if (!node) {
    return null;
  }
  if (!maySet) {
    return (
      <Card title={t("nodes.visibility")} size="small">
        <Alert
          type="info"
          showIcon
          message={t("nodes.visibilityOwnerOnly")}
          description={t("nodes.visibilityPeerNote")}
        />
      </Card>
    );
  }

  const current = (node.visibility === "tenant" ? "tenant" : "private") as "private" | "tenant";
  const value = pending ?? current;

  return (
    <Card title={t("nodes.visibility")} size="small" loading={profileQuery.isLoading}>
      <Space direction="vertical" size={8} style={{ width: "100%" }}>
        <Radio.Group
          value={value}
          disabled={mutation.isPending}
          onChange={(event) => {
            const next = event.target.value as "private" | "tenant";
            setPending(next);
            mutation.mutate(next);
          }}
          optionType="button"
          buttonStyle="solid"
        >
          <Radio.Button value="private">{t("nodes.visibilityPrivate")}</Radio.Button>
          <Radio.Button value="tenant">{t("nodes.visibilityTenant")}</Radio.Button>
        </Radio.Group>
        <Typography.Text type="secondary">{t("nodes.visibilityHint")}</Typography.Text>
      </Space>
    </Card>
  );
}
