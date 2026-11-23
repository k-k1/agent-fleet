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
import { api, isTransientErr } from "../core/api/client.ts";
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
    // `tenant` stays in the URL until the tenant store has settled on it (the store reads it as its
    // landing hint while it boots); then it goes, or a reload would pull the tab back to it after
    // the person switched away.
    tenant = u.searchParams.get("tenant") || "";
    u.searchParams.delete("session");
    history.replaceState(history.state, "", u.toString());
  } catch {
    return;
  }
  openSessionByName(name, tenant, dropTenantParam);
}

function dropTenantParam(): void {
  try {
    const u = new URL(location.href);
    if (!u.searchParams.has("tenant")) return;
    u.searchParams.delete("tenant");
    history.replaceState(history.state, "", u.toString());
  } catch {
    /* a URL that cannot be rewritten is left as it is */
  }
}

/** Opens a session's pane once it is in the list (a workspace still coming up is waited for).
 *  With `tenant`, only while that tenant is the active one (the tenant may still be resolving). */
export function openSessionByName(name: string, tenant = "", settled?: () => void): void {
  if (!/^[A-Za-z0-9_-]{1,40}$/.test(name)) return;
  let tries = 0;
  const tick = () => {
    const wrongTenant = tenant !== "" && useTenantStore.getState().tenant !== tenant;
    const s = wrongTenant ? undefined : useSessionsStore.getState().sessions.find((x) => x.name === name);
    if (s) {
      settled?.();
      (agentOf(s.kind).caps.chat ? openSessionChat : openSessionTerminal)(name);
      return;
    }
    // ~90s covers a workspace still coming up; beyond that the link is stale.
    if (++tries < 90) setTimeout(tick, 1000);
    else settled?.();
  };
  tick();
}

/** A tapped push notification asks an already open Console to open its session (public/sw.js). */
export function listenPushOpen(): void {
  if (!("serviceWorker" in navigator)) return;
  navigator.serviceWorker.addEventListener("message", (e) => {
    // "af-open-tenant-session" is the hand-off that names a tenant; a Console from before this
    // existed ignores it, so the worker's new window (which carries ?tenant=) opens instead of
    // that old tab opening a same-named session of its own tenant.
    if (e.data?.type !== "af-open-session" && e.data?.type !== "af-open-tenant-session") return;
    // Acknowledge first: the worker opens a new window when no page answers.
    e.ports?.[0]?.postMessage("ok");
    if (typeof e.data.session === "string" && e.data.session) {
      void openPushTarget(e.data.session, typeof e.data.tenant === "string" ? e.data.tenant : "");
    }
  });
}

/** The tenant store's membership list is empty until the tenants endpoint has answered, which is
 *  not "no memberships": a tap that arrives in that window waits for it (up to ~30 s). */
async function membershipsKnown(): Promise<boolean> {
  for (let i = 0; i < 60; i++) {
    if (useTenantStore.getState().tenants.length > 0) return true;
    await new Promise((r) => setTimeout(r, 500));
  }
  return false;
}

/** A push belongs to the tenant of the membership it was sent to, which may not be the one this
 *  tab shows: switch to it (when the person is a member there and the switch is not declined)
 *  before opening. The target tenant's list is fetched here and published, because the store's
 *  own refresh keeps the previous tenant's rows on a failed or stale answer, and a row of the
 *  wrong tenant must never be what the name is looked up in. */
async function openPushTarget(name: string, tenant: string): Promise<void> {
  if (tenant && useTenantStore.getState().tenant !== tenant) {
    if (!(await membershipsKnown())) return;
    if (!useTenantStore.getState().tenants.some((t) => t.slug === tenant)) return;
    if (useTenantStore.getState().tenant !== tenant) {
      await useTenantStore.getState().select(tenant);
      if (useTenantStore.getState().tenant !== tenant) return;
      try {
        const d = await api("api/sessions");
        if (isTransientErr(d) || d?.error) return;
        if (useTenantStore.getState().tenant !== tenant) return;
        useSessionsStore.getState().applyList(d.sessions || []);
      } catch {
        return;
      }
    }
  }
  openSessionByName(name, tenant);
}
