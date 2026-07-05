// A tiny out-of-React handoff for the first-turn prompt when a chat is opened with a
// seed (e.g. a Files right-click "アシスタントで開く" — docs/19 Phase C). openChat stashes
// the seed keyed by conversation id; ChatView takes it once on load to prefill the
// composer (never auto-sent — the user reviews and hits Enter). Kept module-level so it
// doesn't need to thread through context, and one-shot so a re-open doesn't re-seed.
const seeds = new Map<string, string>();

export function setChatSeed(conversationId: string, seed: string): void {
  if (seed) seeds.set(conversationId, seed);
}

// takeChatSeed returns the pending seed for a conversation and clears it (one-shot).
export function takeChatSeed(conversationId: string): string | undefined {
  const s = seeds.get(conversationId);
  if (s !== undefined) seeds.delete(conversationId);
  return s;
}
