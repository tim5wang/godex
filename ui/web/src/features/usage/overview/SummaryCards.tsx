import { Card, Col, Row, Statistic } from "antd";
import {
  ApiOutlined,
  ThunderboltOutlined,
  NumberOutlined,
  WarningOutlined,
  SaveOutlined,
} from "@ant-design/icons";
import { useI18n } from "../../../i18n";

interface SummaryData {
  totalCalls: number;
  totalTokens: number;
  totalCredits: number;
  errorRate: number; // percentage 0-100
  inputTokens: number;
  outputTokens: number;
  cacheHits: number;
  cacheHitRate: number; // percentage 0-100
  tokensSaved: number;
}

interface SummaryCardsProps {
  data?: SummaryData;
  loading?: boolean;
}

export function SummaryCards({ data, loading }: SummaryCardsProps) {
  const { t } = useI18n();
  const defaultData: SummaryData = {
    totalCalls: 0,
    totalTokens: 0,
    totalCredits: 0,
    errorRate: 0,
    inputTokens: 0,
    outputTokens: 0,
    cacheHits: 0,
    cacheHitRate: 0,
    tokensSaved: 0,
  };
  const d = data ?? defaultData;

  return (
    <Row gutter={[16, 16]} style={{ marginBottom: 16 }}>
      <Col xs={24} sm={12} lg={6}>
        <Card loading={loading} size="small">
          <Statistic
            title={t("usage.overviewTab.totalCalls")}
            value={d.totalCalls}
            prefix={<ApiOutlined />}
            valueStyle={{ color: "#1677ff" }}
          />
        </Card>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Card loading={loading} size="small">
          <Statistic
            title={t("usage.overviewTab.totalTokens")}
            value={d.totalTokens}
            prefix={<NumberOutlined />}
            valueStyle={{ color: "#52c41a" }}
          />
        </Card>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Card loading={loading} size="small">
          <Statistic
            title={t("usage.overviewTab.totalCredits")}
            value={d.totalCredits}
            prefix={<ThunderboltOutlined />}
            precision={2}
            valueStyle={{ color: "#fa8c16" }}
          />
        </Card>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Card loading={loading} size="small">
          <Statistic
            title={t("usage.overviewTab.errorRate")}
            value={d.errorRate}
            suffix="%"
            precision={2}
            prefix={<WarningOutlined />}
            valueStyle={{ color: d.errorRate > 5 ? "#ff4d4f" : "#52c41a" }}
          />
        </Card>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Card loading={loading} size="small">
          <Statistic
            title={t("usage.overviewTab.cacheSaved")}
            value={d.tokensSaved}
            prefix={<SaveOutlined />}
            valueStyle={{ color: "#722ed1" }}
          />
        </Card>
      </Col>
      <Col xs={24} sm={12} lg={6}>
        <Card loading={loading} size="small">
          <Statistic
            title={t("usage.overviewTab.cacheHitRate")}
            value={d.cacheHitRate}
            suffix="%"
            precision={1}
            valueStyle={{ color: d.cacheHitRate > 50 ? "#52c41a" : "#fa8c16" }}
          />
        </Card>
      </Col>
    </Row>
  );
}

export type { SummaryData };
