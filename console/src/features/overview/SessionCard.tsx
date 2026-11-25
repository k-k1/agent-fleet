// SessionCard — one session as a card in the overview grid (ADR 0078). It is the rail row
// laid out in two dimensions: the same kind square, display name, state chip and badges, read
// through the same helpers (sessionview.ts / sessionkind.ts) so a state or a kind never
// renders differently here than in the rail or a pane head.
//
// Where a click leads depends on whether there is a "beside" to open into (ADR 0078 decision 3).
// On a wide screen a plain click / Enter / Space opens the session BESIDE the grid, which stays
// put — that is the point of the grid. On a phone there is no beside: openInNew stacks two
// half-height panes, so a plain tap opens the session IN this pane and the browser Back button
// brings the grid back (every layout commit pushes a history entry). Ctrl / ⌘ and the middle
// click keep the meaning they have everywhere else in the Console — "open in another pane" — on
// both. In the tabbed layout both paths land on openInTab, i.e. a new tab in the same cell with
// the grid one tab away, so nothing here branches on the layout mode.
//
// Right-click, the ⋯ button and the Menu key open SessionMenu — the very same items as the row
// and the tab.
import { useRef, useState } from "react";
import type { CSSProperties, KeyboardEvent, MouseEvent } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { placeFixed } from "../../lib/placeFixed.ts";
import { usePaneHover } from "../../lib/panehover.tsx";
import { kindClasses, kindIcon, kindLabel } from "../../lib/sessionkind.ts";
import { tCount, useT } from "../../lib/i18n/index.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { ordClass } from "../../layout/badges.ts";
import { relTime } from "../../lib/intl.ts";
import { stateClass, displayName, stateInfo, exitLabel, remainingShort } from "../../lib/sessionview.ts";
import { sessionFolder, lineageColorOf, workingCopyLabel, worktreeTag } from "../../lib/project.ts";
import { agentOf } from "../../agents/registry.ts";
import { isContextMenuKey, menuAnchor } from "../project/contextMenuKey.ts";
import { useReposStore } from "../repos/store.ts";
import { parentSyncLabel, parentSyncTitle } from "../repos/parentSync.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { isWaiting } from "../sessions/waiting.ts";
import { openSessionFromList } from "../sessions/open.ts";
import { elapsedShort } from "./overview.ts";
import type { FoldInfo } from "./overview.ts";
import { SessionMenu } from "../sessions/SessionMenu.tsx";
import { useMySharesStore } from "../sharing/store.ts";
import { ContextBar } from "../mirror/ContextBar.tsx";
import type { SessionActions } from "../sessions/useSessionActions.tsx";
import type { Session } from "../../types/session.ts";

interface SessionCardProps {
  s: Session;
  /** Panes showing this session (ordinal badges); empty when unsplit. */
  opens: { ordinal: number; id: string }[];
  /** Where a plain click leads: beside the grid (wide screens) or into this pane (phone). */
  beside: boolean;
  running: boolean;
  /** Epoch ms this session last entered a wait for a person; 0 = this device cannot tell.
   *  Composed by the view from the notification ledger and this tab's observations. */
  waitingAt?: number;
  actions: SessionActions;
  /** What this card's family fold says (overview.ts owns the arithmetic and the filtering;
   *  this card only draws it). Absent = no fold information, i.e. no control. */
  fold?: FoldInfo;
  /** Present only when the control does something — a card with no children in this group
   *  gets none, rather than a "+" that folds nothing. */
  onFold?: () => void;
  /** The card in the left pane's Active view (ADR 0145) rather than in the grid: one column at
   *  the rail's width, under a heading that already names the project. It drops what the
   *  heading says and what only a wide card has room for, and it carries the pane numbers,
   *  because in the rail they are how a session is found among the panes. */
  rail?: boolean;
  /** Rail only: this session is the one in the focused pane. */
  selected?: boolean;
  /** How many of the rows this card's fold hides wait for the member's answer (0 = none). */
  hiddenWaiting?: number;
}

// Where the menu was asked for: at the pointer (right-click / Menu key) or under the ⋯.
type MenuAt = { x: number; y: number } | "button" | null;

