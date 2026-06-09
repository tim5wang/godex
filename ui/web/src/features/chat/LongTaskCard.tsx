import { useState } from "react";

import { useTaskCenterText } from "./taskCenter.i18n";
import { LongTaskRollbackModal } from "./LongTaskRollbackModal";
import { LongTaskLookupModal } from "./LongTaskLookupModal";
import { LongTaskStoryList } from "./LongTaskStoryList";

export interface LongTaskCardStory {
  id: string;
  status: string;
  verdict?: string;
  commit_hash?: string;
  reverted?: boolean;
  error?: string;
}

export interface LongTaskCardProps {
  longtaskId: string;
  workflowId: string;
  status: string;
  stories: LongTaskCardStory[];
  // Action callbacks. T15 acceptance: the card is a presentation
  // component; the chat page owns the API calls.
  onRun?: () => void;
  onCancelAll?: () => void;
  onFinalize?: (storyId: string) => void;
  onResume?: () => void;
  onRollback?: (storyId: string, reason: string) => void;
  onLookupByCommit?: (commit: string) => void;
  onLookupByStory?: (storyId: string) => void;
  onGc?: (olderThanSeconds: number, apply: boolean) => void;
}

// LongTaskCard is the T15 single-card component. It renders one
// longtask, supports default-collapsed / click-to-expand, and
// owns the rollback / lookup modal visibility (so the chat page
// does not have to track which card has an open modal).
export function LongTaskCard(props: LongTaskCardProps): JSX.Element {
  const text = useTaskCenterText();
  const [expanded, setExpanded] = useState(false);
  const [rollbackNode, setRollbackNode] = useState<string | null>(null);
  const [lookupOpen, setLookupOpen] = useState(false);
  const completed = props.stories.filter((s) => s.status === "completed").length;
  const total = props.stories.length;
  const revertedCount = props.stories.filter((s) => s.reverted).length;

  return (
    <div
      data-testid="longtask-card"
      style={{
        border: "1px solid #2a2d35",
        borderRadius: 6,
        padding: 12,
        marginBottom: 8,
        background: "var(--surface-1, #14171c)",
      }}
    >
      <div
        style={{ display: "flex", alignItems: "center", justifyContent: "space-between", cursor: "pointer" }}
        onClick={() => setExpanded(!expanded)}
      >
        <div>
          <strong>{props.longtaskId}</strong>
          <span style={{ marginLeft: 8, opacity: 0.7, fontSize: 12 }}>{props.status}</span>
        </div>
        <div style={{ fontSize: 12, opacity: 0.7 }}>
          {completed}/{total}
          {revertedCount > 0 ? ` (${revertedCount} ${text.reverted})` : ""}
          {" "}
          <span>{expanded ? text.collapse : text.expand}</span>
        </div>
      </div>
      {expanded ? (
        <div style={{ marginTop: 12 }}>
          <LongTaskStoryList stories={props.stories} />
          <div style={{ display: "flex", gap: 6, flexWrap: "wrap", marginTop: 12 }}>
            {props.onRun ? <button type="button" onClick={props.onRun} style={buttonStyle}>{text.runLongTask}</button> : null}
            {props.onCancelAll ? <button type="button" onClick={props.onCancelAll} style={buttonStyle}>{text.cancelLongTaskNode}</button> : null}
            {props.onResume ? <button type="button" onClick={props.onResume} style={buttonStyle}>{text.resumeLongTask}</button> : null}
            {props.onLookupByCommit || props.onLookupByStory ? (
              <button type="button" onClick={() => setLookupOpen(true)} style={buttonStyle}>
                {text.lookupLongTask}
              </button>
            ) : null}
            {props.onGc ? <button type="button" onClick={() => props.onGc?.(0, false)} style={buttonStyle}>{text.gcLongTask}</button> : null}
          </div>
          {props.stories.length > 0 ? (
            <div style={{ display: "flex", gap: 6, flexWrap: "wrap", marginTop: 8 }}>
              {props.stories.map((s) => (
                <div key={s.id} style={{ display: "flex", gap: 4, alignItems: "center", fontSize: 12 }}>
                  <code>{s.id}</code>
                  {props.onFinalize ? <button type="button" onClick={() => props.onFinalize?.(s.id)} style={buttonStyle}>{text.finalizeLongTask}</button> : null}
                  {s.commit_hash ? (
                    <button type="button" onClick={() => setRollbackNode(s.id)} style={buttonStyle}>
                      {text.rollbackLongTask}
                    </button>
                  ) : null}
                </div>
              ))}
            </div>
          ) : null}
        </div>
      ) : null}
      <LongTaskRollbackModal
        visible={rollbackNode !== null}
        nodeId={rollbackNode ?? ""}
        onCancel={() => setRollbackNode(null)}
        onSubmit={async (reason) => {
          if (rollbackNode) {
            await props.onRollback?.(rollbackNode, reason);
          }
          setRollbackNode(null);
        }}
      />
      <LongTaskLookupModal
        visible={lookupOpen}
        onCancel={() => setLookupOpen(false)}
        onSubmit={async (mode, query) => {
          if (mode === "commit") {
            await props.onLookupByCommit?.(query);
          } else {
            await props.onLookupByStory?.(query);
          }
          setLookupOpen(false);
        }}
      />
    </div>
  );
}

const buttonStyle: React.CSSProperties = {
  padding: "4px 8px",
  fontSize: 12,
  borderRadius: 4,
  background: "transparent",
  color: "inherit",
  border: "1px solid #444",
  cursor: "pointer",
};
