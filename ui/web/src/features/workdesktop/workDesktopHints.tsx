import { Tooltip, Typography } from "antd";
import { InfoCircleOutlined } from "@ant-design/icons";

/**
 * InfoHint collapses verbose explanatory copy into a hover affordance.
 *
 * Long "how this works / what this cannot do" paragraphs used to sit permanently
 * in the layout and cost vertical space on every stream. The text is unchanged
 * and still reachable — it now lives in a tooltip behind a small info icon, so
 * the workspace stays dense without hiding the caveats entirely.
 */
export function InfoHint({ title, text }: { title: string; text: string }) {
  return (
    <Tooltip title={text} placement="bottomLeft">
      <Typography.Text type="secondary" className="wd-hint" role="note" aria-label={`${title}: ${text}`} tabIndex={0}>
        <InfoCircleOutlined aria-hidden="true" />
      </Typography.Text>
    </Tooltip>
  );
}
