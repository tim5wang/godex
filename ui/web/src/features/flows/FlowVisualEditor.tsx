import { useEffect, useMemo, useState } from "react";
import { useMutation } from "@tanstack/react-query";
import {
  App as AntApp,
  Button,
  Empty,
  Input,
  Select,
  Space,
  Table,
  Typography,
} from "antd";
import { DeleteOutlined, PlusOutlined, SaveOutlined } from "@ant-design/icons";
import {
  createFlow,
  type FlowDefinition,
  type FlowEdge,
  type FlowNode,
  type FlowVersionView,
} from "../../lib/api";

const { Text, Paragraph } = Typography;

const NODE_KINDS = ["step", "llm", "decision", "human", "branch", "loop"];
const EDGE_TYPES = ["data_dependency", "handoff", "condition"];

/** Next numeric version ("1" when none; max+1 when numeric; "1" fallback). */
export function nextFlowVersion(versions: FlowVersionView[]): string {
  let max = 0;
  let found = false;
  for (const v of versions) {
    const n = Number.parseInt(v.version, 10);
    if (Number.isFinite(n)) {
      max = Math.max(max, n);
      found = true;
    }
  }
  return found ? String(max + 1) : "1";
}

function blankNode(id: string): FlowNode {
  return { id, kind: "step", title: "", prompt: "" };
}

function blankEdge(id: string, nodes: FlowNode[]): FlowEdge {
  const from = nodes[0]?.id ?? "";
  const to = nodes[1]?.id ?? "";
  return { id, from, to, edge_type: "data_dependency" };
}

/**
 * FlowVisualEditor — structured (table) editing of nodes/edges. The editor
 * renders the latest definition (or a blank flow) as two editable tables and
 * saves the result as a new version through the normal create-flow path, so a
 * user never has to hand-write Flow Spec JSON.
 */
