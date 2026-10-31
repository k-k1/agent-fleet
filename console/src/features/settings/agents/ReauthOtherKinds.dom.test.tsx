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
let startFails = false;

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
  startFails = false;
  raw.mockImplementation((path: string, init?: { method?: string }) => {
    calls.push(`${init?.method || "GET"} ${path}`);
    return Promise.resolve(new Response("{}"));
  });
  const reply = (path: string, init?: { method?: string }) => {
    const call = `${init?.method || "GET"} ${path}`;
    calls.push(call);
    return Promise.resolve(
      path.endsWith("/start")
        ? startFails
          ? { error: "boom" }
          : {
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
    expect(toast.mock.calls[0][0]).toContain("HTTP 502");
  });

  // The sign-out already happened: a start that fails must not leave a dead flow view, and the
  // button has to come back so the member can try again.
  it("shows no flow and re-enables the button when the start fails after the sign-out", async () => {
    startFails = true;
    const reload = vi.fn();
    await c.mount(connected, reload);
    await act(async () => {
      reauthButton()!.click();
    });
    expect(calls.filter((x) => x.includes("/start"))).toEqual([c.start]);
    expect(host?.querySelector(c.shows)).toBeNull();
    expect(toast).toHaveBeenCalledTimes(1);
    expect(reauthButton()!.disabled).toBe(false);
    // Once the refetch lands the ordinary disconnected card (with its connect action) shows.
    act(() => root?.unmount());
    host?.remove();
    await c.mount({ connected: false, supported: true });
    expect(reauthButton()).toBeNull();
    expect(host?.querySelector("button.p-opt, .flow button")).toBeTruthy();
  });

  it("runs one re-authentication at a time and blocks Disconnect meanwhile", async () => {
    let release: (r: Response) => void = () => {};
    raw.mockImplementationOnce((path: string, init?: { method?: string }) => {
      calls.push(`${init?.method || "GET"} ${path}`);
      return new Promise<Response>((res) => (release = res));
    });
    await c.mount(connected);
    await act(async () => {
      reauthButton()!.click();
    });
    expect(reauthButton()!.disabled).toBe(true);
    expect(host?.querySelector<HTMLButtonElement>("button.conn-disconnect")!.disabled).toBe(true);
    await act(async () => {
      reauthButton()!.click();
    });
    await act(async () => {
      release(new Response("{}"));
    });
    expect(calls.filter((x) => x.startsWith("DELETE"))).toEqual([`DELETE api/connections/${kind}`]);
    expect(calls.filter((x) => x.includes("/start"))).toEqual([c.start]);
  });

  // The sign-out already happened, so the flow view must offer a way back to the ordinary card.
  it("lets the flow be cancelled", async () => {
    await c.mount(connected);
    await act(async () => {
      reauthButton()!.click();
    });
    const cancel = [...(host?.querySelectorAll("button") ?? [])].find((b) => b.textContent === "Cancel");
    expect(cancel).toBeTruthy();
    await act(async () => {
      cancel!.click();
    });
    expect(host?.querySelector(c.shows)).toBeNull();
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
