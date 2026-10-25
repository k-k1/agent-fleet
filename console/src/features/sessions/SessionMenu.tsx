// SessionMenu — the session context menu itself, lifted out of SessionRow so the
// SAME items appear wherever a session is shown: the left-pane row's ⋯ /
// right-click, and a right-click on that session's tab in the tabbed grid. The
// caller owns the open state and the placement (a row anchors under its ⋯
// button and clamps inside the rail; a tab opens at the cursor), because those
// are the only two things that actually differ.
//
// It stays mounted while closed on purpose: the handoff / share dialogs it owns
// outlive the menu, and unmounting on close would take them down with it.
import { createPortal } from "react-dom";
import { Fragment, useLayoutEffect, useRef, useState } from "react";
import type { MouseEvent as RMouseEvent, ReactElement, ReactNode, RefObject } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { useDismiss } from "../../lib/useDismiss.ts";
import { useMenuRoving } from "../../lib/useMenuRoving.ts";
import { copyText } from "../../lib/clipboard.ts";
import { sessionFolder } from "../../lib/project.ts";
import { getSettings, useSettings } from "../../lib/settings.ts";
import { activeWorkingSet, repoInSet, workingSetList, toggleWorkingSetMember } from "../../lib/workingSetsStore.ts";
import { useReposStore } from "../repos/store.ts";
import { useT } from "../../lib/i18n/index.ts";
import { displayName, remainingShort, KEEP_AWAKE_HOURS } from "../../lib/sessionview.ts";
import { agentOf } from "../../agents/registry.ts";
import { useKindCatalogVersion } from "../../agents/kindCatalog.ts";
import { openSessionTerminal, openSessionChat, openSessionByName } from "./open.ts";
import { openGallery } from "../gallery/open.ts";
import { openRepoMenuInRail } from "../repos/reveal.ts";
import { useIsMobile } from "../../lib/device.ts";
import { usePopoutMode } from "../../lib/popoutMode.ts";
import { useSessionUI } from "./ui.ts";
import { useSessionsStore } from "./store.ts";
import { HandoffModal } from "./HandoffModal.tsx";
import { exportSessionMarkdown } from "./exportMarkdown.ts";
import { useMemoryReviewUnavailable } from "../../lib/aiAssistResolution.ts";
import { reviewSessionNow } from "./memoryReviewNow.ts";
import { RecreateWorktreeModal } from "./RecreateWorktreeModal.tsx";
import { recreatableGroup } from "./ArchivedModal.tsx";
import { ShareCreateModal } from "../sharing/ShareCreateModal.tsx";
import { HandoffOfferModal } from "../sharing/HandoffOfferModal.tsx";
import type { SessionActions } from "./useSessionActions.tsx";
import type { Session } from "../../types/session.ts";

interface SessionMenuProps {
  s: Session;
  actions: SessionActions;
  /** The workspace agent is reachable — resume/handoff/recreate need it. */
  running: boolean;
  open: boolean;
  /** Position the (position:fixed) menu element; runs on every render while open. */
  place: (el: HTMLElement) => void;
  /** Containers whose mousedown must NOT dismiss (e.g. the ⋯ toggle's wrapper). */
  keepOpenRefs?: Array<RefObject<HTMLElement | null>>;
  onClose: () => void;
}

interface ItemExtra {
  danger?: boolean;
  disabled?: boolean;
  title?: string;
}

interface MenuSection {
  id: string;
  /** Accessible name of the group; sections draw no visible heading of their own. */
  label: string;
  /** The items visible now. Falsy entries are dropped, and a section left empty draws nothing. */
  items: Array<ReactElement | false | null | undefined>;
}

