import { useImperativeHandle, useMemo, useRef, useState, type Ref } from "react";
import { useQuery } from "@tanstack/react-query";
import {
  FreeLayoutEditorProvider,
  EditorRenderer,
  type FreeLayoutProps,
  type FreeLayoutPluginContext,
  type WorkflowNodeRegistry,
  type WorkflowPortEntity,
  type WorkflowLinesManager,
  type WorkflowJSON,
} from "@flowgram.ai/free-layout-editor";
import "@flowgram.ai/free-layout-editor/index.css";
import { Alert, Button, Empty, Input, Modal, Select, Space, Tag, Tooltip, Typography } from "antd";
import {
  ApartmentOutlined,
  DatabaseOutlined,
  EyeOutlined,
  PlusOutlined,
  SaveOutlined,
  SearchOutlined,
} from "@ant-design/icons";
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
  FlowDefProvider,
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
// The Flow Spec node materials are registered
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
  onSaved: (version: string) => void;
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
  const [libSearch, setLibSearch] = useState("");
  const [libFilter, setLibFilter] = useState<string>("all"); // all | builtin | user
  const [libPreview, setLibPreview] = useState<NodeLibraryEntry | null>(null);
  const documentRef = useRef<FreeLayoutPluginContext["document"] | null>(null);
  const toolsRef = useRef<FreeLayoutPluginContext["tools"] | null>(null);

  // Node library (P3): reusable function-node definitions (builtin seeds +
  // user-saved) fetched once per editor mount; clicking an entry inserts it
  // onto the canvas as a function node carrying the entry's spec.
  const libQuery = useQuery({
    queryKey: ["node-library"],
    queryFn: () => listNodeLibrary(token),
  });
  // Scaled browsing: search + source filter over potentially dozens/hundreds
  // of entries; each row offers insert and a preview of the function spec.
  const libEntries = libQuery.data ?? [];
  const filteredLib = useMemo(() => {
    const q = libSearch.trim().toLowerCase();
    return libEntries.filter((e) => {
      if (libFilter !== "all" && e.source !== libFilter) return false;
      if (!q) return true;
      return (
        e.id.toLowerCase().includes(q) ||
        (e.name ?? "").toLowerCase().includes(q) ||
        (e.description ?? "").toLowerCase().includes(q) ||
        (e.tags ?? []).some((tag) => tag.toLowerCase().includes(q))
      );
    });
  }, [libEntries, libSearch, libFilter]);
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
      return workflowToFlowSpec(wf, flowId, nextVersion, "draft", latest);
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
      const def: FlowDefinition = workflowToFlowSpec(wf, flowId, nextVersion, "draft", latest);
      await createFlow(token, {
        flow_id: flowId,
        version: nextVersion,
        status: "draft",
        definition: def,
      });
      onSaved(nextVersion);
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
      // E4: connection constraints — no self-loops, no duplicate edges between
      // the same node pair, and each decision branch (labelled choice port,
      // non-empty portID) accepts exactly ONE outgoing line so the canvas
      // can never disagree with the choice→target routing.
      canAddLine: (_ctx, fromPort, toPort, lines) =>
        canAddWorkflowLine(fromPort, toPort, lines),
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
            maxHeight: 260,
            overflowY: "auto",
            display: "flex",
            flexDirection: "column",
            gap: 8,
          }}
        >
          <Space style={{ justifyContent: "space-between", width: "100%" }} align="center">
            <Text strong style={{ fontSize: 12 }}>
              {t("flows.nodeLibraryPanel")}
            </Text>
            <Tag>{libEntries.length} {t("flows.nodeLibraryCount")}</Tag>
          </Space>
          <Paragraph type="secondary" style={{ fontSize: 11, marginBottom: 0 }}>
            {t("flows.nodeLibraryHint")}
          </Paragraph>
          <Space wrap>
            <Input
              size="small"
              style={{ width: 200 }}
              prefix={<SearchOutlined />}
              placeholder={t("flows.nodeLibrarySearch")}
              value={libSearch}
              onChange={(e) => setLibSearch(e.target.value)}
              allowClear
            />
            <Select
              size="small"
              style={{ width: 110 }}
              value={libFilter}
              onChange={setLibFilter}
              options={[
                { value: "all", label: t("flows.nodeLibraryAll") },
                { value: "builtin", label: "builtin" },
                { value: "user", label: "user" },
              ]}
            />
          </Space>
          {filteredLib.length === 0 ? (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={t("flows.noDefinition")} />
          ) : (
            <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
              {filteredLib.map((entry) => (
                <Space
                  key={entry.id}
                  style={{
                    width: "100%",
                    justifyContent: "space-between",
                    border: "1px solid #eee",
                    borderRadius: 6,
                    padding: "4px 8px",
                    background: "#fff",
                  }}
                >
                  <Space size={6} style={{ minWidth: 0 }}>
                    <Text style={{ fontSize: 12 }} strong>
                      {entry.name}
                    </Text>
                    {entry.source === "builtin" && (
                      <Tag style={{ marginRight: 0 }} color="blue">
                        builtin
                      </Tag>
                    )}
                    {(entry.tags ?? []).slice(0, 3).map((tag) => (
                      <Tag key={tag} style={{ marginRight: 0 }}>
                        {tag}
                      </Tag>
                    ))}
                  </Space>
                  <Space size={4}>
                    <Tooltip title={t("flows.nodeLibraryPreview")}>
                      <Button
                        size="small"
                        type="text"
                        icon={<EyeOutlined />}
                        onClick={() => setLibPreview(entry)}
                      />
                    </Tooltip>
                    <Tooltip title={t("flows.nodeLibraryInsert")}>
                      <Button
                        size="small"
                        type="text"
                        icon={<PlusOutlined />}
                        onClick={() => addLibraryNode(entry)}
                      />
                    </Tooltip>
                  </Space>
                </Space>
              ))}
            </div>
          )}
        </div>
      )}

      <Modal
        title={libPreview ? libPreview.name : ""}
        open={Boolean(libPreview)}
        onCancel={() => setLibPreview(null)}
        footer={
          <Button
            type="primary"
            icon={<PlusOutlined />}
            onClick={() => {
              if (libPreview) addLibraryNode(libPreview);
              setLibPreview(null);
            }}
          >
            {t("flows.nodeLibraryInsert")}
          </Button>
        }
        width={560}
      >
        {libPreview && (
          <div style={{ display: "flex", flexDirection: "column", gap: 8 }}>
            {libPreview.description && (
              <Paragraph type="secondary" style={{ marginBottom: 0, fontSize: 12 }}>
                {libPreview.description}
              </Paragraph>
            )}
            <div>
              <Text strong style={{ fontSize: 12 }}>
                {t("flows.nodeRuntime")}
              </Text>
              <Tag style={{ marginLeft: 6 }}>{libPreview.function.runtime}</Tag>
            </div>
            {libPreview.function.source && (
              <div>
                <Text strong style={{ fontSize: 12 }}>
                  {t("flows.nodeSourceJS")}
                </Text>
                <pre
                  style={{
                    background: "#f5f5f5",
                    padding: 8,
                    borderRadius: 6,
                    fontSize: 11,
                    overflow: "auto",
                    maxHeight: 220,
                    whiteSpace: "pre-wrap",
                    wordBreak: "break-all",
                  }}
                >
                  {libPreview.function.source}
                </pre>
              </div>
            )}
            {libPreview.function.ref && (
              <div>
                <Text strong style={{ fontSize: 12 }}>
                  {t("flows.nodeLibraryRef")}
                </Text>
                <Text code style={{ marginLeft: 6, fontSize: 11 }}>
                  {libPreview.function.ref}
                </Text>
              </div>
            )}
            {libPreview.function.handler && libPreview.function.handler !== "handle" && (
              <div>
                <Text strong style={{ fontSize: 12 }}>
                  {t("flows.nodeHandler")}
                </Text>
                <Text code style={{ marginLeft: 6, fontSize: 11 }}>
                  {libPreview.function.handler}
                </Text>
              </div>
            )}
          </div>
        )}
      </Modal>

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
          <FlowDefProvider def={latest}>
            <RunStatusProvider statuses={runStatusMap}>
              <EditorRenderer className="flowgram-editor" />
            </RunStatusProvider>
          </FlowDefProvider>
        </FreeLayoutEditorProvider>
      </div>

      <Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 0 }}>
        {t("flows.canvasEditorHint")}
      </Paragraph>
      {error && <Alert type="error" showIcon message={error} />}
    </div>
  );
}


/**
 * E4: connection constraints shared by the canvas canAddLine hook —
 *  - no self-loops,
 *  - no duplicate edge between the same node pair,
 *  - each decision branch (labelled choice port, non-empty portID) accepts
 *    exactly ONE outgoing line so the canvas can never disagree with the
 *    choice → target routing (the branch list on the node form is the truth).
 */
function canAddWorkflowLine(
  fromPort: WorkflowPortEntity,
  toPort: WorkflowPortEntity,
  lines: WorkflowLinesManager,
): boolean {
  if (!fromPort || !toPort) return false;
  const fromId = fromPort.node?.id;
  const toId = toPort.node?.id;
  if (!fromId || !toId) return false;
  // No self-loops.
  if (fromId === toId) return false;
  // No duplicate edges between the same node pair.
  for (const line of lines.getAllLines()) {
    const lf = line.fromPort?.node?.id;
    const lt = line.toPort?.node?.id;
    if (lf === fromId && lt === toId) return false;
  }
  // Decision branch ports (portID non-empty): one outgoing line per branch.
  if (
    fromPort.portID !== "" &&
    fromPort.portID != null &&
    fromPort.lines.length > 0
  ) {
    return false;
  }
  return true;
}
