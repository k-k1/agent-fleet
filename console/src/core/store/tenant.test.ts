// Contract test for the tenant store's whoami re-read. whoami was a boot-only snapshot that
// also carries deployment capabilities (scheduler_enabled), so restarting the CP with changed
// settings left stale values until a reload. It is now re-read on a push reconnect; this pins
// that (1) back-to-back reconnects do not hammer it and (2) an error response or a network
// drop never erases an already resolved identity.
import { beforeAll, beforeEach, describe, expect, it, vi } from "vitest";

// The store imports the api client, which binds window.fetch and reads document.baseURI, so
// the globals are stubbed before the import (the same style as workspace.test.ts).
const values = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (key: string) => values.get(key) ?? null,
  setItem: (key: string, value: string) => values.set(key, value),
  removeItem: (key: string) => values.delete(key),
});
vi.stubGlobal("document", { baseURI: "http://localhost/", hidden: false });
const fetchMock = vi.fn<() => Promise<Response>>();
vi.stubGlobal("window", { fetch: fetchMock });
vi.stubGlobal("fetch", fetchMock);

let useTenantStore: (typeof import("./tenant.ts"))["useTenantStore"];
let setTenant: (typeof import("../api/client.ts"))["setTenant"];
beforeAll(async () => {
  ({ useTenantStore } = await import("./tenant.ts"));
  ({ setTenant } = await import("../api/client.ts"));
});

const jsonRes = (body: unknown) =>
  new Response(JSON.stringify(body), { status: 200, headers: { "Content-Type": "application/json" } });

// The throttle is based on Date.now (module-level state), so the test advances the clock.
let now = 1_700_000_000_000;
const advance = (ms: number) => (now += ms);
vi.spyOn(Date, "now").mockImplementation(() => now);

describe("tenant store refreshWhoami", () => {
  // A reconnect reads both whoami and /api/tenants, i.e. 2 fetches per refresh.
  const reconnectRes = (tenants: unknown) =>
    fetchMock.mockImplementation((...args: unknown[]) =>
      Promise.resolve(
        String(args[0]).includes("whoami") ? jsonRes({ user: "u1", scheduler_enabled: true }) : jsonRes(tenants),
      ),
    );

  beforeEach(() => {
    fetchMock.mockReset();
    useTenantStore.setState({ whoami: { user: "u1", scheduler_enabled: false }, superAdmin: false });
    advance(60_000); // move well past the previous test's read
  });

  it("adopts the re-read deployment flags (a CP restart flips them)", async () => {
    reconnectRes({ tenants: [{ slug: "dev" }], super_admin: false });
    await useTenantStore.getState().refreshWhoami();
    expect(useTenantStore.getState().whoami?.scheduler_enabled).toBe(true);
    expect(fetchMock).toHaveBeenCalledTimes(2);
  });

  // The regression guard that matters: superAdmin and the roster were read once at boot, and
  // if that one read hit a database failure the Admin / Tenant-admin items
  // (「管理」/「テナント管理」) stayed gone for the life of the tab. A reconnect is exactly the
  // moment the answer may have changed, so re-reading here heals it without a reload.
  it("re-reads the roster so a boot-time failure heals without a reload", async () => {
    reconnectRes({ tenants: [{ slug: "dev", role: "member" }], super_admin: true });
    await useTenantStore.getState().refreshWhoami();
    expect(useTenantStore.getState().superAdmin).toBe(true);
  });

  // ?tenant= is a landing preselection, so honouring it on every reconnect would yank the
  // person back out of the tenant they switched to afterwards. Read at boot only.
  it("does not re-apply the ?tenant= boot hint on a reconnect", async () => {
    vi.stubGlobal("location", { search: "?tenant=sales", pathname: "/" });
    setTenant("dev");
    reconnectRes({ tenants: [{ slug: "dev" }, { slug: "sales" }], super_admin: false });
    await useTenantStore.getState().refreshWhoami();
    expect(useTenantStore.getState().tenant).toBe("dev");
    vi.stubGlobal("location", { search: "", pathname: "/" });
  });

  it("throttles back-to-back reconnects (tab show/hide reconnects too)", async () => {
    reconnectRes({ tenants: [{ slug: "dev" }], super_admin: false });
    await useTenantStore.getState().refreshWhoami();
    await useTenantStore.getState().refreshWhoami();
    await useTenantStore.getState().refreshWhoami();
    expect(fetchMock).toHaveBeenCalledTimes(2);

    advance(60_000);
    await useTenantStore.getState().refreshWhoami();
    expect(fetchMock).toHaveBeenCalledTimes(4);
  });

  it("never clobbers a resolved identity with an error payload", async () => {
    // A restarting CP answers a plain-text 5xx, which api() synthesizes into http_5xx.
    fetchMock.mockResolvedValue(new Response("workspace agent unreachable", { status: 502 }));
    await useTenantStore.getState().refreshWhoami();
    expect(useTenantStore.getState().whoami).toEqual({ user: "u1", scheduler_enabled: false });
  });

  it("survives a network drop without touching the identity", async () => {
    fetchMock.mockRejectedValue(new Error("network"));
    await expect(useTenantStore.getState().refreshWhoami()).resolves.toBeUndefined();
    expect(useTenantStore.getState().whoami).toEqual({ user: "u1", scheduler_enabled: false });
  });
});

