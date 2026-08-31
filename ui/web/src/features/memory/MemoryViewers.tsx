import { Alert, Card, Descriptions, Empty, Space, Tag, Typography } from "antd";
import { MarkdownContent } from "../../components/MarkdownContent";
import type { MemoryAuditLogEntry, MemoryCandidate, MemoryDigestResult, MemoryRecord } from "../../lib/types";

export function MemoryViewer({ item }: { item: MemoryRecord | MemoryCandidate }) {
  const type = "type" in item ? item.type : item.memory_type;
  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      <Descriptions bordered size="small" column={1} items={[
        { key: "type", label: "Type", children: type },
        { key: "source", label: "Source", children: item.source || "-" },
        { key: "summary", label: "Summary", children: item.summary },
      ]} />
      <MarkdownContent content={item.content} />
    </Space>
  );
}

export function MemoryAuditViewer({ entry }: { entry: MemoryAuditLogEntry }) {
  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      <Descriptions bordered size="small" column={1} items={[
        { key: "id", label: "Audit ID", children: entry.id },
        { key: "action", label: "Action", children: entry.action },
        { key: "title", label: "Title", children: entry.title || entry.after?.title || entry.before?.title || "-" },
        { key: "type", label: "Type", children: entry.memory_type || entry.after?.type || entry.before?.type || "-" },
        { key: "source", label: "Source", children: entry.source || "-" },
        { key: "created", label: "Created", children: formatDate(entry.created_at) },
        { key: "message", label: "Message", children: entry.message || "-" },
      ]} />
      <div className="memory-audit-diff">
        <AuditSnapshot title="Before" record={entry.before} />
        <AuditSnapshot title="After" record={entry.after} />
      </div>
    </Space>
  );
}

function AuditSnapshot({ record, title }: { record?: MemoryRecord; title: string }) {
  return (
    <Card size="small" title={title}>
      {record ? (
        <Space direction="vertical" size={8} style={{ width: "100%" }}>
          <Typography.Text strong>{record.title}</Typography.Text>
          <Typography.Text type="secondary">{record.summary}</Typography.Text>
          <Typography.Text type="secondary">{record.type} / {record.source || "-"}</Typography.Text>
          <Typography.Paragraph style={{ whiteSpace: "pre-wrap", marginBottom: 0 }}>{record.content}</Typography.Paragraph>
        </Space>
      ) : (
        <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} />
      )}
    </Card>
  );
}

export function MemoryDigestViewer({ result }: { result: MemoryDigestResult }) {
  return (
    <Space direction="vertical" size={16} style={{ width: "100%" }}>
      {result.report_path ? <Alert type="info" showIcon message={`Report written to ${result.report_path}`} /> : null}
      <Card size="small" title={`Candidates (${result.candidates.length})`}>
        {result.candidates.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} /> : (
          <Space direction="vertical" size={12} style={{ width: "100%" }}>
            {result.candidates.map((candidate) => (
              <Card key={candidate.fingerprint} size="small">
                <Space direction="vertical" size={2} style={{ width: "100%" }}>
                  <Typography.Text strong>{candidate.title}</Typography.Text>
                  <Typography.Text type="secondary">{candidate.summary}</Typography.Text>
                  <Space wrap>
                    <Tag>{candidate.memory_type}</Tag>
                    {candidate.source ? <Tag>{candidate.source}</Tag> : null}
                  </Space>
                </Space>
              </Card>
            ))}
          </Space>
        )}
      </Card>
      <Card size="small" title="Report">
        <MarkdownContent content={result.report || "_No report returned._"} />
      </Card>
    </Space>
  );
}

export function ContextGroup({ title, items }: { title: string; items: MemoryRecord[] }) {
  return (
    <Card size="small" title={title} extra={<Tag>{items.length}</Tag>}>
      {items.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} /> : (
        <Space direction="vertical" size={10} style={{ width: "100%" }}>
          {items.map((item) => (
            <Card key={item.id} size="small">
              <Typography.Text strong>{item.title}</Typography.Text>
              <Typography.Paragraph type="secondary">{item.summary}</Typography.Paragraph>
            </Card>
          ))}
        </Space>
      )}
    </Card>
  );
}

export function Metric({ title, value }: { title: string; value: string | number }) {
  return (
    <Card size="small">
      <Typography.Text type="secondary">{title}</Typography.Text>
      <Typography.Title level={3} style={{ margin: 0 }}>{value}</Typography.Title>
    </Card>
  );
}

export function formatDate(value?: string) {
  if (!value) {
    return "-";
  }
  const date = new Date(value);
  return Number.isNaN(date.getTime()) ? value : date.toLocaleString();
}
