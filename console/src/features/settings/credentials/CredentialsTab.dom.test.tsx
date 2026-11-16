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
vi.mock("../../../lib/settings.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../lib/settings.ts")>()),
  waitPrefsSaved: () => Promise.resolve(true),
}));
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: { state: string; start: () => void }) => unknown) => sel({ state: "running", start: () => {} }),
  wsStartBusy: () => false,
}));

const { CredentialsTab, CREDENTIAL_GROUPS, CARD_ROUTES } = await import("./CredentialsTab.tsx");
const { resetConnectionsCache } = await import("../parts/useConnections.ts");
const { CATALOG_REFRESH_MS } = await import("./memberCatalog.ts");
const { t } = await import("../../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const ALL_KINDS = ["claude", "codex", "cursor", "copilot", "kiro", "agy", "opencode", "lcpp", "muse"];
/** The live catalogue: the kinds the policy allows and the member has not switched off. */
const catalogOf = (kinds: (string | Record<string, unknown>)[], access: Record<string, boolean> = {}) => ({
  // A bare name is a kind with every route its card stores a credential for allowed.
  kinds: kinds.map((k) => (typeof k === "string" ? { kind: k, fixedRoutes: CARD_ROUTES[k] } : k)),
  access,
});
let kindsAnswer: unknown = catalogOf(ALL_KINDS);
function answer(conns: Record<string, unknown>, kinds: unknown = kindsAnswer) {
  kindsAnswer = kinds;
  api.mockImplementation((path: string) =>
    Promise.resolve(path === "api/connections" ? conns : path === "api/agents/kinds" ? kindsAnswer : {}),
  );
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

beforeEach(() => {
  resetConnectionsCache();
  kindsAnswer = catalogOf(ALL_KINDS);
});
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

  // The catalogue applies every member switch, llama.cpp's own one too, which GET /connections
  // still reports.
  it("draws no card for a kind the catalogue leaves out, even when /connections names it", async () => {
    answer({ claude: {}, lcpp: { connected: true } }, catalogOf(["claude"], { someOffByOwnSettings: true }));
    await mount();
    expect(cards()).toEqual(["claude"]);
  });

  it("brings a switched-off kind's card back after Reset my settings has been saved", async () => {
    const { getSettings, setSettings } = await import("../../../lib/settings.ts");
    setSettings({ lcppEnabled: false, agentEnabled: { codex: false } });
    answer({ claude: {}, codex: {}, lcpp: { connected: true } }, catalogOf(["claude"], { someOffByOwnSettings: true }));
    await mount();
    expect(cards()).toEqual(["claude"]);
    // The Agent reads the saved switches: its next answers include both kinds again.
    answer({ claude: {}, codex: {}, lcpp: { connected: true } }, catalogOf(["claude", "codex", "lcpp"]));
    const reset = Array.from(host!.querySelectorAll<HTMLButtonElement>("button")).find(
      (b) => b.textContent === t("cred.own_settings_reset"),
    )!;
    await act(async () => reset.click());
    await act(async () => {
      await new Promise((r) => setTimeout(r, 0));
    });
    expect(getSettings().lcppEnabled).toBe(true);
    expect(getSettings().agentEnabled).toEqual({});
    expect(cards()).toEqual(["claude", "codex", "lcpp"]);
    expect(host!.textContent).not.toContain(t("cred.own_settings_line"));
  });

  it("draws no card while the catalogue is unavailable", async () => {
    answer({ claude: {}, codex: {} }, { error: { code: "http_502" } });
    await mount();
    expect(cards()).toEqual([]);
  });

  // The shared /connections snapshot outlives the page: one taken before a policy tightened must
  // not be shown again when the fresh fetch cannot be read.
  it("does not show an earlier snapshot when this page's own fetch fails", async () => {
    vi.useFakeTimers();
    try {
      answer({ claude: {}, codex: {} });
      host = document.createElement("div");
      document.body.appendChild(host);
      root = createRoot(host);
      await act(async () => root!.render(<CredentialsTab />));
      await act(async () => { await vi.advanceTimersByTimeAsync(10); });
      expect(cards()).toEqual(["claude", "codex"]);
      act(() => root?.unmount());
      host?.remove();
      // Tightened since: the catalogue now leaves codex out, and /connections cannot be read.
      kindsAnswer = catalogOf(["claude"]);
      api.mockImplementation((path: string) =>
        Promise.resolve(path === "api/connections" ? { error: { code: "http_502" } } : path === "api/agents/kinds" ? kindsAnswer : {}),
      );
      host = document.createElement("div");
      document.body.appendChild(host);
      root = createRoot(host);
      await act(async () => root!.render(<CredentialsTab />));
      await act(async () => { await vi.advanceTimersByTimeAsync(10); });
      expect(cards()).toEqual([]); // the old snapshot is not drawn while the fetch is out
      for (let i = 0; i < 4; i++) await act(async () => { await vi.advanceTimersByTimeAsync(1300); });
      expect(cards()).toEqual([]);
      expect(host!.textContent).toContain("Retry");
    } finally {
      vi.useRealTimers();
    }
  });
});

