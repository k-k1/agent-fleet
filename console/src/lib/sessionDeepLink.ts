// Session deep link: ?session=<name> opens that session's pane directly. The
// producer is the chat bridge (docs/log/37) — its Discord notifications carry
// <base>/?session=<slug> so a tap on the phone lands IN the session, not on the
// Console home. Consumed once at boot: the param is stripped from the URL
// immediately (so reloads/back don't re-trigger), then we wait for the sessions
// store to learn the session (the list loads async; the workspace may still be
// starting) and open it the way a rail click would — chat-capable kinds get the
// mirror, others the terminal. Unknown/never-appearing names give up silently.
import { useSessionsStore } from "../features/sessions/store.ts";
import { agentOf } from "../agents/registry.ts";
import { openSessionChat, openSessionTerminal } from "../features/sessions/open.ts";

export function consumeSessionDeepLink(): void {
  let name = "";
  try {
    const u = new URL(location.href);
    name = u.searchParams.get("session") || "";
    if (!name) return;
    u.searchParams.delete("session");
    history.replaceState(history.state, "", u.toString());
  } catch {
    return;
  }
  if (!/^[a-z0-9][a-z0-9-]{2,39}$/.test(name)) return;
  let tries = 0;
  const tick = () => {
    const s = useSessionsStore.getState().sessions.find((x) => x.name === name);
    if (s) {
      (agentOf(s.kind).caps.chat ? openSessionChat : openSessionTerminal)(name);
      return;
    }
    // ~90s covers a workspace still coming up; beyond that the link is stale.
    if (++tries < 90) setTimeout(tick, 1000);
  };
  tick();
}
