// A tiny out-of-React handoff for the first-turn prompt when a session is launched
// from a repo row's 起動 modal (LaunchModal). The modal creates the session, stashes
// the prompt keyed by the server-allocated slug, and opens the chat mirror; MirrorView
// takes the seed once the session is alive and sends it exactly once (auto-sent — the
// user already wrote it and hit 起動, unlike chatSeed which only prefills the composer).
// Module-level so it doesn't thread through context, and one-shot so a re-open / poll
// re-render doesn't re-send.
const seeds = new Map<string, string>();

export function setLaunchSeed(session: string, prompt: string): void {
  if (prompt) seeds.set(session, prompt);
}

// takeLaunchSeed returns the pending prompt for a session and clears it (one-shot).
// Call it only when actually ready to send — taking it early would drop the prompt.
export function takeLaunchSeed(session: string): string | undefined {
  const s = seeds.get(session);
  if (s !== undefined) seeds.delete(session);
  return s;
}

// hasLaunchSeed peeks without consuming — lets the mirror decide whether it must
// wait for the TUI to become ready before taking (and thereby committing to send).
export function hasLaunchSeed(session: string): boolean {
  return seeds.has(session);
}
