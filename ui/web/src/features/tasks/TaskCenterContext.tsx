import { createContext, useContext, type ReactNode } from "react";
import type { TaskOutcome } from "../chat/taskCenterOutcome";

// P1 / T5g (SPEC §4.1.1): the task center is a workspace-level panel
// whose full <TaskCenterPanel> tree lives in ChatPage (because it
// depends on 18 mutation / query hooks that are local to the chat
// page). To surface that tree inside the App.tsx <Drawer> — which is
// mounted above the chat page in the Layout tree — we bridge the
// 18 props through a small React context.
//
// Shape: the ChatPage provides a `TaskCenterPanelBridge` ReactNode
// (already-rendered <TaskCenterPanel …> with the 18 props filled in).
// The App.tsx Drawer children read that ReactNode and render it.
//
// This keeps the mutation hooks in ChatPage (no centralization) and
// avoids a global "task center controller" store (which would force
// 18 useMutation calls into App.tsx and break encapsulation).

export type TaskCenterPanelBridgeProps = {
  outcomes: TaskOutcome[];
  collapsed: boolean;
  onCollapsedChange: (collapsed: boolean) => void;
  reviewingJobId?: string;
  mergingJobId?: string;
  resumingJobId?: string;
  cancelingJobId?: string;
  runningWorkflowId?: string;
  cancelingLongTask?: { workflowId?: string; nodeId?: string };
  finalizingLongTask?: { workflowId?: string; nodeId?: string };
  onReviewSubagent: (jobId: string) => void;
  onMergeSubagent: (jobId: string) => void;
  onResumeSubagent: (jobId: string) => void;
  onCancelSubagent: (jobId: string) => void;
  onRunLongTask: (workflowId: string) => void;
  onCancelLongTask: (workflowId: string, nodeId: string) => void;
  onFinalizeLongTask: (workflowId: string, nodeId: string) => void;
  onOpenReviewMergeCenter: (jobId?: string) => void;
};

export type TaskCenterPanelBridge = ReactNode;

const TaskCenterContext = createContext<TaskCenterPanelBridge | null>(null);

export function TaskCenterProvider(props: { value: TaskCenterPanelBridge; children: ReactNode }) {
  return <TaskCenterContext.Provider value={props.value}>{props.children}</TaskCenterContext.Provider>;
}

export function useTaskCenterBridge(): TaskCenterPanelBridge | null {
  return useContext(TaskCenterContext);
}

// Test seam: a pure function that the panel-level wiring test can call
// without React. Returns the ReactNode that should be passed to the
// provider. We just return the value as-is so callers can decide what
// to render; the contract is "if you provide a value, the Drawer
// children see the same value".
export function asTaskCenterBridge(value: TaskCenterPanelBridge): TaskCenterPanelBridge {
  return value;
}
