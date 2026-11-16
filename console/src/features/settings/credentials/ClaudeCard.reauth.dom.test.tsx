// Re-authenticating Claude reopens the OAuth flow over the current login, which stays in use
// until the new sign-in completes: no sign-out, and backing out returns to the connected card.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const raw = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: vi.fn(() => Promise.resolve({})),
  raw: (...args: unknown[]) => raw(...args),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => vi.fn() }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({
  useConfirm: () => () => Promise.resolve(true),
}));

const { ClaudeCard } = await import("./ClaudeCard.tsx");
const { t: tr } = await import("../../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const calls: string[] = [];

const reauthButton = () => host?.querySelector<HTMLButtonElement>("button.conn-reauth") ?? null;
const codeInput = () => host?.querySelector<HTMLInputElement>('input[name="claude-oauth-code"]') ?? null;

beforeEach(() => {
  calls.length = 0;
  vi.spyOn(window, "open").mockImplementation(() => null);
  raw.mockImplementation((path: string, init?: { method?: string }) => {
    calls.push(`${init?.method || "GET"} ${path}`);
    return Promise.resolve(new Response("{}"));
  });
  api.mockImplementation((path: string, init?: { method?: string }) => {
    calls.push(`${init?.method || "GET"} ${path}`);
    return Promise.resolve({ url: "https://claude.com/cai/oauth/authorize?x=1", flow_id: "f1" });
  });
});
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.restoreAllMocks();
});

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ClaudeCard running st={{ connected: true, email: "c@example.com" }} reload={() => {}} />,
    );
  });
}

describe("ClaudeCard re-authenticate", () => {
  it("opens the sign-in flow without signing out", async () => {
    await mount();
    await act(async () => {
      reauthButton()!.click();
    });
    expect(calls).toEqual(["POST api/connections/claude/start"]);
    expect(codeInput()).toBeTruthy();
  });

  it("goes back to the connected card when the flow is abandoned", async () => {
    await mount();
    await act(async () => {
      reauthButton()!.click();
    });
    const back = [...host!.querySelectorAll("button")].find((b) => b.textContent === tr("common.back"));
    expect(back).toBeTruthy();
    await act(async () => {
      back!.click();
    });
    expect(codeInput()).toBeNull();
    expect(host?.textContent).toContain("c@example.com");
    expect(calls).toEqual(["POST api/connections/claude/start"]);
  });
});
