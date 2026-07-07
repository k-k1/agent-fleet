// Chat pane-open helpers (old openChat / openAssistantDraft). A conversation is
// identified by id, a not-yet-created draft by its assistant — sameTarget dedups
// so a split-open focuses an existing pane instead of duplicating (docs/19).
import { useLayoutStore } from "../../layout/store.ts";
import { setChatSeed } from "../../lib/chatSeed.ts";
import type { OpenTarget } from "../../layout/types.ts";

export const convTarget = (id: string): OpenTarget => ({
  content: { kind: "chat", conversationId: id, draftAssistantId: null },
});
export const draftTarget = (assistantId: string): OpenTarget => ({
  content: { kind: "chat", conversationId: null, draftAssistantId: assistantId },
});

export function openChat(conversationId: string, seed?: string): void {
  if (seed) setChatSeed(conversationId, seed); // one-shot composer prefill (Phase C)
  useLayoutStore.getState().openTarget(convTarget(conversationId));
}

export function openAssistantDraft(assistantId: string): void {
  useLayoutStore.getState().openTarget(draftTarget(assistantId));
}
