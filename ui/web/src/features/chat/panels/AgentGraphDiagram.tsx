import { useEffect, useMemo, useState } from "react";
import { Alert, Empty, Spin, Space, Table, Tag, Typography } from "antd";
import type { AgentGraphView, AgentGraphNode, AgentGraphEdge } from "../../../lib/types";

// ---------------------------------------------------------------------------
// AgentGraphDiagram
//
// Renders the longtask workflow as a mermaid flowchart (nodes + typed edges,
// status-coloured). Mermaid is lazy-imported so it never affects the main
// bundle. On render failure it degrades to a structured table so diagnostics
// stay usable.
// ---------------------------------------------------------------------------

const STATUS_CLASS: Record<string, string> = {
  pending: "gx-pending",
  running: "gx-running",
  completed: "gx-completed",
  failed: "gx-failed",
  blocked: "gx-blocked",
};

// edge_type -> mermaid link shape
function edgeLink(edge: AgentGraphEdge): string {
  switch (edge.edge_type) {
    case "control_flow":
      return "-.->";
    case "handoff":
      return "==>";
    case "data_dependency":
    default:
      return "-->";
  }
}

// Node ids from the backend are workflow node ids (e.g. "n_xxx"); mermaid ids
// must be identifier-safe, so we map them to a stable short alias.
function safeId(raw: string, index: number): string {
  const slug = raw.replace(/[^a-zA-Z0-9_]/g, "_");
  if (/^[a-zA-Z_][a-zA-Z0-9_]*$/.test(slug)) {
    return `n_${slug}`;
  }
  return `n_${index}`;
}

function nodeLabel(node: AgentGraphNode): string {
  const parts: string[] = [];
  if (node.title) parts.push(node.title);
  if (node.node_type) parts.push(`[${node.node_type}]`);
  if (node.verdict) parts.push(`(${node.verdict})`);
  if (node.attempt && node.attempt > 1) parts.push(`try ${node.attempt}`);
  return parts.join(" ") || node.id;
}

export function toMermaidSource(graph: AgentGraphView): string {
  const lines: string[] = ["flowchart LR"];
  lines.push("  classDef gx-pending fill:#e8e8e8,stroke:#8c8c8c,color:#555");
  lines.push("  classDef gx-running fill:#e6f4ff,stroke:#1677ff,color:#0958d9");
  lines.push("  classDef gx-completed fill:#f6ffed,stroke:#52c41a,color:#237804");
  lines.push("  classDef gx-failed fill:#fff2f0,stroke:#ff4d4f,color:#cf1322");
  lines.push("  classDef gx-blocked fill:#fff7e6,stroke:#fa8c16,color:#ad4e00");

  const alias = new Map<string, string>();
  (graph.nodes ?? []).forEach((node, i) => {
    const id = safeId(node.id, i);
    alias.set(node.id, id);
    lines.push(`  ${id}["${escapeLabel(nodeLabel(node))}"]`);
  });

  (graph.edges ?? []).forEach((edge) => {
    const from = alias.get(edge.from);
    const to = alias.get(edge.to);
    if (!from || !to) return;
    const label = edge.edge_type ? `|${escapeLabel(edge.edge_type)}|` : "";
    lines.push(`  ${from} ${edgeLink(edge)}${label} ${to}`);
  });

  (graph.nodes ?? []).forEach((node, i) => {
    const cls = STATUS_CLASS[node.status];
    if (cls) {
      lines.push(`  class ${alias.get(node.id) ?? safeId(node.id, i)} ${cls}`);
    }
  });
  return lines.join("\n");
}

