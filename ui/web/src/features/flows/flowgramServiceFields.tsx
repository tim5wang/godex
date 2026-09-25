import React from "react";
import {
  type FormRenderProps,
  useWatch,
} from "@flowgram.ai/free-layout-editor";
import { Button, Input, Select, Space, Typography } from "antd";
import { DeleteOutlined, PlusOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import type { FlowNode } from "../../lib/api";

const { Text } = Typography;

// ---- function node fields (P3: js source / wasm ref code node) ------------

export function FunctionFields({
  form,
}: {
  form: FormRenderProps<FlowNode>["form"];
}) {
  const { t } = useI18n();
  const fn = useWatch<{
    runtime?: string;
    source?: string;
    ref?: string;
    handler?: string;
  }>("function") ?? {};
  const setFn = (patch: Record<string, unknown>) => {
    const cur = (form.getValueIn<Record<string, unknown>>("function") ?? {}) as Record<
      string,
      unknown
    >;
    form.setValueIn("function", { ...cur, ...patch });
  };
  const runtime = fn.runtime ?? "js";
  return (
    <div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.nodeRuntime")}</div>
        <Select
          style={{ width: "100%" }}
          value={runtime}
          onChange={(v) => setFn({ runtime: v })}
          options={[
            { value: "js", label: "js (goja sandbox)" },
            { value: "wasm", label: "wasm (plugin ref)" },
          ]}
        />
      </div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.nodeHandler")}</div>
        <Input
          value={fn.handler ?? "handle"}
          placeholder="handle"
          onChange={(e) => setFn({ handler: e.target.value })}
        />
      </div>
      {runtime === "js" ? (
        <div style={{ marginBottom: 8 }}>
          <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.nodeSourceJS")}</div>
          <Input.TextArea
            rows={5}
            style={{ fontFamily: "monospace", fontSize: 11 }}
            value={fn.source ?? ""}
            placeholder={'function handle(ctx, event) {\n  return { result: 1 };\n}'}
            onChange={(e) => setFn({ source: e.target.value })}
          />
        </div>
      ) : (
        <div style={{ marginBottom: 8 }}>
          <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.nodeLibraryRef")}</div>
          <Input
            value={fn.ref ?? ""}
            placeholder="vad_split / asr_transcribe / ..."
            onChange={(e) => setFn({ ref: e.target.value })}
          />
        </div>
      )}
    </div>
  );
}

// ---- declarative HTTP service node ----------------------------------------

export function ServiceFields({
  form,
}: {
  form: FormRenderProps<FlowNode>["form"];
}) {
  const { t } = useI18n();
  const service = useWatch<{
    method?: string;
    url?: string;
    headers?: Record<string, string>;
    body?: unknown;
    auth?: { type?: string; token_env?: string; header_name?: string };
  }>("service") ?? {};
  const nodeId = form.getValueIn<string>("id") ?? "";
  const currentBody = service.body === undefined ? "" : JSON.stringify(service.body, null, 2);
  const [bodyText, setBodyText] = React.useState(currentBody);
  React.useEffect(() => setBodyText(currentBody), [nodeId]);

  const setService = (patch: Record<string, unknown>) => {
    const current = form.getValueIn<Record<string, unknown>>("service") ?? {};
    form.setValueIn("service", { ...current, ...patch });
  };
  const headerRows = Object.entries(service.headers ?? {});
  const updateHeader = (index: number, name: string, value: string) => {
    const next = [...headerRows];
    next[index] = [name, value];
    setService({ headers: Object.fromEntries(next) });
  };
  const authType = service.auth?.type ?? "none";
  const setAuth = (patch: Record<string, unknown>) => {
    const current = service.auth ?? {};
    setService({ auth: { ...current, ...patch } });
  };
  const bodyValid = !bodyText.trim() || (() => {
    try {
      JSON.parse(bodyText);
      return true;
    } catch {
      return false;
    }
  })();

  return (
    <div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.serviceMethod")}</div>
        <Select
          style={{ width: "100%" }}
          value={(service.method ?? "GET").toUpperCase()}
          onChange={(method) => setService({ method })}
          options={["GET", "POST", "PUT", "PATCH", "DELETE"].map((method) => ({ value: method, label: method }))}
        />
      </div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.serviceURL")}</div>
        <Input
          value={service.url ?? ""}
          placeholder="https://api.example.com/v1/items/{{inputs.id}}"
          onChange={(e) => setService({ url: e.target.value })}
        />
      </div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.serviceHeaders")}</div>
        <Space direction="vertical" style={{ width: "100%" }} size={4}>
          {headerRows.map(([name, value], index) => (
            <Space key={index} style={{ width: "100%" }} size={4}>
              <Input
                aria-label={t("flows.serviceHeaderName")}
                placeholder="X-Trace"
                value={name}
                onChange={(e) => updateHeader(index, e.target.value, value)}
              />
              <Input
                aria-label={t("flows.serviceHeaderValue")}
                placeholder="{{inputs.trace}}"
                value={value}
                onChange={(e) => updateHeader(index, name, e.target.value)}
              />
              <Button
                size="small"
                type="text"
                danger
                icon={<DeleteOutlined />}
                onClick={() => setService({ headers: Object.fromEntries(headerRows.filter((_, i) => i !== index)) })}
              />
            </Space>
          ))}
          <Button
            size="small"
            icon={<PlusOutlined />}
            onClick={() => setService({ headers: { ...(service.headers ?? {}), "": "" } })}
          >
            {t("flows.serviceAddHeader")}
          </Button>
        </Space>
      </div>
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.serviceAuth")}</div>
        <Select
          style={{ width: "100%" }}
          value={authType}
          onChange={(type) => {
            if (type === "none") {
              setService({ auth: undefined });
              return;
            }
            setService({
              auth: {
                type,
                token_env: service.auth?.token_env ?? "",
                ...(type === "api_key" ? { header_name: service.auth?.header_name ?? "X-Api-Key" } : {}),
              },
            });
          }}
          options={[
            { value: "none", label: t("flows.serviceAuthNone") },
            { value: "bearer", label: "Bearer" },
            { value: "api_key", label: "API key" },
          ]}
        />
      </div>
      {authType !== "none" && (
        <>
          <div style={{ marginBottom: 8 }}>
            <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.serviceTokenEnv")}</div>
            <Input
              value={service.auth?.token_env ?? ""}
              placeholder="SERVICE_API_TOKEN"
              onChange={(e) => setAuth({ token_env: e.target.value })}
            />
          </div>
          {authType === "api_key" && (
            <div style={{ marginBottom: 8 }}>
              <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.serviceAuthHeader")}</div>
              <Input
                value={service.auth?.header_name ?? ""}
                placeholder="X-Api-Key"
                onChange={(e) => setAuth({ header_name: e.target.value })}
              />
            </div>
          )}
        </>
      )}
      <div style={{ marginBottom: 8 }}>
        <div style={{ fontSize: 12, color: "#666", marginBottom: 4 }}>{t("flows.serviceBody")}</div>
        <Input.TextArea
          rows={5}
          style={{ fontFamily: "monospace", fontSize: 11 }}
          value={bodyText}
          status={bodyValid ? undefined : "error"}
          placeholder={'{\n  "query": "{{inputs.query}}"\n}'}
          onChange={(e) => {
            const text = e.target.value;
            setBodyText(text);
            if (!text.trim()) {
              setService({ body: undefined });
              return;
            }
            try {
              setService({ body: JSON.parse(text) as unknown });
            } catch {
              // Keep the editable draft in the form but save only valid JSON.
            }
          }}
        />
        <Text type={bodyValid ? "secondary" : "danger"} style={{ fontSize: 10 }}>
          {bodyValid ? t("flows.serviceOutputHint") : t("flows.serviceBodyInvalid")}
        </Text>
      </div>
    </div>
  );
}
