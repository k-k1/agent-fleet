// A held MCP call shows as a toast unless its session's mirror is on screen; a call no session
// could be named for is always a toast.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";

const state = vi.hoisted(() => ({
  session: "s1" as string | undefined,
  tenant: "a",
  gets: 0,
  fail: 0,
  held: true,
  posts: [] as unknown[],
  gate: Promise.resolve() as Promise<unknown>,
}));
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../core/api/client.ts")>()),
  getTenant: () => state.tenant,
  apiJSON: async (_path: string, _method: string, body: unknown) => {
    state.posts.push(body);
    await state.gate;
    state.held = false;
    return {};
  },
  api: vi.fn(async (path: string) => {
    if (path !== "api/mcp-elicitations") return {};
    state.gets++;
    if (state.fail > 0) {
      state.fail--;
      throw new Error("dropped");
    }
    return {
      elicitations: !state.held ? [] : [
        {
          id: "e1",
          serverId: "mcp-1",
          server: "gmail",
          tool: "send_mail",
          session: state.session,
          inputs: [{ key: "k", fields: [{ name: "f", required: true, options: [{ value: "A", label: "Allow" }] }] }],
          createdAt: "2026-11-27T00:00:00Z",
          deadline: "2099-01-01T00:00:00Z",
          expiresInMs: 600_000,
        },
      ],
    };
  }),
}));

