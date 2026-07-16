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
import { t, useT } from "../../lib/i18n/index.ts";
import { Trans } from "../../lib/i18n/Trans.tsx";
import type { Session } from "../../types/session.ts";

export interface SessionActions {
  /** Hide from the list but KEEP it (restorable). Live sessions stop first. */
  archive(s: Session): Promise<void>;
  /** Delete outright (shell/ssm — no conversation worth keeping). Irreversible. */
  deleteSession(s: Session): Promise<void>;
  /** Clear all stopped: agent sessions archive (restorable), shell/ssm delete. */
  clearStopped(): Promise<void>;
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
  const tr = useT();
  const closeSessionPanes = useLayoutStore((s) => s.closeSessionPanes);
  const refreshSessions = useSessionsStore((s) => s.refresh);

  const archive = async (s: Session) => {
    const res = await raw(`api/sessions/${encodeURIComponent(s.name)}/archive`, { method: "POST" });
    if (!res.ok) {
      toast(t("sess.archive_failed"));
      return;
    }
    closeSessionPanes(s.name);
    void refreshSessions();
  };

  const deleteSession = async (s: Session) => {
    if (
      !(await askConfirm({
        title: tr("sess.delete_title"),
        body: tr("sess.delete_body", { name: displayName(s) }),
        confirmLabel: tr("common.delete_do"),
        danger: true,
      }))
    )
      return;
    const res = await raw(`api/sessions/${encodeURIComponent(s.name)}/stop`, { method: "POST" });
    if (!res.ok) {
      toast(t("common.delete_failed"));
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
    if (keepable.length) parts.push(t("sess.cleanup_archive_n", { count: keepable.length }));
    if (ephemeral.length) parts.push(t("sess.cleanup_delete_n", { count: ephemeral.length }));
    if (
      !(await askConfirm({
        title: tr("sess.cleanup_title"),
        body: tr("sess.cleanup_body", { parts: parts.join(tr("common.list_sep")) }),
        confirmLabel: tr("sess.cleanup_confirm"),
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

  const halt = async (name: string, display: string) => {
    if (
      !(await askConfirm({
        title: tr("sess.stop_title"),
        body: tr("sess.stop_body", { name: display }),
        confirmLabel: tr("sess.stop_confirm"),
        danger: false,
      }))
    )
      return;
    const res = await raw(`api/sessions/${encodeURIComponent(name)}/halt`, { method: "POST" });
    if (!res.ok) {
      toast(t("sess.stop_failed"));
      return;
    }
    void refreshSessions();
    setTimeout(() => void refreshSessions(), 1200);
  };

  const recreate = async (name: string, display: string) => {
    if (
      !(await askConfirm({
        title: tr("sess.recreate_title"),
        body: <Trans k="session.recreate_body" vars={{ name: display }} components={[<br />, <strong />]} />,
        confirmLabel: tr("sess.recreate_confirm"),
        danger: false,
      }))
    )
      return;
    const res = await raw(`api/sessions/${encodeURIComponent(name)}/recreate`, { method: "POST" });
    if (!res.ok) {
      let msg = t("sess.recreate_failed");
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
        title: toManaged ? tr("sess.switch_to_managed") : tr("sess.switch_to_tui"),
        body: toManaged ? (
          <Trans k="sess.switch_managed_body" vars={{ name: displayName(s) }} components={[<br />]} />
        ) : (
          <Trans
            k="sess.switch_tui_body"
            vars={{ name: displayName(s), cost: tuiMemoryCost ? `（+${tr("common.approx", { v: tuiMemoryCost })}）` : "" }}
            components={[<br />]}
          />
        ),
        confirmLabel: tr("sess.switch_confirm"),
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
          ? t("sess.switch_busy")
          : j?.error?.message || t("sess.switch_failed"),
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
      toast(j?.error?.message || j?.error || t("sess.fork_failed"));
      return;
    }
    void refreshSessions();
    openSessionChatSplit(j.name); // the fork inherits the history → open as chat
    setTimeout(() => void refreshSessions(), 1200);
  };

  return { archive, deleteSession, clearStopped, halt, recreate, fork, switchDriver };
}
