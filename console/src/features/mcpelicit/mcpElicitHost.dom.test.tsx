// A held MCP call shows as a toast unless its session's mirror is on screen; a call no session
// could be named for is always a toast.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";

const state = vi.hoisted(() => ({
  session: "s1" as string | undefined,
  gets: 0,
  fail: 0,
  held: true,
  posts: [] as unknown[],
  gate: Promise.resolve() as Promise<unknown>,
}));
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../core/api/client.ts")>()),
  getTenant: () => "",
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

// Stands in for a mirror on screen, which registers itself as MirrorView does.
function Mirror({ session }: { session: string }) {
  const mirrorShown = useAwsConfirmStore((s) => s.mirrorShown);
  useEffect(() => mirrorShown(session), [session, mirrorShown]);
  return null;
}

let root: Root | null = null;
let host: HTMLDivElement;

beforeEach(() => {
  state.session = "s1";
  state.gets = 0;
  state.fail = 0;
  state.held = true;
  state.posts = [];
  state.gate = Promise.resolve();
  useMcpElicitStore.setState({ items: [], answering: {} });
  useAwsConfirmStore.setState({ requests: [], answering: {}, mirrors: {} });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
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
    expect(toastCount()).toBe(1);
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
