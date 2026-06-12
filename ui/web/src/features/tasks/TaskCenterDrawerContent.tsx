import type { ReactNode } from "react";
import { useTaskCenterBridge } from "./TaskCenterContext";

// P1 / T5g (SPEC §4.1.1): the App.tsx <Drawer> children read the chat
// workspace's <TaskCenterPanel> via React context (provided by the
// chat page). Before the chat page mounts — e.g. on the home route
// before navigation, or when the chat page unmounts during a route
// transition — the context value is null and the Drawer should fall
// back to the original AppNav menu so the panel still has content.
//
// This component encapsulates that policy in one place. It is rendered
// as the Drawer's child, with the AppNav <Menu> passed in as a
// fallback prop. When the context provides a panel, it renders that
// instead.

export function TaskCenterDrawerContent(props: { fallback: ReactNode }) {
  const bridge = useTaskCenterBridge();
  if (bridge) {
    return <>{bridge}</>;
  }
  return <>{props.fallback}</>;
}