// opencode's card offers only the choices, presets and sign-in on routes the policy allows (D10a).
describe("opencode card under a partial policy", () => {
  const presetLabels = () =>
    Array.from(host!.querySelectorAll<HTMLOptionElement>(".p-card select option")).map((o) => o.textContent);
  const usageChoices = () =>
    Array.from(host!.querySelectorAll<HTMLElement>("[data-route]")).map((b) => b.dataset.route);

  it("offers only the Anthropic key when only anthropic-api is allowed", async () => {
    const { setSettings } = await import("../../../lib/settings.ts");
    setSettings({ opencodeCatalog: "own" });
    answer(
      { opencode: { envs: ["ANTHROPIC_API_KEY", "OPENAI_API_KEY"] } },
      catalogOf([{ kind: "opencode", fixedRoutes: ["anthropic-api"] }]),
    );
    await mount();
    expect(presetLabels()).toEqual(["Anthropic"]);
    expect(usageChoices()).toEqual(["own"]);
    // A stored key whose route is forbidden is not listed.
    expect(host!.textContent).toContain("ANTHROPIC_API_KEY");
    expect(host!.textContent).not.toContain("OPENAI_API_KEY");
  });

  it("offers no account sign-in or Zen when only the Go subscription is allowed", async () => {
    const { setSettings } = await import("../../../lib/settings.ts");
    setSettings({ opencodeCatalog: "go" });
    answer({ opencode: { envs: [] } }, catalogOf([{ kind: "opencode", fixedRoutes: ["opencode-go"] }]));
    await mount();
    expect(usageChoices()).toEqual(["go"]);
    expect(host!.textContent).not.toContain(t("agents.oc_account_connect"));
    expect(presetLabels()).toEqual([t("agents.oc_preset_opencode")]);
  });

  it("offers everything under no restriction", async () => {
    const { setSettings } = await import("../../../lib/settings.ts");
    setSettings({ opencodeCatalog: "go" });
    answer(
      { opencode: { envs: [] } },
      catalogOf([{ kind: "opencode", fixedRoutes: ["anthropic-api", "custom-api", "gemini-api", "openai-api", "openrouter-api", "opencode-account", "opencode-go", "opencode-zen", "sakana-api"] }]),
    );
    await mount();
    expect(usageChoices()).toEqual(["own", "free", "go", "zen"]);
    expect(presetLabels().length).toBe(7);
    expect(host!.textContent).toContain(t("agents.oc_account_connect"));
  });
});

