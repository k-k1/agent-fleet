import type { ClipboardEvent as RClipboardEvent, DragEvent as RDragEvent, KeyboardEvent as RKeyboardEvent } from "react";
import { errText, pasteImage } from "../../../core/api/client.ts";
import type { AgentDescriptor } from "../../../agents/registry.ts";
import { setSetting, type Settings } from "../../../lib/settings.ts";
import { isQuickReplyCandidate, recordQuickReply, unhideQuickReply } from "../../../lib/quickReplies.ts";
import { makeAttachment } from "../../../lib/attachDraft.ts";
import { coarsePointer } from "../../../lib/device.ts";
import { scrollComposerViewport } from "../../../lib/keyScroll.ts";
import { t as tr } from "../../../lib/i18n/index.ts";
import { useTtsStore } from "../../../core/store/tts.ts";
import type { useToast } from "../../../ui/ToastProvider.tsx";
import { MEMO_DND_MIME } from "../../memo/dnd.ts";
import { composerSend } from "../composerSend.ts";
import type { MirrorActions } from "./useMirrorActions.ts";
import type { MirrorState } from "./useMirrorState.ts";
import type { useHistorySearch } from "./useHistorySearch.ts";
import type { useMirrorScroll } from "./useMirrorScroll.ts";
import type { useReplySuggest } from "./useReplySuggest.ts";
import type { useSkillPicker } from "./useSkillPicker.ts";

/** What a host that owns a signal line hands the composer (see MirrorView's `signal`). */
export interface MirrorSignal {
  /** The line to append now, or "" for none. */
  line: () => string;
  /** The send carrying it was accepted. */
  sent: () => void;
}

/**
 * composerInput builds the composer's input handlers for this render: attaching files (paste,
 * drop, picker), dropping a memo in, the locks that keep free text away from a pending
 * decision, and send. It holds no hooks; it only closes over the state it is handed.
 */
