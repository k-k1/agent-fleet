import { useEffect, useLayoutEffect, useMemo, useRef } from "react";
import type { CSSProperties, ReactNode } from "react";
import { isManagedSession } from "../../types/session.ts";
import type { Session } from "../../types/session.ts";
import {
  useSettings,
  chatFontStack,
  surfaceBg,
  surfaceAccent,
  effectiveTheme,
} from "../../lib/settings.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { useWorkspaceStore } from "../../core/store/workspace.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { Icon } from "../../ui/Icon.tsx";
import { autoGrowTextarea } from "../../lib/autoGrow.ts";
import { prettyModel } from "../../lib/modelName.ts";
import { MirrorToggle } from "./MirrorToggle.tsx";
import { MirrorBanners } from "./parts/MirrorBanners.tsx";
import { useMirrorTts } from "./parts/useMirrorTts.tsx";
import { useMirrorScroll } from "./parts/useMirrorScroll.ts";
import { useSkillPicker } from "./parts/useSkillPicker.ts";
import { useReplySuggest } from "./parts/useReplySuggest.ts";
import { JumpPills } from "./parts/JumpPills.tsx";
import { useHistorySearch } from "./parts/useHistorySearch.ts";
import {
  DirGoneNotice,
  ResumeNotice,
  ResumingNotice,
  TerminalResumeNotice,
  TerminalUpdateNotice,
  WsStoppedNotice,
} from "./parts/ComposerNotices.tsx";
import { ContextBar } from "./ContextBar.tsx";
import { SpendChip } from "./SpendChip.tsx";
import { useToast } from "../../ui/ToastProvider.tsx";
import { t as tr, useLocale, useT } from "../../lib/i18n/index.ts";
import { agentOf } from "../../agents/registry.ts";
import { stateInfo } from "../../lib/sessionview.ts";
import { ViewHead } from "../../ui/ViewHead.tsx";
import { PaneSessionChip } from "../panes/PaneSessionChip.tsx";
// workSplit lives in transcript/ alongside the turn rendering (owned by TranscriptTurn).
import { awaitingReply, latestWorkPromptIndex, textOfParts } from "./mirrorParts.ts";
import { echoLanded } from "./pendingEcho.ts";
import { coarsePointer } from "../../lib/device.ts";
import { canBranchFrom, canBranchInSession, carriedUserTurns } from "./forkAt.ts";
import { HandoffProposal } from "./HandoffProposal.tsx";
import { DiscardNotice } from "./parts/DiscardNotice.tsx";
import { PendingPeersNotice } from "./parts/PendingPeersNotice.tsx";
import { FileChangeStrip } from "./FileChangeStrip.tsx";
import { useSessionFilesStore } from "./sessionFiles.ts";
// The transcript rendering layer, shared with the shared-session view (docs/log/59). What the
// reader may DO here is expressed as TranscriptCaps — the mirror is the owner, so it fills
// in every capability; a recipient fills in almost none. See transcript/capabilities.ts.
import { TranscriptView } from "./transcript/TranscriptView.tsx";
import { useStableBlockIds } from "./transcript/blockIdentity.ts";
import type { TranscriptCaps } from "./transcript/capabilities.ts";
import type { Group } from "./transcript/types.ts";
import { composerHistory, isNoise, latestContext, spendOf } from "./transcript/model.ts";
import { TaskChecklist } from "./transcript/blocks.tsx";
import { useMarksController } from "./transcript/useMarks.ts";
import { MarkStrip } from "./transcript/MarkStrip.tsx";
import { targetLang } from "./translate.ts";
import { useTranslate } from "./useTranslate.ts";
import { SessionLinkMenuHost } from "../sessions/SessionLinkMenu.tsx";
import { useMirrorState } from "./parts/useMirrorState.ts";
import { useOlderHistory, useTranscriptPoll } from "./parts/useTranscriptPoll.ts";
import { useMirrorActions } from "./parts/useMirrorActions.ts";
import { composerInput, composerKeys, type MirrorSignal } from "./parts/composerInput.ts";
import { titleActions } from "./parts/titleActions.ts";
import { usePlanActions } from "./parts/usePlanActions.ts";
import { groupWithQueue } from "./parts/mirrorGroups.ts";
import { useFinalizeHold } from "./parts/useFinalizeHold.ts";
import { useTranscriptCaps } from "./parts/useTranscriptCaps.ts";
import { MirrorPendingCards } from "./parts/MirrorPendingCards.tsx";
import { MirrorComposer } from "./parts/MirrorComposer.tsx";
import { useSendQueue } from "./sendQueue/useSendQueue.ts";
import { MirrorOverlays } from "./parts/MirrorOverlays.tsx";

