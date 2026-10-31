// agy, cursor, kiro and muse have no CLI re-login: each kind's start route refuses with 409
// "already connected; disconnect first to re-authenticate" while a login exists. So the card's
// Re-authenticate has to sign out (DELETE) and then run that kind's own start flow, and a
// login revoked server side still reads as connected, so the connected card is where it lives.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
const raw = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  raw: (...args: unknown[]) => raw(...args),
}));
const toast = vi.fn();
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => toast }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({
  useConfirm: () => () => Promise.resolve(true),
}));
// window.open for agy's authorize URL
vi.stubGlobal("open", vi.fn());

const { AgyCard } = await import("./AgyCard.tsx");
const { CursorCard } = await import("./CursorCard.tsx");
const { KiroCard } = await import("./KiroCard.tsx");
const { MuseCard } = await import("./MuseCard.tsx");

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const calls: string[] = [];

const reauthButton = () => host?.querySelector<HTMLButtonElement>("button.conn-reauth") ?? null;

type Mounter = (st: Record<string, unknown>, reload?: () => void) => Promise<void>;
function mounter(render: (st: Record<string, unknown>, reload: () => void) => React.ReactElement): Mounter {
  return async (st, reload = () => {}) => {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(render(st, reload));
    });
  };
}
const cards: Record<string, { mount: Mounter; start: string; shows: string }> = {
  agy: {
    mount: mounter((st, reload) => <AgyCard running st={st} reload={reload} agents={false} updateAgents={() => {}} />),
    start: "POST api/connections/agy/start",
    shows: 'input[name="agy-oauth-code"]',
  },
  cursor: {
    mount: mounter((st, reload) => <CursorCard running st={st} reload={reload} />),
    start: "POST api/connections/cursor/start",
    shows: "a, .flow-link, code",
  },
  kiro: {
    mount: mounter((st, reload) => <KiroCard running st={st} reload={reload} />),
    start: "POST api/connections/kiro/start",
    shows: "a, .flow-link, code",
  },
  muse: {
    mount: mounter((st, reload) => <MuseCard running st={st} reload={reload} />),
    start: "POST api/connections/muse/start",
    shows: "a, .flow-link, code",
  },
};

beforeEach(() => {
  calls.length = 0;
  raw.mockImplementation((path: string, init?: { method?: string }) => {
    calls.push(`${init?.method || "GET"} ${path}`);
    return Promise.resolve(new Response("{}"));
  });
  const reply = (path: string, init?: { method?: string }) => {
    const call = `${init?.method || "GET"} ${path}`;
    calls.push(call);
    return Promise.resolve(
      path.endsWith("/start")
        ? {
            url: "https://login.example.test/auth?c=ZZZ-999",
            user_code: "ZZZ-999",
            flow_id: "f1",
          }
        : {},
    );
  };
  api.mockImplementation(reply);
  apiJSON.mockImplementation((path: string, method: string) => reply(path, { method }));
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe.each(Object.keys(cards))("%s card re-authenticate", (kind) => {
  const c = cards[kind];
  const connected = {
    connected: true,
    supported: true,
    email: "u@example.com",
  };

  it("is offered only while connected", async () => {
    await c.mount(connected);
    expect(reauthButton()).toBeTruthy();
    act(() => root?.unmount());
    host?.remove();
    await c.mount({ connected: false, supported: true });
    expect(reauthButton()).toBeNull();
  });

  it("signs out, then runs the kind's own start flow", async () => {
    const reload = vi.fn();
    await c.mount(connected, reload);
    await act(async () => {
      reauthButton()!.click();
    });
    const startCalls = calls.filter((x) => x.includes("/start"));
    expect(calls.filter((x) => !x.endsWith("/install"))[0]).toBe(`DELETE api/connections/${kind}`);
    expect(startCalls).toEqual([c.start]);
    expect(reload).toHaveBeenCalled();
    // st is still connected (the refetch has not landed): the flow has to show anyway.
    expect(host?.querySelector("button.conn-reauth")).toBeNull();
    expect(host?.querySelector(c.shows)).toBeTruthy();
  });

  it("stops with a message when the sign-out fails", async () => {
    raw.mockImplementationOnce(() => Promise.resolve(new Response("{}", { status: 502 })));
    const reload = vi.fn();
    await c.mount(connected, reload);
    await act(async () => {
      reauthButton()!.click();
    });
    expect(calls.filter((x) => x.includes("/start"))).toEqual([]);
    expect(reload).not.toHaveBeenCalled();
    expect(toast).toHaveBeenCalledTimes(1);
  });
});

// muse bills a pasted API key per use; the device flow would silently move it onto the
// subscription, and the key route refuses while a sign-in exists.
describe("muse re-authenticate for a metered API key", () => {
  it("signs out and opens the key form, not the device flow", async () => {
    await cards.muse.mount({ connected: true, supported: true, metered: true });
    await act(async () => {
      reauthButton()!.click();
    });
    expect(calls.filter((x) => !x.endsWith("/install"))).toEqual(["DELETE api/connections/muse"]);
    expect(host?.querySelector('input[type="password"]')).toBeTruthy();
  });
});