function escapeLabel(label: string): string {
  return label.replace(/["\\\n\r]/g, " ").trim();
}

let diagramCounter = 0;
function nextDiagramId() {
  diagramCounter += 1;
  return `godex-agentgraph-${diagramCounter}`;
}

export function AgentGraphDiagram({ graph, onSelectNode }: { graph: AgentGraphView; onSelectNode?: (node: AgentGraphNode) => void }) {
  const code = useMemo(() => toMermaidSource(graph), [graph]);
  const [svg, setSvg] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  // A2: map mermaid g-element ids back to backend node ids so clicks on the
  // rendered SVG can open the node detail drawer. mermaid ids look like
  // "<diagramId>-<safeNodeId>"; we only need the trailing n_<slug> part.
  const idToNode = useMemo(() => {
    const map = new Map<string, AgentGraphNode>();
    (graph.nodes ?? []).forEach((node, i) => {
      map.set(safeId(node.id, i), node);
    });
    return map;
  }, [graph]);

  const handleSvgClick = (event: React.MouseEvent<HTMLDivElement>) => {
    if (!onSelectNode) return;
    const target = event.target as Element | null;
    const g = target?.closest?.("g");
    if (!g) return;
    const raw = g.getAttribute("id") ?? "";
    // Trailing segment that starts with our safe prefix (n_...).
    const segments = raw.split("-");
    for (let i = segments.length - 1; i >= 0; i--) {
      if (!segments[i].startsWith("n_")) continue;
      const node = idToNode.get(segments[i]);
      if (node) {
        onSelectNode(node);
        return;
      }
    }
  };

  useEffect(() => {
    let cancelled = false;
    setSvg(null);
    setError(null);
    (async () => {
      try {
        const mermaid = (await import("mermaid")).default;
        mermaid.initialize({
          startOnLoad: false,
          securityLevel: "strict",
          theme: "default",
          fontFamily: "inherit",
        });
        const id = nextDiagramId();
        const { svg: rendered } = await mermaid.render(id, code);
        if (!cancelled) setSvg(rendered);
      } catch (err) {
        if (!cancelled) setError(err instanceof Error ? err.message : String(err));
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [code]);

  if ((graph.nodes ?? []).length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No graph nodes" />;
  }
  if (error) {
    return (
      <div data-testid="agentgraph-fallback">
        <Alert type="warning" showIcon message="Graph render failed" description={error} style={{ marginBottom: 8 }} />
        <AgentGraphTable graph={graph} />
      </div>
    );
  }
  if (!svg) {
    return (
      <div style={{ padding: 12, textAlign: "center" }}>
        <Spin size="small" />
        <Typography.Text type="secondary" style={{ marginLeft: 8 }}>
          Rendering graph…
        </Typography.Text>
      </div>
    );
  }
  return (
    <div>
      <div
        data-testid="agentgraph-diagram"
        style={{ overflowX: "auto", maxWidth: "100%", cursor: onSelectNode ? "pointer" : undefined }}
        onClick={handleSvgClick}
        dangerouslySetInnerHTML={{ __html: svg }}
      />
      {onSelectNode ? (
        <Space wrap size={[4, 4]} style={{ marginTop: 8 }}>
          {(graph.nodes ?? []).map((node, i) => (
            <Tag
              key={node.id}
              color={node.status === "failed" ? "red" : node.status === "running" ? "processing" : node.status === "completed" ? "green" : "default"}
              style={{ cursor: "pointer" }}
              onClick={() => onSelectNode(node)}
            >
              {node.title || node.id} · {node.status}
            </Tag>
          ))}
        </Space>
      ) : null}
    </div>
  );
}

function AgentGraphTable({ graph }: { graph: AgentGraphView }) {
  const nodeColumns = [
    { title: "Node", dataIndex: "id", key: "id" },
    { title: "Type", dataIndex: "node_type", key: "node_type", render: (v?: string) => v || "—" },
    { title: "Status", dataIndex: "status", key: "status" },
    { title: "Verdict", dataIndex: "verdict", key: "verdict", render: (v?: string) => v || "—" },
  ];
  const edgeColumns = [
    { title: "Edge", dataIndex: "id", key: "id" },
    { title: "Type", dataIndex: "edge_type", key: "edge_type", render: (v?: string) => v || "—" },
    { title: "From", dataIndex: "from", key: "from" },
    { title: "To", dataIndex: "to", key: "to" },
  ];
  return (
    <div>
      <Typography.Text strong>Nodes</Typography.Text>
      <Table size="small" rowKey="id" pagination={false} dataSource={graph.nodes ?? []} columns={nodeColumns} />
      <Typography.Text strong>Edges</Typography.Text>
      <Table size="small" rowKey="id" pagination={false} dataSource={graph.edges ?? []} columns={edgeColumns} />
    </div>
  );
}
