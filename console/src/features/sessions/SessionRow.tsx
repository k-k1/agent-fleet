// SessionRow — one single-line session row plus its ⋯/right-click menu: a
// kind-colored leading icon, the title, and a compact state chip (icon-only for
// the calm states; question/plan/permission keep their text — they demand the
// user). Renders in the flat "その他" list AND under each working-copy node of
// the project tree. Self-contained: it owns its menu open state (outside-click /
// Escape dismiss) and reads pane-hover + layout itself.
// Lifecycle ops come from useSessionActions; the three modal triggers (rename,
// branch-rename, SSM resume) are lifted to the parent via callbacks since their
// dialogs render at section level.
import { createPortal } from "react-dom";
import { useLayoutEffect, useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { copyText } from "../../lib/clipboard.ts";
import { placeFixed } from "../../lib/placeFixed.ts";
import { usePaneHover } from "../../lib/panehover.tsx";
import { kindIcon, kindLabel, kindClass } from "../../lib/sessionkind.ts";
import { useT } from "../../lib/i18n/index.ts";
import { displayName, stateInfo, exitLabel } from "../../lib/sessionview.ts";
import { agentOf } from "../../agents/registry.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { ordClass } from "../../layout/badges.ts";
import { useTtsStore } from "../../core/store/tts.ts";
import {
  openSessionTerminal,
  openSessionTerminalSplit,
  openSessionChat,
  openSessionChatSplit,
} from "./open.ts";
import { useSessionUI } from "./ui.ts";
import type { SessionActions } from "./useSessionActions.tsx";
import type { Session, SessionKind } from "../../types/session.ts";

interface SessionRowProps {
  s: Session;
  selected: boolean;
  /** Panes showing this session (ordinal badges); empty when unsplit. */
  opens: { ordinal: number; id: string }[];
  /** True when the layout is split (badges/cross-highlight are dormant otherwise). */
  multi: boolean;
  running: boolean;
  actions?: SessionActions;
  /** History-only rail used while the workspace agent is unavailable. */
  readOnly?: boolean;
}

export function SessionRow({ s, selected, opens, multi, running, actions, readOnly = false }: SessionRowProps) {
  const setActive = useLayoutStore((st) => st.setActive);
  const openRename = useSessionUI((u) => u.openRename);
  const openBranchRename = useSessionUI((u) => u.openBranchRename);
  const openSsmResume = useSessionUI((u) => u.openSsmResume);
  const { hover, setHover } = usePaneHover();
  const toast = useToast();
  const tr = useT();
  const menuRef = useRef<HTMLDivElement>(null);
  const menuElRef = useRef<HTMLDivElement>(null);
  const menuBtnRef = useRef<HTMLButtonElement>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  useDismiss([menuRef, menuElRef], menuOpen, () => setMenuOpen(false));
  useMenuRoving(menuElRef, menuOpen);
  // The dropdown is position:fixed, anchored under the ⋯ button and clamped
  // on-screen every render — a row near the rail's foot must not push its menu
  // below the viewport (the old absolute top:26px did).
  useLayoutEffect(() => {
    const el = menuElRef.current;
    const anchor = menuBtnRef.current;
    if (!menuOpen || !el || !anchor) return;
    const a = anchor.getBoundingClientRect();
    placeFixed(el, a.right - el.offsetWidth, a.bottom + 2, menuBtnRef.current?.closest<HTMLElement>(".app-rail"));
  });
  // The session's immutable id (e.g. "sk7f3q9") — the thing shown as ID in the
  // row tooltip. The menu label shows the concrete value, not jargon ("slug").
  const copyId = () => {
    void copyText(s.name).then((ok) =>
      ok ? toast(tr("srow.id_copied", { name: s.name }), { kind: "success" }) : toast(tr("common.copy_failed")),
    );
  };

  const dead = !s.alive && s.resumable === false; // dir gone → can't resume
  const open = opens.length > 0;
  const hl = open && hover?.session === s.name;
  const st = stateInfo(s);
  // For a stopped session that ended abnormally, the reason detail (OOM / crash) rides
  // the row tooltip alongside the resume hint.
  const ex = !s.alive ? exitLabel(s) : null;
  // question/plan/permission want the user — keep their text. Everything else
  // (入力待ち/進行中/停止中…) collapses to an icon-only chip; the text moves to title.
  const loud = st.cls.includes("question");
  // このセッションの回答を音声読み上げ中か（ミラー朗読・要約・セッション通知いずれも
  // 発生元セッション名を tts ストアへ載せている）。合成待ち（preparing）も含めて示す。
  const speaking = useTtsStore((t) => t.sessionName === s.name && (t.speaking || t.preparing));

  return (
    <li
      className={
        "sess-row" +
        (selected ? " active" : "") +
        (hl ? " hover" : "") +
        (s.alive ? "" : " stopped") +
        (dead ? " dead" : "")
      }
      onMouseEnter={open ? () => setHover({ session: s.name }) : undefined}
      onMouseLeave={open ? () => setHover(null) : undefined}
      // Right-click opens the same ⋯ menu (open on the trailing contextMenu event
      // so the outside-click listener doesn't immediately close it).
      onContextMenu={(e) => {
        if (readOnly) return;
        e.preventDefault();
        setMenuOpen(true);
      }}
    >
      <button
        type="button"
        className="sess-btn"
        data-rail-row=""
        role="treeitem"
        title={
          // Full display name first — the row ellipsizes it in the narrow rail.
          displayName(s) +
          "\n" +
          (dead
            ? tr("srow.cant_resume")
            : !s.alive
              ? (ex ? ex.hint + "\n" : "") +
                tr("srow.stopped_hint")
              : (s.dir || "") + tr("srow.open_pane_suffix")) +
          `\n${kindLabel(s.kind)} · ${st.text}\nID: ${s.name}`
        }
        aria-disabled={dead || undefined}
        onClick={(e) => {
          const split = e.ctrlKey || e.metaKey;
          if (!s.alive) {
            // Stopped chat-capable (claude) → read-only chat history (no resume;
            // resume happens inside the chat). Other kinds: ⋯ menu.
            if (!dead && agentOf(s.kind).caps.transcript) {
              (split ? openSessionChatSplit : openSessionChat)(s.name);
            } else if (!dead && running) {
              // Shell/SSM have a bounded terminal replay instead of a structured
              // transcript. Opening it never resumes the stopped session.
              (split ? openSessionTerminalSplit : openSessionTerminal)(s.name);
            }
            return;
          }
          // Alive: chat-capable opens the mirror (PTY attaches in the bg);
          // other kinds open the terminal directly.
          const chat = agentOf(s.kind).caps.chat;
          (chat
            ? split ? openSessionChatSplit : openSessionChat
            : split ? openSessionTerminalSplit : openSessionTerminal)(s.name);
        }}
        onMouseDown={(e) => e.button === 1 && e.preventDefault()}
        onAuxClick={(e) => {
          if (e.button !== 1 || dead) return;
          e.preventDefault();
          if (s.alive) (agentOf(s.kind).caps.chat ? openSessionChatSplit : openSessionTerminalSplit)(s.name);
          else if (agentOf(s.kind).caps.transcript) openSessionChatSplit(s.name);
          else if (running) openSessionTerminalSplit(s.name);
        }}
      >
        {/* Leading kind icon: color says claude/codex/… so the text tag is gone. */}
        <span className={"sess-kic kind-" + kindClass(s.kind)} title={kindLabel(s.kind)}>
          <Icon name={kindIcon(s.kind)} />
        </span>
        <span className="sess-l1">{displayName(s)}</span>
        {/* Branch drift: the working copy left the branch this session started
            on — the agent's tree may be swapped out under it. */}
        {s.branchDrift && (
          <span
            className="sess-drift"
            title={tr("srow.branch_switched", { from: s.branch ?? "", to: s.currentBranch ?? "" })}
          >
            <Icon name="warning" /> {s.currentBranch}
          </span>
        )}
        {speaking && (
          <Icon name="unmute" className="sess-speaking" title={tr("srow.speaking")} />
        )}
        <span className={"session-state " + st.cls + (loud ? "" : " mini")} title={st.text}>
          <Icon name={st.icon} spin={st.spin} />
          {loud && <> {st.text}</>}
        </span>
      </button>
      {/* Ordinal badges: pane numbers for a session shown in ≥1 panes; click
          focuses that pane. Only while split. */}
      {multi && opens.length > 0 && (
        <div className="sess-ords">
          {opens.map((o) => (
            <button
              key={o.id}
              type="button"
              className={"rail-ord " + ordClass(o.ordinal)}
              title={tr("common.focus_pane", { ordinal: o.ordinal })}
              onClick={(e) => {
                e.stopPropagation();
                setActive(o.id);
              }}
              onMouseEnter={() => setHover({ session: s.name, paneId: o.id })}
              onMouseLeave={() => setHover(null)}
            >
              {o.ordinal}
            </button>
          ))}
        </div>
      )}
      {!readOnly && actions && (
        <div className="sess-menu-wrap" ref={menuOpen ? menuRef : undefined}>
          <button type="button" className="sess-menu-btn" title={tr("srow.menu")} ref={menuBtnRef} onClick={() => setMenuOpen((v) => !v)}>
            <Icon name="ellipsis" />
          </button>
          {menuOpen &&
            createPortal(
              <div className="ui-menu sess-menu" ref={menuElRef} onMouseDown={(e) => e.stopPropagation()}>
                {/* Resume — kinds with no in-chat resume. SSM resumes through the login
                    modal (SSO handshake before attach). */}
                {!s.alive && !dead && running && !agentOf(s.kind).caps.chat && (
                  <button
                    type="button"
                    className="ui-menu-item"
                    onClick={() => {
                      setMenuOpen(false);
                      if (s.kind === "ssm") openSsmResume(s.name, false);
                      else openSessionTerminal(s.name);
                    }}
                  >
                    <Icon name="play" /> {tr("srow.resume")}
                  </button>
                )}
                {!s.alive && !dead && running && s.kind === "ssm" && (
                  <button
                    type="button"
                    className="ui-menu-item"
                    onClick={() => {
                      setMenuOpen(false);
                      openSsmResume(s.name, true);
                    }}
                  >
                    <Icon name="key" /> {tr("srow.relogin_resume")}
                  </button>
                )}
                {s.alive && (
                  <button
                    type="button"
                    className="ui-menu-item"
                    onClick={() => {
                      setMenuOpen(false);
                      void actions.halt(s.name, displayName(s));
                    }}
                  >
                    <Icon name="debug-stop" /> {tr("srow.stop")}
                  </button>
                )}
                {!dead && running && agentOf(s.kind).managedDriver && (
                  <button
                    type="button"
                    className="ui-menu-item"
                    onClick={() => {
                      setMenuOpen(false);
                      void actions.switchDriver(s);
                    }}
                  >
                    <Icon name={s.driver === "managed" ? "terminal" : "server-process"} />
                    {s.driver === "managed" ? tr("sess.switch_to_tui") : tr("sess.switch_to_managed")}
                  </button>
                )}
                {s.remoteUrl && (
                  <button
                    type="button"
                    className="ui-menu-item"
                    onClick={() => {
                      setMenuOpen(false);
                      window.open(s.remoteUrl, "_blank", "noopener");
                    }}
                  >
                    <Icon name="link-external" /> {tr("srow.open_remote")}
                  </button>
                )}
                <button
                  type="button"
                  className="ui-menu-item"
                  onClick={() => {
                    setMenuOpen(false);
                    copyId();
                  }}
                >
                  <Icon name="copy" /> {tr("srow.copy_id", { name: s.name })}
                </button>
                <button
                  type="button"
                  className="ui-menu-item"
                  onClick={() => {
                    setMenuOpen(false);
                    openRename(s);
                  }}
                >
                  <Icon name="edit" /> {tr("srow.rename")}
                </button>
                {/* Worktree sessions only: rename the worktree's branch (deferred
                    naming); AI suggestion uses THIS session. */}
                {s.worktree && (
                  <button
                    type="button"
                    className="ui-menu-item"
                    onClick={() => {
                      setMenuOpen(false);
                      openBranchRename(s);
                    }}
                  >
                    <Icon name="repo-forked" /> {tr("srow.rename_branch")}
                  </button>
                )}
                {agentOf(s.kind).caps.fork && !dead && running && (
                  <>
                    {(["claude", "codex", "opencode"] as SessionKind[]).map((kind) => (
                      <button
                        key={kind}
                        type="button"
                        className="ui-menu-item"
                        onClick={() => {
                          setMenuOpen(false);
                          void actions.fork(s.name, kind);
                        }}
                      >
                        <Icon name={kind === s.kind ? "git-branch" : agentOf(kind).icon} /> {tr("srow.fork_to", { agent: agentOf(kind).label })}
                      </button>
                    ))}
                  </>
                )}
                {agentOf(s.kind).caps.ephemeral ? (
                  <button
                    type="button"
                    className="ui-menu-item danger"
                    onClick={() => {
                      setMenuOpen(false);
                      void actions.deleteSession(s);
                    }}
                  >
                    <Icon name="trash" /> {tr("common.delete_do")}
                  </button>
                ) : (
                  <button
                    type="button"
                    className="ui-menu-item"
                    onClick={() => {
                      setMenuOpen(false);
                      void actions.archive(s);
                    }}
                  >
                    <Icon name="archive" /> {tr("srow.archive")}
                  </button>
                )}
                {!dead && (
                  <button
                    type="button"
                    className="ui-menu-item"
                    onClick={() => {
                      setMenuOpen(false);
                      void actions.recreate(s.name, displayName(s));
                    }}
                  >
                    <Icon name="refresh" /> {tr("srow.recreate")}
                  </button>
                )}
              </div>,
              document.body,
            )}
        </div>
      )}
    </li>
  );
}
