// SessionRow — one single-line session row plus its ⋯/right-click menu: a
// kind-colored leading icon, the title, and a compact state chip (icon-only for
// the calm states; question/plan/permission keep their text — they demand the
// user). Renders in the flat "その他" list AND under each working-copy node of
// the project tree. Self-contained: it owns its menu open state (outside-click /
// Escape dismiss) and reads pane-hover + layout itself.
// Lifecycle ops come from useSessionActions; the menu items themselves live in
// SessionMenu, shared with the tabbed grid's tab right-click.
import { useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { placeFixed } from "../../lib/placeFixed.ts";
import { usePaneHover } from "../../lib/panehover.tsx";
import { kindIcon, kindLabel, kindClass } from "../../lib/sessionkind.ts";
import { useT } from "../../lib/i18n/index.ts";
import { displayName, stateInfo, exitLabel, remainingShort } from "../../lib/sessionview.ts";
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
import { SessionMenu } from "./SessionMenu.tsx";
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
  const { hover, setHover } = usePaneHover();
  const tr = useT();
  const menuRef = useRef<HTMLDivElement>(null);
  const menuBtnRef = useRef<HTMLButtonElement>(null);
  const [menuOpen, setMenuOpen] = useState(false);
  const myShares = useMySharesStore((st) => st.shares);
  const isShared = myShares.some((sh) => sh.scope.type === "session" && sh.scope.key === s.name);

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
          <SessionMenu
            s={s}
            actions={actions}
            running={running}
            open={menuOpen}
            // Anchored under the ⋯ button, right-aligned to it, and clamped inside
            // the rail so a row near its foot doesn't push the menu off-screen.
            place={(el) => {
              const a = menuBtnRef.current?.getBoundingClientRect();
              if (!a) return;
              placeFixed(el, a.right - el.offsetWidth, a.bottom + 2, menuBtnRef.current?.closest<HTMLElement>(".app-rail"));
            }}
            keepOpenRefs={[menuRef]}
            onClose={() => setMenuOpen(false)}
          />
        </div>
      )}
    </li>
  );
}