export function SessionMenu({ s, actions, running, open, place, keepOpenRefs, onClose }: SessionMenuProps) {
  useKindCatalogVersion(); // the driver-switch item reads the catalog's driver facts
  const openRename = useSessionUI((u) => u.openRename);
  const openBranchRename = useSessionUI((u) => u.openBranchRename);
  const openSsmResume = useSessionUI((u) => u.openSsmResume);
  const openBudget = useSessionUI((u) => u.openBudget);
  const openMove = useSessionUI((u) => u.openMove);
  const startSession = useSessionsStore((st) => st.start);
  const toast = useToast();
  const tr = useT();
  const menuElRef = useRef<HTMLDivElement>(null);
  const [handoffOpen, setHandoffOpen] = useState(false);
  const [shareOpen, setShareOpen] = useState(false);
  const [memberHandoffOpen, setMemberHandoffOpen] = useState(false);
  const [recreateWtOpen, setRecreateWtOpen] = useState(false);
  useDismiss([menuElRef, ...(keepOpenRefs ?? [])], open, onClose);
  useMenuRoving(menuElRef, open);
  // The dropdown is position:fixed and re-placed every render — a row near the
  // rail's foot must not push its menu below the viewport (the old absolute
  // top:26px did), and the width the caller aligns against is only known once
  // the items are laid out.
  useLayoutEffect(() => {
    const el = menuElRef.current;
    if (!open || !el) return;
    place(el);
  });
  // useMenuRoving pulls focus into the menu on open, so closing it destroys the
  // focused element and focus falls to <body> — after a keyboard open (the menu key
  // then Esc, or activating an item) the user is left nowhere to arrow from. Hand focus
  // back to whatever opened the menu, but only when the menu really still had it: a
  // click that landed on some other control must keep the focus the user chose.
  //
  // A LAYOUT effect on purpose. useMenuRoving focuses the first item from a passive
  // effect, so a passive capture here would read the menu item as "the opener" —
  // layout effects run first, before anything has moved focus.
  useLayoutEffect(() => {
    if (!open) return;
    const opener = document.activeElement as HTMLElement | null;
    if (!opener || opener === document.body) return; // opened by mouse — nothing to give back
    return () => {
      const now = document.activeElement;
      if ((!now || now === document.body) && opener.isConnected) opener.focus();
    };
  }, [open]);
  // The session's immutable id (e.g. "sk7f3q9") — the thing shown as ID in the
  // row tooltip. The menu label shows the concrete value, not jargon ("slug").
  const copyId = () => {
    void copyText(s.name).then((ok) =>
      ok ? toast(tr("srow.id_copied", { name: s.name }), { kind: "success" }) : toast(tr("common.copy_failed")),
    );
  };

  // Generated images (ADR 0080 decision 8). The folder is named after the session's UUID,
  // so the file tree is no way in — this item is. Count and path both ride the session wire
  // and an older Agent sends neither, which is exactly when there is nothing to show: no
  // capability probe, no version compare, the item simply is not rendered.
  const generated =
    s.generatedImages && s.generatedImagesPath ? { n: s.generatedImages, path: s.generatedImagesPath } : null;
  const openGeneratedImages = (e: RMouseEvent) => {
    onClose();
    // The slug, not displayName(s): paneTitle takes the Session it resolves from this name
    // and titles the pane itself (Pane.tsx), and migrate.ts only keeps a stored value that
    // still looks like a session name — a title would be dropped on the next reload.
    openGallery(generated!.path, {
      session: s.name,
      newPane: e.ctrlKey || e.metaKey || e.button === 1,
    });
  };

  const dead = !s.alive && s.resumable === false; // dir gone → can't resume
  // A dead session whose folder was a worktree can have the folder put back at the same path,
  // after which the ordinary resume finds its conversation again (issue #1040). The Agent
  // decides the rest — the parent still being there, the path still free.
  const worktreeGone = dead && running && recreatableGroup(s.dir || "", [s]);
  // Working sets (docs/log/52): direct assignment is for repo-less sessions only —
  // a session living in a working copy inherits that repo's membership instead.
  const wsets = workingSetList(useSettings());
  const repos = useReposStore((st) => st.repos);
  const folder = sessionFolder(s);
  const repoLess = !folder || !repos.some((r) => r.name === folder);
  const wsetShown = repoLess && wsets.length > 0;
  // The Agent reads the transcript, so it has to be reachable; the session itself may be stopped.
  const exportShown = agentOf(s.kind).caps.transcript && running;
  // "Suggest memories now" (ADR 0117): only where the review can run - the Agent reachable, a
  // transcript to read, AF memory on and a review mode chosen. The Agent checks all of it again.
  const reviewSettings = useSettings();
  const memoryReviewShown =
    agentOf(s.kind).caps.transcript && running && reviewSettings.agentMemory && reviewSettings.memoryReview !== "off";
  const reviewUnavailable = useMemoryReviewUnavailable(memoryReviewShown && open);
  // The repository menu is the row's own, opened where the row sits in the rail (#1557). No
  // rail to open it in: a pop-out has none, and on a phone the rail is a drawer only the App
  // shell can open.
  const popout = usePopoutMode() === "popout";
  const mobile = useIsMobile();
  const showRepoMenu = running && !repoLess && !popout && !mobile;
  const openRepoMenu = () => {
    onClose();
    const name = folder!;
    void openRepoMenuInRail(name).then((ok) => {
      if (ok) return;
      // Say why when the working set is the reason; otherwise the rail search (or a copy
      // deleted meanwhile) kept the row out of the tree.
      const wset = activeWorkingSet(getSettings());
      const r = useReposStore.getState().repos.find((x) => x.name === name);
      toast(
        wset && r && !repoInSet(wset, r)
          ? tr("srow.repo_menu_outside_wset", { name })
          : tr("srow.repo_menu_hidden", { name }),
      );
    });
  };

  const item = (key: string, onClick: () => void, icon: ReactNode, label: ReactNode, extra: ItemExtra = {}) => (
    <button
      key={key}
      type="button"
      className={extra.danger ? "ui-menu-item danger" : "ui-menu-item"}
      disabled={extra.disabled}
      title={extra.title}
      onClick={onClick}
    >
      {icon} {label}
    </button>
  );
  // A moving or moved source refuses every start (ADR 0115 decision 11.2): its way back is the
  // move dialog's "resume here anyway", never the ordinary resume.
  const moveClosed = !!(s.moving || s.movedTo);
  const resumable = !s.alive && !dead && running && !moveClosed;
  const caps = agentOf(s.kind).caps;
  const moveShown = caps.transcript && !dead && running;

  // The menu is a list of sections, each a list of the items visible right now; the rules between
  // sections are drawn from that list below, never placed by hand, so a section with nothing to
  // show cannot leave a doubled or dangling rule. The order is fixed (issue #2189): it is the same
  // on every surface that mounts this component.
  const sections: MenuSection[] = [
    {
      id: "run",
      label: tr("srow.section_run"),
      items: [
        // Resume — kinds with no in-chat resume. SSM resumes through the login modal (SSO
        // handshake before attach).
        resumable &&
          !caps.chat &&
          item("resume", () => {
            onClose();
            if (s.kind === "ssm") openSsmResume(s.name, false);
            else openSessionTerminal(s.name);
          }, <Icon name="play" />, tr("srow.resume")),
        // Chat-capable kinds (claude/codex/opencode) have no in-menu resume today — opening the
        // row shows the mirror read-only, and its own "resume and continue" button is the only
        // resume path. This mirrors that same POST /start then opens the chat pane directly.
        resumable &&
          caps.chat &&
          item("resume-chat", () => {
            onClose();
            void startSession(s.name).then(() => openSessionChat(s.name));
          }, <Icon name="play" />, tr("srow.resume")),
        resumable &&
          s.kind === "ssm" &&
          item("relogin", () => {
            onClose();
            openSsmResume(s.name, true);
          }, <Icon name="key" />, tr("srow.relogin_resume")),
        s.alive &&
          item("stop", () => {
            onClose();
            void actions.halt(s.name, displayName(s));
          }, <Icon name="debug-stop" />, tr("srow.stop")),
        // terminalDriver === false (lcpp, ADR 0093 decision 2): no Terminal (CLI) route exists to
        // switch to, ever — never offer the toggle (the server would 400 it anyway).
        !dead &&
          running &&
          agentOf(s.kind).managedDriver &&
          agentOf(s.kind).terminalDriver !== false &&
          item("driver", () => {
            onClose();
            void actions.switchDriver(s);
          }, <Icon name={s.driver === "managed" ? "terminal" : "server-process"} />,
          s.driver === "managed" ? tr("sess.switch_to_tui") : tr("sess.switch_to_managed")),
        !!s.remoteUrl &&
          item("remote", () => {
            onClose();
            window.open(s.remoteUrl, "_blank", "noopener");
          }, <Icon name="link-external" />, tr("srow.open_remote")),
      ],
    },
    {
      id: "view",
      label: tr("srow.section_view"),
      items: [
        generated && (
          <button
            key="generated"
            type="button"
            className="ui-menu-item"
            onMouseDown={(e) => e.button === 1 && e.preventDefault()}
            onAuxClick={(e) => e.button === 1 && openGeneratedImages(e)}
            onClick={openGeneratedImages}
          >
            <Icon name="file-media" /> {tr("srow.generated_images", { n: generated.n })}
          </button>
        ),
        showRepoMenu && item("repo-menu", openRepoMenu, <Icon name="repo" />, tr("srow.repo_menu", { name: folder! })),
        item("copy-id", () => {
          onClose();
          copyId();
        }, <Icon name="copy" />, tr("srow.copy_id", { name: s.name })),
        item("rename", () => {
          onClose();
          openRename(s);
        }, <Icon name="edit" />, tr("srow.rename")),
        // Worktree sessions only: rename the worktree's branch (deferred naming); AI suggestion
        // uses THIS session.
        !!s.worktree &&
          item("rename-branch", () => {
            onClose();
            openBranchRename(s);
          }, <Icon name="repo-forked" />, tr("srow.rename_branch")),
      ],
    },
    {
      id: "keep",
      label: tr("srow.section_keep"),
      items: [
        // Handoff to another agent — one unified item for every session that has a conversation
        // (caps.transcript); the target is chosen in the modal, which then calls the operator
        // assistant directly.
        caps.transcript &&
          !dead &&
          running &&
          item("handoff", () => {
            onClose();
            setHandoffOpen(true);
          }, <Icon name="git-branch" />, tr("srow.handoff")),
        // Move to another agent (ADR 0115): beside the handoff, its deterministic sibling. The
        // same dialog finishes or cancels a pending move and takes a moved source back.
        moveShown &&
          !!s.movedTo &&
          item("move-open-target", () => {
            onClose();
            openSessionByName(s.movedTo!);
          }, <Icon name="arrow-right" />, tr("srow.move_open_target")),
        moveShown &&
          item("move", () => {
            onClose();
            openMove(s);
          }, <Icon name="arrow-swap" />, tr(s.movedTo ? "srow.move_reopen" : s.moving ? "srow.move_pending" : "srow.move")),
        // Handoff to a member (ADR 0057): the same offer the proposal card's "Hand off to a member"
        // opens, minus the proposal. The CP refuses it while the owner's Workspace is stopped and
        // reads the working copy's state from the Agent, so the gate is the one above: a transcript
        // kind, a session whose folder still exists, a reachable Agent. Who it can go to (and
        // "not shared yet") is the modal's to say.
        caps.transcript &&
          !dead &&
          running &&
          item("handoff-member", () => {
            onClose();
            setMemberHandoffOpen(true);
          }, <Icon name="broadcast" />, tr("srow.handoff_member")),
        // Session sharing: share the conversation with another user in the same tenant.
        // shell/SSM have no transcript, so they are out of scope (docs/log/59).
        caps.transcript &&
          item("share", () => {
            onClose();
            setShareOpen(true);
          }, <Icon name="broadcast" />, tr("srow.share")),
        memoryReviewShown &&
          item("memory-review", () => {
            onClose();
            void reviewSessionNow(s.name, toast);
          }, <Icon name="sparkle" />, tr("srow.memory_review"), reviewUnavailable ? { disabled: true, title: tr("srow.memory_review_no_backend") } : {}),
        // The three export variants stay captioned rows: a few choices do not earn a submenu, which
        // would add a click and a second roving level to a menu that is one flat list.
        exportShown && <div key="export-caption" className="ui-menu-caption">{tr("srow.export_caption")}</div>,
        ...(exportShown
          ? (["none", "names", "full"] as const).map((tools) =>
              item(`export-${tools}`, () => {
                onClose();
                void exportSessionMarkdown(s.name, tools, toast);
              }, <Icon name="download" />, tr(`srow.export_${tools}` as const)),
            )
          : []),
      ],
    },
    {
      // Working sets (docs/log/52): membership toggles — repo-less rows only.
      id: "wsets",
      label: tr("wset.menu_caption"),
      items: wsetShown
        ? [
            <div key="wset-caption" className="ui-menu-caption">{tr("wset.menu_caption")}</div>,
            ...wsets.map((w) =>
              item(`wset-${w.id}`, () => {
                onClose();
                toggleWorkingSetMember(w.id, "sessions", s.name);
              }, <Icon name="check" className={w.sessions.includes(s.name) ? "wset-check" : "wset-check off"} />, w.name),
            ),
          ]
        : [],
    },
    {
      id: "behaviour",
      label: tr("srow.section_behaviour"),
      items: [
        // Deletion lock (docs/log/45): pin or unpin this row against deletion. The protection
        // itself lives in the Agent (403); this only toggles it and suppresses the affordance.
        item("lock", () => {
          onClose();
          void actions.setLocked(s, !s.locked);
        }, <Icon name={s.locked ? "unlock" : "lock"} />, s.locked ? tr("srow.unlock") : tr("srow.lock")),
        // Keep-awake pin (docs/log/75): protects this session and the Workspace from the idle
        // auto-stop for a bounded time. For shell / ssm, af cannot tell whether a job is running
        // — an abandoned less and a running build look the same by foreground command name, and
        // ssm always shows aws — so this is a declaration by the user instead of a guess.
        item(
          "keep-awake",
          () => {
            onClose();
            void actions.setKeepAwake(s, remainingShort(s.keepAwakeUntil) ? 0 : KEEP_AWAKE_HOURS);
          },
          <Icon name="watch" />,
          remainingShort(s.keepAwakeUntil)
            ? tr("srow.keep_awake_off")
            : tr("srow.keep_awake_on", { hours: KEEP_AWAKE_HOURS }),
          { title: tr("srow.keep_awake_hint") },
        ),
        // Stop-after-turn arm (docs/log/85), the pin's mirror image: fold this session away once
        // the turn it is running ends. Live rows only — a stopped session has no turn to end, and
        // every fold consumes the arm anyway. The same arm is set from inside the conversation by
        // the af_stop_after_turn MCP tool, so this item doubles as the way to take back a stop the
        // session armed on the user's word.
        s.alive &&
          item(
            "stop-after-turn",
            () => {
              onClose();
              void actions.setStopAfterTurn(s, !s.stopAfterTurnAt);
            },
            <Icon name="debug-stop" />,
            s.stopAfterTurnAt ? tr("srow.stop_after_turn_off") : tr("srow.stop_after_turn_on"),
            { title: tr("srow.stop_after_turn_hint") },
          ),
        // Spend budget (#1054): only kinds with a transcript can be priced. On a session the
        // budget stopped, the same item is the "raise and resume" action.
        running &&
          caps.transcript &&
          item("budget", () => {
            onClose();
            openBudget(s);
          }, <Icon name="pulse" />, s.spendCapHitAt && !s.alive ? tr("srow.budget_raise_resume") : tr("srow.budget")),
      ],
    },
    {
      id: "cleanup",
      label: tr("srow.section_cleanup"),
      items: [
        caps.ephemeral
          ? item(
              "delete",
              () => {
                onClose();
                void actions.deleteSession(s);
              },
              <Icon name="trash" />,
              tr("common.delete_do"),
              { danger: true, disabled: s.locked, title: s.locked ? tr("srow.locked_hint") : undefined },
            )
          : item("archive", () => {
              onClose();
              void actions.archive(s);
            }, <Icon name="archive" />, tr("srow.archive")),
        worktreeGone &&
          item(
            "recreate-worktree",
            () => {
              onClose();
              setRecreateWtOpen(true);
            },
            <Icon name="repo" />,
            tr("srow.recreate_worktree"),
            { title: tr("arch.recreate_title") },
          ),
        !dead &&
          item("recreate", () => {
            onClose();
            void actions.recreate(s.name, displayName(s));
          }, <Icon name="refresh" />, tr("srow.recreate")),
      ],
    },
  ];
  const visible = sections
    .map((sec) => ({ ...sec, items: sec.items.filter((x): x is ReactElement => !!x) }))
    .filter((sec) => sec.items.length > 0);

  return (
    <>
      {open &&
        createPortal(
          <div className="ui-menu sess-menu" ref={menuElRef} onMouseDown={(e) => e.stopPropagation()}>
            {visible.map((sec, i) => (
              <Fragment key={sec.id}>
                {i > 0 && <div className="ui-menu-sep" aria-hidden="true" />}
                <div role="group" aria-label={sec.label} data-menu-section={sec.id}>
                  {sec.items}
                </div>
              </Fragment>
            ))}
          </div>,
          document.body,
        )}
      {handoffOpen && <HandoffModal session={s} actions={actions} onClose={() => setHandoffOpen(false)} />}
      {memberHandoffOpen && (
        <HandoffOfferModal
          session={s.name}
          initialTitle={displayName(s)}
          initialPrompt=""
          onClose={() => setMemberHandoffOpen(false)}
        />
      )}
      {recreateWtOpen && (
        // This session is still on the active list, so there is nothing to restore: the modal
        // closes once the folder exists, and the next list refresh makes the row resumable.
        <RecreateWorktreeModal
          dir={s.dir || ""}
          sessions={[]}
          onClose={() => setRecreateWtOpen(false)}
          onChanged={() => void useSessionsStore.getState().refresh()}
        />
      )}
      {shareOpen && (
        <ShareCreateModal initialTarget={`session:${s.name}`} onClose={() => setShareOpen(false)} />
      )}
    </>
  );
}
