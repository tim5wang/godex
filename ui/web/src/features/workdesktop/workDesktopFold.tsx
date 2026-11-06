import { useState, type ReactNode } from "react";
import { Card } from "antd";
import { DownOutlined, RightOutlined } from "@ant-design/icons";

/**
 * FoldCard is a Card whose body can be collapsed from its title.
 *
 * The work desktop lives in the chat's right dock now, which is much narrower than
 * the old standalone page: a single-column overview leaves a long scroll, so each
 * section is individually foldable and the user keeps the ones they are working on
 * open. Folding is opt-in (defaultOpen) rather than the default — this panel is
 * work MEMORY, and silently hiding remembered content is a worse failure than a
 * long list.
 */
export function FoldCard(props: {
  title: ReactNode;
  extra?: ReactNode;
  className?: string;
  defaultOpen?: boolean;
  /** Labels for the toggle's accessible name (it is the only control that folds). */
  openLabel: string;
  closeLabel: string;
  children: ReactNode;
}) {
  const { title, extra, className, defaultOpen = true, openLabel, closeLabel, children } = props;
  const [open, setOpen] = useState(defaultOpen);

  return (
    <Card
      size="small"
      className={`wd-fold${className ? ` ${className}` : ""}`}
      data-folded={open ? "false" : "true"}
      title={
        <span className="wd-fold-head">
          <button
            type="button"
            className="wd-fold-toggle"
            aria-expanded={open}
            // The accessible name says what the NEXT click does, which is the
            // convention for a disclosure: "Collapse" while open.
            aria-label={open ? closeLabel : openLabel}
            title={open ? closeLabel : openLabel}
            onClick={() => setOpen((prev) => !prev)}
          >
            {open ? <DownOutlined aria-hidden="true" /> : <RightOutlined aria-hidden="true" />}
          </button>
          {title}
        </span>
      }
      extra={extra}
    >
      {open ? children : null}
    </Card>
  );
}
