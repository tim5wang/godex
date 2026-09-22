import { useEffect, useMemo, useRef, useState } from "react";
import cytoscape, { type Core, type NodeSingular, type EdgeSingular } from "cytoscape";
import { Alert, Button, Empty, Input, InputNumber, Select, Space, Tag, Typography } from "antd";
import { DeleteOutlined, PlusOutlined, SaveOutlined } from "@ant-design/icons";
import { createFlow, type FlowDefinition, type FlowEdge, type FlowNode, type FlowVersionView } from "../../lib/api";

const { Text, Paragraph } = Typography;

// ---------------------------------------------------------------------------
// FlowGramEditor — editable cytoscape canvas for Flow Spec v1.
//
// Layout: left = canvas (design-time editing), right = node/edge inspector.
// - Six node materials (step / llm / decision / human / branch / loop).
// - Drag nodes to reposition; drag between nodes to create edges.
// - Click a node to edit its form in the inspector.
// - Save persists the definition as a NEW version through createFlow.
//
// 双向 adapter:
//   defToCy(def) -> cytoscape elements (positions from node.pos, layered auto-layout fallback)
//   cyToDef(cy)  -> FlowDefinition (nodes + edges back to spec)
// ---------------------------------------------------------------------------

export const NODE_KINDS = ["step", "llm", "decision", "human", "branch", "loop"] as const;

const KIND_COLOR: Record<string, string> = {
  step: "#1677ff",
  llm: "#722ed1",
  decision: "#fa8c16",
  human: "#13c2c2",
  branch: "#eb2f96",
  loop: "#52c41a",
};

const EDGE_TYPES = ["data_dependency", "handoff", "condition"] as const;

interface CyPos {
  x: number;
  y: number;
}

/** Persistent layout: read/write canvas position from node.canvas_pos
 * (editor-only metadata; the Go model strips it before compile). */
function readPos(n: FlowNode): CyPos | undefined {
  return (n as FlowNode & { canvas_pos?: CyPos }).canvas_pos;
}

function stripPos(n: FlowNode): FlowNode {
  const { canvas_pos: _pos, ...rest } = n as FlowNode & { canvas_pos?: CyPos };
  return rest as FlowNode;
}

function blankNode(id: string, kind: string): FlowNode {
  const base: FlowNode = { id, kind, title: "", prompt: "" };
  switch (kind) {
    case "decision":
      base.decision = { decision_type: "choice", choices: [{ id: "yes" }, { id: "no" }] };
      break;
    case "human":
      base.human = { queue: "ops", assignee_policy: "any" };
      break;
    case "branch":
      base.branch = { cases: [], default_to: "" };
      break;
    case "loop":
      base.loop = { body: [], exit_when: {}, max_iterations: 5 };
      break;
  }
  return base;
}

function blankEdgeId(n: number): string {
  return `e${n + 1}`;
}

/** Convert a FlowDefinition to cytoscape elements (双向 adapter: spec → cy). */
function defToCy(def: FlowDefinition): cytoscape.ElementDefinition[] {
  const els: cytoscape.ElementDefinition[] = [];
  (def.nodes ?? []).forEach((node, i) => {
    const n = node as FlowNode & { canvas_pos?: CyPos };
    els.push({
      data: {
        id: node.id,
        label: node.title || node.prompt || node.id,
        kind: node.kind,
        // Carry the full spec fields through scratch so cy→def round-trips
        // preserve prompt/decision/branch/human/loop/etc.
        spec: stripPos(node),
      },
      position: n.canvas_pos ?? { x: 80 + i * 200, y: 120 + (i % 2) * 100 },
      group: "nodes",
    });
  });
  (def.edges ?? []).forEach((edge) => {
    els.push({
      data: { id: edge.id ?? `${edge.from}->${edge.to}`, source: edge.from, target: edge.to, edge_type: edge.edge_type ?? "data_dependency" },
      group: "edges",
    });
  });
  return els;
}

