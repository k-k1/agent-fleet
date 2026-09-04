// A tiny out-of-React handoff for the first-turn prompt when a session is launched
// from the "start work" modal (「作業を始める」, LaunchModal): the modal creates the
// session, stashes the prompt keyed by the server-allocated slug, and opens the chat
// mirror; MirrorView takes it and shows it as an optimistic echo.
//
// DISPLAY ONLY — the prompt itself is delivered by the Agent (the create call's
// initial_prompt, or POST /input {when_ready} when attachments made the text final only
// after create; see useStartWork.ts). It used to be typed from the mirror, which meant a
// session launched into a background tab was never started at all until the user selected
// that tab (a pane renders only its selected view, so the mirror wasn't mounted).
//
// Module-level so it doesn't thread through context, and one-shot so a re-open / poll
// re-render doesn't show it twice.
const seeds = new Map<string, string>();

export function setLaunchSeed(session: string, prompt: string): void {
  if (prompt) seeds.set(session, prompt);
}

// takeLaunchSeed returns the pending prompt for a session and clears it (one-shot).
export function takeLaunchSeed(session: string): string | undefined {
  const s = seeds.get(session);
  if (s !== undefined) seeds.delete(session);
  return s;
}
