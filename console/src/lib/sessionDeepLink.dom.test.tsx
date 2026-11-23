// The deep link and the push hand-off open a session of the tenant they name, never a same-named
// session of whichever tenant the tab happens to show (#4870).
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const tenantState = {
  tenant: "a",
  tenants: [{ slug: "a" }, { slug: "b" }] as { slug: string }[],
  select: vi.fn(async (slug: string) => {
    if (declineSwitch) return;
    tenantState.tenant = slug;
  }),
};
let declineSwitch = false;
vi.mock("../core/store/tenant.ts", () => ({ useTenantStore: { getState: () => tenantState } }));

// Tenant "a" has a chat-capable `work`; tenant "b" has a terminal-only `work` (a different kind).
let sessions: { name: string; kind: string }[] = [];
const applyList = vi.fn((list: { name: string; kind: string }[]) => void (sessions = list));
vi.mock("../features/sessions/store.ts", () => ({ useSessionsStore: { getState: () => ({ sessions, applyList }) } }));
vi.mock("../agents/registry.ts", () => ({ agentOf: (k: string) => ({ caps: { chat: k === "claude" } }) }));
let apiAnswer: () => Promise<unknown> = async () => ({ sessions: [{ name: "work", kind: "codex" }] });
const apiCalls: string[] = [];
vi.mock("../core/api/client.ts", () => ({
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
  for (let i = 0; i < 10; i++) await Promise.resolve();
};

beforeEach(() => {
  vi.useFakeTimers();
  opened.length = 0;
  declineSwitch = false;
  tenantState.tenant = "a";
  tenantState.tenants = [{ slug: "a" }, { slug: "b" }];
  tenantState.select.mockClear();
  applyList.mockClear();
  apiCalls.length = 0;
  apiAnswer = async () => ({ sessions: [{ name: "work", kind: "codex" }] });
  // A same-named session in the tab's current tenant "a" — the one that must NOT be opened.
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

const tap = async (data: Record<string, unknown>) => {
  const acks: unknown[] = [];
  onMessage({ data: { type: data.tenant ? "af-open-tenant-session" : "af-open-session", ...data }, ports: [{ postMessage: (m: unknown) => acks.push(m) }] });
  await flush();
  return acks;
};

describe("listenPushOpen", () => {
  it("opens the session when the tab already shows the push's tenant", async () => {
    const acks = await tap({ session: "work", tenant: "a" });
    expect(acks).toEqual(["ok"]);
    expect(tenantState.select).not.toHaveBeenCalled();
    expect(opened).toEqual(["chat:work"]);
  });
  it("switches to the push's tenant, then opens that tenant's session by its kind, not the old tenant's row", async () => {
    await tap({ session: "work", tenant: "b" });
    expect(tenantState.select).toHaveBeenCalledWith("b");
    expect(apiCalls).toEqual(["api/sessions"]);
    expect(opened).toEqual(["term:work"]); // b's `work` is a codex session; a's row (claude) is not used
    expect(tenantState.tenant).toBe("b");
  });
  it("opens nothing when the new tenant's list cannot be read, instead of the old tenant's row", async () => {
    apiAnswer = async () => ({ error: { code: "http_502" } });
    await tap({ session: "work", tenant: "b" });
    await vi.advanceTimersByTimeAsync(100_000);
    expect(opened).toEqual([]);
    tenantState.tenant = "a";
    apiAnswer = async () => {
      throw new Error("network");
    };
    await tap({ session: "work", tenant: "b" });
    await vi.advanceTimersByTimeAsync(100_000);
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
  it("opens the session where it is when the memberships never load for a single-tenant tab already on it", async () => {
    tenantState.tenants = [];
    await tap({ session: "work", tenant: "a" });
    expect(opened).toEqual(["chat:work"]);
  });
  it("ignores the hand-off a Console from before the tenant existed would have taken", async () => {
    const acks: unknown[] = [];
    onMessage({ data: { type: "af-open-tenant-session", session: "work", tenant: "a" }, ports: [{ postMessage: (m: unknown) => acks.push(m) }] });
    await flush();
    expect(acks).toEqual(["ok"]); // this Console understands it
  });
  it("opens nothing when the switch is declined (unsaved edits), rather than the other tenant's session", async () => {
    declineSwitch = true;
    await tap({ session: "work", tenant: "b" });
    await vi.advanceTimersByTimeAsync(100_000);
    expect(opened).toEqual([]);
  });
  it("opens nothing for a tenant the person is not a member of", async () => {
    await tap({ session: "work", tenant: "zzz" });
    await vi.advanceTimersByTimeAsync(100_000);
    expect(tenantState.select).not.toHaveBeenCalled();
    expect(opened).toEqual([]);
  });
  it("opens the session where it is when the push names no tenant (an older server)", async () => {
    await tap({ session: "work" });
    expect(tenantState.select).not.toHaveBeenCalled();
    expect(opened).toEqual(["chat:work"]);
  });
  it("still acknowledges, so the worker does not open a second window", async () => {
    expect(await tap({ session: "work", tenant: "zzz" })).toEqual(["ok"]);
  });
});

describe("openSessionByName with a tenant", () => {
  it("waits while another tenant is active and opens once the named one is", async () => {
    sessions = [{ name: "work", kind: "claude" }];
    tenantState.tenant = "b";
    openSessionByName("work", "a");
    expect(opened).toEqual([]);
    tenantState.tenant = "a";
    await vi.advanceTimersByTimeAsync(1000);
    expect(opened).toEqual(["chat:work"]);
  });
});

describe("consumeSessionDeepLink", () => {
  it("keeps ?tenant= for the tenant store while it settles, honours it, then drops it", async () => {
    history.replaceState(null, "", "/?session=work&tenant=b");
    consumeSessionDeepLink();
    expect(location.search).toBe("?tenant=b");
    expect(opened).toEqual([]); // tab shows "a": the same-named session there is not opened
    tenantState.tenant = "b";
    sessions = [{ name: "work", kind: "codex" }];
    await vi.advanceTimersByTimeAsync(1000);
    expect(opened).toEqual(["term:work"]);
    expect(location.search).toBe(""); // a reload must not pull the tab back to b
  });
  it("drops ?tenant= when the link never resolves, and opens nothing for a tenant it never settled on", async () => {
    history.replaceState(null, "", "/?session=work&tenant=zzz");
    consumeSessionDeepLink();
    await vi.advanceTimersByTimeAsync(100_000);
    expect(opened).toEqual([]);
    expect(location.search).toBe("");
  });
});
