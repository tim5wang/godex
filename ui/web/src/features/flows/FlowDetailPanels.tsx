import { useEffect, useMemo, useState, type RefObject } from "react";
import { useMutation, useQuery } from "@tanstack/react-query";
import { App as AntApp, Button, Checkbox, Collapse, Input, InputNumber, Select, Space, Tag, Typography } from "antd";
import { DeleteOutlined, DownloadOutlined, EyeOutlined, PlusOutlined, ReloadOutlined, SaveOutlined, UploadOutlined } from "@ant-design/icons";
import { showError } from "../../lib/notifications";
import CodeEditor from "../files/CodeEditor";
import { createBizKey, createFlow, listBizKeys, revealBizKey, type FlowDefinition, type FlowNetworkPolicy, type FlowVarDef, type FlowSummaryView, type FlowVersionView } from "../../lib/api";
import { type FlowGramFlowEditorHandle } from "./FlowGramFlowEditor";
import { SchemaTreeEditor, type SchemaNode } from "./SchemaTreeEditor";

const { Text, Paragraph } = Typography;

/**
 * VariableScopePanel renders flow inputs/outputs and the node references for
 * each variable in prompts and edge conditions.
 */
export function VariableScopePanel(props: {
  def: FlowDefinition;
  token: string | null;
  flowId: string;
  t: (k: string, v?: Record<string, string | number>) => string;
  message: ReturnType<typeof AntApp.useApp>["message"];
  onRefresh: () => void;
  nextVersion: string;
}) {
  const { def, t, token, flowId, message, onRefresh, nextVersion } = props;
  const nodes = def.nodes ?? [];
  const edges = def.edges ?? [];

  // Editable flow-level inputs/outputs (B5) — previously read-only. Node
  // outputs stay read-only (they belong to the node spec on the canvas).
  // C3: type is a dropdown (string/number/boolean/object/array/any) and
  // object/array/any may carry a nested JSON-Schema-ish fragment (schema)
  // edited inline — “像定义 json schema 一样定义变量”。
  const VAR_TYPES = ["string", "number", "boolean", "object", "array", "any"];
  const [inputs, setInputs] = useState<FlowVarDef[]>(def.inputs ?? []);
  const [outputs, setOutputs] = useState<FlowVarDef[]>(def.outputs ?? []);
  const [network, setNetwork] = useState<FlowNetworkPolicy | undefined>(def.network);
  const [timeoutSec, setTimeoutSec] = useState(def.timeout_sec ?? 0);
  const [targetVersion, setTargetVersion] = useState(nextVersion);
  useEffect(() => {
    setInputs(def.inputs ?? []);
    setOutputs(def.outputs ?? []);
    setNetwork(def.network);
    setTimeoutSec(def.timeout_sec ?? 0);
  }, [def]);

  const patchVar = (list: FlowVarDef[], setter: (v: FlowVarDef[]) => void, i: number, patch: Partial<FlowVarDef>) =>
    setter(list.map((x, j) => (j === i ? { ...x, ...patch } : x)));

  const saveMutation = useMutation({
    mutationFn: async (v: string) => {
      const updated: FlowDefinition = {
        ...def,
        inputs,
        outputs,
        network,
        timeout_sec: timeoutSec,
        version: v,
        status: "draft",
      };
      return createFlow(token, { flow_id: flowId, version: v, status: "draft", definition: updated });
    },
    onSuccess: () => {
      message.success(t("flows.varSaved", { v: targetVersion }));
      onRefresh();
    },
    onError: (err) => showError(message, err, t("flows.saveFailed")),
  });

  // Scan prompt text for {{...}} variable references.
  const refsByVar = new Map<string, string[]>();
  const addRef = (key: string, from: string) => {
    const list = refsByVar.get(key) ?? [];
    if (!list.includes(from)) list.push(from);
    refsByVar.set(key, list);
  };
  const scanText = (text: string | undefined, from: string) => {
    if (!text) return;
    const re = /\{\{\s*([^}]+?)\s*\}\}/g;
    let m: RegExpExecArray | null;
    while ((m = re.exec(text)) !== null) {
      addRef(m[1].trim(), from);
    }
  };
  for (const n of nodes) {
    scanText(n.prompt, n.id);
    scanText(n.service?.url, n.id);
    for (const value of Object.values(n.service?.headers ?? {})) {
      scanText(value, n.id);
    }
    if (n.service?.body !== undefined) {
      scanText(JSON.stringify(n.service.body), n.id);
    }
    if (n.branch?.cases) {
      for (const c of n.branch.cases) scanText(c.condition ? JSON.stringify(c.condition) : undefined, n.id);
    }
  }
  for (const e of edges) {
    if (e.when) scanText(JSON.stringify(e.when), `${e.from}→${e.to}`);
  }

  const varRow = (name: string, type: string | undefined, desc: string | undefined, from: string) => {
    const key = name;
    const refs = refsByVar.get(key) ?? [];
    return (
      <div key={`${from}:${name}`} style={{ display: "flex", gap: 8, alignItems: "center", fontSize: 12, padding: "2px 0" }}>
        <Text code style={{ fontSize: 11 }}>{name}</Text>
        {type && <Tag style={{ fontSize: 10 }}>{type}</Tag>}
        {desc && <Text type="secondary" style={{ fontSize: 11 }}>{desc}</Text>}
        <Text type="secondary" style={{ fontSize: 11 }}>
          {t("flows.varRefs")} {refs.length > 0 ? refs.join(", ") : "—"}
        </Text>
      </div>
    );
  };

  const editableVarList = (
    list: FlowVarDef[],
    setter: (v: FlowVarDef[]) => void,
    kind: "inputs" | "outputs",
  ) => (
    <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
      {list.length === 0 && (
        <Text type="secondary" style={{ fontSize: 11 }}>—</Text>
      )}
      {list.map((v, i) => {
        const nested = v.type === "object" || v.type === "array" || v.type === "any";
        return (
          <div
            key={i}
            style={{
              display: "flex",
              flexDirection: "column",
              gap: 4,
              border: "1px solid #f0f0f0",
              borderRadius: 6,
              padding: 6,
            }}
          >
            <Space size={4} style={{ width: "100%" }}>
              <Input
                size="small"
                style={{ width: 120, fontFamily: "monospace", fontSize: 11 }}
                placeholder={t("flows.varName")}
                value={v.name}
                onChange={(e) => patchVar(list, setter, i, { name: e.target.value })}
              />
              <Select
                size="small"
                style={{ width: 96 }}
                value={v.type ?? "string"}
                onChange={(val) =>
                  patchVar(list, setter, i, {
                    type: val,
                    // Switching away from object/array drops the nested schema.
                    schema: val === "object" || val === "array" || val === "any" ? v.schema : undefined,
                  })
                }
                options={VAR_TYPES.map((tp) => ({ value: tp, label: tp }))}
              />
              <Input
                size="small"
                style={{ flex: 1, minWidth: 0, fontSize: 11 }}
                placeholder={t("flows.varDesc")}
                value={v.desc ?? ""}
                onChange={(e) => patchVar(list, setter, i, { desc: e.target.value })}
              />
              <Button
                size="small"
                type="text"
                danger
                icon={<DeleteOutlined />}
                onClick={() => setter(list.filter((_, j) => j !== i))}
              />
            </Space>
            {nested && (
              <div>
                <Text type="secondary" style={{ fontSize: 10 }}>
                  {t("flows.varSchema")}
                </Text>
                <SchemaTreeEditor
                  value={(v.schema as SchemaNode) ?? undefined}
                  onChange={(next) => patchVar(list, setter, i, { schema: next })}
                />
              </div>
            )}
            <Checkbox
              checked={Boolean(v.required)}
              onChange={(e) => patchVar(list, setter, i, { required: e.target.checked })}
              style={{ fontSize: 11 }}
            >
              {t("flows.varRequired")}
            </Checkbox>
            {kind === "outputs" && (
              <Input
                size="small"
                style={{ fontFamily: "monospace", fontSize: 11 }}
                value={v.source ?? ""}
                placeholder="nodes.finish.outputs.summary"
                onChange={(e) => patchVar(list, setter, i, { source: e.target.value })}
                aria-label={t("flows.varOutputSource")}
              />
            )}
          </div>
        );
      })}
      <Button
        size="small"
        type="dashed"
        icon={<PlusOutlined />}
        onClick={() => setter([...list, { name: "", type: "string" }])}
      >
        {t("flows.varAdd", { kind })}
      </Button>
    </div>
  );

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 12 }}>
      <div
        style={{
          border: "1px solid #f0f0f0",
          borderRadius: 6,
          padding: "6px 8px",
          background: "#fafafa",
        }}
      >
        <Collapse
          ghost
          size="small"
          items={[
            {
              key: "network",
              label: (
                <Space size={6}>
                  <Text strong style={{ fontSize: 12 }}>
                    {t("flows.networkPolicy")}
                  </Text>
                  <Tag color={network?.policy === "allowlist" ? "orange" : "green"} style={{ fontSize: 10 }}>
                    {network?.policy ?? "allow_all"}
                  </Tag>
                </Space>
              ),
              children: (
                <div style={{ display: "flex", flexDirection: "column", gap: 8, paddingTop: 4 }}>
                  <Space size={6} wrap>
                    <Text style={{ fontSize: 11 }}>策略</Text>
                    <Select
                      size="small"
                      style={{ width: 130 }}
                      value={network?.policy ?? "allow_all"}
                      onChange={(p) => setNetwork({
                        ...(network ?? {}),
                        policy: p,
                        allow_private_hosts: p === "allowlist" ? network?.allow_private_hosts : false,
                      })}
                      options={[
                        { value: "allow_all", label: "allow_all" },
                        { value: "allowlist", label: "allowlist" },
                      ]}
                    />
                    <Text style={{ fontSize: 11 }}>超时(秒)</Text>
                    <InputNumber
                      size="small"
                      style={{ width: 70 }}
                      min={1}
                      max={120}
                      value={network?.timeout_seconds ?? 15}
                      onChange={(v) => setNetwork({ ...(network ?? {}), timeout_seconds: v ?? 15 })}
                    />
                    <Text style={{ fontSize: 11 }}>响应上限(字符)</Text>
                    <InputNumber
                      size="small"
                      style={{ width: 90 }}
                      min={1024}
                      value={network?.max_response_chars ?? 1048576}
                      onChange={(v) => setNetwork({ ...(network ?? {}), max_response_chars: v ?? 1048576 })}
                    />
                  </Space>
                  <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
                    <Text type="secondary" style={{ fontSize: 11 }}>
                      {t("flows.networkAllowed")}（allowlist 时生效，支持 *.suffix）
                    </Text>
                    <Select
                      mode="tags"
                      size="small"
                      style={{ width: "100%" }}
                      placeholder="api.openai.com"
                      value={network?.allowed_domains ?? []}
                      onChange={(v: string[]) => setNetwork({
                        ...(network ?? {}),
                        allowed_domains: v,
                        allow_private_hosts: v.length > 0 ? network?.allow_private_hosts : false,
                      })}
                      tokenSeparators={[",", " "]}
                    />
                    <Text type="secondary" style={{ fontSize: 11 }}>
                      {t("flows.networkBlocked")}（始终拦截，两种策略都生效）
                    </Text>
                    <Select
                      mode="tags"
                      size="small"
                      style={{ width: "100%" }}
                      placeholder="*.internal.example"
                      value={network?.blocked_domains ?? []}
                      onChange={(v: string[]) => setNetwork({ ...(network ?? {}), blocked_domains: v })}
                      tokenSeparators={[",", " "]}
                    />
                  </div>
                  <Checkbox
                    checked={Boolean(network?.allow_private_hosts)}
                    disabled={network?.policy !== "allowlist" || (network?.allowed_domains?.length ?? 0) === 0}
                    onChange={(e) => setNetwork({ ...(network ?? {}), allow_private_hosts: e.target.checked })}
                    style={{ fontSize: 11 }}
                  >
                    {t("flows.networkAllowPrivate")}
                  </Checkbox>
                  <Text type="secondary" style={{ fontSize: 10 }}>
                    {t("flows.networkHint")}
                  </Text>
                </div>
              ),
            },
          ]}
        />
      </div>
      <div>
        <Space size={8} wrap>
          <Text strong style={{ fontSize: 12 }}>{t("flows.flowTimeout")}</Text>
          <InputNumber
            size="small"
            min={0}
            max={2_592_000}
            value={timeoutSec}
            onChange={(value) => setTimeoutSec(value ?? 0)}
            addonAfter="s"
          />
          <Text type="secondary" style={{ fontSize: 11 }}>{t("flows.flowTimeoutHint")}</Text>
        </Space>
      </div>
      <div>
        <Text strong style={{ fontSize: 12 }}>{t("flows.varFlowInputs")}</Text>
        <div style={{ marginTop: 4 }}>{editableVarList(inputs, setInputs, "inputs")}</div>
      </div>
      <div>
        <Text strong style={{ fontSize: 12 }}>{t("flows.varFlowOutputs")}</Text>
        <div style={{ marginTop: 4 }}>{editableVarList(outputs, setOutputs, "outputs")}</div>
      </div>
      <Space wrap>
        <Text type="secondary">{t("flows.version")}</Text>
        <Input
          style={{ width: 100 }}
          value={targetVersion}
          onChange={(e) => setTargetVersion(e.target.value.trim())}
          placeholder="1"
        />
        <Button
          type="primary"
          size="small"
          icon={<SaveOutlined />}
          loading={saveMutation.isPending}
          onClick={() => saveMutation.mutate(targetVersion || "1")}
        >
          {t("flows.save")}
        </Button>
        <Text type="secondary" style={{ fontSize: 11 }}>{t("flows.varSaveHint")}</Text>
      </Space>
      <div>
        <Text strong style={{ fontSize: 12 }}>{t("flows.varNodeOutputs")}</Text>
        <div style={{ marginTop: 4, display: "flex", flexDirection: "column", gap: 6 }}>
          {nodes.length === 0 ? (
            <Text type="secondary" style={{ fontSize: 11 }}>—</Text>
          ) : (
            nodes.map((n) => {
              const outs = n.outputs ?? [];
              return (
                <div key={n.id} style={{ border: "1px solid #f0f0f0", borderRadius: 6, padding: 6 }}>
                  <Text style={{ fontSize: 11, fontFamily: "monospace" }}>{n.id}</Text>{" "}
                  <Text type="secondary" style={{ fontSize: 11 }}>({n.kind})</Text>
                  {outs.length === 0 ? (
                    <Text type="secondary" style={{ fontSize: 11, marginLeft: 8 }}>—</Text>
                  ) : (
                    <div style={{ marginTop: 2 }}>{outs.map((v) => varRow(`nodes.${n.id}.outputs.${v.name}`, v.type, v.desc, n.id))}</div>
                  )}
                </div>
              );
            })
          )}
        </div>
      </div>
    </div>
  );
}
/**
 * Editable definition JSON tab that stays in sync with the canvas.
 */
