// Session lifecycle actions, extracted from SessionsSection so the same handlers
// drive a session row wherever it renders (the flat list AND the per-working-copy
// nodes of the project tree). Each op hits the Agent, then refreshes the store and
// closes any stale panes; confirmations and error toasts are built in.
import { raw, sessionSetLock, sessionKeepAwake } from "../../core/api/client.ts";
import { useConfirm } from "../../ui/ConfirmProvider.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useLayoutStore } from "../../layout/store.ts";
import { displayName } from "../../lib/sessionview.ts";
import { agentOf } from "../../agents/registry.ts";
import { useSessionsStore } from "./store.ts";
import { openSessionChat, openSessionTerminal } from "./open.ts";
import { chatCreate } from "../chat/api.ts";
import { openChat } from "../chat/open.ts";
import { autoAddToActiveWorkingSet } from "../../lib/workingSetsStore.ts";
import { t, useT, getLocale } from "../../lib/i18n/index.ts";
import { Trans } from "../../lib/i18n/Trans.tsx";
import type { Session } from "../../types/session.ts";

export interface SessionActions {
  /** Hide from the list but KEEP it (restorable). Live sessions stop first. */
  archive(s: Session): Promise<void>;
  /** Delete outright (shell/ssm — no conversation worth keeping). Irreversible. */
  deleteSession(s: Session): Promise<void>;
  /** Toggle the deletion lock (docs/log/45). While on, this row cannot be removed by any
   *  deletion path: Console, cleanup, the 7-day auto-prune, or the operator. */
  setLocked(s: Session, locked: boolean): Promise<void>;
  setKeepAwake(s: Session, hours: number): Promise<void>;
  /** Bulk-clear every "other session" (an orphan whose working copy is gone):
   * agent sessions archive (restorable), shell/ssm delete. Repo-scoped stopped
   * sessions are bulk-cleared from the Cleanup modal's stage ① instead. */
  clearOrphans(orphans: Session[]): Promise<void>;
  /** Bulk-tidy every STOPPED session in a scope (the right-click repo menu passes
   * that folder's own sessions — a worktree's are a separate folder, so a parent
   * repo's sweep never touches them). Same split as the Cleanup modal's stage ①:
   * agent sessions archive (restorable), shell/ssm delete. Locked and live
   * sessions are left alone. */
  archiveStopped(sessions: Session[]): Promise<void>;
  /** Halt a live session into "stopped" (resumable): kills tmux, keeps the meta. */
  halt(name: string, display: string): Promise<void>;
  /** Mint a NEW live session (fresh slug, same title/dir/model), archive the old. */
  recreate(name: string, display: string): Promise<void>;
  /** Hand this session's conversation off to a target agent: opens an operator chat and
   *  auto-fires the extraction turn; the operator proposes a summary and only creates the
   *  new session after the user consents. `note` is optional extra instruction. */
  handoff(name: string, kind: string, note?: string): Promise<void>;
  /** Exclusive driver switch (docs/log/27 P3 §2): tui ⇄ managed via stop→drain→resume. */
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

  const setLocked = async (s: Session, locked: boolean) => {
    const res = await sessionSetLock(s.name, locked);
    if (res?.error) {
      toast(t("sess.lock_failed"));
      return;
    }
    // The accepted POST is authoritative. Do not leave the row wording to a
    // cached or in-flight list refresh.
    useSessionsStore.getState().setLocked(s.name, res?.locked ?? locked);
    toast(locked ? t("sess.locked_on") : t("sess.locked_off"), { kind: "success" });
    void refreshSessions();
  };

  // Keep-awake pin (docs/log/75): hours>0 sets a deadline, 0 clears it. af cannot tell
  // from the outside whether a shell/ssm job is still running, so the user declares it
  // rather than the container being protected on a guess.
  const setKeepAwake = async (s: Session, hours: number) => {
    const res = await sessionKeepAwake(s.name, hours);
    if (res?.error) {
      toast(t("sess.keep_awake_failed"));
      return;
    }
    useSessionsStore.getState().setKeepAwake(s.name, res?.keepAwakeUntil ?? "");
    toast(hours > 0 ? t("sess.keep_awake_on", { hours }) : t("sess.keep_awake_off"), { kind: "success" });
    void refreshSessions();
  };

