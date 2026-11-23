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
import { pushStamp } from "../core/push/events.ts";
import { setLandingTenantHint, useTenantStore } from "../core/store/tenant.ts";
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
    // The tenant store takes the tenant as its landing hint (kept in memory, so the retries of one
    // boot share it); the URL loses it now, or a reload after the person switched away would pull
    // the tab back to it.
    tenant = u.searchParams.get("tenant") || "";
    u.searchParams.delete("session");
    u.searchParams.delete("tenant");
    history.replaceState(history.state, "", u.toString());
  } catch {
    return;
  }
  if (tenant) setLandingTenantHint(tenant);
  openSessionByName(name, tenant);
}

/** Opens a session's pane once it is in the list (a workspace still coming up is waited for).
 *
 *  With `tenant`, the session is looked up only in that tenant's own list: the tenant must be the
 *  active one, and the list is read from the server for it rather than taken from the store, whose
 *  rows may still be the previous tenant's (the store keeps them across a switch, and a stream
 *  frame or a slow answer can land in either order). */
export function openSessionByName(name: string, tenant = ""): void {
  if (!/^[A-Za-z0-9_-]{1,40}$/.test(name)) return;
  let tries = 0;
  const again = () => {
    // ~90s covers a workspace still coming up; beyond that the link is stale.
    if (++tries < 90) setTimeout(tick, 1000);
  };
  const open = (kind: string) => (agentOf(kind).caps.chat ? openSessionChat : openSessionTerminal)(name);
  const tick = () => {
    if (!tenant) {
      const s = useSessionsStore.getState().sessions.find((x) => x.name === name);
      if (s) open(s.kind);
      else again();
      return;
    }
    if (useTenantStore.getState().tenant !== tenant) return again();
    void (async () => {
      const stamp = pushStamp("sessions");
      let list: { name: string; kind: string }[] | null = null;
      try {
        const d = await api("api/sessions");
        if (d && !d.error && !isTransientErr(d)) list = d.sessions || [];
      } catch {
        /* retried below */
      }
      // The tenant may have moved on while the request was out: its answer is not this tenant's.
      if (!list || useTenantStore.getState().tenant !== tenant) return again();
      // Publish only when no newer frame arrived meanwhile (the store's own rule): a late, older
      // answer must not overwrite it.
      if (pushStamp("sessions") === stamp) useSessionsStore.getState().applyList(list as never);
      const s = list.find((x) => x.name === name);
      if (s) open(s.kind);
      else again();
    })();
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
 *  tab shows: switch to it first when the person is a member there and the switch is not declined. */
async function openPushTarget(name: string, tenant: string): Promise<void> {
  if (tenant && useTenantStore.getState().tenant !== tenant) {
    if (!(await membershipsKnown())) return;
    if (!useTenantStore.getState().tenants.some((t) => t.slug === tenant)) return;
    await useTenantStore.getState().select(tenant);
    if (useTenantStore.getState().tenant !== tenant) return;
  }
  openSessionByName(name, tenant);
}
