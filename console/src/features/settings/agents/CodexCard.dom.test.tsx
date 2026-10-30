// A codex login revoked server side still reads as connected (the status is read from local
// files), so the connected card is the only place a re-authentication can be offered. It must
// sign out first and then sign in the way the card was connected: the device flow for ChatGPT,
// the key form for an API key.
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
// ReauthButton and DisconnectButton ask for confirmation through context; accept it.
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({
  useConfirm: () => () => Promise.resolve(true),
}));

const { CodexCard } = await import("./CodexCard.tsx");

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const calls: string[] = [];

const reauthButton = () => host?.querySelector<HTMLButtonElement>("button.conn-reauth") ?? null;

async function mount(st: Record<string, unknown>, reload = () => {}) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <CodexCard
        running
        st={st}
        reload={reload}
        codex={null}
        updateCodex={() => {}}
        agents={false}
        updateAgents={() => {}}
      />,
    );
  });
}

beforeEach(() => {
  calls.length = 0;
  raw.mockImplementation((path: string, init?: { method?: string }) => {
    calls.push(`${init?.method || "GET"} ${path}`);
    return Promise.resolve(new Response("{}"));
  });
  api.mockImplementation((path: string, init?: { method?: string }) => {
    calls.push(`${init?.method || "GET"} ${path}`);
    return Promise.resolve(
      path.endsWith("device/start")
        ? {
            url: "https://auth.openai.com/codex/device",
            user_code: "ABCD-EFGHI",
            flow_id: "f1",
          }
        : {},
    );
  });
  apiJSON.mockImplementation(() => Promise.resolve({}));
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("CodexCard re-authenticate", () => {
  it("is offered next to disconnect only while connected", async () => {
    await mount({ connected: true, method: "chatgpt", email: "c@example.com" });
    expect(reauthButton()).toBeTruthy();

    act(() => root?.unmount());
    host?.remove();
    await mount({ connected: false });
    expect(reauthButton()).toBeNull();
  });

  it("signs out, then starts the device flow for a ChatGPT login", async () => {
    const reload = vi.fn();
    await mount({ connected: true, method: "chatgpt", email: "c@example.com" }, reload);
    await act(async () => {
      reauthButton()!.click();
    });
    expect(calls).toEqual(["DELETE api/connections/codex", "POST api/connections/codex/device/start"]);
    expect(reload).toHaveBeenCalled();
    // st is still connected here (the refetch has not landed, or failed): the code must show anyway.
    expect(host?.textContent).toContain("ABCD-EFGHI");
  });

  // The device poll answers from the global login state, so a revoked login left behind by a
  // failed sign-out would close the new flow at once and read as success.
  it("stops with a message when the sign-out fails or errors", async () => {
    for (const fail of [
      () => Promise.resolve(new Response("{}", { status: 502 })),
      () => Promise.reject(new Error("network down")),
    ]) {
      calls.length = 0;
      raw.mockImplementationOnce((path: string, init?: { method?: string }) => {
        calls.push(`${init?.method || "GET"} ${path}`);
        return fail();
      });
      const reload = vi.fn();
      await mount({ connected: true, method: "chatgpt", email: "c@example.com" }, reload);
      await act(async () => {
        reauthButton()!.click();
      });
      expect(calls).toEqual(["DELETE api/connections/codex"]);
      expect(reload).not.toHaveBeenCalled();
      expect(toast).toHaveBeenCalledTimes(1);
      toast.mockClear();
      act(() => root?.unmount());
      host?.remove();
    }
  });

  it("runs one re-authentication at a time", async () => {
    let release: (r: Response) => void = () => {};
    raw.mockImplementationOnce((path: string, init?: { method?: string }) => {
      calls.push(`${init?.method || "GET"} ${path}`);
      return new Promise<Response>((res) => (release = res));
    });
    await mount({ connected: true, method: "chatgpt", email: "c@example.com" });
    await act(async () => {
      reauthButton()!.click();
    });
    expect(reauthButton()!.disabled).toBe(true);
    await act(async () => {
      reauthButton()!.click(); // a disabled button ignores this; the in-flight guard is the backstop
    });
    await act(async () => {
      release(new Response("{}"));
    });
    expect(calls).toEqual(["DELETE api/connections/codex", "POST api/connections/codex/device/start"]);
  });

  // The device flow would switch an API-key workspace to a ChatGPT subscription.
  it("signs out and opens the key form, not the device flow, for an API key", async () => {
    const reload = vi.fn();
    await mount({ connected: true, method: "apikey" }, reload);
    await act(async () => {
      reauthButton()!.click();
    });
    expect(calls).toEqual(["DELETE api/connections/codex"]);
    expect(reload).toHaveBeenCalled();
    // The key form shows without waiting for the refetch that clears "connected".
    expect(host?.querySelector('input[type="password"]')).toBeTruthy();
  });
});
