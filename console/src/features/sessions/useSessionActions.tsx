// Session lifecycle actions, extracted from SessionsSection so the same handlers
// drive a session row wherever it renders (the flat list AND the per-working-copy
// nodes of the project tree). Each op hits the Agent, then refreshes the store and
// closes any stale panes; confirmations and error toasts are built in.
import { raw } from "../../core/api/client.ts";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useLayoutStore } from "../../layout/store.ts";
import { displayName } from "../../lib/sessionview.ts";
import { agentOf } from "../../agents/registry.ts";
import { useSessionsStore } from "./store.ts";
import { openSessionChat, openSessionChatSplit, openSessionTerminal } from "./open.ts";
import type { Session } from "../../types/session.ts";

export interface SessionActions {
  /** Hide from the list but KEEP it (restorable). Live sessions stop first. */
  archive(s: Session): Promise<void>;
  /** Delete outright (shell/ssm — no conversation worth keeping). Irreversible. */
  deleteSession(s: Session): Promise<void>;
  /** Clear all stopped: agent sessions archive (restorable), shell/ssm delete. */
  clearStopped(): Promise<void>;
  /** Bulk-clear every "その他のセッション" (orphan whose working copy is gone),
   * same split as clearStopped: agent sessions archive (restorable), shell/ssm delete. */
  clearOrphans(orphans: Session[]): Promise<void>;
  /** Halt a live session into 停止中 (resumable): kills tmux, keeps the meta. */
  halt(name: string, display: string): Promise<void>;
  /** Mint a NEW live session (fresh slug, same title/dir/model), archive the old. */
  recreate(name: string, display: string): Promise<void>;
  /** Branch a claude conversation into a NEW session (source kept); open the fork. */
  fork(name: string): Promise<void>;
  /** ドライバ排他切替（docs/27 P3 §2）: tui ⇄ managed を stop→drain→resume で。 */
  switchDriver(s: Session): Promise<void>;
}

