// A tiny out-of-React handoff for the first-turn prompt when a chat is opened with a
// seed (e.g. a Files right-click "アシスタントで開く" — docs/log/19 Phase C, or a session
// handoff). openChat stashes the seed keyed by conversation id; ChatView takes it once
// on load. `auto` decides what ChatView does with it: false = prefill the composer for
// the user to review and hit Enter; true = fire the first turn automatically once the
// conversation has loaded (the handoff "アシスタントを直接呼び出す" flow). Kept
// module-level so it doesn't need to thread through context, and one-shot so a re-open
// doesn't re-seed.
export interface ChatSeed {
  text: string;
  auto: boolean; // true = send the first turn automatically; false = prefill only
}

const seeds = new Map<string, ChatSeed>();

export function setChatSeed(conversationId: string, seed: string, auto = false): void {
  if (seed) seeds.set(conversationId, { text: seed, auto });
}

// takeChatSeed returns the pending seed for a conversation and clears it (one-shot).
export function takeChatSeed(conversationId: string): ChatSeed | undefined {
  const s = seeds.get(conversationId);
  if (s !== undefined) seeds.delete(conversationId);
  return s;
}
