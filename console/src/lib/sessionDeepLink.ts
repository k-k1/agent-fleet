// Session deep link: ?session=<name> opens that session's pane directly. The
// producer is the chat bridge (docs/log/37) — its Discord notifications carry
// <base>/?session=<slug> so a tap on the phone lands IN the session, not on the
// Console home. Consumed once at boot: the param is stripped from the URL
// immediately (so reloads/back don't re-trigger), then we wait for the sessions
// store to learn the session (the list loads async; the workspace may still be
// starting) and open it the way a rail click would — chat-capable kinds get the
// mirror, others the terminal. Unknown/never-appearing names give up silently.
//
// A link may also name the tenant the session belongs to (a push does: ?tenant=<slug>, or the
// `tenant` of the hand-off message). A session name is only unique within a tenant, so a link that
// names one opens the session only while that tenant is the active one; it never falls back to a
// same-named session of another tenant.
import { useTenantStore } from "../core/store/tenant.ts";
import { useSessionsStore } from "../features/sessions/store.ts";
import { agentOf } from "../agents/registry.ts";
import { openSessionChat, openSessionTerminal } from "../features/sessions/open.ts";

export function consumeSessionDeepLink(): void {
  let name = "";
  let tenant = "";
  try {
    const u = new URL(location.href);
    name = u.searchParams.get("session") || "";
    if (!name) return;
    // `tenant` stays in the URL: the tenant store reads it as its landing hint when it boots.
    tenant = u.searchParams.get("tenant") || "";
    u.searchParams.delete("session");
    history.replaceState(history.state, "", u.toString());
  } catch {
    return;
  }
  openSessionByName(name, tenant);
}

/** Opens a session's pane once it is in the list (a workspace still coming up is waited for).
 *  With `tenant`, only while that tenant is the active one (the tenant may still be resolving). */
export function openSessionByName(name: string, tenant = ""): void {
  if (!/^[A-Za-z0-9_-]{1,40}$/.test(name)) return;
  let tries = 0;
  const tick = () => {
    const wrongTenant = tenant !== "" && useTenantStore.getState().tenant !== tenant;
    const s = wrongTenant ? undefined : useSessionsStore.getState().sessions.find((x) => x.name === name);
    if (s) {
      (agentOf(s.kind).caps.chat ? openSessionChat : openSessionTerminal)(name);
      return;
    }
    // ~90s covers a workspace still coming up; beyond that the link is stale.
    if (++tries < 90) setTimeout(tick, 1000);
  };
  tick();
}

/** A tapped push notification asks an already open Console to open its session (public/sw.js). */
export function listenPushOpen(): void {
  if (!("serviceWorker" in navigator)) return;
  navigator.serviceWorker.addEventListener("message", (e) => {
    if (e.data?.type !== "af-open-session") return;
    // Acknowledge first: the worker opens a new window when no page answers.
    e.ports?.[0]?.postMessage("ok");
    if (typeof e.data.session === "string" && e.data.session) {
      void openPushTarget(e.data.session, typeof e.data.tenant === "string" ? e.data.tenant : "");
    }
  });
}

/** A push belongs to the tenant of the membership it was sent to, which may not be the one this
 *  tab shows: switch to it (when the person is a member there and the switch is not declined)
 *  before opening, and read that tenant's sessions first so the old tenant's list cannot match. */
async function openPushTarget(name: string, tenant: string): Promise<void> {
  const ts = useTenantStore.getState();
  if (tenant && ts.tenant !== tenant) {
    if (!ts.tenants.some((t) => t.slug === tenant)) return;
    await ts.select(tenant);
    if (useTenantStore.getState().tenant !== tenant) return;
    await useSessionsStore.getState().refresh();
  }
  openSessionByName(name, tenant);
}
