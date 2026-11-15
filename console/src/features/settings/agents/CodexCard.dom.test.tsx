// A codex login revoked server side still reads as connected (the status is read from local
// files), so the connected card is the only place a re-authentication can be offered. It signs
// in the way the card was connected — the device flow for ChatGPT, the key form for an API key —
// and never signs out first: the current login stays until the new sign-in completes.
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

  it("starts the device flow for a ChatGPT login without signing out", async () => {
    await mount({ connected: true, method: "chatgpt", email: "c@example.com" });
    await act(async () => {
      reauthButton()!.click();
    });
    expect(calls).toEqual(["POST api/connections/codex/device/start"]);
    // st is still connected: the code must show anyway.
    expect(host?.textContent).toContain("ABCD-EFGHI");
  });

  it("goes back to the connected card when the flow is abandoned", async () => {
    await mount({ connected: true, method: "chatgpt", email: "c@example.com" });
    await act(async () => {
      reauthButton()!.click();
    });
    const back = [...host!.querySelectorAll("button")].find((b) => b.textContent === "戻る" || b.textContent === "Back");
    expect(back).toBeTruthy();
    await act(async () => {
      back!.click();
    });
    expect(host?.textContent).not.toContain("ABCD-EFGHI");
    expect(reauthButton()).toBeTruthy();
    expect(calls).toEqual(["POST api/connections/codex/device/start"]);
  });

  it("runs one re-authentication at a time", async () => {
    let release: (r: unknown) => void = () => {};
    api.mockImplementationOnce((path: string, init?: { method?: string }) => {
      calls.push(`${init?.method || "GET"} ${path}`);
      return new Promise((res) => (release = res));
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
      release({ url: "https://auth.openai.com/codex/device", user_code: "ABCD-EFGHI", flow_id: "f1" });
    });
    expect(calls).toEqual(["POST api/connections/codex/device/start"]);
  });

  // The device flow would switch an API-key workspace to a ChatGPT subscription.
  it("opens the key form, not the device flow, for an API key, without signing out", async () => {
    await mount({ connected: true, method: "apikey" });
    await act(async () => {
      reauthButton()!.click();
    });
    expect(calls).toEqual([]);
    expect(host?.querySelector('input[type="password"]')).toBeTruthy();
  });
});