export function composerInput({
  session,
  settings,
  signal,
  agent,
  managed,
  readOnly,
  toast,
  st,
  sendPrompt,
}: {
  session: string;
  settings: Settings;
  signal?: MirrorSignal;
  agent: AgentDescriptor;
  managed: boolean;
  readOnly: boolean;
  toast: ReturnType<typeof useToast>;
  st: MirrorState;
  sendPrompt: MirrorActions["sendPrompt"];
}) {
  const {
    busy, alive, pending, pendingPlan, pendingPerm, pendingApproval, draft, setDraft, attach, attachments,
    setPasting, setDragging, dragDepth, setHistIdx, inputRef,
  } = st;
  const canPasteImage = agent.caps.imagePaste;

  // addFiles uploads files to the session and holds each as an attachment chip —
  // shared by clipboard paste, drag&drop onto the pane, and the attach picker. Upload +
  // saved path referenced in the prompt (kind-worded by buildImagePrompt).
  const addFiles = async (files: File[]) => {
    if (!files.length) return;
    setPasting(true);
    for (const f of files) {
      try {
        const res = await pasteImage(session, f);
        if (res.status < 300 && res.path && res.name) {
          const path = res.path;
          const nm = res.name;
          // Non-images get no preview URL — the chip shows an icon + name instead.
          attach.add([makeAttachment(f, { name: nm, path })]);
        } else {
          toast(res.error ? errText(res.error) : tr("mirror.attach_failed"));
        }
      } catch {
        toast(tr("mirror.attach_failed_net"));
      }
    }
    setPasting(false);
    inputRef.current?.focus();
  };

  // Paste file(s) from the clipboard into the composer. Non-file pastes fall through
  // to the default (text). Agents without the cap let everything fall through.
  const onPaste = async (e: RClipboardEvent<HTMLTextAreaElement>) => {
    if (!canPasteImage) return;
    const items = e.clipboardData?.items;
    if (!items) return;
    const files: File[] = [];
    for (let i = 0; i < items.length; i++) {
      const it = items[i];
      if (it.kind === "file") {
        const f = it.getAsFile();
        if (f) files.push(f);
      }
    }
    if (!files.length) return; // ordinary text paste — let it happen
    e.preventDefault();
    await addFiles(files);
  };

  const removeAttachment = (i: number) => attach.remove(i);
  // Discard the draft only once the send succeeded, i.e. the paths made it into the prompt.
  const clearAttachments = () => attach.clear();

  // An AskUserQuestion can't be answered by the composer's free text — verified against
  // the terminal (v2.1.204, docs/build/92): the modal IGNORES typed text on option rows
  // entirely (the older "option filter" behavior is gone), so the trailing Enter just
  // confirms the highlighted (first) option — a silent wrong answer. Digit keys 1-9 even
  // select-and-submit instantly, so stray text is doubly dangerous. Lock the composer for
  // ANY pending question and steer the user to the card — its options key-drive the modal
  // (Down×i, Enter) and its free-text row uses the still-working "Type something" path.
  // An empty array (no questions) must not lock: the card only renders for pending.length > 0,
  // so `!!pending` alone would kill the composer with no card to answer in.
  const auqLocksComposer = !!pending?.length;
  // A pending plan approval or permission prompt is a menu decision, NOT a free-text turn:
  // sending would type text + Enter, and that Enter selects the menu's default (approve /
  // allow), silently confirming it. A mode toggle would likewise mis-key the menu. So lock
  // the composer AND the mode chip while one is pending; act via the card's buttons.
  const decisionPending = !!pendingPlan || !!pendingPerm || !!pendingApproval;
  const composerLocked = auqLocksComposer || decisionPending;

  // OS drag&drop anywhere on the pane attaches the dropped files (the composer is a
  // small target — the whole chat area accepts). dragenter/leave nest per child, so a
  // depth counter drives the highlight; drop is ignored while the composer is hidden
  // (read-only history) or locked.
  const canDropFiles = canPasteImage && !readOnly && !composerLocked;
  // A memo dragged from the left-pane queue drops its text into the composer — but ONLY
  // when this session is awaiting input (alive and idle: not working, no lingering background run,
  // not mid-finalize, composer not locked by an AUQ/plan). A busy session would just queue
  // the text unseen, so we refuse the drop there.
  const sessionIdle = alive && !readOnly && !composerLocked && !busy;
  const canDropMemo = sessionIdle;
  // Which kind of drop, if any, this drag offers here (types are readable on enter/over;
  // getData is not, so the branch is decided from the type list).
  const dragIntent = (e: RDragEvent): "file" | "memo" | null => {
    const types = e.dataTransfer?.types;
    if (!types) return null;
    if (canDropFiles && types.includes("Files")) return "file";
    if (canDropMemo && types.includes(MEMO_DND_MIME)) return "memo";
    return null;
  };
  const onDragEnter = (e: RDragEvent) => {
    if (!dragIntent(e)) return;
    e.preventDefault();
    dragDepth.current++;
    setDragging(true);
  };
  const onDragOver = (e: RDragEvent) => {
    if (!dragIntent(e)) return;
    e.preventDefault();
  };
  const onDragLeave = (e: RDragEvent) => {
    if (!dragIntent(e)) return;
    e.preventDefault();
    if (--dragDepth.current <= 0) {
      dragDepth.current = 0;
      setDragging(false);
    }
  };
  const onDrop = async (e: RDragEvent) => {
    const intent = dragIntent(e);
    if (!intent) return;
    e.preventDefault();
    dragDepth.current = 0;
    setDragging(false);
    if (intent === "memo") {
      const text = e.dataTransfer.getData(MEMO_DND_MIME);
      if (text) insertMemoText(text); // the memo stays queued — this is a copy
      return;
    }
    const files = Array.from(e.dataTransfer?.files || []);
    await addFiles(files);
  };

  // Drop a dragged memo's text into the composer: append below any existing draft, then
  // focus and park the caret at the end. Never sends — the user reviews and submits.
  const insertMemoText = (text: string) => {
    setDraft((d) => (d ? d.replace(/\s*$/, "") + "\n" + text : text));
    setHistIdx(null);
    requestAnimationFrame(() => {
      const el = inputRef.current;
      if (el) {
        el.focus();
        el.setSelectionRange(el.value.length, el.value.length);
      }
    });
  };

  // With an override, send that text (a suggestion chip's Alt-click instant send); otherwise
  // send the composer's draft.
  const send = async (override?: string) => {
    if (composerLocked) return;
    const text = (override ?? draft).trim();
    if (!text && !attachments.length) return;
    // Short plain text feeds the reply-suggestion learning. Only sends through here count, so
    // AUQ/plan answers are naturally excluded. Re-sending a phrase that was hidden from the
    // menu says the user wants it back, so unhide it.
    if (text && isQuickReplyCandidate(text, attachments.length > 0)) {
      setSetting("quickReplies", recordQuickReply(settings.quickReplies || {}, text, Date.now()));
      const hidden = settings.quickRepliesHidden || [];
      const unhidden = unhideQuickReply(hidden, text);
      if (unhidden !== hidden) setSetting("quickRepliesHidden", unhidden);
    }
    // Sending from the composer while this session is being read aloud stops that playback:
    // the user is interrupting or following up, so hearing the old answer out is only
    // confusing. Matched by sessionName, so playback from another session keeps running.
    const ts = useTtsStore.getState();
    if (ts.active && ts.sessionName === session) ts.stop();
    const staged = attachments; // restored on failure (revive, below)
    const paths = attachments.map((a) => a.path);
    // managed passes them as wire attachments (the driver converts them into API attachments,
    // docs/log/27 §10.2-3); tui weaves the paths into the prompt body, and the studio signal
    // follows as the last line (composerSend).
    const line = signal?.line() || "";
    const out = composerSend(text, paths, agent.id, managed, line);
    setHistIdx(null);
    setDraft("");
    clearAttachments();
    // On touch devices, drop focus so the soft keyboard (GBoard) retracts once the
    // turn is sent — the reply is what the user wants to read, not keep typing. Desktop
    // keeps focus (and refocuses below) so typing the next turn needs no extra click.
    if (coarsePointer()) inputRef.current?.blur();
    // Restore the attachments too when the send is refused. Restoring only the text is the
    // worst outcome: the message is back, so the user re-sends believing it is the same turn,
    // and sends one with no images.
    if (!(await sendPrompt(out.echo, out.attachments, text, out.wire))) attach.revive(staged);
    else if (line) signal?.sent();
    if (!coarsePointer()) inputRef.current?.focus();
  };

  return {
    addFiles, onPaste, removeAttachment, auqLocksComposer, decisionPending, composerLocked, onDragEnter,
    onDragOver, onDragLeave, onDrop, send,
  };
}

