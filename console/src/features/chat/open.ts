// Chat pane-open helpers (old openChat / openAssistantDraft). A conversation is
// identified by id, a not-yet-created draft by its assistant — sameTarget dedups
// so a split-open focuses an existing pane instead of duplicating (docs/log/19).
import { useLayoutStore } from "../../layout/store.ts";
import { setChatSeed } from "../../lib/chatSeed.ts";
import type { OpenTarget } from "../../layout/types.ts";

export const convTarget = (id: string): OpenTarget => ({
  content: { kind: "chat", conversationId: id, draftAssistantId: null },
});
export const draftTarget = (assistantId: string): OpenTarget => ({
  content: { kind: "chat", conversationId: null, draftAssistantId: assistantId },
});

// openChat opens (or focuses) the chat pane for a conversation. With a seed it also
// stashes a one-shot first-turn prompt: auto=false prefills the composer (Phase C),
// auto=true fires the turn automatically once ChatView loads (session handoff).
export function openChat(conversationId: string, seed?: string, auto = false): void {
  if (seed) setChatSeed(conversationId, seed, auto);
  useLayoutStore.getState().openTarget(convTarget(conversationId));
}

// openChatSplit opens the conversation in a NEW pane (split), leaving the current
// pane alone — the conv twin of openSessionChatSplit, used by Ctrl/Cmd / middle
// click on an auto-linked conversation slug.
export function openChatSplit(conversationId: string): void {
  useLayoutStore.getState().openTargetInNew(convTarget(conversationId));
}

export function openAssistantDraft(assistantId: string): void {
  useLayoutStore.getState().openTarget(draftTarget(assistantId));
}
