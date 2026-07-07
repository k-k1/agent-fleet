// Session-open helpers: the navigation verbs the rail (and later the mirror /
// modals) use to put a session on screen. Old showTerminal/showTerminalSplit.
//
// TODO(P5): the old console opened chat-capable sessions (claude) in the chat
// mirror by default (showChat/showChatSplit). Until MirrorView is ported, every
// open lands on the terminal; a stopped session shows the 再開 mask instead of
// read-only history.
import { useLayoutStore } from "../../layout/store.ts";
import { reconnectSession } from "../../terminal/service.ts";
import { api, apiJSON } from "../../core/api/client.ts";

export function openSessionTerminal(name: string): void {
  useLayoutStore.getState().openTarget({ content: { kind: "terminal", chat: false }, session: name });
  // Re-clicking an already-open but disconnected session doesn't change the
  // pane's props (the declarative attach won't re-run) — revive its dropped
  // socket here so clicking a "[disconnected]" row reconnects it.
  reconnectSession(name);
}

export function openSessionTerminalSplit(name: string): void {
  useLayoutStore.getState().openTargetInNew({ content: { kind: "terminal", chat: false }, session: name });
  reconnectSession(name);
}

// sendPromptWhenAlive delivers a launch prompt (repo 起動 modal) to a freshly
// created session once it's actually up: poll the sessions list until the slot
// reports alive, give the CLI a beat to draw its prompt, then paste-send once.
//
// TODO(P5): this is the interim path — the old console stashed the prompt as a
// launchSeed and let MirrorView auto-send it when the chat mirror attached.
// Replace with that flow when MirrorView lands.
export function sendPromptWhenAlive(name: string, prompt: string): void {
  if (!name || !prompt) return;
  let tries = 0;
  const poll = async () => {
    tries++;
    if (tries > 30) return; // ~30s — give up silently (the user can paste it)
    let alive = false;
    try {
      const d = await api("api/sessions");
      alive = !!(d.sessions || []).find((s: { name: string; alive?: boolean }) => s.name === name)?.alive;
    } catch {}
    if (!alive) {
      setTimeout(poll, 1000);
      return;
    }
    // Alive = tmux is up; wait a beat for the agent CLI to finish drawing its
    // composer before pasting, so the text isn't swallowed by the boot screen.
    setTimeout(() => {
      void apiJSON(`api/sessions/${encodeURIComponent(name)}/input`, "POST", { prompt });
    }, 2500);
  };
  setTimeout(poll, 1200);
}
