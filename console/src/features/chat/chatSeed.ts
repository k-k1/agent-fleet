// A tiny out-of-React handoff for the composer prefill when a chat is opened with a seed
// (e.g. a Files right-click "Open in an assistant", or a session handoff). openChat stashes
// the seed keyed by conversation id; ChatView takes it once on load and puts it in the
// composer for the member to review and send. A seed never sends a turn by itself: a turn can
// make the model act (a session handoff's turn reads another session's output and may call
// create_session), so it needs an explicit member action. Module-level so it doesn't need to
// thread through context, and one-shot so a re-open doesn't re-seed.
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
