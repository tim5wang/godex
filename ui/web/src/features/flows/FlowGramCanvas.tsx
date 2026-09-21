import { useEffect, useMemo, useRef, useState } from "react";
import { Alert, Empty, Spin, Space, Tag, Typography } from "antd";
import type { FlowDefinition, FlowRunEvent } from "../../lib/api";

// ---------------------------------------------------------------------------
// FlowGramCanvas
//
// Renders a Flow Spec v1 definition as a mermaid flowchart (design-time view)
// and, when run events are supplied, overlays the run-time node status
// (running / completed / failed / waiting_human) plus decision confidence.
//
// Node material mapping (design doc §9): step/llm/decision/human/branch/loop.
// Mermaid is lazy-imported so it never affects the main bundle.
// ---------------------------------------------------------------------------

const KIND_LABEL: Record<string, string> = {
  step: "step",
  llm: "llm",
  decision: "decision",
  human: "human",
  branch: "branch",
  loop: "loop",
};

const STATUS_CLASS: Record<string, string> = {
  pending: "gx-pending",
  running: "gx-running",
  completed: "gx-completed",
  failed: "gx-failed",
  waiting_human: "gx-blocked",
};
const STATUS_CLASS_FALLBACK = "gx-unknown";

// Events that imply a node entered the given run-time state.
const EVENT_TO_STATUS: Record<string, string> = {
  node_started: "running",
  node_completed: "completed",
  node_failed: "failed",
  node_error: "failed",
  node_retry: "running",
  decision_made: "completed",
  handoff_written: "completed",
  human_task_created: "waiting_human",
  human_task_resolved: "completed",
};

// Node ids in a Flow definition are user-authored (e.g. "decide"); mermaid ids
// must be identifier-safe, so we map them to a stable short alias.
function safeId(raw: string, index: number): string {
  const slug = raw.replace(/[^a-zA-Z0-9_]/g, "_");
  if (/^[a-zA-Z_][a-zA-Z0-9_]*$/.test(slug)) {
    return slug;
  }
  return `f_${index}`;
}