/** Convert cytoscape state back to a FlowDefinition (双向 adapter: cy → spec). */
function cyToDef(cy: Core, flowId: string, version: string, status: string): FlowDefinition {
  const nodes: FlowNode[] = [];
  cy.nodes().forEach((n: NodeSingular) => {
    const d = n.data();
    const node: FlowNode & { canvas_pos?: CyPos } = {
      id: d.id,
      kind: d.kind ?? "step",
      title: typeof d.label === "string" && d.label !== d.id ? d.label : undefined,
      canvas_pos: { x: n.position("x"), y: n.position("y") },
    };
    // Keep spec-only fields attached via scratch data (carried through edits).
    const extra = n.data("spec");
    if (extra && typeof extra === "object") {
      Object.assign(node, extra);
    }
    nodes.push(node);
  });
  const edges: FlowEdge[] = [];
  cy.edges().forEach((e: EdgeSingular) => {
    const d = e.data();
    edges.push({
      id: d.id,
      from: d.source,
      to: d.target,
      edge_type: d.edge_type ?? "data_dependency",
    });
  });
  return {
    flow_id: flowId,
    version,
    status,
    nodes,
    edges,
  };
}

// ---------------------------------------------------------------------------
// Node inspector form (six node materials, one form per kind)
// ---------------------------------------------------------------------------

function NodeForm({
  node,
  onChange,
  t,
}: {
  node: FlowNode;
  onChange: (patch: Partial<FlowNode>) => void;
  t: (k: string, v?: Record<string, string | number>) => string;
}) {
  const set = (patch: Partial<FlowNode>) => onChange(patch);
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 10 }}>
      <div>
        <Text strong>{t("flows.nodeId")}</Text>
        <Input size="small" value={node.id} onChange={(e) => set({ id: e.target.value })} />
      </div>
      <div>
        <Text strong>{t("flows.kind")}</Text>
        <Select
          size="small"
          style={{ width: "100%" }}
          value={node.kind}
          onChange={(v) => {
            // Keep id, switch to the new kind's blank spec shape.
            set(blankNode(node.id, v));
          }}
          options={NODE_KINDS.map((k) => ({ value: k, label: k }))}
        />
      </div>
      <div>
        <Text strong>{t("flows.title")}</Text>
        <Input size="small" value={node.title ?? ""} onChange={(e) => set({ title: e.target.value })} />
      </div>
      <div>
        <Text strong>{t("flows.prompt")}</Text>
        <Input.TextArea
          size="small"
          rows={3}
          value={node.prompt ?? ""}
          onChange={(e) => set({ prompt: e.target.value })}
        />
      </div>

      {node.kind === "decision" && (
        <DecisionFields node={node} set={set} t={t} />
      )}
      {node.kind === "human" && (
        <HumanFields node={node} set={set} t={t} />
      )}
      {node.kind === "branch" && (
        <BranchFields node={node} set={set} t={t} />
      )}
      {node.kind === "loop" && (
        <LoopFields node={node} set={set} t={t} />
      )}
    </div>
  );
}

function DecisionFields({
  node,
  set,
  t,
}: {
  node: FlowNode;
  set: (patch: Partial<FlowNode>) => void;
  t: (k: string, v?: Record<string, string | number>) => string;
}) {
  const d = node.decision ?? { decision_type: "choice" as const, choices: [] };
  return (
    <>
      <div>
        <Text strong>{t("flows.decisionType")}</Text>
        <Select
          size="small"
          style={{ width: "100%" }}
          value={d.decision_type ?? "choice"}
          onChange={(v) => set({ decision: { ...d, decision_type: v } })}
          options={[
            { value: "choice", label: "choice" },
            { value: "boolean", label: "boolean" },
            { value: "score", label: "score" },
          ]}
        />
      </div>
      {d.decision_type === "choice" && (
        <div>
          <Text strong>{t("flows.choices")}</Text>
          <Space direction="vertical" style={{ width: "100%" }}>
            {(d.choices ?? []).map((c, i) => (
              <Space key={i} style={{ width: "100%" }}>
                <Input
                  size="small"
                  placeholder="id"
                  value={c.id}
                  onChange={(e) => {
                    const choices = [...(d.choices ?? [])];
                    choices[i] = { ...c, id: e.target.value };
                    set({ decision: { ...d, choices } });
                  }}
                />
                <Button
                  size="small"
                  type="text"
                  danger
                  icon={<DeleteOutlined />}
                  onClick={() => set({ decision: { ...d, choices: (d.choices ?? []).filter((_, j) => j !== i) } })}
                />
              </Space>
            ))}
            <Button
              size="small"
              icon={<PlusOutlined />}
              onClick={() => set({ decision: { ...d, choices: [...(d.choices ?? []), { id: `c${(d.choices ?? []).length + 1}` }] } })}
            >
              {t("flows.addChoice")}
            </Button>
          </Space>
        </div>
      )}
    </>
  );
}