const { McpElicitHost } = await import("./McpElicitHost.tsx");
const { useMcpElicitStore } = await import("./store.ts");
const { useAwsConfirmStore } = await import("../awsconfirm/store.ts");
const { useNotificationStore } = await import("../notifications/store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { MirrorElicitCards } = await import("./McpElicitCard.tsx");
const { setLocale, t } = await import("../../lib/i18n/index.ts");
const { useTenantStore } = await import("../../core/store/tenant.ts");

const cardChannel = "af.mcp-elicit.cards";
class TestBroadcastChannel {
  static endpoints = new Set<TestBroadcastChannel>();
  onmessage?: (event: MessageEvent) => void;
  constructor(private name: string) { TestBroadcastChannel.endpoints.add(this); }
  postMessage(data: unknown) {
    for (const other of TestBroadcastChannel.endpoints) {
      if (other !== this && other.name === this.name) other.onmessage?.(new MessageEvent("message", { data }));
    }
  }
  close() { TestBroadcastChannel.endpoints.delete(this); }
}

// Stands in for a mirror on screen, which registers itself as MirrorView does.
function Mirror({ session }: { session: string }) {
  const mirrorShown = useAwsConfirmStore((s) => s.mirrorShown);
  useEffect(() => mirrorShown(session), [session, mirrorShown]);
  return null;
}

let root: Root | null = null;
let host: HTMLDivElement;

beforeEach(() => {
  vi.stubGlobal("BroadcastChannel", TestBroadcastChannel);
  state.session = "s1";
  state.tenant = "a";
  state.gets = 0;
  state.fail = 0;
  state.held = true;
  state.posts = [];
  state.gate = Promise.resolve();
  useTenantStore.setState({ resolved: true, tenant: state.tenant });
  useMcpElicitStore.setState({ items: [], answering: {} });
  useAwsConfirmStore.setState({ requests: [], answering: {}, mirrors: {} });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  TestBroadcastChannel.endpoints.clear();
  vi.unstubAllGlobals();
  setLocale("ja");
  host.remove();
});

const toastShown = () => document.body.textContent!.includes("send_mail");

async function render(mirror: string | null, opts: { toasts?: boolean; host?: boolean } = {}) {
  await act(async () =>
    root!.render(
      <ToastProvider>
        {mirror ? <Mirror session={mirror} /> : null}
        {mirror ? <MirrorElicitCards session={mirror} /> : null}
        {opts.host === false ? null : <McpElicitHost toasts={opts.toasts ?? true} />}
      </ToastProvider>,
    ),
  );
  await act(async () => new Promise((r) => setTimeout(r, 0)));
}
const toastCount = () => document.querySelectorAll(".ui-toast").length;

async function switchTenant(tenant: string) {
  await act(async () => {
    state.tenant = tenant;
    useTenantStore.setState({ tenant });
  });
}

describe("where a held MCP call shows", () => {
  it("is a toast when its session's mirror is not on screen", async () => {
    await render(null);
    expect(toastCount()).toBe(1);
  });
  it("is not a toast when its session's mirror is on screen", async () => {
    await render("s1");
    expect(toastCount()).toBe(0);
    expect(toastShown()).toBe(true); // the mirror's card
  });
  // A session may be named like an object member; an empty list of mirrors has none of them.
  it("is a toast for a session named constructor with no mirror on screen", async () => {
    state.session = "constructor";
    await render(null);
    expect(toastCount()).toBe(1);
  });
  // A popped-out pane mounts a host without toasts: its mirror still gets the card.
  it("feeds a mirror without toasting when toasts are off", async () => {
    await render("s1", { toasts: false });
    expect(toastCount()).toBe(0);
    expect(toastShown()).toBe(true);
  });
  it("withdraws the toast only while another window shows that call's card", async () => {
    await render(null);
    const remote = new BroadcastChannel(cardChannel);
    const show = (sender: string, ids: string[]) => act(() => remote.postMessage({ sender, ids }));

    show("unrelated", ["another-call"]);
    expect(toastCount()).toBe(1);
    show("first", ["e1"]);
    expect(toastCount()).toBe(0);
    show("second", ["e1"]);
    show("first", []);
    expect(toastCount()).toBe(0);
    show("second", []);
    expect(toastCount()).toBe(1);
    expect(state.posts).toEqual([]);
    remote.close();
  });
  it("restores the toast after an unresponsive window's card expires", async () => {
    await render(null);
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout", "performance"] });
    try {
      const remote = new BroadcastChannel(cardChannel);
      act(() => remote.postMessage({ sender: "crashed", ids: ["e1"] }));
      expect(toastCount()).toBe(0);
      remote.close();
      act(() => vi.runOnlyPendingTimers());
      expect(toastCount()).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });
  it("announces the pop-out's card to a window that opened later and withdraws it on hide", async () => {
    await render("s1", { toasts: false });
    const remote = new BroadcastChannel(cardChannel);
    const received = vi.fn();
    remote.onmessage = ({ data }) => received(data);
    remote.postMessage("request");
    expect(received).toHaveBeenLastCalledWith(expect.objectContaining({ ids: ["e1"] }));

    window.dispatchEvent(new Event("pagehide"));
    expect(received).toHaveBeenLastCalledWith(expect.objectContaining({ ids: [] }));
    received.mockClear();
    remote.postMessage("request");
    expect(received).not.toHaveBeenCalled();

    window.dispatchEvent(new Event("pageshow"));
    expect(received).toHaveBeenCalledWith(expect.objectContaining({ ids: ["e1"] }));
    await render(null, { toasts: false });
    expect(received).toHaveBeenLastCalledWith(expect.objectContaining({ ids: [] }));
    remote.close();
  });
  it("translates the active toast's close button without replacing the button", async () => {
    setLocale("en");
    await render(null);
    const x = document.querySelector<HTMLButtonElement>(".ui-toast-x")!;
    expect(x.title).toBe("Decline");
    expect(x.getAttribute("aria-label")).toBe("Decline");
    x.focus();

    act(() => setLocale("ja"));
    expect(document.activeElement).toBe(x);
    expect(x.title).toBe(t("mcpelicit.decline"));
    expect(x.title).not.toBe("Decline");
    expect(x.getAttribute("aria-label")).toBe(x.title);
    expect(state.posts).toEqual([]);
  });
  it("declines the call when its toast is closed, and does not toast it again", async () => {
    await render(null);
    const x = document.querySelector<HTMLButtonElement>(".ui-toast-x")!;
    await act(async () => x.click());
    await act(async () => new Promise((r) => setTimeout(r, 0)));
    expect(state.posts).toEqual([{ id: "e1", action: "decline" }]);
    expect(toastCount()).toBe(0);
  });
  // A poll that still lists the call while its decline is on the way must not bring it back.
  it("does not toast a closed call again while it is being declined", async () => {
    let open!: () => void;
    state.gate = new Promise((r) => (open = () => r(undefined)));
    await render(null);
    await act(async () => document.querySelector<HTMLButtonElement>(".ui-toast-x")!.click());
    await act(async () => useMcpElicitStore.getState().refresh());
    expect(toastCount()).toBe(0);
    await act(async () => open());
    await act(async () => new Promise((r) => setTimeout(r, 0)));
    expect(toastCount()).toBe(0);
  });
  // An answer from the card is on its way: the toast's close must not send a decline after it.
  it("sends no decline when the toast is closed while an answer is on its way", async () => {
    let open!: () => void;
    state.gate = new Promise((r) => (open = () => r(undefined)));
    await render(null);
    const label = [...document.querySelectorAll("label")].find((l) => l.textContent === "Allow")!;
    await act(async () => label.querySelector("input")!.click());
    const send = [...document.querySelectorAll("button")].find((b) => b.textContent === t("mcpelicit.send"))!;
    await act(async () => send.click());
    const x = document.querySelector<HTMLButtonElement>(".ui-toast-x")!;
    expect(x.getAttribute("aria-label")).toBe(t("mcpelicit.decline"));
    await act(async () => x.click());
    await act(async () => open());
    await act(async () => new Promise((r) => setTimeout(r, 0)));
    expect(state.posts).toEqual([{ id: "e1", action: "accept", choices: { k: { f: "A" } } }]);
  });
  // While an answer is on its way the X does nothing, so a failed answer can be sent again with
  // the choices the member made.
  it("keeps the toast and its choices when closed while an answer is on its way", async () => {
    let fail!: () => void;
    state.gate = new Promise((_, rej) => (fail = () => rej(new Error("dropped"))));
    await render(null);
    const allow = () => [...document.querySelectorAll("label")].find((l) => l.textContent === "Allow")!.querySelector("input")!;
    const send = () => [...document.querySelectorAll("button")].find((b) => b.textContent === t("mcpelicit.send"))!;
    await act(async () => allow().click());
    await act(async () => send().click());
    await act(async () => document.querySelector<HTMLButtonElement>(".ui-toast-x")!.click());
    expect(toastCount()).toBe(1);
    await act(async () => fail());
    await act(async () => new Promise((r) => setTimeout(r, 0)));
    expect(allow().checked).toBe(true);
    state.gate = Promise.resolve();
    await act(async () => send().click());
    expect(state.posts).toEqual([
      { id: "e1", action: "accept", choices: { k: { f: "A" } } },
      { id: "e1", action: "accept", choices: { k: { f: "A" } } },
    ]);
  });
  it.each(["send", "decline", "close"] as const)("ignores a failed %s after switching tenants", async (action) => {
    let fail!: () => void;
    state.gate = new Promise((_, reject) => (fail = () => reject(new Error("dropped"))));
    await render(null);
    if (action === "send") await act(async () => document.querySelector<HTMLInputElement>(".mcp-elicit input")!.click());
    const button =
      action === "close"
        ? document.querySelector<HTMLButtonElement>(".ui-toast-x")!
        : [...document.querySelectorAll<HTMLButtonElement>(".mcp-elicit-actions button")].find((b) => b.textContent === t(`mcpelicit.${action}`))!;
    await act(async () => button.click());
    expect(state.posts).toHaveLength(1);

    state.held = false;
    await switchTenant("b");
    expect(toastCount()).toBe(0);
    const reads = state.gets;
    await act(async () => fail());
    await act(async () => new Promise((r) => setTimeout(r, 0)));

    expect(state.gets).toBe(reads);
    expect(toastCount()).toBe(0);
  });
  it("ignores a close after the tenant changes before the old toast is withdrawn", async () => {
    await render(null);
    const x = document.querySelector<HTMLButtonElement>(".ui-toast-x")!;
    const reads = state.gets;

    state.held = false;
    await act(async () => {
      state.tenant = "b";
      useTenantStore.setState({ tenant: state.tenant });
      x.click();
    });
    await act(async () => new Promise((r) => setTimeout(r, 0)));

    expect(state.posts).toEqual([]);
    expect(state.gets).toBe(reads + 1);
    expect(toastCount()).toBe(0);
  });
  // A switch of tenant drops the old list and reads the new one at once, not at the next poll.
  it("reads the list again as soon as the tenant changes", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      state.held = false;
      await render(null);
      expect(toastCount()).toBe(0);
      state.held = true;
      await switchTenant("b");
      await act(async () => new Promise((r) => setTimeout(r, 0)));
      expect(toastCount()).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });
  it("takes its toasts down when it unmounts", async () => {
    await render(null);
    expect(toastCount()).toBe(1);
    await render(null, { host: false });
    expect(toastCount()).toBe(0);
  });
  // A read that fails while nothing is held must not hide a call until its time runs out.
  it("asks again while nothing is held", async () => {
    vi.useFakeTimers({ toFake: ["setInterval", "clearInterval"] });
    try {
      state.fail = 1;
      await render(null);
      expect(toastCount()).toBe(0);
      await act(async () => vi.advanceTimersByTime(30_000));
      await act(async () => new Promise((r) => setTimeout(r, 0)));
      expect(toastCount()).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });
  it("is a toast when no session could be named for it", async () => {
    state.session = undefined;
    await render("s1");
    const remote = new BroadcastChannel(cardChannel);
    act(() => remote.postMessage({ sender: "remote", ids: ["e1"] }));
    expect(toastCount()).toBe(1);
    remote.close();
  });
  it("asks the Agent again when the notification arrives", async () => {
    await render("s1");
    const before = state.gets;
    await act(async () => {
      useNotificationStore.setState((s) => ({
        items: [{ ...(s.items[0] ?? {}), kind: "mcp-elicitation", seq: 99 } as (typeof s.items)[number], ...s.items],
      }));
    });
    await act(async () => new Promise((r) => setTimeout(r, 0)));
    expect(state.gets).toBeGreaterThan(before);
  });
});
