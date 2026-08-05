import { create } from "zustand";

interface NodeContextState {
  /** Active remote node ID. When set, API requests for node-scoped paths are
   *  routed through the center proxy (/control/nodes/{id}/proxy/...). */
  nodeID: string | null;
  nodeName?: string;
  setNode: (nodeID: string, nodeName?: string) => void;
  clearNode: () => void;
}

export const useNodeContextStore = create<NodeContextState>((set) => ({
  nodeID: null,
  nodeName: undefined,
  setNode: (nodeID, nodeName) => set({ nodeID, nodeName }),
  clearNode: () => set({ nodeID: null, nodeName: undefined }),
}));