function HumanFields({
  node,
  set,
  t,
}: {
  node: FlowNode;
  set: (patch: Partial<FlowNode>) => void;
  t: (k: string, v?: Record<string, string | number>) => string;
}) {
  const h = node.human ?? { queue: "ops" };
  return (
    <>
      <div>
        <Text strong>{t("flows.humanQueue")}</Text>
        <Input size="small" value={h.queue ?? ""} onChange={(e) => set({ human: { ...h, queue: e.target.value } })} />
      </div>
      <div>
        <Text strong>{t("flows.humanAssignee")}</Text>
        <Input size="small" value={h.assignee_policy ?? ""} onChange={(e) => set({ human: { ...h, assignee_policy: e.target.value } })} />
      </div>
      <div>
        <Text strong>{t("flows.humanResultVar")}</Text>
        <Input size="small" value={h.result_var ?? ""} onChange={(e) => set({ human: { ...h, result_var: e.target.value } })} />
      </div>
    </>
  );
}

function BranchFields({
  node,
  set,
  t,
}: {
  node: FlowNode;
  set: (patch: Partial<FlowNode>) => void;
  t: (k: string, v?: Record<string, string | number>) => string;
}) {
  const b = node.branch ?? { cases: [], default_to: "" };
  return (
    <>
      <div>
        <Text strong>{t("flows.branchCases")}</Text>
        <Space direction="vertical" style={{ width: "100%" }}>
          {(b.cases ?? []).map((c, i) => (
            <Space key={i} style={{ width: "100%" }}>
              <Input
                size="small"
                placeholder="name"
                style={{ width: 80 }}
                value={c.name ?? ""}
                onChange={(e) => {
                  const cases = [...(b.cases ?? [])];
                  cases[i] = { ...c, name: e.target.value };
                  set({ branch: { ...b, cases } });
                }}
              />
              <Input
                size="small"
                placeholder="to"
                style={{ width: 100 }}
                value={c.to ?? ""}
                onChange={(e) => {
                  const cases = [...(b.cases ?? [])];
                  cases[i] = { ...c, to: e.target.value };
                  set({ branch: { ...b, cases } });
                }}
              />
              <Button
                size="small"
                type="text"
                danger
                icon={<DeleteOutlined />}
                onClick={() => set({ branch: { ...b, cases: (b.cases ?? []).filter((_, j) => j !== i) } })}
              />
            </Space>
          ))}
          <Button
            size="small"
            icon={<PlusOutlined />}
            onClick={() => set({ branch: { ...b, cases: [...(b.cases ?? []), { name: "", to: "", condition: {} }] } })}
          >
            {t("flows.addCase")}
          </Button>
        </Space>
      </div>
      <div>
        <Text strong>{t("flows.branchDefault")}</Text>
        <Input size="small" value={b.default_to ?? ""} onChange={(e) => set({ branch: { ...b, default_to: e.target.value } })} />
      </div>
    </>
  );
}