/**
 * composerKeys builds the composer's keyboard handling for this render: history recall (also
 * the phone's on-screen buttons) and the textarea's onKeyDown, which hands keys to the history
 * search, the skill picker and the suggestion row before scrolling, recall and send.
 */
export function composerKeys({
  st,
  history,
  modSend,
  send,
  histSearch,
  skillPicker,
  suggest,
  scroll,
}: {
  st: MirrorState;
  history: string[];
  modSend: boolean;
  send: () => void;
  histSearch: ReturnType<typeof useHistorySearch>;
  skillPicker: ReturnType<typeof useSkillPicker>;
  suggest: ReturnType<typeof useReplySuggest>;
  scroll: ReturnType<typeof useMirrorScroll>;
}) {
  const { draft, setDraft, histIdx, setHistIdx, inputRef } = st;
  const { bodyRef } = scroll;
  // Recall the previous / next prompt from history (shared by ↑/↓ and the on-screen
  // buttons shown on phones, which have no arrow keys).
  const recallPrev = () => {
    if (!history.length) return;
    const ni = histIdx !== null ? Math.max(0, histIdx - 1) : history.length - 1;
    setHistIdx(ni);
    setDraft(history[ni]);
    inputRef.current?.focus();
  };
  const recallNext = () => {
    if (histIdx === null) return;
    const ni = histIdx + 1;
    if (ni >= history.length) {
      setHistIdx(null);
      setDraft("");
    } else {
      setHistIdx(ni);
      setDraft(history[ni]);
    }
    inputRef.current?.focus();
  };

  const onKeyDown = (e: RKeyboardEvent) => {
    if (histSearch.handleKeyDown(e)) return; // Ctrl+R opens the history search
    if (skillPicker.handleKeyDown(e)) return; // while the skill picker is open it takes ↑↓/Enter/Tab/Esc
    if (suggest.handleKeyDown(e)) return; // Tab: enter the chip row / cycle completions
    // Scroll the transcript without leaving the composer: Ctrl/⌘+↑/↓ nudges, PageUp/PageDown
    // (and Ctrl/⌘+[ / ]) page, Ctrl/⌘+End snaps to the newest turn and re-arms auto-follow.
    // Checked before history recall so the modified arrows don't get swallowed by the ↑/↓
    // recall path below.
    if (!e.nativeEvent.isComposing && scrollComposerViewport(e, bodyRef.current, scroll.jumpToBottom)) return;
    // Shell-style history: ↑/↓ recall past prompts when the field is empty (or once
    // recall is underway). With text present, arrows move the caret as usual. Only the BARE
    // arrows recall — Shift+↑/↓ must stay the textarea's select-by-line (it no longer scrolls
    // the transcript, so without this guard it would fall through to recall here).
    if ((e.key === "ArrowUp" || e.key === "ArrowDown") && !e.nativeEvent.isComposing && !e.shiftKey && !e.altKey) {
      if (e.key === "ArrowUp" && (draft === "" || histIdx !== null) && history.length) {
        e.preventDefault();
        recallPrev();
        return;
      }
      if (e.key === "ArrowDown" && histIdx !== null) {
        e.preventDefault();
        recallNext();
        return;
      }
    }
    // Don't intercept Enter while an IME candidate window is open (JP/CJK input).
    if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
    const mod = e.ctrlKey || e.metaKey;
    if (modSend) {
      // Ctrl/⌘+Enter submits; plain Enter falls through to insert a newline.
      if (mod) {
        e.preventDefault();
        send();
      }
    } else if (!e.shiftKey && !mod) {
      // Enter submits; Shift+Enter falls through to insert a newline.
      e.preventDefault();
      send();
    }
  };

  return { recallPrev, recallNext, onKeyDown };
}