// ?tenant=<slug> is the hint the Control Plane leaves after a sign-in that started
// at /login/<slug> (docs/log/61 §61.10.4), so somebody who opened their department's
// link lands in that department rather than in whichever tenant this browser last
// used. It is a PRESELECTION only: it is honoured just when the server already
// listed that tenant among the person's memberships (ADR0043 decision 14).
describe("tenant store boot hint", () => {
  const boot = async (search: string, slugs: string[], persisted: string) => {
    vi.stubGlobal("location", { search, pathname: "/" });
    setTenant(persisted);
    fetchMock.mockReset();
    fetchMock.mockImplementation((...args: unknown[]) => {
      const url = String(args[0]);
      if (url.includes("whoami")) return Promise.resolve(jsonRes({ user: "u1" }));
      return Promise.resolve(jsonRes({ tenants: slugs.map((slug) => ({ slug })), super_admin: false }));
    });
    advance(60_000);
    await useTenantStore.getState().init();
    return useTenantStore.getState().tenant;
  };

  it("prefers the hinted tenant over the persisted selection", async () => {
    expect(await boot("?tenant=sales", ["dev", "sales"], "dev")).toBe("sales");
  });

  it("ignores a tenant the person is not a member of", async () => {
    // Anyone can type any slug — this is exactly why the hint may never be an
    // authorization input. Fall back to the persisted (still valid) selection.
    expect(await boot("?tenant=secret", ["dev", "sales"], "dev")).toBe("dev");
  });

  it("keeps the persisted selection when there is no hint", async () => {
    expect(await boot("", ["dev", "sales"], "sales")).toBe("sales");
  });
});

// The pre-invite (not_provisioned) landing state (docs/log/61 §61.10.2, P7-2).
//
// AF_PROVISION=invite is the default for new installs, so this is not an error path but the
// first thing someone sees before they are invited. Without the flag the normal Console opens
// and every subsequent request is rejected with a 403, one toast at a time.
describe("tenant store not_provisioned", () => {
  const errRes = (code: string, status = 403) =>
    new Response(JSON.stringify({ error: { code, message: code } }), {
      status,
      headers: { "Content-Type": "application/json" },
    });

  const boot = async (tenantsRes: () => Response) => {
    values.clear();
    fetchMock.mockReset();
    fetchMock.mockImplementation((...args: unknown[]) =>
      Promise.resolve(String(args[0]).includes("whoami") ? jsonRes({ user: "u1", email: "u1@example.com" }) : tenantsRes()),
    );
    advance(60_000);
    useTenantStore.setState({ notProvisioned: false });
    await useTenantStore.getState().init();
  };

  it("flags the landing state so App can render it", async () => {
    await boot(() => errRes("not_provisioned"));
    expect(useTenantStore.getState().notProvisioned).toBe(true);
  });

  // Do not conflate it with other 403s: falling in here on an unselected tenant or a
  // permission error would tell someone who may open the Console that they are not invited.
  it("does not flag any other terminal error", async () => {
    await boot(() => errRes("forbidden_tenant"));
    expect(useTenantStore.getState().notProvisioned).toBe(false);
  });

  // Cleared once the roster answers (an admin added them while the tab was open, and a
  // retry got through).
  it("clears once the roster answers", async () => {
    useTenantStore.setState({ notProvisioned: true });
    await boot(() => jsonRes({ tenants: [{ slug: "dev" }], super_admin: false }));
    expect(useTenantStore.getState().notProvisioned).toBe(false);
  });

  // A super_admin never reaches this state: the CP answers 200 even with zero memberships
  // (decision 23). Break that contract and whoever creates the first tenant is trapped on
  // the landing page.
  it("never lands a super_admin with no membership", async () => {
    await boot(() => jsonRes({ tenants: [], super_admin: true }));
    const s = useTenantStore.getState();
    expect(s.notProvisioned).toBe(false);
    expect(s.superAdmin).toBe(true);
  });

  // The CP reports a database failure as a 500 WITH a JSON body (`{"error":{"code":"internal"}}`).
  // Judging by the code alone reads that as a permanent application error, retries stop and
  // superAdmin stays false — the admin menu then disappears silently, as seen on a real
  // deployment.
  it("retries a JSON-bodied 500 instead of settling on it", async () => {
    let calls = 0;
    useTenantStore.setState({ superAdmin: false });
    await boot(() => {
      calls++;
      return calls === 1 ? errRes("internal", 500) : jsonRes({ tenants: [], super_admin: true });
    });
    expect(useTenantStore.getState().superAdmin).toBe(false); // still down after the 1st try
    await new Promise((r) => setTimeout(r, 900)); // wait for the 700ms-backoff retry
    expect(useTenantStore.getState().superAdmin).toBe(true);
    expect(calls).toBeGreaterThan(1);
  });
});
