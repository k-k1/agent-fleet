import { create } from "zustand";
import { api } from "../../core/api/client.ts";

export interface SharedSession {
  id: string;
  /** The owner's normalised key (`a@x.com` → `a-x-com`), used for grouping and persisted keys. */
  ownerUserKey: string;
  /** The owner's login id (email address). This is what gets displayed — see ownerLabel(). */
  ownerEmail?: string;
  name: string;
  kind: string;
  repo?: string;
  workingCopyId?: string;
  title?: string;
  label?: string;
  createdAt?: string;
  state: "running" | "stopped" | string;
  permission: "ro" | "rw";
  workspaceState: string;
  worktree?: boolean;
  parent?: string;
  /** Branch the working copy currently has checked out (displayed as on the owner's repo row). */
  branch?: string;
  /**
   * Live state of a running session (working | question | plan | permission | blocked |
   * compacting; empty = waiting for input). Empty while stopped. Feeds the same state chip as on
   * the owner side; its freshness depends on the list's sync throttle (60s by default) plus the
   * reload button (docs/log/59 §3).
   */
  activity?: string;
}

/**
 * How an owner is named. People identify themselves by their login id, so prefer email and fall
 * back to the normalised key only for an identity that has none (an admin added it by user_key
 * alone). Never use this for grouping or localStorage keys — those take identity from
 * ownerUserKey.
 */
export function ownerLabel(o: { ownerUserKey: string; ownerEmail?: string }): string {
  return o.ownerEmail || o.ownerUserKey;
}

interface SharedSessionsStore {
  sessions: SharedSession[];
  /** force=true (the section's reload button) also asks the owner for a fresh inventory. */
  refresh(force?: boolean): Promise<void>;
}

export const useSharedSessionsStore = create<SharedSessionsStore>((set) => ({
  sessions: [],
  async refresh(force?: boolean) {
    // The default poll only reads the CP's DB snapshot (it reaches the owner's Workspace at most
    // once a minute). Only ?refresh=1 bypasses that throttle, because "right now" is wanted for a
    // state badge or a deletion only when a person asked for it.
    const d = await api("api/shared-sessions" + (force ? "?refresh=1" : "")).catch(() => ({ sessions: [] }));
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

// Owner side: the shares (session/repo/worktree) this user created. Both the "shared" badge on
// SessionRow/RepoRow and ShareListModal read it. Lightweight rows only, no conversation bodies.
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
