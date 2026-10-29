import { useState } from "react";
import { useMutation, useQueryClient } from "@tanstack/react-query";
import { Alert, App, Button, Card, Form, Input, Space, Typography } from "antd";
import { CopyOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { writeClipboardText } from "../../lib/clipboard";
import { registerAccount } from "../../lib/apiAuth";

/** AdminCreateTenantCard is the operator's documented way to create a tenant while
 *  self-service signup stays closed (design §3.5).
 *
 *  WHY THIS EXISTS: with `web.allow_self_signup` off (the shipped default), the
 *  register tab is hidden and no invite code can help — an invite only ever joins
 *  an EXISTING tenant (§3.6), so there is no code that creates one. Without this
 *  card the operator would have no route in the UI at all and would have to curl
 *  the endpoint with the web token.
 *
 *  The created tenant's session token is deliberately DISCARDED. The operator is
 *  not the new owner: installing that session here would sign the operator out and
 *  hand them someone else's account. Only the tenant identity is shown, plus the
 *  credentials the operator must pass on out of band. */
export function AdminCreateTenantCard({ token }: { token: string | null }) {
  const { t } = useI18n();
  const { message } = App.useApp();
  const queryClient = useQueryClient();
  const [form] = Form.useForm<{ email: string; password: string; tenant_name?: string }>();
  const [created, setCreated] = useState<{ tenantID: string; tenantName: string; email: string; password: string } | null>(
    null,
  );

  const createMutation = useMutation({
    mutationFn: (values: { email: string; password: string; tenant_name?: string }) =>
      registerAccount(
        { email: values.email, password: values.password, tenantName: values.tenant_name },
        token,
      ),
    onSuccess: async (session, values) => {
      // Only the identity is kept; session.token is dropped on purpose (see above).
      setCreated({
        tenantID: session.tenant?.id ?? session.principal.tenant_id ?? "",
        tenantName: session.tenant?.name ?? "",
        email: values.email,
        password: values.password,
      });
      form.resetFields();
      void message.success(t("settings.adminCreateTenantCreated"));
      await queryClient.invalidateQueries({ queryKey: ["admin-tenants", token] });
    },
    onError: (err) => {
      // The server's message is the useful one here (duplicate email, weak
      // password, or "signup is disabled" if the operator's credential lapsed), so
      // it is shown rather than replaced.
      void message.error(
        err instanceof Error ? `${t("settings.adminCreateTenantFailed")}: ${err.message}` : t("settings.adminCreateTenantFailed"),
      );
    },
  });

  const handOver =
    created === null
      ? ""
      : `${t("settings.adminCreateTenantEmail")}: ${created.email}\n${t("settings.adminCreateTenantPassword")}: ${created.password}`;

  return (
    <Card
      title={t("settings.adminCreateTenantTitle")}
      extra={
        <Button
          size="small"
          icon={<CopyOutlined />}
          disabled={handOver === ""}
          onClick={() => {
            void writeClipboardText(handOver);
            void message.success(t("settings.adminCreateTenantCopied"));
          }}
        >
          {t("settings.adminCreateTenantCopy")}
        </Button>
      }
    >
      <Typography.Paragraph type="secondary" style={{ marginTop: 0 }}>
        {t("settings.adminCreateTenantSubtitle")}
      </Typography.Paragraph>
      <Form form={form} layout="vertical" onFinish={(values) => createMutation.mutate(values)}>
        <Form.Item
          name="email"
          label={t("settings.adminCreateTenantEmail")}
          rules={[{ required: true, message: t("settings.adminCreateTenantEmailRequired") }]}
        >
          <Input placeholder="owner@example.com" autoComplete="off" />
        </Form.Item>
        <Form.Item
          name="password"
          label={t("settings.adminCreateTenantPassword")}
          extra={t("settings.adminCreateTenantPasswordHint")}
          rules={[
            { required: true, message: t("settings.adminCreateTenantPasswordRequired") },
            // Mirrors the server's minimum so a too-short password is caught before
            // the round-trip, without inventing a second, stricter rule.
            { min: 8, message: t("settings.adminCreateTenantPasswordTooShort") },
          ]}
        >
          <Input.Password autoComplete="new-password" />
        </Form.Item>
        <Form.Item name="tenant_name" label={t("settings.adminCreateTenantName")}>
          <Input placeholder={t("settings.adminCreateTenantNamePlaceholder")} />
        </Form.Item>
        <Button type="primary" htmlType="submit" loading={createMutation.isPending}>
          {t("settings.adminCreateTenantAction")}
        </Button>
      </Form>

      {created ? (
        <Alert
          style={{ marginTop: 16 }}
          type="success"
          showIcon
          message={t("settings.adminCreateTenantCreated")}
          description={
            <Space direction="vertical" size={4} style={{ width: "100%" }}>
              <Typography.Text>
                {t("settings.adminCreateTenantTenantCol")}: {created.tenantName || created.tenantID}{" "}
                <Typography.Text type="secondary" copyable={{ text: created.tenantID }}>
                  {created.tenantID}
                </Typography.Text>
              </Typography.Text>
              <Typography.Text code copyable={{ text: handOver }} style={{ whiteSpace: "pre-wrap" }}>
                {handOver}
              </Typography.Text>
              <Typography.Text type="secondary">{t("settings.adminCreateTenantHandOver")}</Typography.Text>
            </Space>
          }
        />
      ) : null}
    </Card>
  );
}
