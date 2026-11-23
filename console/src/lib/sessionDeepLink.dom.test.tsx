// The deep link and the push hand-off open a session of the tenant they name, looked up in that
// tenant's own list, never a same-named session of whichever tenant the tab shows (#4870).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

type Row = { name: string; kind: string };

// Tenant "a" has a chat-capable `work` (claude); tenant "b" has a terminal-only `work` (codex).
const tenantState = {
  tenant: "a",
  tenants: [{ slug: "a" }, { slug: "b" }] as { slug: string }[],
  select: vi.fn(async (slug: string) => {
    if (declineSwitch) return;
    tenantState.tenant = slug;
  }),
};
let declineSwitch = false;
const landing: string[] = [];
vi.mock("../core/store/tenant.ts", () => ({
  useTenantStore: { getState: () => tenantState },
  setLandingTenantHint: (s: string) => void landing.push(s),
}));

let sessions: Row[] = [];
const applyList = vi.fn((list: Row[]) => void (sessions = list));
vi.mock("../features/sessions/store.ts", () => ({ useSessionsStore: { getState: () => ({ sessions, applyList }) } }));
vi.mock("../agents/registry.ts", () => ({ agentOf: (k: string) => ({ caps: { chat: k === "claude" } }) }));
let stamp = 0;
vi.mock("../core/push/events.ts", () => ({ pushStamp: () => stamp }));

const apiCalls: string[] = [];
let apiAnswer: () => Promise<unknown> = async () => ({ sessions: [{ name: "work", kind: "codex" }] });
let clientTenant: string | null = null; // the API client's own selection; null = follows the store
vi.mock("../core/api/client.ts", () => ({
  getTenant: () => clientTenant ?? tenantState.tenant,
  api: (path: string) => {
    apiCalls.push(path);
    return apiAnswer();
  },
  isTransientErr: (d: any) => !!d?.error && String(d.error.code).startsWith("http_5"),
}));
const opened: string[] = [];
vi.mock("../features/sessions/open.ts", () => ({
  openSessionChat: (n: string) => void opened.push(`chat:${n}`),
  openSessionTerminal: (n: string) => void opened.push(`term:${n}`),
}));

import { consumeSessionDeepLink, listenPushOpen, openSessionByName } from "./sessionDeepLink.ts";

let onMessage: (e: unknown) => void = () => {};
const flush = async () => {
  for (let i = 0; i < 20; i++) await Promise.resolve();
};

beforeEach(() => {
  vi.useFakeTimers();
  opened.length = 0;
  landing.length = 0;
  apiCalls.length = 0;
  declineSwitch = false;
  stamp = 0;
  clientTenant = null;
  tenantState.tenant = "a";
  tenantState.tenants = [{ slug: "a" }, { slug: "b" }];
  tenantState.select.mockClear();
  applyList.mockClear();
  apiAnswer = async () => ({ sessions: [{ name: "work", kind: "codex" }] });
  // The store still holds tenant a's row: the one that must NOT be what a tenant-b open uses.
  sessions = [{ name: "work", kind: "claude" }];
  Object.defineProperty(navigator, "serviceWorker", {
    configurable: true,
    value: { addEventListener: (_: string, f: (e: unknown) => void) => (onMessage = f) },
  });
  listenPushOpen();
});
afterEach(() => {
  vi.useRealTimers();
  history.replaceState(null, "", "/");
});

const tap = async (data: Record<string, unknown>, type = data.tenant ? "af-open-tenant-session" : "af-open-session") => {
  const acks: unknown[] = [];
  onMessage({ data: { type, ...data }, ports: [{ postMessage: (m: unknown) => acks.push(m) }] });
  await flush();
  return acks;
};

