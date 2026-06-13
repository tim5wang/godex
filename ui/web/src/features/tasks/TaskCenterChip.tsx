import { useMemo } from "react";
import { Badge, Button } from "antd";
import { useChatStore } from "../../store/chat";
import { buildTaskOutcomes } from "../chat/taskCenterOutcome";
import { selectTaskCenterHeaderContract } from "./selectors";

// P1 / T4 (SPEC §4.2 + §5): the chat header chip that replaces the old
// "任务中心 + 5 状态计数 chip" row. Renders `任务 N` with a red / amber
// dot (or no dot) derived from the live task outcome pool.
//
// Data flow (per SPEC §4.1.1): the chip is a thin view over
//   useChatStore.overlayItems
//     -> buildTaskOutcomes({ longTasks, subagents, pendingPermissions,
//                            queuedTurns, running, activeTurnId, activePhase })
//     -> selectTaskCenterHeaderContract(outcomes)
//
// The chip does NOT own open-state: the parent (App.tsx) decides where
// the task center content is rendered. The chip just calls onOpen.

export function TaskCenterChip({ onOpen, label }: { onOpen: () => void; label: string }) {
  // We read the live chat store. buildTaskOutcomes only needs the
  // overlayItems (kind === 'subagent' | 'command' | 'warning' | 'error'
  // per the chat store) plus the running/activeTurnId flags. The full
  // LongTaskView / pendingPermissions / queuedTurns slices are optional
  // (buildTaskOutcomes tolerates undefined) — the chip intentionally does
  // not pull them so it stays decoupled from the chat page's per-turn
  // queries. The dock panel (P1-6b) is the place that pulls them.
  const overlayItems = useChatStore((s) => s.overlayItems);
  const running = useChatStore((s) => s.running);
  const currentTurnId = useChatStore((s) => s.currentTurnId);

  const header = useMemo(() => {
    // Use buildTaskOutcomes with the minimal input. The full Task Center
    // page passes a richer input; for the chip we only need the
    // running/activeTurnId hints plus whatever the overlay items carry
    // (subagent-kind items are folded in by buildTaskOutcomes).
    const outcomes = buildTaskOutcomes({
      subagents: overlayItems as never,
      running,
      activeTurnId: currentTurnId || undefined,
    });
    return selectTaskCenterHeaderContract(outcomes);
  }, [overlayItems, running, currentTurnId]);

  const dot =
    header.dotColor === "red" ? (
      <span data-testid="task-center-dot-red" aria-label="needs review" style={{ color: "var(--error)" }}>●</span>
    ) : header.dotColor === "amber" ? (
      <span data-testid="task-center-dot-amber" aria-label="blocked" style={{ color: "var(--warning, #b45309)" }}>●</span>
    ) : null;

  return (
    <Badge
      data-testid="task-center-chip"
      data-has-unread={header.hasUnread ? "true" : "false"}
      count={header.total > 0 ? header.total : 0}
      offset={[-2, 2]}
      size="small"
    >
      <Button
        type="text"
        size="small"
        onClick={onOpen}
        aria-label={label}
        data-testid="task-center-chip-button"
      >
        <span style={{ display: "inline-flex", alignItems: "center", gap: 6 }}>
          {dot}
          <span>{label}</span>
        </span>
      </Button>
    </Badge>
  );
}
