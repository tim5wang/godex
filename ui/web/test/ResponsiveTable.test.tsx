import { describe, expect, it } from "vitest";
import { Button } from "antd";
import type { TableColumnsType } from "antd";
import { cardFields } from "../src/components/ResponsiveTable";

interface Row {
  id: string;
  name: string;
  tokens: number;
  nested?: { value?: string };
}

const columns: TableColumnsType<Row> = [
  { title: "Name", dataIndex: "name", key: "name" },
  { title: "Tokens", dataIndex: "tokens", key: "tokens", render: (v: number) => v.toLocaleString() },
  {
    title: "",
    key: "actions",
    render: (_: unknown, r: Row) => <Button>edit {r.id}</Button>,
  },
];

describe("cardFields", () => {
  it("skips the action column and renders every data column", () => {
    const fields = cardFields(columns, { id: "a", name: "alpha", tokens: 1200 }, 0);
    expect(fields.map((f) => f.key)).toEqual(["name", "tokens"]);
    expect(fields[0].label).toBe("Name");
    expect(fields[0].value).toBe("alpha");
  });

  it("applies column render functions", () => {
    const fields = cardFields(columns, { id: "a", name: "alpha", tokens: 1200 }, 0);
    expect(fields[1].value).toBe("1,200");
  });

  it("falls back to '-' for null/undefined values", () => {
    const fields = cardFields(
      [{ title: "Name", dataIndex: "name", key: "name" }],
      { id: "c", name: null as unknown as string, tokens: 5 },
      0,
    );
    expect(fields[0].value).toBe("-");
  });

  it("resolves nested dataIndex paths", () => {
    const fields = cardFields(
      [{ title: "Nested", dataIndex: ["nested", "value"], key: "nested" }],
      { id: "d", name: "delta", tokens: 1, nested: { value: "deep" } },
      0,
    );
    expect(fields[0].value).toBe("deep");
  });
});
