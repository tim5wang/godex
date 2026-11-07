import { useEffect, useState, type ReactNode } from "react";
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
 *
 * A revealed card also flashes briefly (see REVEAL_FLASH_MS below): opening and
 * scrolling are not enough on their own, because landing inside a long single-column
 * panel still leaves the user hunting for WHAT changed.
 */

/**
 * How long a revealed card stays visibly highlighted. Long enough to catch the
 * eye after a jump (the whole point), short enough that it never becomes another
 * permanent decoration competing for attention.
 */
const REVEAL_FLASH_MS = 2000;

export function FoldCard(props: {
  title: ReactNode;
  extra?: ReactNode;
  className?: string;
  defaultOpen?: boolean;
  /** True while an external actor wants this card revealed NOW (e.g. the rail's unread
   *  badge). A boolean rather than a nonce because the caller decides when it is
   *  pending: passing a nonce forced a mount-value comparison, which silently dropped
   *  the reveal whenever the card was MOUNTED by the request (the rail switches to
   *  Overview and reveals in one click, so the panel is created by the very request
   *  it was supposed to react to). */
  reveal?: boolean;
  /** Labels for the toggle's accessible name (it is the only control that folds). */
  openLabel: string;
  closeLabel: string;
  children: ReactNode;
}) {
  const { title, extra, className, defaultOpen = true, reveal = false, openLabel, closeLabel, children } = props;
  const [open, setOpen] = useState(defaultOpen);
  const [flash, setFlash] = useState(false);

  // Fires on mount when the card IS the result of a reveal, and on every later
  // false->true transition. Only the true direction acts, so answering the request
  // (reveal going back to false) never fights the user's own collapse.
  useEffect(() => {
    if (!reveal) return;
    setOpen(true);
    // Opening is not enough on its own: the reveal scrolls the card into view inside
    // a long single-column panel, and the user (rightly) reported that landing on the
    // section still left them hunting for WHAT changed. So the reveal also marks the
    // card for a moment — "this is the thing you clicked for".
    setFlash(true);
  }, [reveal]);

  // The highlight is deliberately transient rather than a persistent "focused"
  // state: a lasting marker would become a second thing to dismiss, and the work
  // desktop's whole premise is that it does not add chores.
  useEffect(() => {
    if (!flash) return undefined;
    const timer = window.setTimeout(() => setFlash(false), REVEAL_FLASH_MS);
    return () => window.clearTimeout(timer);
  }, [flash]);

  return (
    <Card
      size="small"
      className={`wd-fold${className ? ` ${className}` : ""}${flash ? " wd-reveal-flash" : ""}`}
      data-folded={open ? "false" : "true"}
      data-flash={flash ? "true" : "false"}
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