describe("listenPushOpen", () => {
  it("looks the session up in the push's tenant's own list even when the tab already shows it", async () => {
    // The tab is on "b" but its store still holds a's rows (the picker switch's list has not arrived).
    tenantState.tenant = "b";
    await tap({ session: "work", tenant: "b" });
    expect(tenantState.select).not.toHaveBeenCalled();
    expect(apiCalls).toEqual(["api/sessions"]);
    expect(opened).toEqual(["term:work"]); // b's codex row, not the stale claude one
  });
  it("switches to the push's tenant, then opens by that tenant's kind", async () => {
    await tap({ session: "work", tenant: "b" });
    expect(tenantState.select).toHaveBeenCalledWith("b");
    expect(opened).toEqual(["term:work"]);
    expect(tenantState.tenant).toBe("b");
    expect(applyList).toHaveBeenCalledWith([{ name: "work", kind: "codex" }]);
  });
  it("discards an answer that a newer pushed list overtook, and asks again", async () => {
    let answer!: (v: unknown) => void;
    apiAnswer = () => new Promise((r) => (answer = r));
    await tap({ session: "work", tenant: "b" });
    stamp = 1; // a stream frame arrived while the request was out
    apiAnswer = async () => ({ sessions: [{ name: "work", kind: "codex" }] });
    answer({ sessions: [{ name: "work", kind: "codex" }] });
    await flush();
    expect(opened).toEqual([]);
    expect(applyList).not.toHaveBeenCalled();
    await vi.advanceTimersByTimeAsync(1000);
    expect(opened).toEqual(["term:work"]);
    expect(applyList).toHaveBeenCalledOnce();
  });
  it("does not open a session that a newer pushed list shows as gone", async () => {
    let answer!: (v: unknown) => void;
    apiAnswer = () => new Promise((r) => (answer = r));
    await tap({ session: "work", tenant: "b" });
    stamp = 1;
    apiAnswer = async () => ({ sessions: [] }); // deleted meanwhile
    answer({ sessions: [{ name: "work", kind: "codex" }] }); // the stale answer
    await vi.advanceTimersByTimeAsync(100_000);
    expect(opened).toEqual([]);
  });
  it("gives up on a request that hangs and asks again within the time limit", async () => {
    apiAnswer = () => new Promise(() => {}); // never answers
    await tap({ session: "work", tenant: "b" });
    await vi.advanceTimersByTimeAsync(5000);
    expect(opened).toEqual([]);
    apiAnswer = async () => ({ sessions: [{ name: "work", kind: "codex" }] });
    await vi.advanceTimersByTimeAsync(7000);
    expect(opened).toEqual(["term:work"]);
  });
  it("sends no request while the API client is pinned to another tenant than the store shows (pop-out boot)", async () => {
    clientTenant = "b"; // pinned first; the store still holds the saved "a"
    openSessionByName("work", "a");
    await vi.advanceTimersByTimeAsync(3000);
    expect(apiCalls).toEqual([]);
    expect(opened).toEqual([]);
    clientTenant = "a";
    await vi.advanceTimersByTimeAsync(1000);
    expect(opened).toEqual(["term:work"]);
  });
  it("keeps looking while b has no such session yet (a workspace coming up), and opens it once it appears", async () => {
    apiAnswer = async () => ({ sessions: [] });
    await tap({ session: "work", tenant: "b" });
    expect(opened).toEqual([]);
    apiAnswer = async () => ({ sessions: [{ name: "work", kind: "codex" }] });
    await vi.advanceTimersByTimeAsync(1000);
    expect(opened).toEqual(["term:work"]);
  });
  it("opens nothing when the target tenant's list cannot be read, instead of the old tenant's row", async () => {
    apiAnswer = async () => ({ error: { code: "http_502" } });
    await tap({ session: "work", tenant: "b" });
    await vi.advanceTimersByTimeAsync(100_000);
    expect(opened).toEqual([]);
    expect(applyList).not.toHaveBeenCalled();
  });
  it("opens nothing when the switch is declined (unsaved edits)", async () => {
    declineSwitch = true;
    await tap({ session: "work", tenant: "b" });
    await vi.advanceTimersByTimeAsync(100_000);
    expect(opened).toEqual([]);
    expect(apiCalls).toEqual([]);
  });
  it("opens nothing for a tenant the person is not a member of", async () => {
    await tap({ session: "work", tenant: "zzz" });
    await vi.advanceTimersByTimeAsync(100_000);
    expect(tenantState.select).not.toHaveBeenCalled();
    expect(opened).toEqual([]);
  });
  it("waits for the memberships when the tap arrives before they have loaded", async () => {
    tenantState.tenants = [];
    await tap({ session: "work", tenant: "b" });
    expect(opened).toEqual([]);
    tenantState.tenants = [{ slug: "a" }, { slug: "b" }];
    await vi.advanceTimersByTimeAsync(600);
    expect(tenantState.select).toHaveBeenCalledWith("b");
    expect(opened).toEqual(["term:work"]);
  });
  it("opens from the store, as before, when the push names no tenant (an older server)", async () => {
    await tap({ session: "work" });
    expect(apiCalls).toEqual([]);
    expect(opened).toEqual(["chat:work"]);
  });
  it("acknowledges both hand-off types, so the worker does not open a second window", async () => {
    expect(await tap({ session: "work", tenant: "a" })).toEqual(["ok"]);
    expect(await tap({ session: "work" })).toEqual(["ok"]);
  });
});

describe("openSessionByName with a tenant", () => {
  it("waits while another tenant is active and opens once the named one is", async () => {
    tenantState.tenant = "b";
    openSessionByName("work", "a");
    await flush();
    expect(opened).toEqual([]);
    expect(apiCalls).toEqual([]);
    tenantState.tenant = "a";
    apiAnswer = async () => ({ sessions: [{ name: "work", kind: "claude" }] });
    await vi.advanceTimersByTimeAsync(1000);
    expect(opened).toEqual(["chat:work"]);
  });
  it("drops an answer that arrives after the tenant moved on", async () => {
    let answer!: (v: unknown) => void;
    apiAnswer = () => new Promise((r) => (answer = r));
    openSessionByName("work", "a");
    await flush();
    tenantState.tenant = "b";
    answer({ sessions: [{ name: "work", kind: "claude" }] });
    await flush();
    expect(opened).toEqual([]);
    expect(applyList).not.toHaveBeenCalled();
  });
});

describe("consumeSessionDeepLink", () => {
  it("hands ?tenant= to the tenant store and strips both params at once", async () => {
    history.replaceState(null, "", "/?session=work&tenant=b");
    consumeSessionDeepLink();
    expect(location.search).toBe(""); // a reload after switching away must not return to b
    expect(landing).toEqual(["b"]);
    expect(opened).toEqual([]); // the tab shows "a": a's row is not opened for b's link
    tenantState.tenant = "b";
    await vi.advanceTimersByTimeAsync(1000);
    expect(opened).toEqual(["term:work"]);
  });
  it("opens nothing for a tenant the tab never settles on", async () => {
    history.replaceState(null, "", "/?session=work&tenant=zzz");
    consumeSessionDeepLink();
    await vi.advanceTimersByTimeAsync(100_000);
    expect(opened).toEqual([]);
  });
  it("a link without a tenant behaves as it always did", async () => {
    history.replaceState(null, "", "/?session=work");
    consumeSessionDeepLink();
    expect(landing).toEqual([]);
    expect(opened).toEqual(["chat:work"]);
  });
});