export function useSessionActions(): SessionActions {
  const askConfirm = useConfirm();
  const toast = useToast();
  const closeSessionPanes = useLayoutStore((s) => s.closeSessionPanes);
  const refreshSessions = useSessionsStore((s) => s.refresh);

  const archive = async (s: Session) => {
    const res = await raw(`api/sessions/${encodeURIComponent(s.name)}/archive`, { method: "POST" });
    if (!res.ok) {
      toast("アーカイブに失敗しました");
      return;
    }
    closeSessionPanes(s.name);
    void refreshSessions();
  };

  const deleteSession = async (s: Session) => {
    if (
      !(await askConfirm({
        title: "セッションを削除",
        body: `「${displayName(s)}」を削除します。この操作は取り消せません。`,
        confirmLabel: "削除する",
        danger: true,
      }))
    )
      return;
    const res = await raw(`api/sessions/${encodeURIComponent(s.name)}/stop`, { method: "POST" });
    if (!res.ok) {
      toast("削除に失敗しました");
      return;
    }
    closeSessionPanes(s.name);
    void refreshSessions();
  };

  const clearStopped = async () => {
    const stopped = useSessionsStore.getState().sessions.filter((s) => !s.alive);
    if (stopped.length === 0) return;
    const ephemeral = stopped.filter((s) => agentOf(s.kind).caps.ephemeral);
    const keepable = stopped.filter((s) => !agentOf(s.kind).caps.ephemeral);
    const parts = [];
    if (keepable.length) parts.push(`${keepable.length} 件をアーカイブ`);
    if (ephemeral.length) parts.push(`shell/ssm ${ephemeral.length} 件を削除`);
    if (
      !(await askConfirm({
        title: "停止中のセッションを整理",
        body: `${parts.join("・")}します。`,
        confirmLabel: "整理する",
        danger: ephemeral.length > 0,
      }))
    )
      return;
    await Promise.all([
      ...keepable.map((s) =>
        raw(`api/sessions/${encodeURIComponent(s.name)}/archive`, { method: "POST" }).catch(() => {}),
      ),
      ...ephemeral.map((s) =>
        raw(`api/sessions/${encodeURIComponent(s.name)}/stop`, { method: "POST" }).catch(() => {}),
      ),
    ]);
    for (const s of stopped) closeSessionPanes(s.name);
    void refreshSessions();
  };

  const clearOrphans = async (orphans: Session[]) => {
    if (orphans.length === 0) return;
    // Same split as clearStopped: agent sessions archive (conversation kept,
    // restorable); shell/ssm have no conversation worth keeping, so they delete.
    const ephemeral = orphans.filter((s) => agentOf(s.kind).caps.ephemeral);
    const keepable = orphans.filter((s) => !agentOf(s.kind).caps.ephemeral);
    const alive = orphans.filter((s) => s.alive).length;
    const parts: string[] = [];
    if (keepable.length) parts.push(`${keepable.length} 件をアーカイブへ退避`);
    if (ephemeral.length) parts.push(`shell/ssm ${ephemeral.length} 件を削除`);
    if (
      !(await askConfirm({
        title: "その他のセッションを整理",
        body: (
          <>
            作業コピーのないセッションを整理します（{parts.join("・")}）
            {alive > 0 ? <>（実行中 {alive} 件を含む）</> : null}。
            <br />
            アーカイブ分は会話を保持し、あとで<strong>復帰できます</strong>。
            {ephemeral.length > 0 ? <>shell/ssm の削除は取り消せません。</> : null}
          </>
        ),
        confirmLabel: "整理する",
        danger: ephemeral.length > 0,
      }))
    )
      return;
    // /archive hides but KEEPS the meta/jsonl (restorable); /stop forgets it
    // (shell/ssm delete). Best-effort per session so one failure doesn't abort
    // the rest — mirrors the per-row アーカイブ / 削除.
    await Promise.all([
      ...keepable.map((s) =>
        raw(`api/sessions/${encodeURIComponent(s.name)}/archive`, { method: "POST" }).catch(() => {}),
      ),
      ...ephemeral.map((s) =>
        raw(`api/sessions/${encodeURIComponent(s.name)}/stop`, { method: "POST" }).catch(() => {}),
      ),
    ]);
    for (const s of orphans) closeSessionPanes(s.name);
    void refreshSessions();
  };

  const halt = async (name: string, display: string) => {
    if (
      !(await askConfirm({
        title: "セッションを停止",
        body: `「${display}」を停止します。会話は保持され、あとで再開できます。`,
        confirmLabel: "停止する",
        danger: false,
      }))
    )
      return;
    const res = await raw(`api/sessions/${encodeURIComponent(name)}/halt`, { method: "POST" });
    if (!res.ok) {
      toast("停止に失敗しました");
      return;
    }
    void refreshSessions();
    setTimeout(() => void refreshSessions(), 1200);
  };

  const recreate = async (name: string, display: string) => {
    if (
      !(await askConfirm({
        title: "新しい会話で作り直す",
        body: (
          <>
            「{display}」を新しいセッションで開始します。
            <br />
            今の会話は<strong>アーカイブに退避</strong>し、あとで復帰できます。
          </>
        ),
        confirmLabel: "作り直す",
        danger: false,
      }))
    )
      return;
    const res = await raw(`api/sessions/${encodeURIComponent(name)}/recreate`, { method: "POST" });
    if (!res.ok) {
      let msg = "作り直しに失敗しました";
      try {
        const j = await res.json();
        if (j?.error?.message) msg += "：" + j.error.message;
      } catch {}
      toast(msg);
      void refreshSessions();
      return;
    }
    const created = await res.json().catch(() => null);
    const newName = created?.name || name;
    if (newName !== name) closeSessionPanes(name);
    // Open the fresh session: claude → live chat mirror, others → terminal.
    (created && agentOf(created.kind).caps.chat ? openSessionChat : openSessionTerminal)(newName);
    void refreshSessions();
    setTimeout(() => void refreshSessions(), 1200);
  };

  const switchDriver = async (s: Session) => {
    const toManaged = s.driver !== "managed";
    const target = toManaged ? "managed" : "tui";
    const tuiMemoryCost = agentOf(s.kind).tuiMemoryCost;
    if (
      !(await askConfirm({
        title: toManaged ? "Managed ドライバに切り替え" : "CLI (TUI) ドライバに切り替え",
        body: toManaged ? (
          <>
            「{displayName(s)}」を共有ランタイム駆動（チャット専用・省メモリ）へ切り替えます。
            <br />
            会話は引き継がれます。ターミナル画面は使えなくなります。
          </>
        ) : (
          <>
            「{displayName(s)}」をターミナル（TUI）駆動へ切り替えます。
            <br />
            会話は引き継がれます。セッション毎に TUI プロセス分のメモリ
            {tuiMemoryCost ? `（+${tuiMemoryCost}）` : ""}を消費します。
          </>
        ),
        confirmLabel: "切り替える",
        danger: false,
      }))
    )
      return;
    const res = await raw(`api/sessions/${encodeURIComponent(s.name)}/driver`, {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ driver: target }),
    });
    if (!res.ok) {
      const j = await res.json().catch(() => null);
      const code = j?.error?.code;
      toast(
        code === "busy_switch"
          ? "実行中のターンがあります。完了を待つか停止してから切り替えてください"
          : j?.error?.message || "ドライバの切り替えに失敗しました",
      );
      return;
    }
    // 旧ドライバのペイン（managed 化ならターミナル）は無効になる — 開き直す。
    closeSessionPanes(s.name);
    openSessionChat(s.name);
    void refreshSessions();
    setTimeout(() => void refreshSessions(), 1200);
  };

  const fork = async (name: string) => {
    const res = await raw(`api/sessions/${encodeURIComponent(name)}/fork`, { method: "POST" });
    const j = await res.json().catch(() => ({}) as any);
    if (!res.ok || !j.name) {
      toast(j?.error?.message || j?.error || "分岐に失敗しました");
      return;
    }
    void refreshSessions();
    openSessionChatSplit(j.name); // the fork inherits the history → open as chat
    setTimeout(() => void refreshSessions(), 1200);
  };

  return { archive, deleteSession, clearStopped, clearOrphans, halt, recreate, fork, switchDriver };
}
