import { Collapse, Space, Typography } from "antd";
import { SettingOutlined } from "@ant-design/icons";
import { useI18n } from "../../i18n";
import { JoinCenterCard } from "./JoinCenterCard";
import { JoinNodeCard } from "./JoinNodeCard";

/**
 * Low-frequency node onboarding lives behind a collapsed section.
 *
 * Both cards used to sit ABOVE the node list, so every visit started with two
 * forms and a warning banner and the actual list (what you came for) was below
 * the fold. Collapsing them keeps them one click away without deleting them —
 * and the empty state opens this panel automatically, because with no nodes yet
 * onboarding IS the task.
 */
export function NodeSettingsPanel({ activeKey, onChange }: { activeKey: string[]; onChange: (key: string[]) => void }) {
  const { t } = useI18n();

  return (
    <Collapse
      className="node-settings-collapse"
      activeKey={activeKey}
      onChange={(keys) => onChange(keys.map(String))}
      items={[
        {
          key: "settings",
          label: (
            <span className="node-settings-collapse-label">
              <SettingOutlined />
              <span>{t("nodes.settingsTitle")}</span>
              <Typography.Text type="secondary" className="node-settings-hint">
                {t("nodes.settingsSubtitle")}
              </Typography.Text>
            </span>
          ),
          children: (
            <Space direction="vertical" size={16} style={{ width: "100%" }}>
              <JoinCenterCard />
              <JoinNodeCard />
            </Space>
          ),
        },
      ]}
    />
  );
}
