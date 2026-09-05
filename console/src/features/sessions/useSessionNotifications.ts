// Desktop notifications on claude state arrivals, extracted from SessionsSection so
// it lives once at the app shell (the flat Sessions section no longer owns the rail).
// Asks for permission once (best-effort — badges work regardless), then fires when a
// polled session flips working→idle ("an answer came back") or reaches "question",
// skipping the session currently on the active pane. Reads the shared stores itself.
import { useEffect, useRef } from "react";
import { t } from "../../lib/i18n/index.ts";
import { displayName } from "../../lib/sessionview.ts";
import { agentOf } from "../../agents/registry.ts";
import { useLayoutStore } from "../../layout/store.ts";
import { activePane } from "../../layout/ops.ts";
import { getSettings } from "../../lib/settings.ts";
import { announce, sessionVoiceOpts } from "../chat/tts.ts";
import { hasTurnReader } from "../mirror/turnTts.ts";
import { useSessionsStore } from "./store.ts";

const notify = (title: string, body: string) => {
  if (!("Notification" in window) || Notification.permission !== "granted") return;
  try {
    new Notification(title, { body });
  } catch {
    /* ignore */
  }
};

export function useSessionNotifications(enabled = true): void {
  const sessions = useSessionsStore((s) => s.sessions);
  const layout = useLayoutStore((s) => s.layout);
  const activeSession = activePane(layout)?.session ?? null;
  const prevStates = useRef<Record<string, string | undefined>>({});

  // Notify on claude state arrivals (skip the session being viewed).
  useEffect(() => {
    if (!enabled) return;
    const prev = prevStates.current;
    const seen: Record<string, boolean> = {};
    for (const s of sessions) {
      seen[s.name] = true;
      if (agentOf(s.kind).caps.fixedAliveChip || !s.alive) {
        prev[s.name] = s.state;
        continue;
      }
      const before = prev[s.name];
      if (before !== undefined && before !== s.state && s.name !== activeSession) {
        // Browser notification plus a spoken one (docs/log/24 Tier1, when enabled): a
        // background session that answered or asked gets a short announcement prefixed with
        // its name, through the serial queue so it never interrupts. Sessions whose body the
        // mirror already reads aloud (ttsAutoReadAllPanes) get no second announcement —
        // answers are covered by the auto-read, questions by ttsReadPending.
        const st = getSettings();
        const speak = st.ttsSessionNotify;
        const mirrored = st.ttsEnabled && st.ttsAutoReadAllPanes && hasTurnReader(s.name);
        if (s.state === "idle" && before === "working") {
          notify(t("sx.notify_answered_title"), displayName(s));
          // The voice is fixed per session (sessionVoiceOpts), keyed by session name rather
          // than display name so a rename does not change it.
          if (speak && !(mirrored && st.ttsAutoReadMirror))
            announce(t("sx.notify_answered_body", { name: displayName(s) }), displayName(s), sessionVoiceOpts(s.name), s.name, "session-notification");
        } else if (s.state === "question") {
          notify(t("sx.notify_question_title"), displayName(s));
          if (speak && !(mirrored && st.ttsReadPending))
            announce(t("sx.notify_question_body", { name: displayName(s) }), displayName(s), sessionVoiceOpts(s.name), s.name, "session-notification");
        }
      }
      prev[s.name] = s.state;
    }
    for (const n of Object.keys(prev)) if (!seen[n]) delete prev[n];
  }, [sessions, activeSession, enabled]);
}
