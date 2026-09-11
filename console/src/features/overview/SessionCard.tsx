// SessionCard — one session as a card in the overview grid (ADR 0078). It is the rail row
// laid out in two dimensions: the same kind square, display name, state chip and badges, read
// through the same helpers (sessionview.ts / sessionkind.ts) so a state or a kind never
// renders differently here than in the rail or a pane head.
//
// Click / Enter / Space open the session BESIDE the grid (openSessionFromList with split),
// never in its place: the grid is the thing being watched and must stay. Right-click, the ⋯
// button and the Menu key open SessionMenu — the very same items as the row and the tab.
import { useRef, useState } from "react";
import type { CSSProperties, KeyboardEvent, MouseEvent } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { placeFixed } from "../../lib/placeFixed.ts";
import { usePaneHover } from "../../lib/panehover.tsx";
import { kindIcon, kindLabel, kindClass } from "../../lib/sessionkind.ts";
import { useT } from "../../lib/i18n/index.ts";
import { relTime } from "../../lib/intl.ts";
import { displayName, stateInfo, exitLabel, remainingShort } from "../../lib/sessionview.ts";
import { sessionFolder, lineageColorOf, workingCopyLabel, worktreeTag } from "../../lib/project.ts";
import { agentOf } from "../../agents/registry.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { ordClass } from "../../layout/badges.ts";
import { isContextMenuKey, menuAnchor } from "../project/contextMenuKey.ts";
import { useReposStore } from "../repos/store.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionFromList } from "../sessions/open.ts";
import { SessionMenu } from "../sessions/SessionMenu.tsx";
import { useMySharesStore } from "../sharing/store.ts";
import { contextWindow } from "../mirror/ContextBar.tsx";
import type { SessionActions } from "../sessions/useSessionActions.tsx";
import type { Session } from "../../types/session.ts";

interface SessionCardProps {
  s: Session;
  /** Panes showing this session (ordinal badges); empty when unsplit. */
  opens: { ordinal: number; id: string }[];
  /** True when the layout is split (badges/cross-highlight are dormant otherwise). */
  multi: boolean;
  running: boolean;
  actions: SessionActions;
}

// Where the menu was asked for: at the pointer (right-click / Menu key) or under the ⋯.
type MenuAt = { x: number; y: number } | "button" | null;

