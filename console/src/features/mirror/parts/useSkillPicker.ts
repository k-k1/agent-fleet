import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent as RKeyboardEvent, RefObject } from "react";
import { sessionSkills } from "../../../core/api/client.ts";
import type { SessionSkill } from "../../../core/api/client.ts";
import { t as tr } from "../../../lib/i18n/index.ts";
import { coarsePointer } from "../../../lib/device.ts";
import { useDismiss } from "../../../lib/useDismiss.ts";
import type { AgentDescriptor } from "../../../agents/registry.ts";
import {
  applySkillToDraft,
  exactSkills,
  filterSkills,
  hasTriggerHead,
  pickerTokenAt,
  slashTokenAt,
  type SlashToken,
} from "../skillPicker.ts";

/**
 * Skill picker (docs/log/50 / ADR0034, v2 cross-agent + §8 cross-skill injection): the completion
 * list of skills/commands callable in the session. Besides native invocation (invoke - "/name", or
 * codex "$name"), a SKILL.md from another convention (foreign - carries path/origin) is inserted
 * as a "read this path and follow its instructions" prompt; being plain prompt text it works for
 * any kind/driver. Two ways to open: typing the leading trigger character (keyboard users; a kind
 * with skillTrigger="" is button-only) and the dedicated button (mouse/tap users). Selection uses
 * the sel-index scheme that keeps focus in the textarea (same shape as CommandPalette). For a kind
 * whose managed invocation is unverified (opencode), only native items are dropped via
 * slashSkillsManaged=false - foreign items are not gated.
 *
 * Reads composerLocked, so call this after composerLocked has been decided (i.e. after the
 * composer's setup).
 */