export function SessionCard({ s, opens, beside, running, waitingAt = 0, actions, fold, onFold, rail = false, selected = false, hiddenWaiting = 0 }: SessionCardProps) {
  const tr = useT();
  const { hover, setHover } = usePaneHover();
  const setActive = useLayoutStore((l) => l.setActive);
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
  // Which model actually ANSWERED, not which one the session was launched with (ADR 0078
  // decision 13): `context.model` is read off the newest assistant turn, so it follows a
  // model switched mid-conversation — and on a grid the question is always "what is running
  // in here now". Falls back to the launch model until the session has answered once.
  const model = s.context?.model || s.model;
  // Context fill (absent for agy / shell / ssm). The gauge is the mirror's own ContextBar, so one session's
  // card and its chat can never disagree about how full it is.
  const used = s.context ? s.context.read + s.context.create + s.context.fresh : 0;
  const ctx = s.context && used > 0 ? s.context : null;
  // The spend trend needs a wide card; at the rail's width the gauge alone is what fits.
  const spends = rail ? undefined : s.tokenSpends;
  const started = rail ? "" : relTime(s.createdAt);
  // How long this has been waiting for a person — the number the card is watched for. While it
  // is still waiting the clock runs on the wait itself; once answered, the same instant reads
  // as "how long it has been working since you replied". Nothing is shown when neither ledger
  // saw the transition (no notification for this kind, or a wait older than the retention
  // window): an invented "0m" would be worse than a blank (ADR 0078 decision 11).
  const waited = s.alive ? elapsedShort(waitingAt) : "";
  const waitingNow = isWaiting(s);
  const awake = remainingShort(s.keepAwakeUntil);
  const budgetStopped = !s.alive && !!s.spendCapHitAt;
  const ords = rail ? opens : [];
  const hasIcons = !!(s.locked || awake || (s.alive && s.stopAfterTurnAt) || budgetStopped || isShared);
  const where = !rail || !!wt || !!repo?.integration || !!s.branchDrift || hasIcons || ords.length > 0;

  // newPane = the modifier was held (or the wheel was clicked): open in another pane whatever
  // the screen. Without it, `beside` decides.
  const openIt = (newPane: boolean) => {
    if (inert) return;
    openSessionFromList(s, newPane || beside, running);
  };
  const onKeyDown = (e: KeyboardEvent<HTMLElement>) => {
    if (e.target !== e.currentTarget) return;
    if (e.key === "Enter" || e.key === " ") {
      e.preventDefault();
      openIt(e.ctrlKey || e.metaKey);
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
        stateClass("ovw-card", st.cls) +
        (open ? " open" : "") +
        (hl ? " ovw-card-hover" : "") +
        (s.alive ? "" : " ovw-card--stopped") +
        (inert ? " ovw-card--dead" : "") +
        (lineage ? " ovw-card-lineage" : "") +
        (rail ? " ovw-card--rail" : "") +
        (selected ? " ovw-card--selected" : "")
      }
      style={lineage ? ({ "--sess-lineage": lineage } as CSSProperties) : undefined}
      role="button"
      tabIndex={0}
      aria-disabled={inert || undefined}
      aria-current={selected || undefined}
      title={
        displayName(s) +
        "\n" +
        (inert
          ? tr("srow.cant_resume")
          : s.alive
            ? tr(beside ? "ovw.open_hint" : "ovw.open_hint_here")
            : (ex ? ex.hint + "\n" : "") + tr("srow.stopped_hint")) +
        `\n${kindLabel(s.kind)} · ${st.text}\nID: ${s.name}`
      }
      onClick={(e) => openIt(e.ctrlKey || e.metaKey)}
      onAuxClick={(e) => {
        if (e.button !== 1) return;
        e.preventDefault();
        openIt(true);
      }}
      onKeyDown={onKeyDown}
      onContextMenu={onContextMenu}
      onMouseEnter={open ? () => setHover({ session: s.name }) : undefined}
      onMouseLeave={open ? () => setHover(null) : undefined}
    >
      <header className="ovw-head">
        {onFold && (
          <button
            type="button"
            className="ovw-fold"
            aria-expanded={!fold?.hidden}
            title={fold?.hidden ? tr("ovw.expand") : tr("ovw.collapse")}
            onClick={(e) => {
              // The card itself opens the session; the fold must not do both.
              e.preventDefault();
              e.stopPropagation();
              onFold();
            }}
            // A middle click on the card opens the session in another pane; on this button it
            // must do nothing.
            onAuxClick={(e) => e.stopPropagation()}
          >
            <Icon name={fold?.hidden ? "chevron-right" : "chevron-down"} />
          </button>
        )}
        <span className={"sess-kic " + kindClasses(s.kind, "sess-kic")} title={kindLabel(s.kind)}>
          <Icon name={kindIcon(s.kind)} />
        </span>
        <span className="ovw-title">{displayName(s)}</span>
        {!!fold?.hidden && (
          <span className="ovw-hidden" title={tr("ovw.hidden_children_hint", { n: fold.hidden })}>
            {tr("ovw.hidden_children", { n: fold.hidden })}
          </span>
        )}
        {!!fold?.hidden && hiddenWaiting > 0 && (
          <span className="ovw-hidden ovw-hidden--waiting" title={tCount("active.hidden_waiting_hint", hiddenWaiting)}>
            <Icon name="question" />
            {hiddenWaiting}
          </span>
        )}
        {/* The state reads from the top-right corner, where the eye lands first on a grid of
            cards, and the row below is left for what only some cards carry. The label is its
            own element so a narrow card can fold the CALM states back to their icon and give
            the width to the title — the states that need a person keep their words (CSS). */}
        <span className={stateClass("session-state", st.cls)} title={st.text}>
          <Icon name={st.icon} spin={st.spin} />
          {" "}
          <span className="ovw-lbl">{st.text}</span>
        </span>
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
            // Enter / Space activate this button, not the card. Escape has to reach the
            // document, where the open menu's esc layer closes it.
            onKeyDown={(e) => {
              if (e.key === "Enter" || e.key === " ") e.stopPropagation();
            }}
          >
            <Icon name="ellipsis" />
          </button>
        </span>
      </header>
      {where && (
      <div className="ovw-where" title={s.dir || ""}>
        {ords.length > 0 && (
          <span className="sess-ords">
            {ords.map((o) => (
              <button
                key={o.id}
                type="button"
                className={"rail-ord " + ordClass(o.ordinal)}
                title={tr("common.focus_pane", { ordinal: o.ordinal })}
                aria-label={tr("common.focus_pane", { ordinal: o.ordinal })}
                onClick={(e) => {
                  e.stopPropagation();
                  setActive(o.id);
                }}
                onAuxClick={(e) => e.stopPropagation()}
                onKeyDown={(e) => {
                  if (e.key === "Enter" || e.key === " ") e.stopPropagation();
                }}
                onMouseEnter={() => setHover({ session: s.name, paneId: o.id })}
                onMouseLeave={() => setHover({ session: s.name })}
              >
                {o.ordinal}
              </button>
            ))}
          </span>
        )}
        <span className="ovw-where-main">
        {!rail && <span className="ovw-project">{project || folder || tr("pj.other_sessions")}</span>}
        {wt && (
          <span className="ovw-wt" title={branch || wt}>
            <Icon name="git-branch" />
            {wt}
          </span>
        )}
        {/* Right of the branch: how this worktree stands against the working copy it was cut
            from — the same chip and the same wording as the rail's repo row (parentSync.ts),
            because "親+2・FF可" must not mean two things in one Console. */}
        {repo?.integration && (
          <span
            className={"ui-badge repo-chip integration repo-chip--" + repo.integration.relation}
            title={parentSyncTitle(repo.integration)}
          >
            {parentSyncLabel(repo.integration)}
          </span>
        )}
        {s.branchDrift && (
          <span className="sess-drift" title={tr("srow.branch_switched", { from: s.branch ?? "", to: s.currentBranch ?? "" })}>
            <Icon name="warning" /> {s.currentBranch}
          </span>
        )}
        </span>
        {/* The state icons only SOME cards have (lock / keep-awake / stop-armed / budget /
            shared) share this line, right-aligned, so a card never spends a row on badges
            alone. The pane ordinals lead the line (rail form only: ADR 0078 decision 10). */}
        {hasIcons && (
          <span className="ovw-icons">
        {s.locked && <Icon name="lock" className="sess-lock" title={tr("srow.locked_badge")} />}
        {remainingShort(s.keepAwakeUntil) && (
          <Icon name="debug-pause" className="sess-awake" title={tr("srow.keep_awake_badge", { left: remainingShort(s.keepAwakeUntil) })} />
        )}
        {s.alive && s.stopAfterTurnAt && <Icon name="debug-stop" className="sess-stoparm" title={tr("srow.stop_after_turn_badge")} />}
        {budgetStopped && <Icon name="pulse" className="sess-budget" title={tr("srow.budget_paused_hint")} />}
        {isShared && <Icon name="broadcast" className="sess-shared" title={tr("srow.shared_badge")} />}
          </span>
        )}
      </div>
      )}
      {/* The context gauge and the token-spend trend, the mirror's own strip (ADR 0078
          decision 13). Borrowed rather than reimplemented for the same reason as stateInfo
          and SessionMenu: a second arithmetic for "how full is it" would drift from the one
          the chat shows. What the card changes is only its scale — the labels fold to their
          short forms and the row wraps at a card's width (overview.css). */}
      {ctx && (
        <div className="ovw-ctx">
          <ContextBar
            read={ctx.read}
            create={ctx.create}
            fresh={ctx.fresh}
            model={ctx.model || s.model}
            window={ctx.window}
            spends={spends}
            maxSpend={spends && spends.length ? Math.max(...spends) : 0}
          />
        </div>
      )}
      <div className="ovw-meta">
        {/* The KIND is not spelled out here (the user's call, 2026-09-12): the coloured square
            in the head already says which agent this is, and the word repeated on every card
            was a column of "Claude". What the row leads with instead is the model that
            answered last, which is the part that actually varies. */}
        {model && <span title={model}>{model}</span>}
        {started && <span>{tr("ovw.started", { ago: started })}</span>}
        {waited && (
          <span className={waitingNow ? "ovw-waited ovw-waited--on" : "ovw-waited"} title={tr(waitingNow ? "ovw.waiting_hint" : "ovw.since_wait_hint")}>
            {tr(waitingNow ? "ovw.waiting_for" : "ovw.since_wait", { d: waited })}
          </span>
        )}
      </div>
      {/* The last thing the agent SAID, one line (ADR 0078 decision 12). It is the bottom row
          because it is the only part of the card whose height depends on the content: the rows
          above keep their place whether or not a session has spoken. Blank when nothing has
          been said yet, and for the kinds the Agent does not read a transcript for — the row
          is then not drawn at all rather than reserved, so those cards stay as short as they
          were. */}
      {s.lastSay && (
        <div className="ovw-say" title={s.lastSay + "\n" + tr("ovw.last_say_hint")}>
          {s.lastSay}
        </div>
      )}
      {/* The menu is a CHILD of the card, and a React event bubbles through the component
          tree even out of a portal — so without this boundary every menu item also counted as
          a click on the card: choosing "Stop" opened the session in another pane behind the
          confirmation dialog (reported 2026-09-12). The rail's row never had this because its
          menu is a SIBLING of the clickable button; a card is clickable as a whole, so the
          boundary has to be explicit. Keys stop only when they are the ones the card acts on
          (Enter / Space): a React stopPropagation also stops the native event at the root, and
          Escape has to reach the document, where the menu's and its dialogs' esc layers close
          them. */}
      <span
        className="ovw-menu-host"
        onClick={(e) => e.stopPropagation()}
        onAuxClick={(e) => e.stopPropagation()}
        onKeyDown={(e) => {
          if (e.key === "Enter" || e.key === " ") e.stopPropagation();
        }}
        onContextMenu={(e) => e.stopPropagation()}
      >
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
      </span>
    </article>
  );
}
