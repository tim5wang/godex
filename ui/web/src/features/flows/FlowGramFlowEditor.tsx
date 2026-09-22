import { useImperativeHandle, useMemo, useRef, useState, type Ref } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  FreeLayoutEditorProvider,
  EditorRenderer,
  type FreeLayoutProps,
  type FreeLayoutPluginContext,
  type WorkflowNodeRegistry,
  type WorkflowJSON,
} from "@flowgram.ai/free-layout-editor";
import "@flowgram.ai/free-layout-editor/index.css";
import { Alert, Button, Empty, Space, Tag, Typography } from "antd";
import { ApartmentOutlined, DatabaseOutlined, PlusOutlined, SaveOutlined } from "@ant-design/icons";
import {
  createFlow,
  listNodeLibrary,
  type FlowDefinition,
  type FlowRunEvent,
  type FlowVersionView,
  type NodeLibraryEntry,
} from "../../lib/api";
import { flowSpecToWorkflow, workflowToFlowSpec, blankFlowNode } from "./flowgramAdapter";
import {
  FLOWGRAM_NODE_REGISTRIES,
  FlowGramBaseNode,
  KIND_COLOR,
  RunStatusProvider,
} from "./flowgramNodes";
import { statusFromEvents } from "./FlowGramCanvas";

const { Text, Paragraph } = Typography;

// ---------------------------------------------------------------------------
// FlowGramFlowEditor — Flow Spec v1 editable canvas on bytedance/flowgram.ai
// (free-layout-editor, the Coze-class AI-native flow editor).
//
// Replaces the previous self-built cytoscape canvas (FlowGramEditor.tsx).
// The six node materials (step/llm/decision/human/branch/loop) are registered
// with built-in forms; nodes and edges round-trip through the adapter.
//
// Save persists the definition as a NEW version via createFlow.
// ---------------------------------------------------------------------------

export interface FlowGramFlowEditorHandle {
  /** Snapshot the current canvas as a Flow Spec v1 definition (no save). */
  getCurrentDefinition: () => FlowDefinition | null;
}

interface FlowGramFlowEditorProps {
  flowId: string;
  token: string | null;
  t: (k: string, v?: Record<string, string | number>) => string;
  versions: FlowVersionView[];
  onSaved: () => void;
  onSaveError: (err: unknown) => void;
  /** External definition pushed from the JSON editor; stamp forces a remount. */
  externalDef?: { def: FlowDefinition; stamp: number };
  /** Run-time events for canvas highlight (debug mode); empty = no highlight. */
  runEvents?: FlowRunEvent[];
  ref?: Ref<FlowGramFlowEditorHandle>;
}