function LoopFields({
  node,
  set,
  t,
}: {
  node: FlowNode;
  set: (patch: Partial<FlowNode>) => void;
  t: (k: string, v?: Record<string, string | number>) => string;
}) {
  const l = node.loop ?? { body: [], exit_when: {}, max_iterations: 5 };
  return (
    <div>
      <Text strong>{t("flows.maxIterations")}</Text>
      <InputNumber
        size="small"
        style={{ width: "100%" }}
        min={1}
        value={l.max_iterations ?? 5}
        onChange={(v) => set({ loop: { ...l, max_iterations: v ?? 5 } })}
      />
    </div>
  );
}

// ---------------------------------------------------------------------------
// FlowGramEditor
// ---------------------------------------------------------------------------

export function FlowGramEditor(props: {
  flowId: string;
  token: string | null;
  t: (k: string, v?: Record<string, string | number>) => string;
  versions: FlowVersionView[];
  onSaved: () => void;
  onSaveError: (err: unknown) => void;
}) {
  const { flowId, token, t, versions, onSaved, onSaveError } = props;
  const containerRef = useRef<HTMLDivElement | null>(null);
  const cyRef = useRef<Core | null>(null);
  const [selected, setSelected] = useState<FlowNode | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const latest = useMemo(
    () => [...versions].reverse().find((v) => v.definition)?.definition,
    [versions],
  );
  const nextVersion = useMemo(() => {
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
  }, [versions]);

  // (Re)build the canvas whenever the latest definition changes.
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const def = latest ?? {
      flow_id: flowId,
      version: nextVersion,
      status: "draft",
      nodes: [],
      edges: [],
    } satisfies FlowDefinition;

    const cy = cytoscape({
      container,
      elements: defToCy(def),
      style: [
        {
          selector: "node",
          style: {
            "background-color": (el) => KIND_COLOR[el.data("kind") as string] ?? "#8c8c8c",
            label: "data(label)",
            color: "#fff",
            "text-valign": "center",
            "text-halign": "center",
            "font-size": 11,
            width: 120,
            height: 44,
            shape: "round-rectangle",
            "border-width": 1,
            "border-color": "#00000033",
          },
        },
        {
          selector: "edge",
          style: {
            width: 2,
            "line-color": (el) => (el.data("edge_type") === "condition" ? "#fa8c16" : "#8c8c8c"),
            "curve-style": "bezier",
            "target-arrow-shape": "triangle",
            "target-arrow-color": (el) => (el.data("edge_type") === "condition" ? "#fa8c16" : "#8c8c8c"),
            label: (el: cytoscape.EdgeSingular) => (el.data("edge_type") === "condition" ? "when" : ""),
            "font-size": 9,
          },
        },
        {
          selector: ":selected",
          style: { "border-width": 3, "border-color": "#1677ff" },
        },
      ],
      layout: { name: "preset" },
      userPanningEnabled: true,
      userZoomingEnabled: true,
      boxSelectionEnabled: true,
    });

    // Edge creation: drag from node to node.
    let dragSource: string | null = null;
    cy.on("dragstart", "node", (ev) => {
      dragSource = ev.target.id();
    });
    cy.on("dragfree", "node", (ev) => {
      const targetId = ev.target.id();
      if (dragSource && dragSource !== targetId) {
        const exists = cy.edges().some((e) => {
          const ee = e as EdgeSingular;
          return ee.data("source") === dragSource && ee.data("target") === targetId;
        });
        if (!exists) {
          cy.add({
            group: "edges",
            data: {
              id: blankEdgeId(cy.edges().length),
              source: dragSource,
              target: targetId,
              edge_type: "data_dependency",
            },
          });
        }
      }
      dragSource = null;
    });

    // Selection: node -> inspector form.
    cy.on("select", "node", (ev) => {
      const n = ev.target as NodeSingular;
      const d = n.data();
      const spec = (n.data("spec") as Partial<FlowNode> | undefined) ?? {};
      setSelected({
        id: d.id,
        kind: d.kind ?? "step",
        title: typeof d.label === "string" && d.label !== d.id ? d.label : "",
        prompt: typeof spec.prompt === "string" ? spec.prompt : "",
        ...spec,
      });
    });
    cy.on("unselect", "node", () => setSelected(null));

    // Double-click blank area -> add a step node.
    cy.on("tap", (ev) => {
      if (ev.target === cy) {
        const pos = ev.position;
        const id = `n${cy.nodes().length + 1}`;
        cy.add({
          group: "nodes",
          data: { id, label: id, kind: "step" },
          position: pos,
        });
        setSelected(blankNode(id, "step"));
      }
    });

    cyRef.current = cy;
    return () => {
      cy.destroy();
      cyRef.current = null;
    };
  }, [latest, flowId, nextVersion]);

  const syncNode = (node: FlowNode) => {
    const cy = cyRef.current;
    if (!cy) return;
    const el = cy.getElementById(node.id);
    if (el.length === 0) return;
    el.data("label", node.title || node.prompt || node.id);
    el.data("kind", node.kind);
    el.data("spec", stripPos(node));
    setSelected({ ...node });
  };

  const addNode = (kind: string) => {
    const cy = cyRef.current;
    if (!cy) return;
    const id = `n${cy.nodes().length + 1}`;
    const node = blankNode(id, kind);
    cy.add({
      group: "nodes",
      data: { id, label: id, kind, spec: stripPos(node) },
      position: { x: 80 + cy.nodes().length * 40, y: 120 + (cy.nodes().length % 2) * 80 },
    });
    setSelected(node);
  };

  const deleteSelected = () => {
    const cy = cyRef.current;
    if (!cy) return;
    const selectedNodes = cy.nodes(":selected");
    const selectedEdges = cy.edges(":selected");
    if (selectedNodes.length > 0) {
      cy.remove(selectedNodes);
    }
    if (selectedEdges.length > 0) {
      cy.remove(selectedEdges);
    }
    setSelected(null);
  };

  const save = async () => {
    const cy = cyRef.current;
    if (!cy) return;
    setSaving(true);
    setError(null);
    try {
      const def = cyToDef(cy, flowId, nextVersion, "draft");
      await createFlow(token, { flow_id: flowId, version: nextVersion, status: "draft", definition: def });
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      onSaveError(err);
    } finally {
      setSaving(false);
    }
  };

  const nodeKinds = NODE_KINDS;

  return (
    <div style={{ display: "flex", gap: 12, height: 520 }}>
      <div style={{ flex: 1, display: "flex", flexDirection: "column", gap: 8, minWidth: 0 }}>
        <Space style={{ justifyContent: "space-between", width: "100%" }} align="center">
          <Space wrap size={[4, 4]}>
            {nodeKinds.map((k) => (
              <Button key={k} size="small" icon={<PlusOutlined />} onClick={() => addNode(k)}>
                <Tag color={KIND_COLOR[k]} style={{ marginRight: 0 }}>
                  {k}
                </Tag>
              </Button>
            ))}
          </Space>
          <Space>
            <Button size="small" danger icon={<DeleteOutlined />} onClick={deleteSelected}>
              {t("flows.deleteSelected")}
            </Button>
            <Button type="primary" size="small" icon={<SaveOutlined />} loading={saving} onClick={save}>
              {t("flows.saveVersion")} {nextVersion}
            </Button>
          </Space>
        </Space>
        <div
          ref={containerRef}
          style={{ flex: 1, border: "1px solid #e5e5e5", borderRadius: 8, background: "#fafafa", minHeight: 360 }}
        />
        <Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 0 }}>
          {t("flows.canvasEditorHint")}
        </Paragraph>
        {error && <Alert type="error" showIcon message={error} />}
      </div>

      <div style={{ width: 280, flexShrink: 0, border: "1px solid #e5e5e5", borderRadius: 8, padding: 12, overflowY: "auto" }}>
        <Text strong>{t("flows.inspector")}</Text>
        {selected ? (
          <NodeForm
            node={selected}
            onChange={(patch) => syncNode({ ...selected, ...patch })}
            t={t}
          />
        ) : (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("flows.inspectorEmpty")} style={{ marginTop: 24 }} />
        )}
      </div>
    </div>
  );
}