export function useSkillPicker({
  session,
  agent,
  managed,
  draft,
  setDraft,
  setHistIdx,
  inputRef,
  composerLocked,
}: {
  session: string;
  agent: AgentDescriptor;
  managed: boolean;
  draft: string;
  setDraft: (v: string) => void;
  setHistIdx: (v: number | null) => void;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  composerLocked: boolean;
}) {
  const canSkills = agent.caps.slashSkills;
  const skillTrigger = agent.skillTrigger; // "" = button only (typing never opens it)
  const [skills, setSkills] = useState<SessionSkill[] | null>(null); // null = not fetched yet
  const [slashTok, setSlashTok] = useState<SlashToken | null>(null); // leading /-token being typed
  const [skillBtnOpen, setSkillBtnOpen] = useState(false); // opened from the button (shows everything)
  const [skillSel, setSkillSel] = useState(0);
  const skillDismissRef = useRef<string | null>(null); // token at the time Esc/outside-click closed it (stays closed until it changes)
  const skillPopRef = useRef<HTMLDivElement>(null);
  const skillBtnRef = useRef<HTMLButtonElement>(null);
  const skillSelRef = useRef<HTMLButtonElement>(null);

  // slashOpen: the leading trigger token is alive and was not just dismissed.
  // skillListVisible: when the list is actually drawn - typing-initiated shows nothing when there
  // are no matches (so a hand-typed plain /plan is not covered up), while button-initiated shows
  // the empty state so "there are none" is visible. Opening requires typing the trigger (a bare
  // token does not open it); filtering uses the same token for either origin, so typing after
  // opening with the button still narrows the list.
  // skillArgs (passive display): the command is fully typed and arguments are being written. The
  // list stays up so the argument hint remains readable, but is narrowed to the single settled
  // item and does not capture the keyboard (Enter still sends - taking Enter here would make it
  // impossible to send while typing arguments).
  const slashOpen = canSkills && !composerLocked && slashTok !== null && !slashTok.bare && skillDismissRef.current !== slashTok.token;
  const skillArgs = slashOpen && !!slashTok?.args;
  const skillsOpen = canSkills && !composerLocked && (skillBtnOpen || slashOpen);
  const skillQuery = slashTok?.token ?? "";
  const skillItems = (skillArgs ? exactSkills(skills ?? [], skillQuery) : skills ? filterSkills(skills, skillQuery) : [])
    // For a kind with unverified managed invocation, drop only the native items (a foreign
    // injection is just a prompt).
    .filter((s) => !!s.path || !managed || agent.caps.slashSkillsManaged);
  // Passive display only when exactly one item matched, so the popup does not appear and vanish
  // while loading, or while a "/"-leading sentence that matches nothing is being written; the
  // looser button-initiated/typing-initiated conditions are deliberately not used here.
  const skillListVisible = skillsOpen && (skillArgs ? skillItems.length > 0 : skillBtnOpen || skills === null || skillItems.length > 0);
  // Capture the keyboard (↑↓ to move, Enter/Tab to confirm) only in the active display.
  const skillNavActive = skillListVisible && !skillArgs;
  // Native inserts invoke as is; foreign is built into a "read this path and follow its
  // instructions" prompt (trailing space, so arguments can be typed right after).
  const skillInsertText = (s: SessionSkill): string =>
    s.invoke || tr("mirror.skills_use_foreign", { path: s.path ?? "" }) + " ";

  // Fetch on open (reset when the session changes). Fetched every time: having the session create
  // a SKILL.md mid-conversation is a normal way to work, so each open pulls a fresh list (the scan
  // is cheap).
  useEffect(() => setSkills(null), [session]);
  useEffect(() => {
    if (!skillsOpen || !session) return;
    let live = true;
    sessionSkills(session)
      .then((d) => live && setSkills(d.skills || []))
      .catch(() => live && setSkills((s) => s ?? [])); // on failure: keep what was fetched, treat unfetched as empty
    return () => {
      live = false;
    };
  }, [skillsOpen, session]);

  // Close when the draft drifts from the token we hold (cleared on send, history recall and other
  // direct setDraft writes). The head also accepts full-width aliases (／ and ＄ from a Japanese
  // IME), so use hasTriggerHead rather than startsWith (a bare token has no trigger at all, hence
  // the check only for non-bare).
  useEffect(() => {
    if (!slashTok) return;
    if ((!slashTok.bare && !hasTriggerHead(draft, skillTrigger)) || !draft.slice(0, slashTok.end).endsWith(slashTok.token))
      setSlashTok(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draft]);

  // Reset the selection to the top when the filter changes, and scroll the selection into view
  // when it moves.
  useEffect(() => setSkillSel(0), [slashTok?.token, skillBtnOpen]);
  // Write this with a block body (never return the expression): since Chrome 150 scrollIntoView()
  // returns a Promise for scroll completion, so an implicit return stores that Promise as the
  // effect's cleanup, React calls it as a function on the next run and the resulting TypeError
  // goes uncaught, unmounting the whole root - a black screen. This effect re-runs whenever the
  // item count changes, so a filter going from 1 to 0 items hits it.
  useEffect(() => {
    skillSelRef.current?.scrollIntoView({ block: "nearest" });
  }, [skillSel, skillItems.length]);

  // Insertion: replace the token being typed (or the head of the whole draft if there is none)
  // with the invocation string (invoke - "/name " or "$name "), keeping the existing body as
  // arguments. Do not focus on touch devices (GBoard covers the screen - same rule as
  // applySuggestion). Does not send; the user sends after adding arguments.
  const pickSkill = (invoke: string) => {
    const el = inputRef.current;
    const caret = el ? (el.selectionStart ?? draft.length) : draft.length;
    const { next, caret: nc } = applySkillToDraft(draft, caret, invoke, skillTrigger, skillBtnOpen);
    setDraft(next);
    setHistIdx(null);
    setSkillBtnOpen(false);
    skillDismissRef.current = null;
    // Right after invoke the caret sits past the trailing space = the argument position, so this
    // becomes an args token: the list stays in passive display and the chosen skill's argument
    // hint stays readable while the arguments are written.
    setSlashTok(slashTokenAt(next, nc, skillTrigger));
    if (coarsePointer()) {
      inputRef.current?.blur();
      return;
    }
    requestAnimationFrame(() => {
      const el2 = inputRef.current;
      if (el2) {
        el2.focus();
        el2.setSelectionRange(nc, nc);
      }
    });
  };

  // Close (Esc, outside click, pressing the button again). Typing-initiated leaves a "do not
  // reopen while the token is unchanged" mark - deleting and retyping (a different token) opens
  // it again.
  const closeSkillPicker = () => {
    setSkillBtnOpen(false);
    skillDismissRef.current = slashTok?.token ?? null;
  };
  // Close on outside click. A click inside the textarea (caret move) is excluded: onSelect
  // re-tracks the token there and the list should stay alive, so inputRef is part of refs.
  useDismiss([skillPopRef, skillBtnRef, inputRef], skillListVisible, closeSkillPicker);

  // Open from the button, or close it if already open (the "/" button).
  const toggleFromButton = () => {
    if (skillListVisible) {
      closeSkillPicker();
      return;
    }
    skillDismissRef.current = null;
    setSkillBtnOpen(true);
    // Use a leading token that is already written as the query straight away (it opens already
    // filtered). With the caret in the second word or later this is null = everything.
    const el = inputRef.current;
    setSlashTok(pickerTokenAt(draft, el?.selectionStart ?? draft.length, skillTrigger, true));
  };

  // Skill picker trigger tracking: the token is set only while the caret is inside the single
  // token following the leading trigger character. When the token dies the Esc suppression is
  // released too (retyping shows it again). While opened from the button, a leading token with no
  // trigger is picked up as well, so it filters directly.
  const trackTyping = (value: string, caret: number) => {
    if (!canSkills) return;
    const tok = pickerTokenAt(value, caret, skillTrigger, skillBtnOpen);
    if (!tok) skillDismissRef.current = null;
    setSlashTok(tok);
  };
  /** Re-track whether the token is alive on caret moves (click, arrow keys) too. */
  const trackCaret = (value: string, caret: number) => {
    if (canSkills) setSlashTok(pickerTokenAt(value, caret, skillTrigger, skillBtnOpen));
  };

  // While the skill picker is open, capture ↑/↓ (move selection), Enter/Tab (confirm) and Esc
  // (close) here, ahead of history recall (↑/↓), chip Tab and send Enter. Do not touch keys during
  // IME composition. Ctrl/⌘+Enter and Shift+Enter pass through (an escape hatch to send or insert
  // a newline). The passive display (typing arguments = skillArgs) captures nothing - it only
  // shows an argument hint, so Enter still sends and ↑/↓ still move the caret; only the closing
  // Esc is accepted. Returns true when the key was captured (the caller stops there).
  const handleKeyDown = (e: RKeyboardEvent): boolean => {
    if (!skillListVisible || e.nativeEvent.isComposing) return false;
    if (skillNavActive && (e.key === "ArrowDown" || e.key === "ArrowUp") && skillItems.length) {
      e.preventDefault();
      const n = skillItems.length;
      setSkillSel((s) => (s + (e.key === "ArrowDown" ? 1 : n - 1)) % n);
      return true;
    }
    if (skillNavActive && ((e.key === "Enter" && !e.ctrlKey && !e.metaKey && !e.shiftKey) || e.key === "Tab") && skillItems[skillSel]) {
      e.preventDefault();
      pickSkill(skillInsertText(skillItems[skillSel]));
      return true;
    }
    if (e.key === "Escape") {
      e.preventDefault();
      closeSkillPicker();
      return true;
    }
    return false;
  };

  return {
    canSkills,
    trigger: skillTrigger,
    skills,
    items: skillItems,
    sel: skillSel,
    setSel: setSkillSel,
    query: skillQuery,
    listVisible: skillListVisible,
    passive: skillArgs,
    popRef: skillPopRef,
    btnRef: skillBtnRef,
    selRef: skillSelRef,
    pick: (s: SessionSkill) => pickSkill(skillInsertText(s)),
    toggleFromButton,
    trackTyping,
    trackCaret,
    handleKeyDown,
  };
}
