import { create } from "zustand";
import { api } from "../../core/api/client.ts";

export interface SharedSession {
  id: string;
  ownerUserKey: string;
  name: string;
  kind: string;
  repo?: string;
  workingCopyId?: string;
  title?: string;
  label?: string;
  createdAt?: string;
  state: "running" | "stopped" | string;
  archived?: boolean;
  permission: "ro" | "rw";
  workspaceState: string;
}

interface SharedSessionsStore {
  sessions: SharedSession[];
  refresh(): Promise<void>;
}

export const useSharedSessionsStore = create<SharedSessionsStore>((set) => ({
  sessions: [],
  async refresh() {
    const d = await api("api/shared-sessions").catch(() => ({ sessions: [] }));
    if (!d?.error) set({ sessions: Array.isArray(d.sessions) ? d.sessions : [] });
  },
}));

export function startSharedSessionsPolling(): () => void {
  const load = () => { if (!document.hidden) void useSharedSessionsStore.getState().refresh(); };
  load();
  const timer = window.setInterval(load, 5000);
  document.addEventListener("visibilitychange", load);
  return () => { window.clearInterval(timer); document.removeEventListener("visibilitychange", load); };
}
