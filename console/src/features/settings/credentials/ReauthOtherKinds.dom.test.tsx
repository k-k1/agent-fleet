// agy, cursor, kiro and muse sign in again under a staging home on the Agent, so Re-authenticate
// runs the kind's own start flow WITHOUT signing out: the current login stays until the new
// sign-in completes. A login revoked server side still reads as connected, so the connected
// card is where it lives.
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
    mount: mounter((st, reload) => <AgyCard running st={st} reload={reload} />),
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
    mount: mounter((st, reload) => <MuseCard running st={st} reload={reload} allowed={new Set(["muse-login", "meta-api"])} />),
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
  // A once-implementation a card never consumed (agy starts through apiJSON, the others through
  // api) must not leak into the next test; beforeEach installs the defaults again.
  api.mockReset();
  apiJSON.mockReset();
  raw.mockReset();
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

  it("runs the kind's own start flow without signing out", async () => {
    await c.mount(connected);
    await act(async () => {
      reauthButton()!.click();
    });
    expect(calls.filter((x) => !x.endsWith("/install"))).toEqual([c.start]);
    // st is still connected: the flow has to show anyway.
    expect(host?.querySelector("button.conn-reauth")).toBeNull();
    expect(host?.querySelector(c.shows)).toBeTruthy();
  });

  // Nothing was signed out, so a start that fails leaves the connected card as it was.
  it("keeps the connected card when the start fails", async () => {
    startFails = true;
    await c.mount(connected);
    await act(async () => {
      reauthButton()!.click();
    });
    expect(calls.filter((x) => x.includes("/start"))).toEqual([c.start]);
    expect(host?.querySelector(c.shows)).toBeNull();
    expect(toast).toHaveBeenCalledTimes(1);
    expect(reauthButton()!.disabled).toBe(false);
    expect(host?.textContent).toContain("u@example.com");
  });

  it("runs one re-authentication at a time and blocks Disconnect meanwhile", async () => {
    let release: (r: unknown) => void = () => {};
    const pending = (path: string, init?: { method?: string }) => {
      calls.push(`${init?.method || "GET"} ${path}`);
      return new Promise((res) => (release = res));
    };
    // After mounting: kiro and muse ask about their install on mount, which would consume it.
    await c.mount(connected);
    api.mockImplementationOnce(pending);
    apiJSON.mockImplementationOnce((path: string, method: string) => pending(path, { method }));
    await act(async () => {
      reauthButton()!.click();
    });
    expect(reauthButton()!.disabled).toBe(true);
    expect(host?.querySelector<HTMLButtonElement>("button.conn-disconnect")!.disabled).toBe(true);
    await act(async () => {
      reauthButton()!.click();
    });
    await act(async () => {
      release({ url: "https://login.example.test/auth?c=ZZZ-999", user_code: "ZZZ-999", flow_id: "f1" });
    });
    expect(calls.filter((x) => x.startsWith("DELETE"))).toEqual([]);
    expect(calls.filter((x) => x.includes("/start"))).toEqual([c.start]);
  });

  it("lets the flow be cancelled back to the connected card", async () => {
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
    expect(reauthButton()).toBeTruthy();
  });
});

// The device kinds poll the Agent, which installs the staged sign-in on the poll that sees it
// finish. After Cancel no poll may run, or a code approved later would replace the login the
// member chose to keep.
describe.each(["cursor", "kiro", "muse"])("%s device flow polling", (kind) => {
  const c = cards[kind];
  const connected = { connected: true, supported: true, email: "u@example.com" };
  const polls = () => calls.filter((x) => x.endsWith("/poll")).length;

  it("stops after Cancel", async () => {
    vi.useFakeTimers();
    try {
      await c.mount(connected);
      await act(async () => {
        reauthButton()!.click();
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3000);
      });
      expect(polls()).toBe(1);
      const cancel = [...host!.querySelectorAll("button")].find((b) => b.textContent === "Cancel");
      await act(async () => {
        cancel!.click();
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30000);
      });
      expect(polls()).toBe(1);
    } finally {
      vi.useRealTimers();
    }
  });

  it("shows a flow the Agent reports as failed, and stops", async () => {
    vi.useFakeTimers();
    try {
      apiJSON.mockImplementation((path: string, method: string) => {
        calls.push(`${method} ${path}`);
        return Promise.resolve(
          path.endsWith("/start")
            ? { url: "https://login.example.test/auth?c=ZZZ-999", user_code: "ZZZ-999", flow_id: "f1" }
            : { connected: false, failed: true, detail: "code expired" },
        );
      });
      await c.mount(connected);
      await act(async () => {
        reauthButton()!.click();
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30000);
      });
      expect(polls()).toBe(1);
      expect(host?.textContent).toContain("code expired");
      expect(host?.querySelector(".p-waiting .codicon")).toBeNull(); // no spinner on an ended flow
    } finally {
      vi.useRealTimers();
    }
  });

  // The Agent answers 500 when it took the flow but could not install it; the flow is gone, so
  // polling on would only spin to the deadline.
  it("stops on an error answer and says so", async () => {
    vi.useFakeTimers();
    try {
      apiJSON.mockImplementation((path: string, method: string) => {
        calls.push(`${method} ${path}`);
        return Promise.resolve(
          path.endsWith("/start")
            ? { url: "https://login.example.test/auth?c=ZZZ-999", user_code: "ZZZ-999", flow_id: "f1" }
            : { error: { code: "install_failed", message: "disk full" } },
        );
      });
      await c.mount(connected);
      await act(async () => {
        reauthButton()!.click();
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(30000);
      });
      expect(polls()).toBe(1);
      expect(host?.querySelector(".p-waiting .codicon")).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });
});

// muse bills a pasted API key per use; the device flow would silently move it onto the
// subscription. A key replaces a key only when one is submitted, so nothing is signed out.
describe("muse re-authenticate for a metered API key", () => {
  it("opens the key form, not the device flow, without signing out", async () => {
    await cards.muse.mount({ connected: true, supported: true, metered: true });
    await act(async () => {
      reauthButton()!.click();
    });
    expect(calls.filter((x) => !x.endsWith("/install"))).toEqual([]);
    expect(host?.querySelector('input[type="password"]')).toBeTruthy();
  });
});
