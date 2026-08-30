import { create } from "zustand";
import { api } from "../../core/api/client.ts";

export interface SharedSession {
  id: string;
  /** 所有者の正規化キー(`a@x.com` → `a-x-com`)。グルーピングと永続キーの同一性判定用。 */
  ownerUserKey: string;
  /** 所有者のログイン ID(メールアドレス)。表示はこちら — ownerLabel() 参照。 */
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
  /** 作業コピーが今チェックアウトしているブランチ(所有者側の repo 行と同じ表示)。 */
  branch?: string;
  /**
   * 稼働中セッションの live state(working | question | plan | permission | blocked |
   * compacting、空=入力待ち)。停止中は空。所有者側と同じ状態チップの素で、鮮度は
   * 一覧の同期間引き(既定60秒)＋リロードボタン次第(docs/log/59 §3)。
   */
  activity?: string;
}

/**
 * 所有者の名乗り。人が名乗るのはログイン ID(メールアドレス)なので email を優先し、
 * email を持たない identity(管理者が user_key だけで足した場合)だけ正規化キーへ落とす。
 * グルーピングや localStorage の鍵には使わない — あちらは ownerUserKey で同一性を取る。
 */
export function ownerLabel(o: { ownerUserKey: string; ownerEmail?: string }): string {
  return o.ownerEmail || o.ownerUserKey;
}

interface SharedSessionsStore {
  sessions: SharedSession[];
  /** force=true(セクションのリロードボタン)は所有者在庫の再取得まで要求する。 */
  refresh(force?: boolean): Promise<void>;
}

export const useSharedSessionsStore = create<SharedSessionsStore>((set) => ({
  sessions: [],
  async refresh(force?: boolean) {
    // 既定のポーリングは CP の DB スナップショットを読むだけ(所有者 Workspace へは
    // 最大60秒に1回しか行かない)。?refresh=1 だけがその間引きを飛び越える — 状態
    // バッジや削除の反映を「今すぐ」取り直したいのは人が押したときだけなので。
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