  const clearOrphans = async (all: Session[]) => {
    const orphans = all.filter((s) => !s.locked); // deletion-locked rows (docs/log/45) are never bulk-cleared
    if (orphans.length === 0) return;
    // Same split as the Cleanup modal's stage ①: agent sessions archive (conversation kept,
    // restorable); shell/ssm have no conversation worth keeping, so they delete.
    const ephemeral = orphans.filter((s) => agentOf(s.kind).caps.ephemeral);
    const keepable = orphans.filter((s) => !agentOf(s.kind).caps.ephemeral);
    const alive = orphans.filter((s) => s.alive).length;
    const parts: string[] = [];
    if (keepable.length) parts.push(t("sess.cleanup_archive_n", { count: keepable.length }));
    if (ephemeral.length) parts.push(t("sess.cleanup_delete_n", { count: ephemeral.length }));
    if (
      !(await askConfirm({
        title: tr("sess.tidy_orphans_title"),
        body: (
          <>
            {tr("sess.tidy_orphans_body", { parts: parts.join(tr("common.list_sep")) })}
            {alive > 0 ? <> {tr("sess.tidy_orphans_alive", { count: alive })}</> : null}
            <br />
            <Trans k="sess.tidy_orphans_restore" components={[<strong />]} />
            {ephemeral.length > 0 ? <> {tr("sess.tidy_orphans_irreversible")}</> : null}
          </>
        ),
        confirmLabel: tr("sess.cleanup_confirm"),
        danger: ephemeral.length > 0,
      }))
    )
      return;
    // /archive hides but KEEPS the meta/jsonl (restorable); /stop forgets it
    // (shell/ssm delete). Best-effort per session so one failure doesn't abort
    // the rest — mirrors the per-row archive / delete. Count the successes and toast the
    // result: if every call 403s or 409s, saying nothing looks like it worked.
    const call = (s: Session, ep: "archive" | "stop") =>
      raw(`api/sessions/${encodeURIComponent(s.name)}/${ep}`, { method: "POST" })
        .then((res) => ({ s, ok: res.ok }))
        .catch(() => ({ s, ok: false }));
    const results = await Promise.all([
      ...keepable.map((s) => call(s, "archive")),
      ...ephemeral.map((s) => call(s, "stop")),
    ]);
    const done = results.filter((r) => r.ok).length;
    const failed = results.length - done;
    // A failed row survives — don't close its pane either.
    for (const r of results) if (r.ok) closeSessionPanes(r.s.name);
    toast(failed ? t("clean.run_done", { done, failed }) : t("clean.run_done_ok", { done }));
    void refreshSessions();
  };

