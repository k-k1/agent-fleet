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

// 所有者側: 自分が作成した共有(session/repo/worktree)の一覧。SessionRow/RepoRow の
// 「共有中」バッジと ShareListModal が共通で参照する。会話本文は含まない軽量な行のみ。
export interface MyShare {
  id: string;
  recipientUserKey: string;
  scope: { type: string; key: string };
  permission: "ro" | "rw";
}

interface MySharesStore {
  shares: MyShare[];
  refresh(): Promise<void>;
}

export const useMySharesStore = create<MySharesStore>((set) => ({
  shares: [],
  async refresh() {
    const d = await api("api/session-shares").catch(() => ({ shares: [] }));
    if (!d?.error) set({ shares: Array.isArray(d.shares) ? d.shares : [] });
  },
}));

export function startMySharesPolling(): () => void {
  const load = () => { if (!document.hidden) void useMySharesStore.getState().refresh(); };
  load();
  const timer = window.setInterval(load, 5000);
  document.addEventListener("visibilitychange", load);
  return () => { window.clearInterval(timer); document.removeEventListener("visibilitychange", load); };
}