function escapeLabel(label: string): string {
  return label.replace(/["\\\n\r]/g, " ").trim();
}

function edgeLink(edgeType?: string): string {
  switch (edgeType) {
    case "condition":
      return "-.->";
    case "handoff":
      return "==>";
    case "data_dependency":
    default:
      return "-->";
  }
}

function nodeTitle(def: FlowDefinition, id: string): string {
  const n = def.nodes.find((x) => x.id === id);
  if (!n) return id;
  const kind = KIND_LABEL[n.kind] ?? n.kind;
  const title = n.title || n.prompt || id;
  return `${kind}: ${escapeLabel(title).slice(0, 40)}`;
}

/** Derive per-node run status + decision confidence from the event log. */
function statusFromEvents(events: FlowRunEvent[]): Map<string, { status: string; confidence?: number; choice?: string }> {
  const map = new Map<string, { status: string; confidence?: number; choice?: string }>();
  for (const ev of events) {
    if (!ev.node_id) continue;
    const status = EVENT_TO_STATUS[ev.event];
    if (!status) continue;
    const prev = map.get(ev.node_id);
    // A later event can downgrade/upgrade: failure is terminal, completed wins
    // over started, waiting_human is transient.
    if (!prev || status === "failed" || (status === "completed" && prev.status !== "failed")) {
      map.set(ev.node_id, {
        status,
        confidence: typeof ev.confidence === "number" ? ev.confidence : prev?.confidence,
        choice: typeof ev.choice === "string" ? ev.choice : prev?.choice,
      });
    }
  }
  return map;
}

export function toFlowMermaidSource(def: FlowDefinition, events?: FlowRunEvent[]): string {
  const lines: string[] = ["flowchart LR"];
  lines.push("  classDef gx-pending fill:#e8e8e8,stroke:#8c8c8c,color:#555");
  lines.push("  classDef gx-running fill:#e6f4ff,stroke:#1677ff,color:#0958d9");
  lines.push("  classDef gx-completed fill:#f6ffed,stroke:#52c41a,color:#237804");
  lines.push("  classDef gx-failed fill:#fff2f0,stroke:#ff4d4f,color:#cf1322");
  lines.push("  classDef gx-blocked fill:#fff7e6,stroke:#fa8c16,color:#ad4e00");
  lines.push("  classDef gx-unknown fill:#fafafa,stroke:#8c8c8c,color:#595959");

  const statuses = events ? statusFromEvents(events) : new Map<string, { status: string; confidence?: number; choice?: string }>();
  const alias = new Map<string, string>();

  (def.nodes ?? []).forEach((node, i) => {
    const id = safeId(node.id, i);
    alias.set(node.id, id);
    let label = nodeTitle(def, node.id);
    const st = statuses.get(node.id);
    if (st?.choice) {
      label += `\\n→ ${escapeLabel(st.choice)}`;
    }
    if (typeof st?.confidence === "number") {
      label += `\\nconf ${(st.confidence * 100).toFixed(0)}%`;
    }
    lines.push(`  ${id}["${label}"]`);
  });

  (def.edges ?? []).forEach((edge) => {
    const from = alias.get(edge.from);
    const to = alias.get(edge.to);
    if (!from || !to) return;
    let label = "";
    if (edge.edge_type === "condition" && edge.when) {
      label = `|${escapeLabel(conditionLabel(edge.when))}|`;
    } else if (edge.edge_type === "handoff") {
      label = "|handoff|";
    }
    lines.push(`  ${from} ${edgeLink(edge.edge_type)}${label} ${to}`);
  });

  (def.nodes ?? []).forEach((node, i) => {
    const st = statuses.get(node.id);
    const cls = st ? (STATUS_CLASS[st.status] ?? STATUS_CLASS_FALLBACK) : STATUS_CLASS_FALLBACK;
    lines.push(`  class ${alias.get(node.id) ?? safeId(node.id, i)} ${cls}`);
  });
  return lines.join("\n");
}

function conditionLabel(c: { status?: string; verdict?: string; choice?: string; node?: string; confidence?: { op: string; value: number }; output?: { path: string; op: string; value: unknown } }): string {
  if (c.choice) return `choice=${c.choice}`;
  if (c.status) return `status=${c.status}`;
  if (c.verdict) return `verdict=${c.verdict}`;
  if (c.node) return `node=${c.node}`;
  if (c.confidence) return `conf ${c.confidence.op} ${c.confidence.value}`;
  if (c.output) return `${c.output.path} ${c.output.op}`;
  return "when";
}

let diagramCounter = 0;
function nextDiagramId() {
  diagramCounter += 1;
  return `godex-flowgram-${diagramCounter}`;
}

export function FlowGramCanvas({ def, events }: { def: FlowDefinition; events?: FlowRunEvent[] }) {
  const code = useMemo(() => toFlowMermaidSource(def, events), [def, events]);
  const [svg, setSvg] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);
  const renderedCodeRef = useRef<string | null>(null);

  useEffect(() => {
    let cancelled = false;
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
        if (!cancelled) {
          setSvg(rendered);
          renderedCodeRef.current = code;
        }
      } catch (err) {
        if (!cancelled && renderedCodeRef.current === null) {
          setError(err instanceof Error ? err.message : String(err));
        }
      }
    })();
    return () => {
      cancelled = true;
    };
  }, [code]);

  if ((def.nodes ?? []).length === 0) {
    return <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description="No flow nodes" />;
  }
  if (error) {
    return (
      <Alert type="warning" showIcon message="Flow canvas render failed" description={error} style={{ marginBottom: 8 }} />
    );
  }
  if (!svg) {
    return (
      <div style={{ padding: 12, textAlign: "center" }}>
        <Spin size="small" />
        <Typography.Text type="secondary" style={{ marginLeft: 8 }}>
          Rendering canvas…
        </Typography.Text>
      </div>
    );
  }
  return (
    <div>
      <div
        data-testid="flowgram-canvas"
        style={{ overflowX: "auto", maxWidth: "100%" }}
        dangerouslySetInnerHTML={{ __html: svg }}
      />
      {events && events.length > 0 ? (
        <Space wrap size={[4, 4]} style={{ marginTop: 8 }}>
          {(def.nodes ?? []).map((node) => {
            const st = statusFromEvents(events).get(node.id);
            return (
              <Tag
                key={node.id}
                color={st?.status === "failed" ? "red" : st?.status === "running" ? "processing" : st?.status === "completed" ? "green" : st?.status === "waiting_human" ? "orange" : "default"}
              >
                {node.id} · {st?.status ?? "pending"}
              </Tag>
            );
          })}
        </Space>
      ) : null}
    </div>
  );
}
