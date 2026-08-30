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
import { sessionFolder } from "../../lib/project.ts";
import { useSettings } from "../../lib/settings.ts";
import { workingSetList, toggleWorkingSetMember } from "../../lib/workingSetsStore.ts";
import { useReposStore } from "../repos/store.ts";
import { useT } from "../../lib/i18n/index.ts";
import { displayName, stateInfo, exitLabel, remainingShort, KEEP_AWAKE_HOURS } from "../../lib/sessionview.ts";
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
import { writeLS } from "../mirror/transcript/blocks.tsx";
import { useSessionUI } from "./ui.ts";
import { useSessionsStore } from "./store.ts";
import { HandoffModal } from "./HandoffModal.tsx";
import { ShareCreateModal } from "../sharing/ShareCreateModal.tsx";
import { useMySharesStore } from "../sharing/store.ts";
import type { SessionActions } from "./useSessionActions.tsx";
import type { Session } from "../../types/session.ts";

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
  const startSession = useSessionsStore((st) => st.start);
  const { hover, setHover } = usePaneHover();
  const toast = useToast();
  const tr = useT();
  const menuRef = useRef<HTMLDivElement>(null);
  const menuElRef = useRef<HTMLDivElement>(null);
  const menuBtnRef = useRef<HTMLButtonElement>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const [handoffOpen, setHandoffOpen] = useState(false);
  const [shareOpen, setShareOpen] = useState(false);
  const myShares = useMySharesStore((st) => st.shares);
  const isShared = myShares.some((sh) => sh.scope.type === "session" && sh.scope.key === s.name);
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
  // A dir-missing session keeps its transcript (stored under the agent's home,
  // e.g. ~/.claude, not in the deleted working dir), so a chat-capable one still
  // opens read-only history — only resume is gone. inert = truly non-clickable
  // (no transcript to show and can't resume): those stay grayed/disabled.
  const historyOnly = dead && agentOf(s.kind).caps.transcript;
  const inert = dead && !historyOnly;
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
  // 作業グループ (docs/log/52): direct assignment is for repo-less sessions only —
  // a session living in a working copy inherits that repo's membership instead.
  const wsets = workingSetList(useSettings());
  const repos = useReposStore((st) => st.repos);
  const folder = sessionFolder(s);
  const repoLess = !folder || !repos.some((r) => r.name === folder);

  return (
    <li
      className={
        "sess-row" +
        (selected ? " active" : "") +
        (hl ? " hover" : "") +
        (s.alive ? "" : " stopped") +
        (inert ? " dead" : "")
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
          (inert
            ? tr("srow.cant_resume")
            : historyOnly
              ? tr("srow.history_only")
              : !s.alive
                ? (ex ? ex.hint + "\n" : "") +
                  tr("srow.stopped_hint")
                : (s.dir || "") + (s.subdir ? "/" + s.subdir : "") + tr("srow.open_pane_suffix")) +
          `\n${kindLabel(s.kind)} · ${st.text}\nID: ${s.name}`
        }
        aria-disabled={inert || undefined}
        onClick={(e) => {
          const split = e.ctrlKey || e.metaKey;
          if (!s.alive) {
            // Stopped chat-capable (claude) → read-only chat history (no resume;
            // resume happens inside the chat). A dir-missing (dead) session keeps
            // its transcript, so history stays viewable — only resume is blocked.
            if (agentOf(s.kind).caps.transcript) {
              (split ? openSessionChatSplit : openSessionChat)(s.name);
            } else if (!inert && running) {
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
          if (e.button !== 1 || inert) return;
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
        {/* 削除ロック（docs/log/45）: 鍵バッジ。「なぜ削除が押せないのか」を行の上で示す。 */}
        {s.locked && <Icon name="lock" className="sess-lock" title={tr("srow.locked_badge")} />}
        {/* 停止しないピン（docs/log/75）: 期限が生きている間だけ出す。切れたピンをバッジに
            残すと「守られているつもり」で放置されるので、時計は表示側でも見る。 */}
        {remainingShort(s.keepAwakeUntil) && (
          <Icon
            name="debug-pause"
            className="sess-awake"
            title={tr("srow.keep_awake_badge", { left: remainingShort(s.keepAwakeUntil) })}
          />
        )}
        {isShared && <Icon name="broadcast" className="sess-shared" title={tr("srow.shared_badge")} />}
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
                {/* Chat-capable kinds (claude/codex/opencode) have no in-menu resume today —
                    opening the row shows the mirror read-only, and its own "再開して続ける"
                    button is the only resume path. This mirrors that same POST /start then
                    opens the chat pane directly. */}
                {!s.alive && !dead && running && agentOf(s.kind).caps.chat && (
                  <button
                    type="button"
                    className="ui-menu-item"
                    onClick={() => {
                      setMenuOpen(false);
                      void startSession(s.name).then(() => openSessionChat(s.name));
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
                {/* 変更ファイル（docs/log/68）— ミラーを開いて帯を広げる。専用のペイン種は
                    作らない（ADR 0049 決定 4）ので、「覗く」導線はミラーを開くこと自体で、
                    その開閉状態は帯が per-session に憶えている localStorage を先に書いて
                    渡す。転写を持たない kind（shell/ssm）には一覧の材料が無い。 */}
                {agentOf(s.kind).caps.transcript && (
                  <button
                    type="button"
                    className="ui-menu-item"
                    onClick={() => {
                      setMenuOpen(false);
                      writeLS("af.mirror-files-open." + s.name, "1");
                      openSessionChat(s.name);
                    }}
                  >
                    <Icon name="edit" /> {tr("srow.changed_files")}
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
                {/* Handoff to another agent — one unified item for every session that
                    has a conversation (caps.transcript); the target is chosen in the
                    modal, which then calls the operator assistant directly. */}
                {actions && agentOf(s.kind).caps.transcript && !dead && running && (
                  <button
                    type="button"
                    className="ui-menu-item"
                    onClick={() => {
                      setMenuOpen(false);
                      setHandoffOpen(true);
                    }}
                  >
                    <Icon name="git-branch" /> {tr("srow.handoff")}
                  </button>
                )}
                {/* セッション共有: 同一テナントの別ユーザーへ会話を共有する。shell/SSM は
                    transcript が無く共有対象外(docs/log/59)。 */}
                {agentOf(s.kind).caps.transcript && (
                  <button
                    type="button"
                    className="ui-menu-item"
                    onClick={() => {
                      setMenuOpen(false);
                      setShareOpen(true);
                    }}
                  >
                    <Icon name="broadcast" /> {tr("srow.share")}
                  </button>
                )}
                {/* 作業グループ (docs/log/52): membership toggles — repo-less rows only. */}
                {repoLess && wsets.length > 0 && (
                  <>
                    <div className="ui-menu-caption">{tr("wset.menu_caption")}</div>
                    {wsets.map((w) => (
                      <button
                        key={w.id}
                        type="button"
                        className="ui-menu-item"
                        onClick={() => {
                          setMenuOpen(false);
                          toggleWorkingSetMember(w.id, "sessions", s.name);
                        }}
                      >
                        <Icon name="check" className={w.sessions.includes(s.name) ? "wset-check" : "wset-check off"} /> {w.name}
                      </button>
                    ))}
                  </>
                )}
                {/* 削除ロック（docs/log/45）: この行を削除保護に固定/解除する。保護の実体は
                    Agent 側（403）なので、ここは切替と見た目の抑止だけを担う。 */}
                <button
                  type="button"
                  className="ui-menu-item"
                  onClick={() => {
                    setMenuOpen(false);
                    void actions.setLocked(s, !s.locked);
                  }}
                >
                  <Icon name={s.locked ? "unlock" : "lock"} />{" "}
                  {s.locked ? tr("srow.unlock") : tr("srow.lock")}
                </button>
                {/* 停止しないピン（docs/log/75）: アイドル自動停止からこのセッションと Workspace を
                    期限付きで守る。shell / ssm では「ジョブが走っているか」を af が判定できない
                    （放置された less と実行中のビルドが前景コマンド名で区別できず、ssm は常に
                    aws を張る）ため、推測ではなく宣言に倒した逃げ道。 */}
                <button
                  type="button"
                  className="ui-menu-item"
                  title={tr("srow.keep_awake_hint")}
                  onClick={() => {
                    setMenuOpen(false);
                    void actions.setKeepAwake(s, remainingShort(s.keepAwakeUntil) ? 0 : KEEP_AWAKE_HOURS);
                  }}
                >
                  <Icon name="watch" />{" "}
                  {remainingShort(s.keepAwakeUntil)
                    ? tr("srow.keep_awake_off")
                    : tr("srow.keep_awake_on", { hours: KEEP_AWAKE_HOURS })}
                </button>
                {agentOf(s.kind).caps.ephemeral ? (
                  <button
                    type="button"
                    className="ui-menu-item danger"
                    disabled={s.locked}
                    title={s.locked ? tr("srow.locked_hint") : undefined}
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
      {handoffOpen && actions && (
        <HandoffModal session={s} actions={actions} onClose={() => setHandoffOpen(false)} />
      )}
      {shareOpen && (
        <ShareCreateModal initialTarget={`session:${s.name}`} onClose={() => setShareOpen(false)} />
      )}
    </li>
  );
}
