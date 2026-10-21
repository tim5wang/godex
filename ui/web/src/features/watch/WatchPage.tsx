import { useState } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { Alert, App, Button, Card, Checkbox, Empty, Grid, Input, Popconfirm, Space, Spin, Table, Tag, Typography } from "antd";
import { CheckCircleOutlined, DeleteOutlined, LinkOutlined, ReloadOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { WATCH_SCOPES, approveWatchPairing, listWatchDevices, revokeWatchDevice, type WatchDevice, type WatchScope } from "../../lib/apiWatch";
import { useSettingsStore } from "../../store/settings";
import { isPairingCodeComplete, normalizePairingCode, PAIRING_CODE_LENGTH } from "./pairingCode";

/**
 * Watch pairing and device management (mockup 10, trusted side).
 *
 * The watch can display a pairing code but cannot approve one — that is the point
 * of the flow, and it is what stops a found watch from granting itself access to
 * a node. Approving therefore has to happen on a client the user already trusts,
 * which is this page.
 *
 * Before this existed the only way to pair a watch was to call
 * `POST /watch/pair/approve` with a bearer token by hand, so the client was
 * unusable for anyone but its author.
 */
export function WatchPage() {
  const { t } = useI18n();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const token = useSettingsStore((state) => state.token);
  const screens = Grid.useBreakpoint();
  const isNarrow = !screens.md;

  const [code, setCode] = useState("");
  const [scopes, setScopes] = useState<WatchScope[]>([...WATCH_SCOPES]);
  const [approving, setApproving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [paired, setPaired] = useState<WatchDevice | null>(null);

  const devicesQuery = useQuery({
    queryKey: ["watch-devices", token],
    refetchInterval: 15_000,
    queryFn: async () => listWatchDevices(token || null),
  });

  const complete = isPairingCodeComplete(code);
  // The node falls back to its full default scope set when sent an empty list
  // (watchauth.NormalizeScopes), so submitting with nothing ticked would grant
  // MORE access than any visible checkbox — the opposite of what unchecking
  // everything looks like it should do. Refusing the submit is the only honest
  // option; silently granting everything would be the worst possible reading.
  const canSubmit = complete && scopes.length > 0;

  const approve = async () => {
    if (!canSubmit) {
      return;
    }
    setError(null);
    setApproving(true);
    try {
      const device = await approveWatchPairing(token || null, normalizePairingCode(code), scopes);
      setPaired(device);
      setCode("");
      void message.success(t("watch.approveSuccess"));
      await queryClient.invalidateQueries({ queryKey: ["watch-devices", token] });
    } catch (err) {
      // A wrong or expired code is the node's answer, shown as-is: inventing a
      // friendlier message here would hide the difference between "expired" and
      // "you typed it wrong".
      setError(err instanceof Error ? err.message : String(err));
    } finally {
      setApproving(false);
    }
  };

  const revoke = async (device: WatchDevice) => {
    try {
      await revokeWatchDevice(token || null, device.device_id);
      void message.success(t("watch.revokeSuccess"));
      await queryClient.invalidateQueries({ queryKey: ["watch-devices", token] });
    } catch (err) {
      void message.error(err instanceof Error ? err.message : String(err));
    }
  };

  const active = (updater: (current: WatchScope[]) => WatchScope[]) => {
    setScopes((current) => updater(current));
  };

  const renderScopes = (values: string[]) => (
    <Space size={[4, 4]} wrap>
      {values.map((scope) => (
        <Tag key={scope} color={scope === "watch:approve" ? "orange" : "default"}>
          {t(`watch.scope.${scope.replace("watch:", "")}`)}
        </Tag>
      ))}
    </Space>
  );

  const renderRevoke = (device: WatchDevice) =>
    device.revoked ? (
      <Tag>{t("watch.revoked")}</Tag>
    ) : (
      <Popconfirm
        title={t("watch.revokeTitle")}
        description={t("watch.revokeConfirm")}
        okText={t("watch.revokeTitle")}
        cancelText={t("app.cancel")}
        onConfirm={() => void revoke(device)}
      >
        <Button size="small" danger icon={<DeleteOutlined />}>
          {t("watch.revoke")}
        </Button>
      </Popconfirm>
    );

  const devices = devicesQuery.data ?? [];

  const deviceCards = devices.map((device) => (
    <Card
      key={device.device_id}
      size="small"
      title={
        <Space direction="vertical" size={2}>
          <Typography.Text strong>{device.name || device.device_id}</Typography.Text>
          <Typography.Text type="secondary" copyable={{ text: device.device_id }}>
            {device.device_id}
          </Typography.Text>
        </Space>
      }
      extra={renderRevoke(device)}
    >
      <Space direction="vertical" size={8} style={{ width: "100%" }}>
        {renderScopes(device.scopes ?? [])}
        <div>
          <Typography.Text type="secondary">{t("watch.lastSeen")}: </Typography.Text>
          <Typography.Text>{device.last_seen ? new Date(device.last_seen).toLocaleString() : t("watch.neverSeen")}</Typography.Text>
        </div>
        <div>
          <Typography.Text type="secondary">{t("watch.expiresAt")}: </Typography.Text>
          <Typography.Text>{device.expires_at ? new Date(device.expires_at).toLocaleDateString() : "-"}</Typography.Text>
        </div>
      </Space>
    </Card>
  ));

  return (
    <main className="page-shell">
      <Card
        title={
          <Space>
            <LinkOutlined />
            {t("watch.pairTitle")}
          </Space>
        }
      >
        <Space direction="vertical" size={12} style={{ width: "100%" }}>
          <Typography.Paragraph type="secondary" style={{ marginBottom: 0 }}>
            {t("watch.pairHint")}
          </Typography.Paragraph>

          <div
            onPaste={(e) => {
              // Paste is the realistic path: the code is copied from the watch. The
              // clipboard holds the displayed form ("9903 7225"), and letting that
              // text reach the cells verbatim would fill a slot with the space and
              // drop the last digit, so it is normalised here instead.
              const digits = normalizePairingCode(e.clipboardData.getData("text"));
              if (digits) {
                e.preventDefault();
                setCode(digits);
              }
            }}
            onKeyDown={(e) => {
              // A space is never part of a code. Swallowing it keeps a user
              // mirroring the watch's "9903 7225" display from silently losing a
              // digit to a space-filled cell.
              if (e.key === " ") {
                e.preventDefault();
              }
            }}
          >
            <Input.OTP
              length={PAIRING_CODE_LENGTH}
              value={normalizePairingCode(code)}
              // Grouping is a separator, not a formatter: antd's `formatter`
              // rewrites the joined cell string, so an inserted space would occupy
              // a cell and truncate the code.
              separator={(index) => (index === 3 ? " " : null)}
              onChange={(value) => setCode(value)}
              disabled={approving}
            />
          </div>

          <div>
            <Typography.Text type="secondary">{t("watch.scopesLabel")}</Typography.Text>
            <Space size={[8, 4]} wrap style={{ display: "flex", marginTop: 6 }}>
              <Checkbox
                checked={scopes.includes("watch:read")}
                onChange={(e) => active((current) => (e.target.checked ? [...current, "watch:read"] : current.filter((s) => s !== "watch:read")))}
              >
                {t("watch.scope.read")}
              </Checkbox>
              <Checkbox
                checked={scopes.includes("watch:command")}
                onChange={(e) => active((current) => (e.target.checked ? [...current, "watch:command"] : current.filter((s) => s !== "watch:command")))}
              >
                {t("watch.scope.command")}
              </Checkbox>
              <Checkbox
                checked={scopes.includes("watch:approve")}
                onChange={(e) => active((current) => (e.target.checked ? [...current, "watch:approve"] : current.filter((s) => s !== "watch:approve")))}
              >
                {t("watch.scope.approve")}
              </Checkbox>
            </Space>
            <Typography.Paragraph type="secondary" style={{ marginTop: 6, marginBottom: 0 }}>
              {t("watch.scopeApproveHint")}
            </Typography.Paragraph>
            {scopes.length === 0 ? (
              <Typography.Paragraph type="warning" style={{ marginTop: 6, marginBottom: 0 }}>
                {t("watch.noScopes")}
              </Typography.Paragraph>
            ) : null}
          </div>

          {error ? <Alert type="error" showIcon message={error} /> : null}

          {paired ? (
            <Alert
              type="success"
              showIcon
              icon={<CheckCircleOutlined />}
              message={t("watch.approvedBanner", { name: paired.name || paired.device_id })}
            />
          ) : null}

          <Button type="primary" disabled={!canSubmit} loading={approving} onClick={() => void approve()}>
            {t("watch.approve")}
          </Button>
        </Space>
      </Card>

      <Card
        title={t("watch.devicesTitle")}
        extra={
          <Button
            icon={<ReloadOutlined />}
            onClick={() => void queryClient.invalidateQueries({ queryKey: ["watch-devices", token] })}
          >
            {t("watch.refresh")}
          </Button>
        }
      >
        {isNarrow ? (
          <Spin spinning={devicesQuery.isLoading}>
            <Space direction="vertical" size={12} style={{ width: "100%" }}>
              {deviceCards}
              {devices.length === 0 ? <Empty description={t("watch.devicesEmpty")} /> : null}
            </Space>
          </Spin>
        ) : (
          <Table<WatchDevice>
            rowKey="device_id"
            loading={devicesQuery.isLoading}
            dataSource={devices}
            locale={{ emptyText: <Empty description={t("watch.devicesEmpty")} /> }}
            pagination={{ pageSize: 20 }}
            columns={[
              {
                title: t("watch.deviceName"),
                render: (_value, device) => (
                  <Space direction="vertical" size={0}>
                    <Typography.Text strong>{device.name || device.device_id}</Typography.Text>
                    <Typography.Text type="secondary" copyable={{ text: device.device_id }}>
                      {device.device_id}
                    </Typography.Text>
                  </Space>
                ),
              },
              {
                title: t("watch.scopesLabel"),
                render: (_value, device) => renderScopes(device.scopes ?? []),
              },
              {
                title: t("watch.lastSeen"),
                render: (_value, device) =>
                  device.last_seen ? new Date(device.last_seen).toLocaleString() : t("watch.neverSeen"),
              },
              {
                title: t("watch.status"),
                render: (_value, device) =>
                  device.revoked ? <Tag color="red">{t("watch.revoked")}</Tag> : <Tag color="green">{t("watch.active")}</Tag>,
              },
              {
                title: t("watch.actions"),
                width: 110,
                render: (_value, device) => renderRevoke(device),
              },
            ]}
          />
        )}
      </Card>
    </main>
  );
}