export function DefinitionJsonTab(props: {
  versions: FlowVersionView[];
  t: (k: string, v?: Record<string, string | number>) => string;
  message: ReturnType<typeof AntApp.useApp>["message"];
  onRefresh: () => void;
  onApplyJson: (def: FlowDefinition) => void;
  editorHandleRef: RefObject<FlowGramFlowEditorHandle | null>;
}) {
  const { versions, t, message, onRefresh, onApplyJson, editorHandleRef } = props;
  const latestDef = useMemo(
    () => [...versions].reverse().find((v) => v.definition)?.definition,
    [versions],
  );
  const [dirty, setDirty] = useState(false);
  const [jsonText, setJsonText] = useState(() =>
    latestDef ? JSON.stringify(latestDef, null, 2) : "",
  );

  // Refresh the text when versions reload, unless the user has unsaved edits.
  useEffect(() => {
    if (dirty) return;
    setJsonText(latestDef ? JSON.stringify(latestDef, null, 2) : "");
  }, [latestDef, dirty]);

  const getFromCanvas = () => {
    const def = editorHandleRef.current?.getCurrentDefinition();
    if (!def) {
      message.warning(t("flows.noDefinition"));
      return;
    }
    setJsonText(JSON.stringify(def, null, 2));
    setDirty(false);
    message.success(t("flows.jsonFromCanvas"));
  };

  const apply = () => {
    let parsed: FlowDefinition;
    try {
      parsed = JSON.parse(jsonText) as FlowDefinition;
      if (!Array.isArray(parsed.nodes) || !Array.isArray(parsed.edges)) {
        throw new Error("missing nodes/edges arrays");
      }
    } catch (err) {
      message.error(
        `${t("flows.jsonInvalid")}: ${err instanceof Error ? err.message : String(err)}`,
      );
      return;
    }
    onApplyJson(parsed);
    setDirty(false);
    message.success(t("flows.jsonApplied"));
  };

  return (
    <div>
      <div style={{ border: "1px solid #e5e5e5", borderRadius: 6, overflow: "hidden", height: 420 }}>
        <CodeEditor
          content={jsonText}
          filePath="flow.json"
          onChange={(v) => {
            setJsonText(v);
            setDirty(true);
          }}
        />
      </div>
      <Space style={{ marginTop: 12 }} wrap>
        <Button icon={<ReloadOutlined />} onClick={onRefresh}>
          {t("flows.refresh")}
        </Button>
        <Button icon={<DownloadOutlined />} onClick={getFromCanvas}>
          {t("flows.getFromCanvas")}
        </Button>
        <Button type="primary" icon={<UploadOutlined />} onClick={apply}>
          {t("flows.applyToCanvas")}
        </Button>
      </Space>
    </div>
  );
}
/**
 * Shows how external clients call this flow through the production gateway.
 */
