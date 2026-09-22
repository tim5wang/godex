import { useMemo, useRef, useState } from "react";
import {
  FreeLayoutEditorProvider,
  EditorRenderer,
  type FreeLayoutProps,
  type FreeLayoutPluginContext,
  type WorkflowNodeRegistry,
  type WorkflowJSON,
} from "@flowgram.ai/free-layout-editor";
import "@flowgram.ai/free-layout-editor/index.css";
import { Alert, Button, Space, Tag, Typography } from "antd";
import { PlusOutlined, SaveOutlined } from "@ant-design/icons";
import { createFlow, type FlowDefinition, type FlowVersionView } from "../../lib/api";
import { flowSpecToWorkflow, workflowToFlowSpec, blankFlowNode } from "./flowgramAdapter";
import { FLOWGRAM_NODE_REGISTRIES, FlowGramBaseNode, KIND_COLOR } from "./flowgramNodes";

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

interface FlowGramFlowEditorProps {
  flowId: string;
  token: string | null;
  t: (k: string, v?: Record<string, string | number>) => string;
  versions: FlowVersionView[];
  onSaved: () => void;
  onSaveError: (err: unknown) => void;
}

export function FlowGramFlowEditor({
  flowId,
  token,
  t,
  versions,
  onSaved,
  onSaveError,
}: FlowGramFlowEditorProps) {
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const documentRef = useRef<FreeLayoutPluginContext["document"] | null>(null);

  // Latest definition with content (versions are ascending; reverse = latest).
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

  // Initial data for the editor; keyed by version so switching flows/versions
  // rebuilds the canvas (flowgram re-imports initialData on remount).
  const initialWorkflow = useMemo<WorkflowJSON>(
    () => (latest ? flowSpecToWorkflow(latest) : { nodes: [], edges: [] }),
    [latest],
  );
  const canvasKey = latest ? `${latest.flow_id}-${latest.version}` : "empty";

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
      // Keep the document handle for programmatic add/save.
      onInit: (ctx) => {
        documentRef.current = ctx.document;
      },
      // Auto-layout the first render so newly imported definitions are readable.
      onAllLayersRendered: (ctx) => {
        ctx.tools.fitView?.(false);
      },
      onContentChange: () => {
        // no-op: the save path reads document.toJSON() directly, so we do not
        // need a live JSON mirror (avoids editor remount churn).
      },
    }),
    [initialWorkflow],
  );

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
          <EditorRenderer className="flowgram-editor" />
        </FreeLayoutEditorProvider>
      </div>

      <Paragraph type="secondary" style={{ fontSize: 12, marginBottom: 0 }}>
        {t("flows.canvasEditorHint")}
      </Paragraph>
      {error && <Alert type="error" showIcon message={error} />}
    </div>
  );
}
