import { useLayoutEffect, useState, type Key, type ReactNode } from "react";
import { Card, Checkbox, Empty, Pagination, Spin, Table } from "antd";
import type { TableColumnType, TableColumnsType, TablePaginationConfig, TableProps } from "antd";

// Matches the responsive CSS breakpoint in styles.css and layout store
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

/** Normalize a rowKey prop to a string key for a record (falls back to index). */
function recordKey<RecordType extends object>(rowKey: RowKey<RecordType>, record: RecordType, index: number): Key {
  const raw = typeof rowKey === "function" ? rowKey(record) : (record as Record<string, unknown>)[String(rowKey)];
  return (raw as Key) ?? index;
}

/**
 * Field key for a card: explicit col.key first, then the first dataIndex
 * segment, then the column index — so render-only columns (no dataIndex)
 * still get a stable, meaningful key.
 */
function fieldKey<RecordType extends object>(col: TableColumnType<RecordType>, colIndex: number): string {
  if (col.key != null) return String(col.key);
  const di = col.dataIndex;
  if (typeof di === "string") return di;
  if (Array.isArray(di) && di.length > 0) return String(di[0]);
  return String(colIndex);
}

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
  rowSelection?: TableProps<RecordType>["rowSelection"];
}

export interface CardField<RecordType extends object> {
  key: string;
  label: ReactNode;
  value: ReactNode;
}

/**
 * Derives the card field list (label + rendered value) from table columns for
 * one record. Action columns (key "actions") are skipped — their render output
 * goes into the card header via `extra` instead. Render-only columns (no
 * dataIndex) use the column title as label and render with `undefined` raw
 * value, which matches how antd calls `render` for such columns.
 */
export function cardFields<RecordType extends object>(
  columns: TableColumnsType<RecordType>,
  record: RecordType,
  index: number,
): CardField<RecordType>[] {
  return columns
    .filter((col): col is TableColumnType<RecordType> => "render" in col || "dataIndex" in col)
    .filter((col) => col.key !== "actions")
    .map((col, colIndex) => {
      const label: ReactNode = typeof col.title === "function" ? "—" : (col.title as ReactNode);
      const raw = "dataIndex" in col ? recordValue(record, col.dataIndex) : undefined;
      const rendered = col.render ? col.render(raw, record, index) : raw;
      return {
        key: fieldKey(col, colIndex),
        label,
        value: rendered == null ? "-" : (rendered as ReactNode),
      };
    });
}

function CardList<RecordType extends object>({ columns, dataSource, rowKey, cardTitle, rowSelection }: CardListProps<RecordType>) {
  const actionColumn = columns.find((col) => col.key === "actions");
  const selectedKeys = rowSelection?.selectedRowKeys ?? [];
  const selectionType = rowSelection?.type ?? "checkbox";
  return (
    <div
      style={{
        display: "grid",
        gridTemplateColumns: "repeat(auto-fill, minmax(240px, 1fr))",
        gap: 12,
      }}
    >
      {dataSource.map((record, index) => {
        const key = recordKey(rowKey, record, index);
        const actions = actionColumn?.render ? (actionColumn.render(undefined, record, index) as ReactNode) : undefined;
        const checked = selectedKeys.includes(key);
        const toggle = () => {
          const next = checked ? selectedKeys.filter((k) => k !== key) : [...selectedKeys, key];
          const rows = dataSource.filter((_, i) => next.includes(recordKey(rowKey, dataSource[i], i)));
          rowSelection?.onChange?.(next, rows as RecordType[], { type: checked ? "none" : "single" });
        };
        return (
          <Card
            key={key}
            size="small"
            title={
              <span style={{ display: "inline-flex", alignItems: "center", gap: 8 }}>
                {rowSelection ? (
                  <Checkbox
                    checked={checked}
                    onChange={toggle}
                    aria-label={`Select row ${String(key)}`}
                  />
                ) : null}
                {cardTitle?.(record)}
              </span>
            }
            extra={actions}
          >
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
  rowSelection?: TableProps<RecordType>["rowSelection"];
  locale?: TableProps<RecordType>["locale"];
  className?: string;
}

/**
 * Table that renders as a normal antd Table on wide screens and as a card
 * grid on narrow viewports (<=900px). All columns keep working in card mode:
 * render-only columns render with `undefined` raw value (same as antd), and
 * the column with key "actions" moves into the card header.
 */
export function ResponsiveTable<RecordType extends object>({
  columns,
  dataSource,
  rowKey,
  loading,
  size,
  pagination,
  cardTitle,
  rowSelection,
  locale,
  className,
}: ResponsiveTableProps<RecordType>) {
  const narrow = useIsNarrow();

  if (!narrow) {
    return (
      <Table<RecordType>
        columns={columns}
        dataSource={dataSource}
        rowKey={rowKey}
        loading={loading}
        size={size}
        pagination={pagination}
        rowSelection={rowSelection}
        locale={locale}
        className={className}
      />
    );
  }

  const isEmpty = !loading && dataSource.length === 0;
  const plainPagination = pagination == null || pagination === false ? false : pagination;
  const pager =
    plainPagination && typeof plainPagination === "object"
      ? {
          current: plainPagination.current,
          pageSize: plainPagination.pageSize,
          total: plainPagination.total ?? dataSource.length,
          showSizeChanger: plainPagination.showSizeChanger,
          pageSizeOptions: plainPagination.pageSizeOptions,
          size: plainPagination.size,
          onChange: plainPagination.onChange,
        }
      : undefined;

  const current = pager?.current ?? 1;
  const pageSize = pager?.pageSize ?? 10;
  const rowData = pager ? dataSource.slice((current - 1) * pageSize, current * pageSize) : dataSource;

  const emptyText = typeof locale?.emptyText === "function" ? locale.emptyText() : locale?.emptyText;
  return (
    <div className={className}>
      <Spin spinning={!!loading}>
        {isEmpty ? (
          emptyText != null ? (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={emptyText} />
          ) : (
            <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
          )
        ) : (
          <CardList columns={columns} dataSource={rowData} rowKey={rowKey} cardTitle={cardTitle} rowSelection={rowSelection} />
        )}
      </Spin>
      {pager && (
        <div style={{ display: "flex", justifyContent: "flex-end", marginTop: 16 }}>
          <Pagination {...pager} />
        </div>
      )}
    </div>
  );
}