const q = encodeURIComponent;

export type { MirrorSignal } from "./parts/composerInput.ts";

// MirrorView (user-facing: "chat") is a read-mostly Markdown view of a claude
// session, built on the same Agent endpoints the MCP drive tools use: GET
// /sessions/{name}/messages?since=<cursor> (the jsonl transcript as structured turns
// — role + Markdown text + timestamp — plus a line cursor and live status) and POST
// /sessions/{name}/input (tmux send-keys). It overlays the still-mounted terminal
// (Pane keeps the PTY socket alive), so the user toggles terminal/chat freely.
//
// Limits (case-A): the transcript is written per turn, so turns appear per response,
// not token-by-token. Prompts typed in the raw terminal DO appear (they're logged as
// user turns), just at the next poll.
export function MirrorView(props: Parameters<typeof MirrorViewBody>[0]) {
  // Session slugs in the transcript open the session context menu (SessionLinkMenu.tsx).
  return (
    <SessionLinkMenuHost>
      <MirrorViewBody {...props} />
    </SessionLinkMenuHost>
  );
}

function MirrorViewBody({
  paneId,
  session,
  sessionMeta,
  active,
  mirror,
  onToggleMirror,
  readOnly = false,
  onResume,
  headerActions,
  signal,
  toolCard,
  composerBlock,
  aboveComposer,
}: {
  paneId: string;
  session: string;
  sessionMeta?: Session | null;
  active?: boolean;
  mirror?: boolean;
  onToggleMirror: (v: boolean) => void;
  readOnly?: boolean;
  onResume?: () => void;
  /** Pane popout/wrap/close (tabbed-grid mode only — see Pane.tsx tabHeaderActions). */
  headerActions?: ReactNode;
  /** The image studio's signal line (ADR 0100 decision 5), appended as the LAST line of what a
   *  composer send puts on the wire. Only the composer: seeds, peers and schedules carry none. */
  signal?: MirrorSignal;
  /** A host's own card for some tool calls (see TranscriptCaps.toolCard). */
  toolCard?: TranscriptCaps["toolCard"];
  /** Why this host holds the composer shut, drawn in its place (the image studio with no model
   *  chosen, ADR 0100 revision 9). Absent → the composer as usual. */
  composerBlock?: ReactNode;
  /** Drawn between the transcript and the composer (or whatever stands in its place): the
   *  image studio's draft bar. Absent → nothing. */
  aboveComposer?: ReactNode;
}) {
  const settings = useSettings();
  // Per-agent descriptor: how this session's assistant signs its turns, and which
  // chat affordances (image paste, …) it supports. Defaults to claude for a not-yet
  // loaded meta. codex/opencode reuse the same chat; only these bits differ.
  const agent = agentOf(sessionMeta?.kind);
  // Managed (paneless) sessions have no terminal, so the mirror is the primary UI: no toggle
  // is rendered, and answers go out as Interaction responses (/respond) rather than keys/seq.
  const managed = isManagedSession(sessionMeta);
  const agentName = agent.assistantName;
  // Store bridge (old context values): plans open as doc panes, edit-diffs as
  // diff panes; bumpSessions refreshes the shared list; wsState gates attach.
  const openTargetInNew = useLayoutStore((s) => s.openTargetInNew);
  const setPaneTarget = useLayoutStore((s) => s.setPaneTarget);
  const setActivePane = useLayoutStore((s) => s.setActive);
  const refreshSessions = useSessionsStore((s) => s.refresh);
  const sessionRow = useSessionsStore((st) => st.sessions.find((x) => x.name === session));
  const bumpSessions = () => void refreshSessions();
  const wsState = useWorkspaceStore((s) => s.state);
  const toast = useToast();
  useT(); // subscribe: a locale change re-renders MirrorView and its (unmemoized) turn subtree
  const locale = useLocale(); // the translation target when no answer language is fixed
  const running = wsState === "running"; // WS down → resume is inert, mirror the terminal's resume
  // "mod-enter" (default): Ctrl/⌘+Enter submits, plain Enter newlines (phone-safe).
  // "enter": Enter submits, Shift+Enter newlines.
  const modSend = settings.mirrorSend !== "enter";
  const st = useMirrorState({ session, sessionMeta, settings });
  const {
    turns, pendingSends, applyEchoes, loaded, handoffs, updateHandoff, termState, compactProg, status, bgBusy,
    bgBusyReason, finalizing, busy, tasks, files, queuedPrompts, pendingPeers, queueShown, discardView, alive,
    dirGone, pending, liveText, pendingPlan, pendingPerm, pendingApproval, carried, mode, agentCtx,
    suggestedTitle, titleActing, setManagedSettingsOpen, managedSettings, setForkAtTarget, draft, setDraft,
    dragging, setHistIdx, hasMore, loadingOlder, topSentinelRef, statusRef, tickRef, inputRef,
  } = st;
  // All transcript scroll positioning (bottom follow, scroll-to-top of a finished turn,
  // position restore, floating pills, prepending older history) lives in parts/useMirrorScroll.
  // Called before the TTS hook because that needs bodyRef.
  const scroll = useMirrorScroll();
  const { bodyRef, atBottomRef } = scroll;

  // --- Karaoke read-aloud (turnTts, docs/log/24) -------------------------------------
  // The whole TTS set (karaoke highlighting, auto read-aloud, the quiet reading of work
  // steps, confirmation announcements, "read from here") lives in parts/useMirrorTts; its two
  // reset calls sit in useTranscriptPoll.
  const tts = useMirrorTts({
    session,
    sessionMeta,
    paneId,
    active,
    readOnly,
    settings,
    bodyRef,
    statusRef,
    loaded,
    pending,
    pendingPlan,
    pendingPerm,
  });
  // Marks drawn on the conversation (docs/log/69 / ADR 0050). This is the owner's view, so it
  // may delete anyone's mark. Do not add a poll for them — reload() rides the transcript load
  // (see useTranscriptPoll).
  const marks = useMarksController({
    path: session ? `api/sessions/${q(session)}/marks` : "",
    canEdit: true,
    isOwner: true,
    viewerId: "",
    ownerLabel: tr("chat.you"),
    youLabel: tr("chat.you"),
  });
  // The transcript poll effect must not re-subscribe, so hand it the latest reload via a ref.
  const marksReloadRef = useRef(marks.reload);
  marksReloadRef.current = marks.reload;

  // Per-answer translation (docs/log/97). The reader's own button: nothing is fetched here
  // except the list of translations this session already has, once per open.
  const translate = useTranslate({
    session: session || "",
    lang: targetLang(settings.outputLanguage, locale),
    enabled: settings.mirrorTranslateEnabled !== false,
    auto: settings.mirrorAutoTranslate === true,
  });

  useTranscriptPoll({ session, sessionMeta, st, scroll, tts, marksReloadRef });

  // Reconcile optimistic echoes: once a sent prompt's real user turn lands in the
  // transcript (a matching non-noise user turn; managed attachments also have a unique
  // saved-path fallback),
  // drop the echo so the message isn't shown twice.
  useEffect(() => {
    applyEchoes((prev) => {
      if (!prev.length) return prev;
      const next = prev.filter((e) => !echoLanded(e, turns, isNoise));
      return next.length === prev.length ? prev : next;
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [turns, status]);

  // Re-decide bottom follow / scroll-to-top of a finished turn / position restore whenever the
  // transcript moves. The decision lives in parts/useMirrorScroll.applyFollow; only the deps
  // stay here.
  //
  // Being a LAYOUT effect is the point: it runs after the DOM changes but before paint and
  // before scroll events fire. `groups` and `loaded` move together with the deps below, so the
  // closure is fresh each time; leaving them out of the deps keeps unrelated re-renders (every
  // keystroke in the composer) from re-firing it.
  useLayoutEffect(() => {
    scroll.applyFollow({ groups, loaded, busy, live: !!liveText, pending, pendingPlan, pendingPerm: pendingPerm || (pendingApproval ? pendingApproval.id : null) });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [turns, pending, pendingPlan, pendingPerm, pendingApproval, status, bgBusy, finalizing, pendingSends, queuedPrompts, liveText]);


  const loadOlder = useOlderHistory({ session, st, scroll, toast });

  // Auto-grow the composer to fit its content (up to ~10 lines via the CSS max-height,
  // then it scrolls). Runs on every draft change, including the per-session draft restored
  // on mount. Shrinking to two rows to measure would let the transcript (this textarea's
  // sibling) grow its clientHeight in that instant, clamping a scrollTop that was pinned to
  // the bottom — keeping that shrink from escaping is autoGrowTextarea's job (see
  // lib/autoGrow.ts).
  useEffect(() => {
    autoGrowTextarea(inputRef.current);
  }, [draft]);

  // Focus the composer when this pane becomes the active chat — but not on touch
  // devices, where auto-focus would pop the on-screen keyboard just from switching
  // to read the chat. There the user taps the composer to type. (The other focus
  // calls are keystroke-driven — send / history nav — so the keyboard is
  // already up and refocusing is fine.)
  useEffect(() => {
    if (active && !coarsePointer()) inputRef.current?.focus();
  }, [active]);

  // After the user hits resume-and-continue, focus the composer the moment it becomes usable
  // (readOnly clears + the session goes alive + no resume menu in the terminal), so they
  // can type straight away. Flag is set on the resume click so this fires only for a
  // user-initiated resume, not a background one.
  const wantResumeFocusRef = useRef(false);
  useEffect(() => {
    if (wantResumeFocusRef.current && !readOnly && alive && termState !== "resume") {
      wantResumeFocusRef.current = false;
      inputRef.current?.focus();
    }
  }, [readOnly, alive, termState]);

  const actions = useMirrorActions({ session, running, managed, toast, st, scroll });
  const { wsDown, postKeys, draftBusy, dropPendingPeer, restoreDiscard, closeDiscard } = actions;

  const input = composerInput({ session, settings, signal, agent, managed, readOnly, toast, st, sendPrompt: actions.sendPrompt });
  const { composerLocked, onDragEnter, onDragOver, onDragLeave, onDrop, send } = input;


  // --- Skill picker (docs/log/50) --- implemented in parts/useSkillPicker; called here
  // because it reads composerLocked.
  const skillPicker = useSkillPicker({
    session,
    agent,
    managed,
    draft,
    setDraft,
    setHistIdx,
    inputRef,
    composerLocked,
  });


  // Pre-send queue (#1083): drains when the session is idle and nothing blocks a send.
  const sendQueue = useSendQueue({
    session,
    busy: st.busy,
    canSend: alive && !readOnly && !composerLocked && !st.sending && running,
    sendItem: input.sendQueued,
  });

  const plan = usePlanActions({
    session,
    sessionMeta,
    running,
    readOnly,
    toast,
    st,
    actions,
    openTargetInNew,
    setPaneTarget,
    setActivePane,
  });

  // Publish the edited-file list for readers outside this pane (the command palette's
  // "changes in this session" mode), so they don't have to poll the transcript themselves.
  useEffect(() => {
    if (session) useSessionFilesStore.getState().set(session, files);
  }, [session, files]);

  const { acceptTitle, dismissTitle } = titleActions({ session, st, wsDown, bumpSessions });
  // Composer history = the user's own prompts in this conversation (so ↑ works even
  // after a reload, not just for prompts typed since mount). Injected turns — operator,
  // schedule, peer, auto-resume … — are left out; see composerHistory.
  const history = composerHistory(turns);

  // Ctrl+R: bash's reverse-i-search over the same history ↑/↓ walks (parts/useHistorySearch).
  // ↑ is a fine way back through the last few prompts and a poor one through fifty.
  const histSearch = useHistorySearch({ history, draft, setDraft, setHistIdx, inputRef, composerLocked });

  // Memoized on the three states it reads: this walks every turn in the window and rebuilds every
  // block, and MirrorView re-renders for reasons that have nothing to do with the conversation —
  // a keystroke in the composer, a scroll flag, a chip. Recomputing then also hands every block a
  // new identity, which is what makes the memoized TranscriptTurn below actually skip.
  const grouped = useMemo(() => groupWithQueue(turns, pendingSends, queueShown), [turns, pendingSends, queueShown]);
  // useStableBlockIds, not groupTurns' own numbering: a backward page can prepend older rows of
  // the block the reader is IN, and the block must not change its name (React key / data-turn-idx)
  // under them when it does. See blockIdentity.ts.
  const groups = useStableBlockIds(grouped, session);

  // replyPending: the newest user prompt has no assistant reply after it yet — i.e. the
  // answer to the latest turn hasn't rendered. This is the signal that the mirror is still
  // waiting for the reply even if the session already reads idle.
  const replyPending = awaitingReply(groups);

  // "Fork from here" (docs/log/55). The conditions differ per kind (canBranchInSession);
  // without filtering here the button would either be pressable but always 400, or never
  // appear at all for claude.
  const canForkAt = canBranchInSession(agent.caps, { managed, readOnly });
  const openForkAt = (turn: Group) => {
    if (!canBranchFrom(turn)) return;
    setForkAtTarget({
      anchorId: turn.anchorId!,
      text: turn.text || "",
      carried: carriedUserTurns(groups, turn),
    });
  };

  // Reply suggestions (lib/quickReplies). The group after the newest user message is the
  // latest reply; its final text is the context for the B-1 heuristic, combined with the
  // frequency learning in settings.quickReplies.
  const lastUserGi = latestWorkPromptIndex(groups);
  // …and with no prompt in the window at all (a long autonomous stretch), the newest block is the
  // latest reply. Leaving it undefined there is what hid "start of the reply" on exactly the
  // sessions whose replies are long enough to need it.
  const replyGroup = lastUserGi >= 0 ? groups[lastUserGi + 1] : groups[groups.length - 1];
  const lastReplyText = replyGroup && replyGroup.role === "assistant" ? textOfParts(replyGroup.parts) : "";
  // Target of "reply from the top". Written to a ref during render so the ResizeObserver /
  // onScroll closures — created once under [] — can read the current value (same shape as
  // ttsCaptureRef).
  scroll.lastReplyIdxRef.current = replyGroup && replyGroup.role !== "user" ? replyGroup.idx : undefined;
  // Reply suggestions (lib/quickReplies plus v2's LLM candidates), implemented in
  // parts/useReplySuggest. Called here because the latest reply's final text is the context
  // for the candidates and is only settled at this point.
  const suggest = useReplySuggest({
    session,
    settings,
    draft,
    setDraft,
    setHistIdx,
    inputRef,
    composerLocked,
    modSend,
    lastReplyText,
    send,
    toast,
    wsDown,
  });

  const { recallPrev, recallNext, onKeyDown } = composerKeys({
    st,
    history,
    modSend,
    send,
    queue: input.queueDraft,
    histSearch,
    skillPicker,
    suggest,
    scroll,
  });

  useFinalizeHold({ st, replyPending });

  // Auto read-aloud of a new reply (P2). The decision lives in parts/useMirrorTts.syncAutoRead;
  // all that stays here is when to re-evaluate it — the deps, which the hook cannot subscribe
  // to (turns / groups are not visible inside it).
  useEffect(() => {
    tts.syncAutoRead({ turns, groups, status });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [turns, status, active, readOnly, session, settings.ttsEnabled, settings.ttsAutoReadMirror, settings.ttsAutoReadAllPanes, settings.ttsWorkRead]);

  // A /context-like gauge: the newest assistant turn's prompt size (input + cache) is
  // the current context fill. The per-category split (/context) is computed inside
  // claude and isn't in the transcript, but the cache breakdown is real usage data.
  // Fallback: an agent-reported session-level fill (agy — no per-turn usage exists),
  // rendered as a single un-broken-down segment against the agent's own window.
  const ctxUsage =
    latestContext(groups) ??
    (agentCtx && agentCtx.tokens > 0
      ? { read: 0, create: 0, fresh: agentCtx.tokens, model: undefined, window: agentCtx.window }
      : null);

  // Per-assistant-turn "spend" = newly-consumed tokens (uncached input + cache creation +
  // output). Cache reads are reused context, not fresh spend, so they're excluded (the ↑
  // number still carries total context). Drives the per-turn bar and the trend Sparkline.
  const spends = groups.filter((g) => g.role !== "user").map(spendOf).filter((n) => n > 0);
  const maxSpend = spends.length ? Math.max(...spends) : 0;

  const caps = useTranscriptCaps({
    session,
    sessionMeta,
    settings,
    managed,
    readOnly,
    agentName,
    st,
    actions,
    plan,
    openForkAt,
    canForkAt,
    tts,
    marks,
    translate,
    toolCard,
    maxSpend,
  });

  // Whether the session is in Plan mode. Case-insensitive so it holds against either the
  // labeled agent ("Plan") or an older one ("plan") — so the toggle direction (enter vs
  // exit) stays correct even before the Workspace picks up the new Agent image.
  const isPlan = mode.toLowerCase() === "plan";

  // Status chip: prefer the live polled status, fall back to the session meta.
  // rateLimitResumeAt rides along from the meta: the polled status is a bare string, so
  // without it the rate-limit-wait chip here could not say when the session moves again.
  const chip = status
    ? stateInfo({
        kind: "claude",
        alive: status !== "stopped",
        state: status,
        backgroundBusy: bgBusy,
        backgroundBusyReason: bgBusyReason,
        rateLimitResumeAt: sessionMeta?.rateLimitResumeAt,
      } as any)
    : sessionMeta
      ? stateInfo(sessionMeta)
      : null;

  // Region theme + surface color for the session mirror: data-theme scopes the base
  // tokens (tokens.css), and --chat-bg/--chat-accent are derived for the mirror's own
  // effective theme so a flipped mirror doesn't inherit the app-theme surface tint. The
  // accent falls back through the other surfaces (viewer/leftpane/topbar) as before.
  const mirrorEff = effectiveTheme(settings.mirrorTheme, settings.theme);
  const mirrorBg = surfaceBg(settings.chatColor, mirrorEff);
  const mirrorAccent =
    surfaceAccent(settings.chatColor) ||
    surfaceAccent(settings.viewerColor) ||
    surfaceAccent(settings.leftpaneColor) ||
    surfaceAccent(settings.topbarColor);

  return (
    <div
      ref={scroll.mirrorRef}
      className={"mirrorview" + (dragging ? " dragging" : "")}
      data-theme={settings.mirrorTheme !== "inherit" ? settings.mirrorTheme : undefined}
      style={{
        "--chat-font": chatFontStack(settings.chatFont),
        "--chat-size": settings.chatSize + "px",
        ...(mirrorBg ? { "--chat-bg": mirrorBg } : {}),
        ...(mirrorAccent ? { "--chat-accent": mirrorAccent } : {}),
      } as CSSProperties}
      onDragEnter={onDragEnter}
      onDragOver={onDragOver}
      onDragLeave={onDragLeave}
      onDrop={onDrop}
    >
      <ViewHead
        actions={
          <>
            {/* A managed (paneless) session has no terminal, so the toggle is not rendered. */}
            {!managed && <MirrorToggle mirror={!!mirror} onToggle={onToggleMirror} running={running} />}
            {/* Last = rightmost. In the tabbed grid the cell actions (pop out / close) go
                here, so keep them at the end to occupy the same top-right corner as the
                floating cluster does outside tabbed mode. */}
            {headerActions}
          </>
        }
      >
        {sessionMeta ? (
          <PaneSessionChip session={sessionMeta} state={chip} />
        ) : (
          <span className="view-title">{tr("mirror.session_fallback")}</span>
        )}
        {managed && (
          <button
            type="button"
            className="ghost managed-settings-btn"
            disabled={!running || !alive || readOnly}
            title={running && alive && !readOnly ? tr("mirror.exec_settings_edit") : tr("mirror.exec_settings_after_resume")}
            onClick={() => setManagedSettingsOpen(true)}
          >
            <Icon name="gear" />
            {managedSettings?.model ? prettyModel(managedSettings.model) : tr("mirror.exec_settings")}
            {managedSettings?.effort && <span> · {managedSettings.effort}</span>}
            {managedSettings?.mode === "plan" && <span> · Plan</span>}
          </button>
        )}
      </ViewHead>

      {ctxUsage && (
        <ContextBar
          {...ctxUsage}
          spends={spends}
          maxSpend={maxSpend}
          action={running && !readOnly && sessionRow ? <SpendChip s={sessionRow} /> : undefined}
        />
      )}
      {/* These keys exist to rebuild each strip per session, and siblings must never share one.
          When the key changes, React collects the leftover fibers in a Map keyed by key; a
          duplicate is overwritten last-wins, so the earlier one (ToDo) falls out of the Map and
          is left stranded in the DOM. Measured: every session switch stacked up one more of the
          previous session's ToDo strips (dev warns "two children with the same key", a
          production build is silent). Hence the prefixes. */}
      {tasks.length > 0 && <TaskChecklist key={"todo-" + session} tasks={tasks} session={session} />}
      <FileChangeStrip key={"files-" + session} session={session} files={files} />
      <MarkStrip key={"marks-" + session} marks={marks} storageKey={session} />
      <MirrorBanners
        isPlan={isPlan}
        termState={termState}
        compactProg={compactProg}
        suggestedTitle={suggestedTitle}
        titleActing={titleActing}
        onOpenTerminal={() => onToggleMirror(false)}
        onSkipUpdate={() => {
          postKeys(["2"]);
          setTimeout(() => tickRef.current?.(), 500);
        }}
        onAcceptTitle={acceptTitle}
        onDismissTitle={dismissTitle}
      />

      <div
        className="mirror-body"
        // The transcript is a vertically scrolled reading surface: one unwrappable long string
        // overflowing horizontally must not kill the phone's horizontal swipe between sessions
        // (app/swipeGuard.ts).
        data-swipe-y=""
        ref={bodyRef}
        onScroll={scroll.onBodyScroll}
        onMouseUp={tts.captureSel}
        // How position restore is abandoned (see the note on endRestoreOnInput). Wheel and touch
        // are caught here: .mirror-scroll's pointerdown/keydown (noteInteraction) never fire for
        // a wheel.
        onWheelCapture={scroll.endRestoreOnInput}
        onTouchStartCapture={scroll.endRestoreOnInput}
        onPointerDownCapture={scroll.noteReaderInput}
        onKeyDownCapture={scroll.noteReaderInput}
      >
        {/* Wrapper whose height == the transcript's total height, so a ResizeObserver can
            re-pin a bottom-stuck view to the true bottom as late content lays out — that's
            what makes opening a session land at the bottom, and keeps streaming glued to the
            tail. The jump-to-latest button stays OUTSIDE it (a direct child of the scroll
            container) so it sticks to the viewport. The interaction handlers tell that
            observer which reflows the READER caused (noteInteraction). */}
        <div
          className="mirror-scroll"
          ref={scroll.scrollBoxRef}
          onPointerDownCapture={scroll.noteInteraction}
          onKeyDownCapture={scroll.noteInteraction}
        >
        {loaded && hasMore && (
          <div className="mirror-loadmore" ref={topSentinelRef}>
            <button
              type="button"
              className="ghost mirror-loadmore-btn"
              disabled={loadingOlder}
              onClick={loadOlder}
            >
              {loadingOlder ? (
                <>
                  <Icon name="loading" spin /> {tr("chat.ph_loading")}
                </>
              ) : (
                <>
                  <Icon name="chevron-up" /> {tr("mirror.load_earlier")}
                </>
              )}
            </button>
          </div>
        )}
        {!loaded ? (
          running ? (
            // First fetch in flight (opening a session, or switching terminal → chat):
            // show a spinner instead of flashing the "no conversation yet" text.
            <div className="mirror-empty muted mirror-loading">
              <Icon name="loading" spin /> {tr("chat.ph_loading")}
            </div>
          ) : (
            // Workspace stopped: the transcript can't be fetched (the Agent is down), so
            // never spin forever — say so and point at the explicit Start.
            <div className="mirror-empty muted">
              {tr("mirror.ws_stopped_history")}
            </div>
          )
        ) : groups.length === 0 && !pending && !pendingPlan && !pendingPerm && !pendingApproval && !carried && handoffs.length === 0 ? (
          // handoffs.length === 0: with an empty transcript the proposals are the only
          // thing to show, and they now live inside renderGroups (which the empty branch
          // would skip).
          <div className="mirror-empty muted">
            {readOnly
              ? tr("mirror.no_history")
              : tr("mirror.no_conversation")}
          </div>
        ) : (
          <TranscriptView
            groups={groups}
            caps={caps}
            working={busy}
            autoCollapseWork={atBottomRef.current}
            inlineCards={handoffs.map((h) => ({
              at: h.created_at,
              node: (
                <HandoffProposal
                  key={"handoff-" + h.id}
                  session={session}
                  sessionMeta={sessionMeta}
                  proposal={h}
                  onChange={(next) => updateHandoff(h.id, next)}
                />
              ),
            }))}
          />
        )}
        <MirrorPendingCards
          session={session}
          sessionMeta={sessionMeta}
          managed={managed}
          agentName={agentName}
          toast={toast}
          translate={translate}
          st={st}
          actions={actions}
          plan={plan}
        />
        </div>
        <JumpPills
          showJump={scroll.showJump}
          showReplyTop={scroll.showReplyTop}
          onJumpBottom={scroll.jumpToBottom}
          onJumpReplyTop={scroll.jumpToReplyTop}
        />
      </div>

      {aboveComposer}
      <PendingPeersNotice items={pendingPeers} onDrop={(id) => void dropPendingPeer(id)} />
      {managed && !readOnly && running && !composerBlock && (
        <DiscardNotice notices={discardView} draftBusy={draftBusy} onRestore={restoreDiscard} onClose={closeDiscard} />
      )}
      {readOnly ? (
        dirGone ? (
          <DirGoneNotice />
        ) : (
          <ResumeNotice
            running={running}
            onResume={() => {
              wantResumeFocusRef.current = true;
              onResume?.();
            }}
          />
        )
      ) : composerBlock ? (
        composerBlock
      ) : !running ? (
        // Workspace stopped (or not yet running): the agent is down, so the composer can't
        // deliver a prompt — a send would just 502. When the WS stops, the sessions poll
        // freezes and this pane's `alive` stays stuck at its last live value (a CP 502 is an
        // error, so setAlive never flips), which used to leave the live composer enabled and
        // accepting input that silently failed. Block it here and frame the mirror as the
        // read-only history it now is; Start from the top bar brings it back. (readOnly handles
        // its own stopped case above; a live agent's resume/update menu can't be up with the WS
        // down, so this precedes those checks.)
        <WsStoppedNotice />
      ) : termState === "resume" ? (
        <TerminalResumeNotice onOpenTerminal={() => onToggleMirror(false)} />
      ) : termState === "update" ? (
        <TerminalUpdateNotice
          onSkip={() => {
            postKeys(["2"]);
            setTimeout(() => tickRef.current?.(), 500);
          }}
          onSkipUntilNext={() => {
            postKeys(["3"]);
            setTimeout(() => tickRef.current?.(), 500);
          }}
        />
      ) : !alive ? (
        <ResumingNotice />
      ) : (
        <MirrorComposer
          session={session}
          settings={settings}
          agent={agent}
          managed={managed}
          running={running}
          modSend={modSend}
          isPlan={isPlan}
          history={history}
          recallPrev={recallPrev}
          recallNext={recallNext}
          onKeyDown={onKeyDown}
          st={st}
          actions={actions}
          input={input}
          suggest={suggest}
          skillPicker={skillPicker}
          histSearch={histSearch}
          sendQueue={sendQueue}
        />
      )}
      <MirrorOverlays
        session={session}
        sessionMeta={sessionMeta}
        toast={toast}
        bumpSessions={bumpSessions}
        openTargetInNew={openTargetInNew}
        st={st}
      />
      {tts.pillPortal}
    </div>
  );
}
