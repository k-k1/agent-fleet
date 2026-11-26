// A held MCP call shows as a toast unless its session's mirror is on screen; a call no session
// could be named for is always a toast.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, useEffect } from "react";
import { createRoot, type Root } from "react-dom/client";

const state = vi.hoisted(() => ({ session: "s1" as string | undefined, gets: 0 }));
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<typeof import("../../core/api/client.ts")>()),
  getTenant: () => "",
  apiJSON: async () => ({}),
  api: vi.fn(async (path: string) => {
    if (path !== "api/mcp-elicitations") return {};
    state.gets++;
    return {
      elicitations: [
        {
          id: "e1",
          serverId: "mcp-1",
          server: "gmail",
          tool: "send_mail",
          session: state.session,
          inputs: [{ key: "k", fields: [{ name: "f", required: true, options: [{ value: "A", label: "Allow" }] }] }],
          createdAt: "2026-11-27T00:00:00Z",
          deadline: "2026-11-27T00:10:00Z",
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

async function render(mirror: string | null) {
  await act(async () =>
    root!.render(
      <ToastProvider>
        {mirror ? <Mirror session={mirror} /> : null}
        <McpElicitHost />
      </ToastProvider>,
    ),
  );
  await act(async () => new Promise((r) => setTimeout(r, 0)));
}

describe("where a held MCP call shows", () => {
  it("is a toast when its session's mirror is not on screen", async () => {
    await render(null);
    expect(toastShown()).toBe(true);
  });
  it("is not a toast when its session's mirror is on screen", async () => {
    await render("s1");
    expect(toastShown()).toBe(false);
  });
  it("is a toast when no session could be named for it", async () => {
    state.session = undefined;
    await render("s1");
    expect(toastShown()).toBe(true);
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
