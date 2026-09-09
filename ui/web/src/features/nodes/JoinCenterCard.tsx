import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, App, Button, Card, Form, Input, Select, Space, Tag, Typography } from "antd";
import { ApiOutlined, CheckCircleOutlined, ReloadOutlined, SaveOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { getConfigView, joinSelfCenter, updateConfig } from "../../lib/api";
import { useSettingsStore } from "../../store/settings";

/**
 * Node-side "join a center" card. This is the counterpart of the center-side
 * JoinNodeCard (which GENERATES the `godex node join` command for a remote
 * machine): here the operator configures THIS node to join a center directly
 * from its own Web UI — no CLI needed, which is what makes it workable on
 * Android / containers where running `godex node join` is impractical.
 *
 * The backend registers this node with the center (web token auth), receives a
 * per-node credential, persists control.center_url / credential / center_token /
 * node_id / trust_level, and the live-apply path (controlRuntime) then starts
 * the relay agent + heartbeat immediately — no restart required.
 *
 * The card also hosts the node's TCP forward allowlist (control.forward_allow,
 * migrated from the Settings page's Control Plane section) since both are
 * node-scoped join/egress settings that were previously buried in Settings.
 */
export function JoinCenterCard() {
  const { t } = useI18n();
  const token = useSettingsStore((state) => state.token);
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const [form] = Form.useForm();

  const configView = useQuery({
    queryKey: ["config-view", token],
    enabled: !!token,
    queryFn: () => getConfigView(token || null),
  });
  const effective = configView.data?.effective_values ?? {};
  const joinedURL = (effective["control.center_url"] as string) || "";
  const nodeID = (effective["control.node_id"] as string) || "";
  const trustLevel = (effective["control.trust_level"] as string) || "";
  const forwardAllow = Array.isArray(effective["control.forward_allow"])
    ? (effective["control.forward_allow"] as string[])
    : [];

  const refresh = async () => {
    await Promise.all([
      queryClient.invalidateQueries({ queryKey: ["config-view", token] }),
      queryClient.invalidateQueries({ queryKey: ["control-nodes", token] }),
    ]);
  };

  const join = async (values: { center_url: string; center_token: string; node_id?: string; name?: string; trust_level: string }) => {
    try {
      await joinSelfCenter(token || null, {
        center_url: values.center_url.trim(),
        token: values.center_token.trim(),
        node_id: values.node_id?.trim() || undefined,
        name: values.name?.trim() || undefined,
        trust_level: values.trust_level,
      });
      void message.success(t("nodes.joinCenterSuccess"));
      form.resetFields();
      await refresh();
    } catch (err) {
      void message.error(t("nodes.joinCenterFailed") + ": " + (err instanceof Error ? err.message : String(err)));
    }
  };

  const saveForwardAllow = async (values: { forward_allow?: string[] }) => {
    try {
      await updateConfig(token || null, {
        values: { "control.forward_allow": values.forward_allow ?? [] },
        clear_secrets: [],
      });
      void message.success(t("nodes.forwardAllowSaved"));
      await refresh();
    } catch (err) {
      void message.error(t("nodes.forwardAllowSaveFailed") + ": " + (err instanceof Error ? err.message : String(err)));
    }
  };

  const joined = joinedURL !== "";

  return (
    <Card
      title={
        <Space size={8}>
          <ApiOutlined />
          <span>{t("nodes.joinCenterTitle")}</span>
          {joined ? <Tag color="green" icon={<CheckCircleOutlined />}>{t("nodes.joinCenterJoined")}</Tag> : null}
        </Space>
      }
      size="small"
      style={{ marginBottom: 16 }}
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {t("nodes.joinCenterSubtitle")}
      </Typography.Paragraph>
      <Alert
        type="warning"
        showIcon
        style={{ marginBottom: 12 }}
        message={t("nodes.joinCenterTrustWarning")}
      />

      {joined ? (
        <Typography.Paragraph style={{ marginBottom: 12 }}>
          <Tag color="blue">{joinedURL}</Tag>
          {nodeID ? <Tag>{t("nodes.joinCenterNodeID")}: {nodeID}</Tag> : null}
          {trustLevel ? <Tag>{t("nodes.trustLevel")}: {trustLevel}</Tag> : null}
        </Typography.Paragraph>
      ) : null}

      <Form form={form} layout="inline" onFinish={(v) => void join(v)} style={{ rowGap: 12 }}>
        <Form.Item name="center_url" rules={[{ required: true, message: t("nodes.joinCenterURLRequired") }]}>
          <Input placeholder={t("nodes.joinCenterURLPlaceholder")} style={{ width: 280 }} />
        </Form.Item>
        <Form.Item name="center_token" rules={[{ required: true, message: t("nodes.joinCenterTokenRequired") }]}>
          <Input.Password placeholder={t("nodes.joinCenterTokenPlaceholder")} style={{ width: 220 }} />
        </Form.Item>
        <Form.Item name="node_id">
          <Input placeholder={t("nodes.joinIDPlaceholder")} style={{ width: 180 }} allowClear />
        </Form.Item>
        <Form.Item name="name">
          <Input placeholder={t("nodes.joinNamePlaceholder")} style={{ width: 160 }} allowClear />
        </Form.Item>
        <Form.Item name="trust_level" initialValue="trusted">
          <Select
            options={[
              { value: "trusted", label: t("nodes.joinTrustTrusted") },
              { value: "guarded-remote", label: t("nodes.joinTrustGuarded") },
            ]}
            style={{ width: 180 }}
          />
        </Form.Item>
        <Form.Item>
          <Button type="primary" icon={<ApiOutlined />} loading={configView.isLoading} htmlType="submit">
            {t("nodes.joinCenterJoin")}
          </Button>
        </Form.Item>
      </Form>

      <div style={{ marginTop: 16 }}>
        <Typography.Text strong>{t("nodes.forwardAllowLabel")}</Typography.Text>
        <Form
          layout="inline"
          initialValues={{ forward_allow: forwardAllow }}
          onFinish={(v) => void saveForwardAllow(v)}
          style={{ marginTop: 8, rowGap: 12 }}
        >
          <Form.Item name="forward_allow" style={{ minWidth: 360 }}>
            <Select mode="tags" tokenSeparators={[","]} placeholder={t("settings.placeholderCommaValues")} open={false} style={{ width: 420 }} />
          </Form.Item>
          <Form.Item>
            <Button icon={<SaveOutlined />} htmlType="submit">{t("nodes.forwardAllowSave")}</Button>
          </Form.Item>
        </Form>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          <span role="img" aria-label="hint">⚠️</span> {t("nodes.forwardAllowHint")}
        </Typography.Paragraph>
        <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
          <span role="img" aria-label="least">🔒</span> {t("nodes.forwardAllowLeastPrivilege")}
        </Typography.Paragraph>
      </div>

      <div style={{ marginTop: 12 }}>
        <Button size="small" icon={<ReloadOutlined />} onClick={() => void refresh()}>
          {t("nodes.refresh")}
        </Button>
      </div>
    </Card>
  );
}