export function SessionCard({ s, opens, multi, running, actions }: SessionCardProps) {
  const tr = useT();
  const setActive = useLayoutStore((st) => st.setActive);
  const { hover, setHover } = usePaneHover();
  const [menuAt, setMenuAt] = useState<MenuAt>(null);
  const menuWrapRef = useRef<HTMLSpanElement>(null);
  const menuBtnRef = useRef<HTMLButtonElement>(null);
  const cardRef = useRef<HTMLElement>(null);

  const folder = sessionFolder(s);
  const repo = useReposStore((st) => st.repos.find((r) => r.name === folder));
  const { project, branch } = workingCopyLabel(folder, repo);
  const wt = worktreeTag(folder, repo);
  const myShares = useMySharesStore((st) => st.shares);
  const isShared = myShares.some((sh) => sh.scope.type === "session" && sh.scope.key === s.name);
  const lineage = useSessionsStore((st) => lineageColorOf(st.sessions, s.name));

  const dead = !s.alive && s.resumable === false;
  const inert = dead && !agentOf(s.kind).caps.transcript;
  const st = stateInfo(s);
  const ex = !s.alive ? exitLabel(s) : null;
  const open = opens.length > 0;
  const hl = open && hover?.session === s.name;
  // Context fill, claude only: the same arithmetic as the ContextBar gauge, shown as a
  // percentage because a card has no room for the segmented bar.
  const used = s.context ? s.context.read + s.context.create + s.context.fresh : 0;
  const ctxPct = s.context && used > 0 ? Math.min(100, Math.round((used / contextWindow(s.context.model || s.model, used)) * 100)) : null;
  const started = relTime(s.createdAt);

  const openIt = () => {
    if (inert) return;
    openSessionFromList(s, true, running);
  };
  const onKeyDown = (e: KeyboardEvent<HTMLElement>) => {
    if (e.target !== e.currentTarget) return;
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      openIt();
    } else if (isContextMenuKey(e)) {
      e.preventDefault();
      setMenuAt(menuAnchor(e.currentTarget));
    }
  };
  const onContextMenu = (e: MouseEvent<HTMLElement>) => {
    e.preventDefault();
    setMenuAt({ x: e.clientX, y: e.clientY });
  };

  return (
    <article
      ref={cardRef}
      className={
        "ovw-card " +
        st.cls +
        (open ? " open" : "") +
        (hl ? " hover" : "") +
        (s.alive ? "" : " stopped") +
        (inert ? " dead" : "") +
        (lineage ? " lineage" : "")
      }
      style={lineage ? ({ "--sess-lineage": lineage } as CSSProperties) : undefined}
      role="button"
      tabIndex={0}
      aria-disabled={inert || undefined}
      title={
        displayName(s) +
        "\n" +
        (inert ? tr("srow.cant_resume") : s.alive ? tr("ovw.open_hint") : (ex ? ex.hint + "\n" : "") + tr("srow.stopped_hint")) +
        `\n${kindLabel(s.kind)} · ${st.text}\nID: ${s.name}`
      }
      onClick={openIt}
      onAuxClick={(e) => {
        if (e.button !== 1) return;
        e.preventDefault();
        openIt();
      }}
      onKeyDown={onKeyDown}
      onContextMenu={onContextMenu}
      onMouseEnter={open ? () => setHover({ session: s.name }) : undefined}
      onMouseLeave={open ? () => setHover(null) : undefined}
    >
      <header className="ovw-head">
        <span className={"sess-kic kind-" + kindClass(s.kind)} title={kindLabel(s.kind)}>
          <Icon name={kindIcon(s.kind)} />
        </span>
        <span className="ovw-title">{displayName(s)}</span>
        <span className="ovw-menu-wrap" ref={menuWrapRef}>
          <button
            type="button"
            className="ui-btn ui-btn-ghost ui-iconbtn ovw-menu-btn"
            title={tr("srow.menu")}
            ref={menuBtnRef}
            onClick={(e) => {
              e.stopPropagation();
              setMenuAt((m) => (m ? null : "button"));
            }}
            onKeyDown={(e) => e.stopPropagation()}
          >
            <Icon name="ellipsis" />
          </button>
        </span>
      </header>
      <div className="ovw-where" title={s.dir || ""}>
        <span className="ovw-project">{project || folder || tr("pj.other_sessions")}</span>
        {wt && (
          <span className="ovw-wt" title={branch || wt}>
            <Icon name="git-branch" />
            {wt}
          </span>
        )}
        {s.branchDrift && (
          <span className="sess-drift" title={tr("srow.branch_switched", { from: s.branch ?? "", to: s.currentBranch ?? "" })}>
            <Icon name="warning" /> {s.currentBranch}
          </span>
        )}
      </div>
      <div className="ovw-row">
        <span className={"session-state " + st.cls} title={st.text}>
          <Icon name={st.icon} spin={st.spin} />
          {" "}
          {st.text}
        </span>
        {s.locked && <Icon name="lock" className="sess-lock" title={tr("srow.locked_badge")} />}
        {remainingShort(s.keepAwakeUntil) && (
          <Icon name="debug-pause" className="sess-awake" title={tr("srow.keep_awake_badge", { left: remainingShort(s.keepAwakeUntil) })} />
        )}
        {s.alive && s.stopAfterTurnAt && <Icon name="debug-stop" className="sess-stoparm" title={tr("srow.stop_after_turn_badge")} />}
        {isShared && <Icon name="broadcast" className="sess-shared" title={tr("srow.shared_badge")} />}
        {multi && opens.length > 0 && (
          <span className="sess-ords">
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
          </span>
        )}
      </div>
      <div className="ovw-meta">
        <span>{kindLabel(s.kind)}</span>
        {s.model && <span title={s.model}>{s.model}</span>}
        {ctxPct != null && <span title={tr("ovw.ctx_hint")}>{tr("ovw.ctx", { pct: ctxPct })}</span>}
        {started && <span>{tr("ovw.started", { ago: started })}</span>}
      </div>
      <SessionMenu
        s={s}
        actions={actions}
        running={running}
        open={menuAt !== null}
        place={(el) => {
          if (menuAt === "button") {
            const a = menuBtnRef.current?.getBoundingClientRect();
            if (a) placeFixed(el, a.right - el.offsetWidth, a.bottom + 2);
          } else if (menuAt) {
            placeFixed(el, menuAt.x, menuAt.y);
          }
        }}
        keepOpenRefs={[menuWrapRef]}
        onClose={() => setMenuAt(null)}
      />
    </article>
  );
}