export function FlowGramFlowEditor({
  flowId,
  token,
  t,
  versions,
  onSaved,
  onSaveError,
  externalDef,
  runEvents,
  ref,
}: FlowGramFlowEditorProps) {
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [layouting, setLayouting] = useState(false);
  const [libOpen, setLibOpen] = useState(false);
  const documentRef = useRef<FreeLayoutPluginContext["document"] | null>(null);
  const toolsRef = useRef<FreeLayoutPluginContext["tools"] | null>(null);

  // Node library (P3): reusable function-node definitions (builtin seeds +
  // user-saved) fetched once per editor mount; clicking an entry inserts it
  // onto the canvas as a function node carrying the entry's spec.
  const libQuery = useQuery({
    queryKey: ["node-library"],
    queryFn: () => listNodeLibrary(token),
  });
  // Run the automatic topology layout at most once per editor mount, so
  // later onAllLayersRendered firings (addNode / content change) never
  // re-arrange nodes the user already dragged by hand.
  const autoLaidOutRef = useRef(false);

  // Latest definition with content (versions are ascending; reverse = latest).
  // An externally applied JSON definition (JSON editor → canvas) wins over
  // the stored versions until the next save clears it.
  const latest = useMemo(
    () =>
      externalDef?.def ?? [...versions].reverse().find((v) => v.definition)?.definition,
    [versions, externalDef],
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

  // Initial data for the editor; keyed by version so switching flows/versions
  // rebuilds the canvas (flowgram re-imports initialData on remount).
  const initialWorkflow = useMemo<WorkflowJSON>(
    () => (latest ? flowSpecToWorkflow(latest) : { nodes: [], edges: [] }),
    [latest],
  );
  const canvasKey = externalDef
    ? `ext-${externalDef.stamp}`
    : latest
      ? `${latest.flow_id}-${latest.version}`
      : "empty";

  // Whether the definition carries saved canvas positions. When absent
  // (template / fresh flows get a flat cascade fallback), run a topology
  // auto-layout once on load; when present, respect the saved manual layout.
  const hasStoredLayout = useMemo(
    () => !!latest && (latest.nodes ?? []).some((n) => n.canvas_pos),
    [latest],
  );

  // Expose a snapshot handle for the JSON editor (get from canvas).
  useImperativeHandle(ref, () => ({
    getCurrentDefinition: () => {
      const doc = documentRef.current as unknown as { toJSON?: () => WorkflowJSON } | null;
      const wf = doc?.toJSON?.() ?? initialWorkflow;
      return workflowToFlowSpec(wf, flowId, nextVersion, "draft");
    },
  }));

  // Programmatic node creation (the canvas also has its own add UX; this is
  // the toolbar fallback). Mirrors the previous editor's toolbar buttons.
  const addNode = (kind: string) => {
    const doc = documentRef.current as unknown as {
      createWorkflowNodeByType?: (
        type: string,
        position?: { x: number; y: number },
        json?: Record<string, unknown>,
      ) => unknown;
    };
    if (typeof doc?.createWorkflowNodeByType !== "function") return;
    const id = `n${Date.now().toString(36).slice(-4)}`;
    doc.createWorkflowNodeByType(kind, { x: 80 + Math.random() * 200, y: 120 }, {
      id,
      data: { ...blankFlowNode(id, kind), kind },
    });
  };

  // Insert a node-library entry onto the canvas as a function node whose
  // function spec comes from the saved entry (runtime/source/ref/handler).
  const addLibraryNode = (entry: NodeLibraryEntry) => {
    const doc = documentRef.current as unknown as {
      createWorkflowNodeByType?: (
        type: string,
        position?: { x: number; y: number },
        json?: Record<string, unknown>,
      ) => unknown;
    };
    if (typeof doc?.createWorkflowNodeByType !== "function") return;
    const id = `n${Date.now().toString(36).slice(-4)}`;
    const node = blankFlowNode(id, "function");
    node.function = { ...entry.function };
    node.title = entry.name;
    doc.createWorkflowNodeByType("function", { x: 80 + Math.random() * 200, y: 120 }, {
      id,
      data: { ...node, kind: "function" },
    });
  };

  const save = async () => {
    const doc = documentRef.current as unknown as { toJSON?: () => WorkflowJSON } | null;
    const wf = doc?.toJSON?.() ?? initialWorkflow;
    setSaving(true);
    setError(null);
    try {
      const def: FlowDefinition = workflowToFlowSpec(wf, flowId, nextVersion, "draft");
      await createFlow(token, {
        flow_id: flowId,
        version: nextVersion,
        status: "draft",
        definition: def,
      });
      onSaved();
    } catch (err) {
      setError(err instanceof Error ? err.message : String(err));
      onSaveError(err);
    } finally {
      setSaving(false);
    }
  };

  // Topology auto-layout (dagre, LR): arranges nodes by their connection
  // graph instead of a flat cascade — fixes "template nodes are lined up
  // regardless of the edge topology".
  const runAutoLayout = async () => {
    const tools = toolsRef.current;
    if (!tools) return;
    setLayouting(true);
    try {
      await tools.autoLayout({
        enableAnimation: true,
        animationDuration: 600,
        layoutConfig: { rankdir: "LR", nodesep: 100, ranksep: 100 },
      });
    } finally {
      setLayouting(false);
    }
  };

  // Editor props: wired like the official demo (fixed-layout sample), but
  // without the heavy plugin set (minimap/panel/etc.) for a lean integration.
  const editorProps = useMemo<FreeLayoutProps>(
    () => ({
      background: true,
      readonly: false,
      twoWayConnection: true,
      initialData: initialWorkflow,
      nodeRegistries: FLOWGRAM_NODE_REGISTRIES as WorkflowNodeRegistry[],
      getNodeDefaultRegistry: (type) => ({
        type,
        meta: { defaultExpanded: true, size: { width: 300, height: 120 } },
        formMeta: undefined,
      }),
      materials: {
        renderDefaultNode: FlowGramBaseNode,
      },
      nodeEngine: { enable: true },
      variableEngine: { enable: false },
      history: { enable: true, enableChangeNode: true },
      lineColor: {
        hidden: "transparent",
        default: "#4d53e8",
        drawing: "#5DD6E3",
        hovered: "#37d0ff",
        selected: "#37d0ff",
        error: "red",
        flowing: "#4d53e8",
      },
      // Bidirectional adapter hooks: keep Flow Spec fields alive.
      fromNodeJSON: (_node, json) => json,
      toNodeJSON: (_node, json) => json,
      // Keep the document/tools handles for programmatic add / layout / save.
      onInit: (ctx) => {
        documentRef.current = ctx.document;
        toolsRef.current = ctx.tools;
      },
      // Auto-layout only when the definition has no saved canvas positions
      // (template / fresh flows with the flat cascade fallback) — topology-
      // aware dagre LR instead of the unreadable node list. Saved manual
      // layouts are respected untouched.
      onAllLayersRendered: async (ctx) => {
        toolsRef.current = ctx.tools;
        if (
          !autoLaidOutRef.current &&
          !hasStoredLayout &&
          ctx.document.getAllNodes().length > 1
        ) {
          autoLaidOutRef.current = true;
          await ctx.tools
            .autoLayout({
              enableAnimation: false,
              layoutConfig: { rankdir: "LR", nodesep: 100, ranksep: 100 },
            })
            .catch(() => undefined);
        }
        await ctx.tools.fitView?.(false).catch(() => undefined);
      },
      onContentChange: () => {
        // no-op: the save path reads document.toJSON() directly, so we do not
        // need a live JSON mirror (avoids editor remount churn).
      },
    }),
    [initialWorkflow, hasStoredLayout],
  );

  // Per-node run status map derived from the debug run's event log; fed to
  // the nodes via RunStatusProvider so borders highlight execution state.
  const runStatusMap = useMemo(() => {
    const full = statusFromEvents(runEvents ?? []);
    const out = new Map<string, string>();
    full.forEach((v, k) => out.set(k, v.status));
    return out;
  }, [runEvents]);

  const nodeCount = initialWorkflow.nodes.length;

  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 8, height: "100%" }}>
      <Space style={{ justifyContent: "space-between", width: "100%" }} align="center" wrap>
        <Space wrap size={[4, 4]}>
          {Object.entries(KIND_COLOR).map(([kind, color]) => (
            <Button key={kind} size="small" icon={<PlusOutlined />} onClick={() => addNode(kind)}>
              <Tag color={color} style={{ marginRight: 0 }}>
                {kind}
              </Tag>
            </Button>
          ))}
        </Space>
        <Space>
          <Button
            size="small"
            icon={<DatabaseOutlined />}
            type={libOpen ? "primary" : "default"}
            onClick={() => setLibOpen((v) => !v)}
          >
            {t("flows.nodeLibrary")}
          </Button>
          <Button
            size="small"
            icon={<ApartmentOutlined />}
            loading={layouting}
            onClick={runAutoLayout}
          >
            {t("flows.autoLayout")}
          </Button>
          <Text type="secondary" style={{ fontSize: 12 }}>
            {nodeCount} nodes · save as v{nextVersion}
          </Text>
          <Button
            type="primary"
            size="small"
            icon={<SaveOutlined />}
            loading={saving}
            onClick={save}
          >
            {t("flows.saveVersion")} {nextVersion}
          </Button>
        </Space>
      </Space>

      {libOpen && (
        <div
          style={{
            border: "1px solid #e5e5e5",
            borderRadius: 8,
            padding: 10,
            background: "#fafafa",
            maxHeight: 180,
            overflowY: "auto",
          }}
        >
          <Text strong style={{ fontSize: 12 }}>
            {t("flows.nodeLibraryPanel")}
          </Text>
          <Paragraph type="secondary" style={{ fontSize: 11, marginBottom: 8 }}>
            {t("flows.nodeLibraryHint")}
          </Paragraph>
          {(libQuery.data ?? []).length === 0 ? (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("flows.noDefinition")} />
          ) : (
            <Space wrap size={[4, 4]}>
              {(libQuery.data ?? []).map((entry) => (
                <Button
                  key={entry.id}
                  size="small"
                  icon={<PlusOutlined />}
                  title={entry.description}
                  onClick={() => addLibraryNode(entry)}
                >
                  {entry.name}
                  {entry.source === "builtin" && (
                    <Tag style={{ marginLeft: 4, marginRight: 0 }} color="blue">
                      builtin
                    </Tag>
                  )}
                </Button>
              ))}
            </Space>
          )}
        </div>
      )}

      <div
        style={{
          flex: 1,
          minHeight: 420,
          border: "1px solid #e5e5e5",
          borderRadius: 8,
          overflow: "hidden",
          position: "relative",
          background: "#f2f3f5",
        }}
      >
        <FreeLayoutEditorProvider key={canvasKey} {...editorProps}>
          <RunStatusProvider statuses={runStatusMap}>
            <EditorRenderer className="flowgram-editor" />
          </RunStatusProvider>
        </FreeLayoutEditorProvider>
      </div>

      <Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 0 }}>
        {t("flows.canvasEditorHint")}
      </Paragraph>
      {error && <Alert type="error" showIcon message={error} />}
    </div>
  );
}
