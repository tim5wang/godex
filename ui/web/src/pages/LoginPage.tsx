import { useState } from "react";
import { Alert, App as AntApp, Button, Card, Form, Input, Segmented, Space, Typography } from "antd";
import { useMutation } from "@tanstack/react-query";
import { useI18n } from "../i18n";
import { loginAccount, registerAccount } from "../lib/apiAuth";
import { acceptMemberInvite } from "../lib/apiAccount";

type Mode = "login" | "register" | "invite" | "token";

/**
 * LoginPage is the gate shown when the instance requires authentication and the
 * browser holds no usable credential.
 *
 * Three ways in, because three real cases exist:
 *   - login: an existing SaaS account (email + password);
 *   - register: a new tenant (design §3.5 self-service signup). Offered only when
 *     the instance accepts anonymous signup (web.allow_self_signup); otherwise the
 *     server refuses it and the tab would be a dead end, so it is hidden;
 *   - invite: join an EXISTING tenant with a one-time code (design §3.6). Without
 *     this mode a second person could never join a tenant, because register
 *     always creates a new one;
 *   - token: the shared web token, which is what every deployment used before
 *     accounts existed. Keeping it avoids locking an existing single-operator
 *     instance out of its own machine.
 */
export function LoginPage(props: {
  /** Called with the credential to install after a successful sign-in. */
  onSessionToken: (token: string) => void;
  onWebToken: (token: string) => void;
  /** Optional message, e.g. "your session expired". */
  notice?: string;
  /**
   * Whether this instance accepts anonymous signup. False (the server default)
   * hides the register tab, since POST /auth/register would answer 403.
   */
  allowSelfSignup?: boolean;
}) {
  const { t } = useI18n();
  const { message } = AntApp.useApp();
  const [mode, setMode] = useState<Mode>("login");
  const signupOpen = props.allowSelfSignup ?? false;

  const loginMutation = useMutation({
    mutationFn: (values: { email: string; password: string }) => loginAccount(values),
    onSuccess: (session) => {
      props.onSessionToken(session.token);
    },
    onError: (error: unknown) => {
      // Deliberately the raw server message: it says "invalid email or password"
      // for both unknown emails and wrong passwords, so showing it leaks nothing.
      void message.error(t("auth.loginFailed") + ": " + errorText(error));
    },
  });

  const registerMutation = useMutation({
    mutationFn: (values: { email: string; password: string; tenantName?: string }) =>
      registerAccount({ email: values.email, password: values.password, tenantName: values.tenantName }),
    onSuccess: (session) => {
      void message.success(t("auth.registerSuccess"));
      props.onSessionToken(session.token);
    },
    onError: (error: unknown) => {
      void message.error(t("auth.registerFailed") + ": " + errorText(error));
    },
  });

  const inviteMutation = useMutation({
    mutationFn: (values: { code: string; email: string; password: string }) =>
      acceptMemberInvite({ code: values.code.trim(), email: values.email, password: values.password }),
    onSuccess: (session) => {
      void message.success(t("auth.inviteSuccess"));
      props.onSessionToken(session.token);
    },
    onError: (error: unknown) => {
      void message.error(t("auth.inviteFailed") + ": " + errorText(error));
    },
  });

  const tokenMutation = useMutation({
    mutationFn: (values: { token: string }) => Promise.resolve(values.token.trim()),
    onSuccess: (token) => {
      if (!token) {
        void message.error(t("auth.tokenRequired"));
        return;
      }
      props.onWebToken(token);
    },
  });

  const pending =
    loginMutation.isPending ||
    registerMutation.isPending ||
    inviteMutation.isPending ||
    tokenMutation.isPending;

  return (
    <div className="godex-auth-gate">
      <Card style={{ width: "min(440px, 92vw)" }}>
        <Space direction="vertical" size={16} style={{ width: "100%" }}>
          <div>
            <Typography.Title level={4} style={{ marginBottom: 4 }}>
              {t("auth.title")}
            </Typography.Title>
            <Typography.Text type="secondary">{t("auth.subtitle")}</Typography.Text>
          </div>

          {props.notice ? <Alert type="warning" showIcon message={props.notice} /> : null}

          <Segmented<Mode>
            block
            value={mode}
            onChange={setMode}
            options={[
              { value: "login", label: t("auth.modeLogin") },
              ...(signupOpen ? [{ value: "register" as Mode, label: t("auth.modeRegister") }] : []),
              { value: "invite", label: t("auth.modeInvite") },
              { value: "token", label: t("auth.modeToken") },
            ]}
          />

          {mode === "login" ? (
            <Form
              layout="vertical"
              requiredMark={false}
              onFinish={(values: { email: string; password: string }) => loginMutation.mutate(values)}
            >
              <Form.Item
                name="email"
                label={t("auth.email")}
                rules={[{ required: true, message: t("auth.emailRequired") }]}
              >
                <Input autoComplete="username" placeholder="you@example.com" />
              </Form.Item>
              <Form.Item
                name="password"
                label={t("auth.password")}
                rules={[{ required: true, message: t("auth.passwordRequired") }]}
              >
                <Input.Password autoComplete="current-password" />
              </Form.Item>
              <Button type="primary" htmlType="submit" loading={pending} block>
                {t("auth.signIn")}
              </Button>
            </Form>
          ) : null}

          {mode === "register" ? (
            <Form
              layout="vertical"
              requiredMark={false}
              onFinish={(values: { email: string; password: string; tenantName?: string }) =>
                registerMutation.mutate(values)
              }
            >
              <Form.Item
                name="email"
                label={t("auth.email")}
                rules={[{ required: true, message: t("auth.emailRequired") }]}
              >
                <Input autoComplete="username" placeholder="you@example.com" />
              </Form.Item>
              <Form.Item
                name="password"
                label={t("auth.password")}
                extra={t("auth.passwordHint")}
                rules={[{ required: true, message: t("auth.passwordRequired") }]}
              >
                <Input.Password autoComplete="new-password" />
              </Form.Item>
              <Form.Item name="tenantName" label={t("auth.tenantName")}>
                <Input placeholder={t("auth.tenantNamePlaceholder")} />
              </Form.Item>
              <Button type="primary" htmlType="submit" loading={pending} block>
                {t("auth.createAccount")}
              </Button>
              <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0, fontSize: 12 }}>
                {t("auth.registerScopeNote")}
              </Typography.Paragraph>
            </Form>
          ) : null}

          {mode === "invite" ? (
            <Form
              layout="vertical"
              requiredMark={false}
              onFinish={(values: { code: string; email: string; password: string }) =>
                inviteMutation.mutate(values)
              }
            >
              <Form.Item
                name="code"
                label={t("auth.inviteCode")}
                extra={t("auth.inviteCodeHint")}
                rules={[{ required: true, message: t("auth.inviteCodeRequired") }]}
              >
                <Input placeholder="gui_..." />
              </Form.Item>
              <Form.Item
                name="email"
                label={t("auth.email")}
                rules={[{ required: true, message: t("auth.emailRequired") }]}
              >
                <Input autoComplete="username" placeholder="you@example.com" />
              </Form.Item>
              <Form.Item
                name="password"
                label={t("auth.password")}
                extra={t("auth.passwordHint")}
                rules={[{ required: true, message: t("auth.passwordRequired") }]}
              >
                <Input.Password autoComplete="current-password" />
              </Form.Item>
              <Button type="primary" htmlType="submit" loading={pending} block>
                {t("auth.acceptInvite")}
              </Button>
              <Typography.Paragraph type="secondary" style={{ marginTop: 12, marginBottom: 0, fontSize: 12 }}>
                {t("auth.inviteScopeNote")}
              </Typography.Paragraph>
            </Form>
          ) : null}

          {mode === "token" ? (
            <Form layout="vertical" requiredMark={false} onFinish={(values: { token: string }) => tokenMutation.mutate(values)}>
              <Form.Item
                name="token"
                label={t("auth.sharedToken")}
                extra={t("auth.sharedTokenHint")}
                rules={[{ required: true, message: t("auth.tokenRequired") }]}
              >
                <Input.Password placeholder={t("auth.sharedTokenPlaceholder")} />
              </Form.Item>
              <Button type="primary" htmlType="submit" loading={pending} block>
                {t("auth.useSharedToken")}
              </Button>
            </Form>
          ) : null}
        </Space>
      </Card>
    </div>
  );
}

function errorText(error: unknown): string {
  return error instanceof Error ? error.message : String(error);
}
