// Which of our sessions a pane view puts on screen. `view.session` alone does not answer it:
//
//   - it is the pane's terminal BINDING, kept while the pane shows something else (a file, a
//     studio) so "back to the terminal" knows where to go — so it counts only for terminal
//     content;
//   - a session bound to an image studio opens in the studio pane (ADR 0100 decision 10), which
//     embeds its mirror and carries only the studio id.
//
// Read the field as "shown" and a studio opened over a pane that showed another session
// acknowledges THAT session's notifications and wears its question ring, while the studio's
// own session is never acknowledged — the jump to the next session that needs you then lands
// on the same studio on every press.
import type { PaneContent } from "../../layout/types.ts";

export function shownSession(
  view: { session?: string | null; content: PaneContent } | null | undefined,
  sessions: { name: string; studio?: string }[],
): string {
  if (!view) return "";
  if (view.content.kind === "terminal") return view.session || "";
  if (view.content.kind === "imagegen" && view.content.studioId) {
    const id = view.content.studioId;
    return sessions.find((s) => s.studio === id)?.name || "";
  }
  return "";
}