export function ProductionGatewayTab(props: {
  flow: FlowSummaryView;
  token: string | null;
  t: (k: string, v?: Record<string, string | number>) => string;
  versions: FlowVersionView[];
}) {
  const { flow, token, t, versions } = props;
  const { message } = AntApp.useApp();
  const published = versions.find((v) => v.status === "published")?.version;

  // 复用业务 agent 的 BizKey 接入机制（B7）：列出绑定此 flow 的业务 key，
  // 支持一键创建绑定 key + PIN 解锁查看完整密钥，并用真实 key 生成调用示例。
  const keysQuery = useQuery({
    queryKey: ["biz-keys", token],
    enabled: Boolean(token),
    queryFn: () => listBizKeys(token),
  });
  const flowKeys = useMemo(
    () => (keysQuery.data ?? []).filter((k) => k.flow_id === flow.flow_id),
    [keysQuery.data, flow.flow_id],
  );

  const [newSecret, setNewSecret] = useState<string>();
  const createKeyMutation = useMutation({
    mutationFn: () =>
      createBizKey(token, {
        name: `flow-${flow.flow_id}`,
        flow_id: flow.flow_id,
        description: `Business Flow ${flow.flow_id} gateway key`,
      }),
    onSuccess: (res) => {
      setNewSecret(res.secret);
      void keysQuery.refetch();
      message.success(t("flows.productionKeyCreated"));
    },
    onError: (err) => showError(message, err, t("flows.saveFailed")),
  });

  // PIN 解锁完整密钥（与业务 agent 管理页一致）。
  const [revealed, setRevealed] = useState<Record<string, string>>({});
  const [pinInput, setPinInput] = useState<Record<string, string>>({});
  const revealMutation = useMutation({
    mutationFn: ({ id, pin }: { id: string; pin: string }) => revealBizKey(token, id, pin),
    onSuccess: (res, vars) => {
      setRevealed((prev) => ({ ...prev, [vars.id]: res.secret }));
      message.success(t("flows.productionRevealed"));
    },
    onError: (err) => showError(message, err, t("flows.productionRevealFailed")),
  });

  const activeKey = flowKeys[0];
  const curlKey = (activeKey && revealed[activeKey.id]) || activeKey?.key_prefix || "<biz_key>";
  const curl = `curl -X POST http://127.0.0.1:8088/v1/gateway/${encodeURIComponent(flow.flow_id)} \\
  -H "Authorization: Bearer ${curlKey}" \\
  -H "Content-Type: application/json" \\
  -d '{"inputs": {"task": "..."}, "wait_ms": 60000}'`;

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
      <Paragraph type="secondary" style={{ fontSize: 12 }}>
        {t("flows.productionHint")}
      </Paragraph>

      {/* 绑定此 flow 的业务 key */}
      <div>
        <Text strong style={{ fontSize: 12 }}>
          {t("flows.productionBoundKeys")}
        </Text>
        {flowKeys.length === 0 ? (
          <Paragraph type="secondary" style={{ fontSize: 11, marginBottom: 4 }}>
            {t("flows.productionNoKey")}
          </Paragraph>
        ) : (
          <div style={{ marginTop: 4, display: "flex", flexDirection: "column", gap: 4 }}>
            {flowKeys.map((k) => (
              <Space key={k.id} size={6} style={{ width: "100%" }} wrap>
                <Tag color="blue">{k.name}</Tag>
                <Text code style={{ fontSize: 11 }}>
                  {k.key_prefix}
                </Text>
                {k.enabled ? <Tag color="green">enabled</Tag> : <Tag color="red">disabled</Tag>}
                {revealed[k.id] ? (
                  <Text code style={{ fontSize: 10 }}>
                    {revealed[k.id]}
                  </Text>
                ) : (
                  <Space size={4}>
                    <Input.Password
                      size="small"
                      style={{ width: 110, fontSize: 11 }}
                      placeholder={t("flows.productionPin")}
                      value={pinInput[k.id] ?? ""}
                      onChange={(e) => setPinInput((prev) => ({ ...prev, [k.id]: e.target.value }))}
                    />
                    <Button
                      size="small"
                      icon={<EyeOutlined />}
                      loading={revealMutation.isPending}
                      onClick={() => revealMutation.mutate({ id: k.id, pin: pinInput[k.id] ?? "" })}
                    >
                      {t("flows.productionReveal")}
                    </Button>
                  </Space>
                )}
              </Space>
            ))}
          </div>
        )}
        <Space style={{ marginTop: 8 }} wrap>
          <Button
            size="small"
            type="primary"
            icon={<PlusOutlined />}
            loading={createKeyMutation.isPending}
            onClick={() => createKeyMutation.mutate()}
          >
            {t("flows.productionCreateKey")}
          </Button>
          {newSecret && (
            <Text code style={{ fontSize: 11 }}>
              {newSecret}
            </Text>
          )}
        </Space>
        {newSecret && (
          <Paragraph type="warning" style={{ fontSize: 11, marginTop: 4, marginBottom: 0 }}>
            {t("flows.productionSecretOnce")}
          </Paragraph>
        )}
      </div>

      {/* 调用示例（真实 key） */}
      <pre
        style={{
          background: "#f5f5f5",
          padding: 12,
          borderRadius: 6,
          fontSize: 11,
          overflow: "auto",
          marginBottom: 0,
        }}
      >
        {curl}
      </pre>
      <Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 0 }}>
        {published
          ? `${t("flows.productionPublished")} ${published}`
          : t("flows.productionNoRoute")}
      </Paragraph>
    </div>
  );
}
