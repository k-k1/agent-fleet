// SessionRow — one two-line session row plus its ⋯/right-click menu, extracted
// from SessionsSection so the same row renders in the flat list AND under each
// working-copy node of the project tree. Self-contained: it owns its menu open
// state (outside-click / Escape dismiss) and reads pane-hover + layout itself.
// Lifecycle ops come from useSessionActions; the three modal triggers (rename,
// branch-rename, SSM resume) are lifted to the parent via callbacks since their
// dialogs render at section level.
import { useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { usePaneHover } from "../../lib/panehover.tsx";
import { kindIcon, kindLabel, kindClass } from "../../lib/sessionkind.ts";
import { displayName, stateInfo } from "../../lib/sessionview.ts";
import { agentOf } from "../../agents/registry.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { ordClass } from "../../layout/badges.ts";
import {
  openSessionTerminal,
  openSessionTerminalSplit,
  openSessionChat,
  openSessionChatSplit,
} from "./open.ts";
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
  actions: SessionActions;
  onRename: (s: Session) => void;
  onBranchRename: (s: Session) => void;
  onResumeSsm: (name: string, force: boolean) => void;
}

export function SessionRow({ s, selected, opens, multi, running, actions, onRename, onBranchRename, onResumeSsm }: SessionRowProps) {
  const setActive = useLayoutStore((st) => st.setActive);
  const { hover, setHover } = usePaneHover();
  const menuRef = useRef<HTMLDivElement>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  useDismiss(menuRef, menuOpen, () => setMenuOpen(false));

  const dead = !s.alive && s.resumable === false; // dir gone → can't resume
  const open = opens.length > 0;
  const hl = open && hover?.session === s.name;
  const st = stateInfo(s);

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
        e.preventDefault();
        setMenuOpen(true);
      }}
    >
      <button
        type="button"
        className="sess-btn"
        title={
          (dead
            ? "作業フォルダが存在しないため再開できません"
            : !s.alive
              ? "停止中（クリックで開いて再開ボタン / Ctrl・中クリックで新ペイン）"
              : (s.dir || "") + "（Ctrl/中クリックで新ペインに開く）") + `\nID: ${s.name}`
        }
        aria-disabled={dead || undefined}
        onClick={(e) => {
          const split = e.ctrlKey || e.metaKey;
          if (!s.alive) {
            // Stopped chat-capable (claude) → read-only chat history (no resume;
            // resume happens inside the chat). Other kinds: ⋯ menu.
            if (!dead && agentOf(s.kind).caps.transcript) {
              (split ? openSessionChatSplit : openSessionChat)(s.name);
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
        }}
      >
        <span className="sess-l1">{displayName(s)}</span>
        <span className="sess-l2">
          <span className={"kind-tag kind-" + kindClass(s.kind)}>
            <Icon name={kindIcon(s.kind)} /> {kindLabel(s.kind)}
          </span>
          <span className={"session-state " + st.cls}>
            <Icon name={st.icon} spin={st.spin} /> {st.text}
          </span>
          {/* Branch drift: the working copy left the branch this session started
              on — the agent's tree may be swapped out under it. */}
          {s.branchDrift && (
            <span
              className="sess-drift"
              title={`このセッションの作業コピーは起動時のブランチ「${s.branch}」から「${s.currentBranch}」へ切り替わっています。稼働中エージェントの作業ツリーが入れ替わり、編集や差分が食い違っている可能性があります。`}
            >
              <Icon name="warning" /> {s.currentBranch}
            </span>
          )}
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
              title={`ペイン${o.ordinal}にフォーカス`}
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
      <div className="sess-menu-wrap" ref={menuOpen ? menuRef : undefined}>
        <button type="button" className="sess-menu-btn" title="メニュー" onClick={() => setMenuOpen((v) => !v)}>
          <Icon name="ellipsis" />
        </button>
        {menuOpen && (
          <div className="ui-menu sess-menu">
            {/* Resume — kinds with no in-chat resume. SSM resumes through the login
                modal (SSO handshake before attach). */}
            {!s.alive && !dead && running && !agentOf(s.kind).caps.chat && (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  setMenuOpen(false);
                  if (s.kind === "ssm") onResumeSsm(s.name, false);
                  else openSessionTerminal(s.name);
                }}
              >
                <Icon name="play" /> 再開する
              </button>
            )}
            {!s.alive && !dead && running && s.kind === "ssm" && (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  setMenuOpen(false);
                  onResumeSsm(s.name, true);
                }}
              >
                <Icon name="key" /> 再ログインして再開
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
                <Icon name="debug-stop" /> 停止する（あとで再開できる）
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
                <Icon name="link-external" /> リモートセッションを開く
              </button>
            )}
            <button
              type="button"
              className="ui-menu-item"
              onClick={() => {
                setMenuOpen(false);
                onRename(s);
              }}
            >
              <Icon name="edit" /> タイトルを変更
            </button>
            {/* Worktree sessions only: rename the worktree's branch (deferred
                naming); AI suggestion uses THIS session. */}
            {s.worktree && (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  setMenuOpen(false);
                  onBranchRename(s);
                }}
              >
                <Icon name="repo-forked" /> ブランチ名を変更
              </button>
            )}
            {agentOf(s.kind).caps.fork && !dead && running && (
              <button
                type="button"
                className="ui-menu-item"
                onClick={() => {
                  setMenuOpen(false);
                  void actions.fork(s.name);
                }}
              >
                <Icon name="git-branch" /> 分岐（会話を引き継いで新規）
              </button>
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
                <Icon name="trash" /> 削除する
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
                <Icon name="archive" /> アーカイブする（一覧から消す）
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
                <Icon name="refresh" /> 作り直す（今の会話はアーカイブへ）
              </button>
            )}
          </div>
        )}
      </div>
    </li>
  );
}
