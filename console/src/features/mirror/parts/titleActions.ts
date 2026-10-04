import { raw } from "../../../core/api/client.ts";
import type { MirrorActions } from "./useMirrorActions.ts";
import type { MirrorState } from "./useMirrorState.ts";

const q = encodeURIComponent;

/** titleActions answers the suggested-title banner (MirrorBanners) for this render. */
export function titleActions({
  session,
  st,
  wsDown,
  bumpSessions,
}: {
  session: string;
  st: MirrorState;
  wsDown: MirrorActions["wsDown"];
  bumpSessions: () => void;
}) {
  const { titleActing, setTitleActing, setSuggestedTitle } = st;
  // Auto-suggested title (session_title.go): accepting promotes it to the session's real
  // title (bumpSessions so the left-pane label updates without waiting for its own
  // poll); dismissing discards it. Either way the server never offers one again.
  const acceptTitle = async () => {
    if (!session || titleActing) return;
    if (wsDown()) return; // title accept/dismiss is agent-served (session_title.go) → 502 while stopped
    setTitleActing(true);
    try {
      const res = await raw(`api/sessions/${q(session)}/title/accept`, { method: "POST" });
      if (res.ok) {
        setSuggestedTitle("");
        bumpSessions();
      }
    } catch {
      /* transient — next poll re-syncs suggestedTitle either way */
    } finally {
      setTitleActing(false);
    }
  };
  const dismissTitle = async () => {
    if (!session || titleActing) return;
    if (wsDown()) return;
    setTitleActing(true);
    try {
      const res = await raw(`api/sessions/${q(session)}/title/dismiss`, { method: "POST" });
      if (res.ok) setSuggestedTitle("");
    } catch {
      /* same as above */
    } finally {
      setTitleActing(false);
    }
  };
  return { acceptTitle, dismissTitle };
}