// Every card is judged by the route each of its controls stores a credential for (D10a).
describe("credential cards under a mixed policy", () => {
  const optionTitles = () => Array.from(host!.querySelectorAll<HTMLElement>(".p-opt-t")).map((e) => e.textContent || "");

  it("draws no Claude card when only the Anthropic API key is allowed", async () => {
    answer({ claude: { connected: true, email: "c@example.com" }, opencode: {} }, catalogOf([
      { kind: "claude", routes: ["anthropic-api"] },
      { kind: "opencode", fixedRoutes: ["anthropic-api"] },
    ]));
    await mount();
    expect(cards()).toEqual(["opencode"]);
    expect(host!.textContent).not.toContain("c@example.com");
  });

  it("offers codex's API key but not its subscription when only openai-api is allowed", async () => {
    answer({ codex: { connected: false } }, catalogOf([{ kind: "codex", fixedRoutes: ["openai-api"] }]));
    await mount();
    expect(optionTitles()).toEqual([t("agents.codex_connect_key")]);
    expect(host!.textContent).toContain(t("cred.codex_desc_key_only"));
    expect(host!.textContent).not.toContain("ChatGPT");
  });

  it("does not show a stored ChatGPT sign-in whose route is forbidden", async () => {
    answer(
      { codex: { connected: true, method: "chatgpt", email: "sub@example.com", plan: "plus" } },
      catalogOf([{ kind: "codex", fixedRoutes: ["openai-api"] }]),
    );
    await mount();
    expect(host!.textContent).not.toContain("sub@example.com");
    expect(optionTitles()).toEqual([t("agents.codex_connect_key")]);
  });

  it("offers muse's API key only when the sign-in route is forbidden", async () => {
    answer({ muse: { supported: true, connected: false } }, catalogOf([{ kind: "muse", fixedRoutes: ["meta-api"] }]));
    await mount();
    expect(optionTitles()).toEqual([t("agents.muse_use_key")]);
  });

  it("unrestricted: offers both of codex's and muse's ways in", async () => {
    answer({ codex: { connected: false }, muse: { supported: true, connected: false } });
    await mount();
    const titles = optionTitles();
    expect(titles.some((x) => x.startsWith(t("agents.codex_connect_sub")))).toBe(true);
    expect(titles).toContain(t("agents.codex_connect_key"));
    expect(titles.some((x) => x.startsWith(t("agents.muse_connect")))).toBe(true);
    expect(titles).toContain(t("agents.muse_use_key"));
  });
});

describe("opencode under an account-only policy", () => {
  it("lets a fresh member switch opencode on and offers the account sign-in", async () => {
    const { getSettings, setSettings } = await import("../../../lib/settings.ts");
    setSettings({ opencodeCatalog: "off" });
    answer({ opencode: { envs: [] } }, catalogOf([{ kind: "opencode", fixedRoutes: ["opencode-account"] }]));
    await mount();
    const on = Array.from(host!.querySelectorAll<HTMLButtonElement>(".seg-btn")).find((b) => b.textContent === t("agents.oc_enabled_on"))!;
    await act(async () => on.click());
    expect(getSettings().opencodeCatalog).not.toBe("own");
    expect(getSettings().opencodeCatalog).not.toBe("off");
    expect(host!.textContent).toContain(t("agents.oc_account_connect"));
    // No key preset is offered: none of their routes is allowed.
    expect(host!.querySelector(".p-card select")).toBeNull();
  });
});

describe("the catalogue's freshness", () => {
  it("draws no card from an Agent that does not prove its catalogue filtered", async () => {
    answer({ claude: {}, codex: {} }, { kinds: ALL_KINDS.map((kind) => ({ kind, fixedRoutes: CARD_ROUTES[kind] })) });
    await mount();
    expect(cards()).toEqual([]);
  });

  it("drops a card on the next refresh after the policy tightens, with the page left open", async () => {
    vi.useFakeTimers();
    try {
      answer({ claude: {}, codex: {} });
      host = document.createElement("div");
      document.body.appendChild(host);
      root = createRoot(host);
      await act(async () => root!.render(<CredentialsTab />));
      await act(async () => { await vi.advanceTimersByTimeAsync(10); });
      expect(cards()).toEqual(["claude", "codex"]);
      kindsAnswer = catalogOf(["claude"]);
      await act(async () => { await vi.advanceTimersByTimeAsync(CATALOG_REFRESH_MS); });
      expect(cards()).toEqual(["claude"]);
    } finally {
      vi.useRealTimers();
    }
  });

  it("forgets the previous member's catalogue on a member switch, and asks again", async () => {
    const { useTenantStore } = await import("../../../core/store/tenant.ts");
    answer({ claude: {}, codex: {} });
    await mount();
    expect(cards()).toEqual(["claude", "codex"]);
    let release: (v: unknown) => void = () => {};
    api.mockImplementation((path: string) =>
      path === "api/agents/kinds" ? new Promise((r) => (release = r)) : Promise.resolve(path === "api/connections" ? { claude: {}, codex: {} } : {}),
    );
    await act(async () => useTenantStore.setState((s) => ({ identityRev: s.identityRev + 1 })));
    await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
    expect(cards()).toEqual([]);
    await act(async () => release(catalogOf(["codex"])));
    await act(async () => { await new Promise((r) => setTimeout(r, 0)); });
    expect(cards()).toEqual(["codex"]);
  });
});
