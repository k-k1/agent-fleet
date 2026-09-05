// core/store/tts — global state for text-to-speech (docs/log/24). Only one playback exists in
// the whole app: a chat answer and a FileView selection share the same single playback. TopBar
// subscribes to `speaking` to show the speaking indicator and its stop button, which calls
// stop(). The engine (features/chat/tts.ts) calls setActive/setSpeaking from outside React.
import { create } from "zustand";
import type { TtsController } from "../../features/chat/tts.ts";
import { getSettings, setSetting } from "../../lib/settings.ts";
import { toast } from "../../ui/toast.ts";
import { t } from "../../lib/i18n/index.ts";

interface TtsStore {
  speaking: boolean; // true while audio is playing or queued for synthesis
  preparing: boolean; // waiting on synthesis before the first sound (drives TopBar's spinner)
  source: string; // label for what is being read (chat, a selection, ...)
  voice: string; // character name of the voice, to tell per-session voices apart ("" = hidden)
  sessionName: string; // session name (id) the reading came from, for the left pane's row icon; "" = not a session (chat, a selection, ...)
  purpose: "reading" | "session-notification" | "usage-notification" | "manual";
  active: TtsController | null; // current playback controller (internal; not for subscribers)
  setActive(c: TtsController | null, source: string, voice?: string, sessionName?: string, purpose?: TtsStore["purpose"]): void;
  setSpeaking(v: boolean): void;
  setPreparing(v: boolean): void;
  stop(): void;
}

export const useTtsStore = create<TtsStore>((set, get) => ({
  speaking: false,
  preparing: false,
  source: "",
  voice: "",
  sessionName: "",
  purpose: "reading",
  active: null,
  setActive: (c, source, voice = "", sessionName = "", purpose = "reading") => set({ active: c, source, voice, sessionName, purpose }),
  setSpeaking: (v) => set((s) => (s.speaking === v ? s : { speaking: v })),
  setPreparing: (v) => set((s) => (s.preparing === v ? s : { preparing: v })),
  stop: () => get().active?.stop(),
}));

// Speech on/off toggle, shared by TopBar's speaker button and the keyboard command.
// Pressing it while playing means "silence this", so it stops AND clears the flag of whatever
// produced the sound: stopping alone would leave ttsEnabled on and the next answer would speak
// again. While idle it simply toggles ttsEnabled.
export function toggleTtsPlayback(): void {
  const st = useTtsStore.getState();
  const busy = st.speaking || st.preparing;
  if (busy) {
    st.stop();
    if (st.purpose === "session-notification") setSetting("ttsSessionNotify", false);
    else if (st.purpose === "usage-notification") setSetting("usageResetNotify", false);
    else if (st.purpose !== "manual") setSetting("ttsEnabled", false);
    // Pressed while playing = "silence it now": report the stop rather than a specific
    // on/off, since which switch flipped depends on what was playing.
    toast(t("keys.toast.ttsStopped"), { kind: "success" });
    return;
  }
  const next = !getSettings().ttsEnabled;
  setSetting("ttsEnabled", next);
  toast(t(next ? "keys.toast.ttsOn" : "keys.toast.ttsOff"), { kind: "success" });
}
