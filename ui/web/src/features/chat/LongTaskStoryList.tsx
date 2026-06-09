import { useTaskCenterText } from "./taskCenter.i18n";
import type { LongTaskCardStory } from "./LongTaskCard";

export interface LongTaskStoryListProps {
  stories: LongTaskCardStory[];
}

// LongTaskStoryList renders the expanded story view of a
// longtask. Active (running / blocked) stories come first so
// the user can spot what is in flight without scrolling. T15
// acceptance: rolled-back stories are visibly tagged.
export function LongTaskStoryList(props: LongTaskStoryListProps): JSX.Element {
  const text = useTaskCenterText();
  const sorted = [...props.stories].sort((a, b) => {
    return storyOrderKey(a) - storyOrderKey(b);
  });
  if (sorted.length === 0) {
    return <div style={{ opacity: 0.6, fontSize: 12 }}>(no stories)</div>;
  }
  return (
    <ul data-testid="longtask-story-list" style={{ listStyle: "none", padding: 0, margin: 0 }}>
      {sorted.map((s) => (
        <li
          key={s.id}
          data-testid={`story-row-${s.id}`}
          style={{
            display: "flex",
            alignItems: "center",
            gap: 8,
            padding: "4px 0",
            fontSize: 13,
            borderBottom: "1px solid #2a2d35",
          }}
        >
          <span style={{ width: 18 }}>{statusMarker(s.status)}</span>
          <code style={{ flex: 1 }}>{s.id}</code>
          {s.commit_hash ? <span style={{ opacity: 0.6, fontSize: 12 }}>{s.commit_hash.slice(0, 8)}</span> : null}
          {s.reverted ? <span style={{ fontSize: 11, color: "#f59e0b" }}>[{text.reverted}]</span> : null}
          {s.error ? <span style={{ fontSize: 11, color: "#ef4444" }}>{s.error}</span> : null}
        </li>
      ))}
    </ul>
  );
}

function statusMarker(status: string): string {
  switch (status) {
    case "completed":
      return "[x]";
    case "running":
      return "[~]";
    case "blocked":
    case "error":
      return "[!]";
    case "canceled":
      return "[-]";
    default:
      return "[ ]";
  }
}

function storyOrderKey(s: LongTaskCardStory): string {
  switch (s.status) {
    case "pending":
    case "":
      return "0" + s.id;
    case "running":
    case "blocked":
      return "1" + s.id;
    case "completed":
      return "2" + s.id;
    case "canceled":
      return "3" + s.id;
  }
  return "9" + s.id;
}
