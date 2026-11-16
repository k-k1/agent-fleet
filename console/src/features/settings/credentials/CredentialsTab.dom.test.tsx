// Settings > Model credentials groups the cards by destination (ADR 0138 D16) and draws a card
// only for an entry GET /connections sent: a kind the policy forbids, or the member switched off,
// has no entry and no card (D10a), and a group with no card has no heading either.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: vi.fn(() => Promise.resolve({})),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: { state: string; start: () => void }) => unknown) => sel({ state: "running", start: () => {} }),
  wsStartBusy: () => false,
}));

const { CredentialsTab, CREDENTIAL_GROUPS } = await import("./CredentialsTab.tsx");
const { resetConnectionsCache } = await import("../parts/useConnections.ts");
const { t } = await import("../../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function answer(conns: Record<string, unknown>) {
  api.mockImplementation((path: string) => Promise.resolve(path === "api/connections" ? conns : {}));
}
async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<CredentialsTab />);
  });
  await act(async () => {
    await new Promise((r) => setTimeout(r, 0));
  });
}
const cards = () => Array.from(host!.querySelectorAll<HTMLElement>(".p-card")).map((c) => c.dataset.settingsTarget);
const groups = () => Array.from(host!.querySelectorAll<HTMLElement>("[data-cred-group]")).map((g) => g.dataset.credGroup);

beforeEach(() => resetConnectionsCache());
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  resetConnectionsCache();
});

describe("Model credentials page", () => {
  it("groups every card by destination, in D16's order", async () => {
    const all = CREDENTIAL_GROUPS.flatMap((g) => g.kinds);
    answer(Object.fromEntries(all.map((k) => [k, { connected: false }])));
    await mount();
    expect(groups()).toEqual(["anthropic", "openai", "opencode", "google", "meta", "github", "selfhosted"]);
    expect(cards()).toEqual(all);
    const heads = Array.from(host!.querySelectorAll(".cred-group > .ds-title")).map((h) => h.textContent);
    expect(heads[0]).toBe(t("cred.group_anthropic"));
  });

  it("draws no card for a kind the Agent sent no entry for, and no empty group", async () => {
    // The policy forbids claude (and the member switched agy off): neither has an entry.
    answer({ codex: { connected: true }, opencode: {}, lcpp: { connected: false }, github: { connected: true } });
    await mount();
    expect(cards()).toEqual(["codex", "opencode", "lcpp"]);
    expect(groups()).toEqual(["openai", "opencode", "selfhosted"]);
    expect(host!.textContent).not.toContain(t("cred.group_anthropic"));
    expect(host!.textContent).not.toContain(t("cred.group_google"));
  });

  it("never draws a card for the GitHub connection itself", async () => {
    answer({ github: { connected: true }, copilot: { connected: true } });
    await mount();
    expect(cards()).toEqual(["copilot"]);
  });
});
