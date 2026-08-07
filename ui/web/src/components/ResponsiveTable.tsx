import { useLayoutEffect, useState, type ReactNode } from "react";
import { Card, Empty, Pagination, Spin, Table } from "antd";
import type { TableColumnType, TableColumnsType, TableProps } from "antd";

// Matches the responsive CSS breakpoint in styles.css and chatV2Store
// (max-width: 900px). Below this width tables render as a card grid so the
// usage panels stay usable on phones and small tablets.
const NARROW_VIEWPORT_QUERY = "(max-width: 900px)";

export function useIsNarrow(): boolean {
  const [narrow, setNarrow] = useState<boolean>(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return false;
    return window.matchMedia(NARROW_VIEWPORT_QUERY).matches;
  });
  useLayoutEffect(() => {
    if (typeof window === "undefined" || typeof window.matchMedia !== "function") return;
    const mq = window.matchMedia(NARROW_VIEWPORT_QUERY);
    const onChange = (e: MediaQueryListEvent) => setNarrow(e.matches);
    mq.addEventListener("change", onChange);
    setNarrow(mq.matches);
    return () => mq.removeEventListener("change", onChange);
  }, []);
  return narrow;
}

type RowKey<RecordType extends object> = TableProps<RecordType>["rowKey"];

function recordValue<RecordType extends object>(record: RecordType, dataIndex: unknown): unknown {
  if (typeof dataIndex === "string") {
    return (record as Record<string, unknown>)[dataIndex];
  }
  if (Array.isArray(dataIndex)) {
    let value: unknown = record;
    for (const key of dataIndex) {
      if (value == null) return undefined;
      value = (value as Record<string, unknown>)[key];
    }
    return value;
  }
  return undefined;
}

interface CardListProps<RecordType extends object> {
  columns: TableColumnsType<RecordType>;
  dataSource: readonly RecordType[];
  rowKey: RowKey<RecordType>;
  cardTitle?: (record: RecordType) => ReactNode;
}

export interface CardField<RecordType extends object> {
  key: string;
  label: ReactNode;
  value: ReactNode;
}

/**
 * Derives the card field list (label + rendered value) from table columns for
 * one record. Column groups and the action column (key "actions") are skipped;
 * the action column renders into the card header instead.
 */
export function cardFields<RecordType extends object>(
  columns: TableColumnsType<RecordType>,
  record: RecordType,
  index: number,
): CardField<RecordType>[] {
  return columns
    .filter((col): col is TableColumnType<RecordType> => "dataIndex" in col && col.key !== "actions")
    .map((col, colIndex) => {
      const label: ReactNode = typeof col.title === "function" ? "—" : (col.title as ReactNode);
      const raw = recordValue(record, col.dataIndex);
      const rendered = col.render ? col.render(raw, record, index) : raw;
      return {
        key: String(col.key ?? colIndex),
        label,
        value: rendered == null ? "-" : (rendered as ReactNode),
      };
    });
}

function CardList<RecordType extends object>({ columns, dataSource, rowKey, cardTitle }: CardListProps<RecordType>) {
  const actionColumn = columns.find((col) => col.key === "actions");
  return (
    <div
      style={{
        display: "grid",
        gridTemplateColumns: "repeat(auto-fill, minmax(240px, 1fr))",
        gap: 12,
      }}
    >
      {dataSource.map((record, index) => {
        const rawKey = typeof rowKey === "function" ? rowKey(record) : (record as Record<string, unknown>)[String(rowKey)];
        const key = (rawKey as React.Key) ?? index;
        const actions = actionColumn?.render ? (actionColumn.render(undefined, record, index) as ReactNode) : undefined;
        return (
          <Card key={key} size="small" title={cardTitle?.(record)} extra={actions}>
            {cardFields(columns, record, index).map((field) => (
              <div
                key={field.key}
                style={{ display: "flex", gap: 8, marginBottom: 8, alignItems: "flex-start" }}
              >
                <span style={{ width: 96, flexShrink: 0, fontSize: 12, color: "var(--godex-muted)", paddingTop: 2 }}>
                  {field.label}
                </span>
                <span style={{ flex: 1, fontSize: 13, wordBreak: "break-word", minWidth: 0 }}>
                  {field.value}
                </span>
              </div>
            ))}
          </Card>
        );
      })}
    </div>
  );
}

export interface ResponsiveTableProps<RecordType extends object> {
  columns: TableColumnsType<RecordType>;
  dataSource: readonly RecordType[];
  rowKey: RowKey<RecordType>;
  loading?: boolean;
  size?: TableProps<RecordType>["size"];
  pagination?: TableProps<RecordType>["pagination"];
  cardTitle?: (record: RecordType) => ReactNode;
}

export function ResponsiveTable<RecordType extends object>({
  columns,
  dataSource,
  rowKey,
  loading,
  size,
  pagination,
  cardTitle,
}: ResponsiveTableProps<RecordType>) {
  const narrow = useIsNarrow();

  if (!narrow) {
    return <Table<RecordType> columns={columns} dataSource={dataSource} rowKey={rowKey} loading={loading} size={size} pagination={pagination} />;
  }

  const isEmpty = !loading && dataSource.length === 0;
  const pager =
    pagination && typeof pagination === "object"
      ? {
          current: pagination.current,
          pageSize: pagination.pageSize,
          total: pagination.total,
          showSizeChanger: pagination.showSizeChanger,
          pageSizeOptions: pagination.pageSizeOptions,
          onChange: pagination.onChange,
        }
      : undefined;

  return (
    <div>
      <Spin spinning={!!loading}>
        {isEmpty ? (
          <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
        ) : (
          <CardList columns={columns} dataSource={dataSource} rowKey={rowKey} cardTitle={cardTitle} />        )}
      </Spin>
      {pager && (
        <div style={{ display: "flex", justifyContent: "flex-end", marginTop: 16 }}>
          <Pagination {...pager} />
        </div>
      )}
    </div>
  );
}