  const archiveStopped = async (all: Session[]) => {
    const targets = all.filter((s) => !s.alive && !s.locked); // live and deletion-locked rows are out of scope
    if (targets.length === 0) return;
    // Same split as the Cleanup modal's stage ①: agent sessions archive (restorable);
    // shell/ssm have no conversation worth keeping, so they delete.
    const ephemeral = targets.filter((s) => agentOf(s.kind).caps.ephemeral);
    const keepable = targets.filter((s) => !agentOf(s.kind).caps.ephemeral);
    const parts: string[] = [];
    if (keepable.length) parts.push(t("sess.cleanup_archive_n", { count: keepable.length }));
    if (ephemeral.length) parts.push(t("sess.cleanup_delete_n", { count: ephemeral.length }));
    if (
      !(await askConfirm({
        title: tr("clean.stage1_confirm_title"),
        body: tr("clean.stage1_confirm_body", { parts: parts.join(tr("common.list_sep")) }),
        confirmLabel: tr("clean.confirm_do", { count: targets.length }),
        danger: ephemeral.length > 0,
      }))
    )
      return;
    const call = (s: Session, ep: "archive" | "stop") =>
      raw(`api/sessions/${encodeURIComponent(s.name)}/${ep}`, { method: "POST" })
        .then((res) => ({ s, ok: res.ok }))
        .catch(() => ({ s, ok: false }));
    const results = await Promise.all([
      ...keepable.map((s) => call(s, "archive")),
      ...ephemeral.map((s) => call(s, "stop")),
    ]);
    const done = results.filter((r) => r.ok).length;
    const failed = results.length - done;
    for (const r of results) if (r.ok) closeSessionPanes(r.s.name);
    toast(failed ? t("clean.run_done", { done, failed }) : t("clean.run_done_ok", { done }));
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
        if (j?.error?.message) msg += t("common.detail_sep") + j.error.message;
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
            vars={{ name: displayName(s), cost: tuiMemoryCost ? tr("common.paren", { v: "+" + tr("common.approx", { v: tuiMemoryCost }) }) : "" }}
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
    // The old driver's pane (the terminal, when switching to managed) is now dead — reopen.
    closeSessionPanes(s.name);
    openSessionChat(s.name);
    void refreshSessions();
    setTimeout(() => void refreshSessions(), 1200);
  };

  const handoff = async (name: string, kind: string, note?: string) => {
    const target = kind || "claude";
    // This prompt is unusual for an LLM prompt: openChat auto-sends it, so it renders as
    // the user's OWN message in the operator chat (and steers the reply language in the
    // default outputLanguage=auto). So it must follow the UI locale — an English user must
    // not see a Japanese paragraph as their own turn, nor get a Japanese reply.
    // i18n-exempt-start: LLM prompt (model behaviour, not display; docs/log/28 §4) — locale branch
    const trimmed = note?.trim();
    const prompt =
      getLocale() === "en"
        ? `I'd like to hand off the conversation of session "${name}" to ${target} and start a new session. ` +
          "First, use get_session_output to review the source session, then present a concise handoff summary — " +
          "the key points the new agent needs, unfinished tasks, changed files, and the next steps. " +
          "Do not create the session yet: confirm the handoff summary, working folder, and starting agent with me. " +
          `Only after I explicitly agree, call create_session with kind=${target} and set the approved summary as initial_prompt.` +
          (trimmed ? `\nAdditional instruction from the user: ${trimmed}` : "")
        : `セッション「${name}」の会話を ${target} へ引き継いで新規セッションを始めたいです。` +
          "まず get_session_output で元セッションの状況を確認し、新しいエージェントに必要な要点、未完了タスク、" +
          "変更済みファイル、次の作業を簡潔な引継ぎ案として提示してください。この時点ではセッションを作成せず、" +
          "引継ぎ案・作業フォルダ・開始エージェントを私に確認してください。私が明示的に同意した後だけ、" +
          `kind=${target} で create_session を呼び、承認した引継ぎ案を initial_prompt に設定してください。` +
          (trimmed ? `\n利用者からの補足指示: ${trimmed}` : "");
    // i18n-exempt-end
    try {
      // A dedicated operator conversation preserves any unfinished draft. openChat's auto
      // flag fires this extraction turn as soon as ChatView loads — the assistant is called
      // directly and comes back with a proposal; the consent
      // gate before create_session lives in the prompt above.
      const conv = await chatCreate("operator", tr("srow.handoff_title", { name }));
      if (!conv?.id) throw new Error("conversation was not created");
      autoAddToActiveWorkingSet("convs", conv.id); // docs/log/52 §1: auto-join the selected working set
      openChat(conv.id, prompt, true);
    } catch {
      toast(t("sess.handoff_failed"));
    }
  };

  return { archive, deleteSession, setLocked, setKeepAwake, clearOrphans, archiveStopped, halt, recreate, handoff, switchDriver };
}
