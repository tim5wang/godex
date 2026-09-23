import { Button, Input, Select, Space, Tag, Typography } from "antd";
import { DeleteOutlined, PlusOutlined } from "@ant-design/icons";

const { Text } = Typography;

/** One JSON-Schema-ish node: type + optional nested properties/items. */
export interface SchemaNode {
  type?: string;
  description?: string;
  properties?: Record<string, SchemaNode>;
  items?: SchemaNode;
  [k: string]: unknown;
}

const TYPES = ["string", "number", "boolean", "object", "array", "any"];

function TypeSelect({
  value,
  onChange,
  size = "small",
}: {
  value: string;
  onChange: (v: string) => void;
  size?: "small" | "middle";
}) {
  return (
    <Select
      size={size}
      style={{ width: 90 }}
      value={value}
      onChange={onChange}
      options={TYPES.map((v) => ({ value: v, label: v }))}
    />
  );
}

/**
 * Lightweight recursive JSON-Schema editor built on antd (no new dependency):
 *
 *   object → editable `properties` (each property name is the field path),
 *   array  → editable `items`,
 *   scalar → just the type select (+ optional description).
 *
 * The outermost node's name is the variable name (edited in the row above);
 * every nested property name is its JSON path within the variable.
 */
export function SchemaTreeEditor(props: {
  value?: SchemaNode | null;
  onChange: (next: SchemaNode | undefined) => void;
  depth?: number;
}) {
  const { value, onChange, depth = 0 } = props;
  const node: SchemaNode = value ?? { type: "object" };
  const type = (node.type as string) || "object";

  const patch = (p: Partial<SchemaNode>) => onChange({ ...node, ...p });

  return (
    <div
      style={{
        display: "flex",
        flexDirection: "column",
        gap: 4,
        paddingLeft: depth > 0 ? 10 : 0,
        borderLeft: depth > 0 ? "1px solid #e8e8e8" : "none",
      }}
    >
      <Space size={4} style={{ width: "100%" }}>
        {depth > 0 && (
          <Tag style={{ marginRight: 0, fontSize: 10 }} color="default">
            field
          </Tag>
        )}
        <TypeSelect
          value={type}
          onChange={(t) => {
            // Keep properties/items data across type switches (only the
            // rendered branch changes), so toggling never destroys work.
            patch({ type: t });
          }}
        />
        <Input
          size="small"
          style={{ flex: 1, minWidth: 0, fontSize: 11 }}
          placeholder="description"
          value={node.description ?? ""}
          onChange={(e) => patch({ description: e.target.value })}
        />
      </Space>

      {type === "object" && (
        <ObjectProperties node={node} onChange={onChange} depth={depth + 1} />
      )}
      {type === "array" && (
        <ArrayItems node={node} onChange={onChange} depth={depth + 1} />
      )}
    </div>
  );
}

function ObjectProperties({
  node,
  onChange,
  depth,
}: {
  node: SchemaNode;
  onChange: (next: SchemaNode | undefined) => void;
  depth: number;
}) {
  const props = node.properties ?? {};
  const keys = Object.keys(props);
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
      <Text type="secondary" style={{ fontSize: 10 }}>
        properties
      </Text>
      {keys.length === 0 && (
        <Text type="secondary" style={{ fontSize: 10, fontStyle: "italic" }}>
          — no properties —
        </Text>
      )}
      {keys.map((k) => {
        const child = props[k];
        return (
          <div
            key={k}
            style={{
              display: "flex",
              flexDirection: "column",
              gap: 2,
              border: "1px solid #f0f0f0",
              borderRadius: 4,
              padding: 4,
              background: "#fff",
            }}
          >
            <Space size={4} style={{ width: "100%" }}>
              <Input
                size="small"
                style={{ width: 120, fontFamily: "monospace", fontSize: 11 }}
                value={k}
                placeholder="field name"
                onChange={(e) => {
                  const renamed = e.target.value;
                  const next: Record<string, SchemaNode> = {};
                  for (const [oldK, v] of Object.entries(props)) {
                    next[oldK === k ? renamed : oldK] = v;
                  }
                  onChange({ ...node, properties: next });
                }}
              />
              <Text style={{ fontSize: 10, color: "#999" }}>
                {typeof child?.type === "string" ? child.type : "object"}
              </Text>
              <Button
                size="small"
                type="text"
                danger
                icon={<DeleteOutlined />}
                onClick={() => {
                  const next = { ...props };
                  delete next[k];
                  onChange({ ...node, properties: next });
                }}
              />
            </Space>
            <SchemaTreeEditor
              value={child}
              onChange={(v) => {
                const next = { ...props };
                if (v === undefined) {
                  delete next[k];
                } else {
                  next[k] = v;
                }
                onChange({ ...node, properties: next });
              }}
              depth={depth}
            />
          </div>
        );
      })}
      <Button
        size="small"
        type="dashed"
        icon={<PlusOutlined />}
        style={{ alignSelf: "flex-start" }}
        onClick={() =>
          onChange({
            ...node,
            properties: { ...props, [`field${keys.length + 1}`]: { type: "string" } },
          })
        }
      >
        Add property
      </Button>
    </div>
  );
}

function ArrayItems({
  node,
  onChange,
  depth,
}: {
  node: SchemaNode;
  onChange: (next: SchemaNode | undefined) => void;
  depth: number;
}) {
  return (
    <div style={{ display: "flex", flexDirection: "column", gap: 4 }}>
      <Text type="secondary" style={{ fontSize: 10 }}>
        items
      </Text>
      <div
        style={{
          border: "1px solid #f0f0f0",
          borderRadius: 4,
          padding: 4,
          background: "#fff",
        }}
      >
        <SchemaTreeEditor value={node.items} onChange={(v) => onChange({ ...node, items: v })} depth={depth} />
      </div>
    </div>
  );
}
