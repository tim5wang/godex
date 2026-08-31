import { Card, Progress, Space, Tag, Typography } from "antd";
import { ResponsiveTable } from "../../components/ResponsiveTable";
import type { PackageQualityReport, ToolStat } from "../../lib/types";

type Translate = (key: string, vars?: Record<string, string | number>) => string;

type ToolStatRow = {
  name: string;
  total: number;
  success: number;
  failure: number;
  successRate: number;
};

type FailureReason = {
  reason: string;
  count: number;
};

type ToolAnalytics = {
  inspectedSessions: number;
  totalRuns: number;
  successRuns: number;
  failureRuns: number;
  successRate: number;
  byTool: ToolStatRow[];
  failureReasons: FailureReason[];
};

const emptyToolAnalytics: ToolAnalytics = {
  inspectedSessions: 0,
  totalRuns: 0,
  successRuns: 0,
  failureRuns: 0,
  successRate: 0,
  byTool: [],
  failureReasons: [],
};

export function SkillAnalyticsPanel({
  analytics,
  activeSkillCount,
  installedSkillCount,
  loading,
  t,
}: {
  analytics: ToolAnalytics;
  activeSkillCount: number;
  installedSkillCount: number;
  loading: boolean;
  t: Translate;
}) {
  return (
    <Card
      className="skill-analytics-card"
      loading={loading}
      title={
        <Space direction="vertical" size={0}>
          <Typography.Text strong>{t("skills.analyticsTitle")}</Typography.Text>
          <Typography.Text type="secondary">{t("skills.analyticsSubtitle", { count: analytics.inspectedSessions })}</Typography.Text>
        </Space>
      }
    >
      <div className="skill-analytics-layout">
        <div className="skill-analytics-metrics">
          <AnalyticsMetric title={t("skills.installedSkills")} value={installedSkillCount} />
          <AnalyticsMetric title={t("skills.activeSkills")} value={activeSkillCount} />
          <AnalyticsMetric title={t("skills.toolRuns")} value={analytics.totalRuns} />
          <div className="skill-analytics-rate">
            <Typography.Text type="secondary">{t("skills.successRate")}</Typography.Text>
            <Progress
              percent={analytics.successRate}
              size="small"
              status={analytics.failureRuns > 0 ? "normal" : "success"}
              format={(percent) => `${Math.round(percent ?? 0)}%`}
            />
            <Typography.Text type="secondary">
              {analytics.successRuns} / {analytics.totalRuns}
            </Typography.Text>
          </div>
        </div>
        <div className="skill-failure-panel">
          <Typography.Text strong>{t("skills.failureReasons")}</Typography.Text>
          {analytics.failureReasons.length === 0 ? (
            <Typography.Text type="secondary">{analytics.totalRuns ? t("skills.noFailureReasons") : t("skills.noToolRuns")}</Typography.Text>
          ) : (
            <Space direction="vertical" size={8} style={{ width: "100%" }}>
              {analytics.failureReasons.map((item) => (
                <div className="skill-failure-row" key={item.reason}>
                  <Typography.Text ellipsis title={item.reason}>{item.reason}</Typography.Text>
                  <Tag color="red">{item.count}</Tag>
                </div>
              ))}
            </Space>
          )}
        </div>
      </div>
      <ResponsiveTable<ToolStatRow>
        className="skill-tool-table"
        size="small"
        rowKey="name"
        dataSource={analytics.byTool}
        locale={{ emptyText: t("skills.noToolRuns") }}
        pagination={analytics.byTool.length > 6 ? { pageSize: 6, size: "small" } : false}
        columns={[
          { title: t("skills.toolName"), dataIndex: "name", render: (value) => <Typography.Text strong>{value}</Typography.Text> },
          { title: t("skills.runs"), dataIndex: "total", width: 96 },
          { title: t("skills.failures"), dataIndex: "failure", width: 96, render: (value) => value ? <Tag color="red">{value}</Tag> : <Tag color="green">0</Tag> },
          {
            title: t("skills.successRate"),
            dataIndex: "successRate",
            width: 180,
            render: (value: number) => <Progress percent={value} size="small" format={(percent) => `${Math.round(percent ?? 0)}%`} />,
          },
        ]}
      />
    </Card>
  );
}

function AnalyticsMetric({ title, value }: { title: string; value: number }) {
  return (
    <div className="skill-analytics-metric">
      <Typography.Text type="secondary">{title}</Typography.Text>
      <Typography.Title level={3} style={{ margin: 0 }}>{value}</Typography.Title>
    </div>
  );
}

export function qualityToAnalytics(report?: PackageQualityReport): ToolAnalytics {
  if (!report?.tool_health) {
    return emptyToolAnalytics;
  }
  const toolHealth = report.tool_health;
  return {
    inspectedSessions: toolHealth.inspected_sessions,
    totalRuns: toolHealth.total_runs,
    successRuns: toolHealth.success_runs,
    failureRuns: toolHealth.failure_runs,
    successRate: toolHealth.success_rate,
    byTool: (toolHealth.by_tool ?? []).map((row: ToolStat) => ({
      name: row.name,
      total: row.total,
      success: row.success,
      failure: row.failure,
      successRate: row.success_rate,
    })),
    failureReasons: report.failure_reasons ?? [],
  };
}