export function FlowVisualEditor(props: {
  flowId: string;
  token: string | null;
  t: (k: string, v?: Record<string, string | number>) => string;
  versions: FlowVersionView[];
  onApplied: () => void;
}) {
  const { flowId, token, t, versions, onApplied } = props;
  const { message } = AntApp.useApp();

  const latest = useMemo(() => versions.find((v) => v.definition)?.definition, [versions]);

  const [version, setVersion] = useState<string>(() => nextFlowVersion(versions));
  const [nodes, setNodes] = useState<FlowNode[]>(() =>
    latest ? latest.nodes.map((n) => ({ ...n })) : [],
  );
  const [edges, setEdges] = useState<FlowEdge[]>(() =>
    latest ? latest.edges.map((e) => ({ ...e })) : [],
  );

  // Re-seed from the latest definition whenever the version list refreshes
  // (e.g. after an apply/save elsewhere) — but only when the editor state is
  // still pristine (no edits made yet).
  const [dirty, setDirty] = useState(false);
  useEffect(() => {
    if (!dirty && latest) {
      setNodes(latest.nodes.map((n) => ({ ...n })));
      setEdges(latest.edges.map((e) => ({ ...e })));
    }
  }, [latest, dirty]);

  const saveMutation = useMutation({
    mutationFn: async ({ targetVersion }: { targetVersion: string }) => {
      const def: FlowDefinition = {
        flow_id: flowId,
        version: targetVersion,
        status: "draft",
        nodes: nodes.map((n) => ({ ...n })),
        edges: edges.map((e) => ({ ...e })),
      };
      return createFlow(token, { flow_id: flowId, version: targetVersion, status: "draft", definition: def });
    },
    onSuccess: () => {
      message.success(t("flows.templateApplied"));
      setDirty(false);
      onApplied();
    },
    onError: (err) => {
      message.error(err instanceof Error ? err.message : String(err));
    },
  });

  const nodeOptions = nodes.map((n) => ({ value: n.id, label: n.id }));

  const updateNode = (id: string, patch: Partial<FlowNode>) => {
    setDirty(true);
    setNodes((prev) => prev.map((n) => (n.id === id ? { ...n, ...patch } : n)));
  };

  const updateEdge = (id: string | undefined, patch: Partial<FlowEdge>) => {
    setDirty(true);
    setEdges((prev) => prev.map((e) => (e.id === id ? { ...e, ...patch } : e)));
  };

  return (
    <div>
      <Space style={{ marginBottom: 12 }} align="center">
        <Text type="secondary">{t("flows.version")}</Text>
        <Input
          style={{ width: 120 }}
          value={version}
          onChange={(e) => setVersion(e.target.value.trim())}
          placeholder="1"
        />
        <Button
          type="primary"
          icon={<SaveOutlined />}
          loading={saveMutation.isPending}
          onClick={() => saveMutation.mutate({ targetVersion: version || "1" })}
        >
          {t("flows.save")}
        </Button>
      </Space>

      <Text strong>{t("flows.nodes")}</Text>
      <Table<FlowNode>
        rowKey="id"
        size="small"
        style={{ marginTop: 8, marginBottom: 16 }}
        dataSource={nodes}
        pagination={false}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("flows.noNodes")} /> }}
        columns={[
          {
            title: t("flows.nodeId"),
            dataIndex: "id",
            width: 120,
            render: (id: string, row) => (
              <Input
                size="small"
                value={id}
                onChange={(e) => updateNode(row.id, { id: e.target.value })}
              />
            ),
          },
          {
            title: t("flows.kind"),
            dataIndex: "kind",
            width: 130,
            render: (kind: string, row) => (
              <Select
                size="small"
                style={{ width: "100%" }}
                value={kind}
                onChange={(v) => updateNode(row.id, { kind: v })}
                options={NODE_KINDS.map((k) => ({ value: k, label: k }))}
              />
            ),
          },
          {
            title: t("flows.title"),
            dataIndex: "title",
            render: (title: string | undefined, row) => (
              <Input size="small" value={title ?? ""} onChange={(e) => updateNode(row.id, { title: e.target.value })} />
            ),
          },
          {
            title: t("flows.prompt"),
            dataIndex: "prompt",
            render: (prompt: string | undefined, row) => (
              <Input size="small" value={prompt ?? ""} onChange={(e) => updateNode(row.id, { prompt: e.target.value })} />
            ),
          },
          {
            title: "",
            key: "actions",
            width: 60,
            render: (_: unknown, row) => (
              <Button
                size="small"
                type="text"
                danger
                icon={<DeleteOutlined />}
                onClick={() => {
                  setDirty(true);
                  setNodes((prev) => prev.filter((n) => n.id !== row.id));
                  setEdges((prev) => prev.filter((e) => e.from !== row.id && e.to !== row.id));
                }}
              />
            ),
          },
        ]}
        footer={() => (
          <Button
            size="small"
            icon={<PlusOutlined />}
            onClick={() => {
              setDirty(true);
              setNodes((prev) => [...prev, blankNode(`n${prev.length + 1}`)]);
            }}
          >
            {t("flows.addNode")}
          </Button>
        )}
      />

      <Text strong>{t("flows.edges")}</Text>
      <Table<FlowEdge>
        rowKey="id"
        size="small"
        style={{ marginTop: 8 }}
        dataSource={edges}
        pagination={false}
        locale={{ emptyText: <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("flows.noEdges")} /> }}
        columns={[
          {
            title: t("flows.from"),
            dataIndex: "from",
            width: 150,
            render: (from: string, row) => (
              <Select
                size="small"
                style={{ width: "100%" }}
                value={from}
                onChange={(v) => updateEdge(row.id, { from: v })}
                options={nodeOptions}
                showSearch
              />
            ),
          },
          {
            title: t("flows.to"),
            dataIndex: "to",
            width: 150,
            render: (to: string, row) => (
              <Select
                size="small"
                style={{ width: "100%" }}
                value={to}
                onChange={(v) => updateEdge(row.id, { to: v })}
                options={nodeOptions}
                showSearch
              />
            ),
          },
          {
            title: t("flows.edgeType"),
            dataIndex: "edge_type",
            width: 170,
            render: (et: string | undefined, row) => (
              <Select
                size="small"
                style={{ width: "100%" }}
                value={et ?? "data_dependency"}
                onChange={(v) => updateEdge(row.id, { edge_type: v as FlowEdge["edge_type"] })}
                options={EDGE_TYPES.map((k) => ({ value: k, label: k }))}
              />
            ),
          },
          {
            title: "",
            key: "actions",
            width: 60,
            render: (_: unknown, row) => (
              <Button
                size="small"
                type="text"
                danger
                icon={<DeleteOutlined />}
                onClick={() => {
                  setDirty(true);
                  setEdges((prev) => prev.filter((e) => e.id !== row.id));
                }}
              />
            ),
          },
        ]}
        footer={() => (
          <Button
            size="small"
            icon={<PlusOutlined />}
            onClick={() => {
              setDirty(true);
              setEdges((prev) => [...prev, blankEdge(`e${prev.length + 1}`, nodes)]);
            }}
          >
            {t("flows.addEdge")}
          </Button>
        )}
      />

      <Paragraph type="secondary" style={{ marginTop: 12, fontSize: 12 }}>
        {t("flows.editorHint")}
      </Paragraph>
    </div>
  );
}
