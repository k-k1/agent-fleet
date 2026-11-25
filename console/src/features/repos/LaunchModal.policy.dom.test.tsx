// The launch dialog under the credential policy (ADR 0138 D10a, D11b, D16): harness → route →
// model from the live, policy-filtered catalogue; the member's default route preselected only when
// it can be used; a forbidden route absent; an allowed one with no credential listed but not
// choosable; the one line when nothing can be launched; and the policy's refusals said in the
// dialog's own words.
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let ssmProfiles: unknown[] = [];
// When set, api/env/ui-prefs waits on it: the synced prefs "arrive" when a test resolves it.
let prefsGate: Promise<unknown> | null = null;
const apiMock = vi.fn(async (url: string) => {
  if (url.includes("api/env/ui-prefs") && prefsGate) return prefsGate;
  if (url.includes("api/ssm/profiles")) return ssmProfiles;
  if (url.includes("api/me/ui-state")) return {};
  return { branches: [] };
});

vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  getTenant: () => "",
  getUser: () => "",
  api: (...a: unknown[]) => apiMock(...(a as [string])),
  repoPromptTemplates: async () => ({ groups: [] }),
  errText: (e: { message?: string }) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));

const { LaunchModal } = await import("./LaunchModal.tsx");
const { resetAttachDraftDB } = await import("../../lib/attachDraft.ts");
const { getSettings, setSettings } = await import("../../lib/settings.ts");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
import type { LaunchAccess, LaunchOpts, LaunchResult } from "./LaunchModal.tsx";
import type { LiveKindAccess } from "../../agents/launchAccess.ts";

type Launch = (o: LaunchOpts) => Promise<LaunchResult>;

let root: Root | null = null;
let host: HTMLDivElement;
let onLaunch: Mock<Launch>;

const access = (kinds: LiveKindAccess[] | null, extra: Partial<LaunchAccess> = {}): LaunchAccess => ({
  kinds: kinds ? new Map(kinds.map((k) => [k.kind, k])) : null,
  noRoute: false,
  allOff: false,
  ...extra,
});

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

async function render(kinds: string[], a: LaunchAccess, extra: { settling?: boolean; claudeExpired?: boolean } = {}): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <LaunchModal repo="app" branch="main" kinds={kinds} access={a} settling={extra.settling} claudeExpired={extra.claudeExpired} onClose={() => {}} onLaunch={onLaunch} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await settle();
}

const routeSelect = () =>
  [...document.querySelectorAll<HTMLSelectElement>("select")].find((s) => [...s.options].some((o) => o.value === "anthropic-api" || o.value === "bedrock"));
const launchBtn = () => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes("Start in a worktree"))!;
const click = async (el: Element) => {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
  await settle();
};
const choose = async (sel: HTMLSelectElement, value: string) =>
  act(async () => {
    sel.value = value;
    sel.dispatchEvent(new Event("change", { bubbles: true }));
  });
const launchedWith = (): LaunchOpts => onLaunch.mock.calls[0][0] as LaunchOpts;
const setDefault = (kind: string, route: { route: string; connection?: string } | undefined) =>
  setSettings({ agentLaunchDefaults: { ...getSettings().agentLaunchDefaults, [kind]: { ...getSettings().agentLaunchDefaults[kind], route } } });

beforeEach(() => {
  localStorage.clear();
  // These suites drive the agent, model and advanced controls, which sit in the Details fold
  // (ADR 0142 decision 7); start with it open, as a device that left it open does. The fold
  // itself is tested in LaunchModal.dom.test.tsx ("what -> where -> details").
  localStorage.setItem("af.launch-open.details", "1");
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
  ssmProfiles = [];
  prefsGate = null;
  setDefault("claude", undefined);
  onLaunch = vi.fn<Launch>(async () => ({ ok: true }));
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("route step", () => {
  it("lists only the allowed routes, before the model", async () => {
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-api", "anthropic-subscription"] }]));
    const sel = routeSelect()!;
    expect([...sel.options].map((o) => o.value)).toEqual(["", "anthropic-api", "anthropic-subscription"]);
    // The route decides who pays, so it sits with the agent and model, in the run-with strip.
    expect(sel.closest('[data-testid="run-strip"]')?.querySelector(".ui-seg.big")).toBeTruthy();
    expect(sel.closest(".launch-sec")).toBeNull();
    // A route the policy forbids (bedrock here) is not named anywhere.
    expect(document.body.textContent).not.toContain("Bedrock");
  });

  it("lists an allowed route with no credential, but it cannot be chosen", async () => {
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-api", "anthropic-subscription"], usableRoutes: ["anthropic-subscription"] }]));
    const opt = [...routeSelect()!.options].find((o) => o.value === "anthropic-api")!;
    expect(opt.disabled).toBe(true);
    expect(opt.textContent).toContain("no credential yet");
    expect(document.body.textContent).toContain("Model credentials");
  });

  it("launches claude on an Anthropic key alone, once the route is chosen", async () => {
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-api", "anthropic-subscription"], usableRoutes: ["anthropic-api"] }]));
    const sel = routeSelect()!;
    // No subscription to run on: "Default" is not offered, and nothing is picked for the member.
    expect([...sel.options].find((o) => o.value === "")!.disabled).toBe(true);
    expect(sel.value).toBe("");
    expect(launchBtn().disabled).toBe(true);
    await choose(sel, "anthropic-api");
    expect(launchBtn().disabled).toBe(false);
    await click(launchBtn());
    expect(launchedWith().kind).toBe("claude");
    expect(launchedWith().route).toBe("anthropic-api");
  });

  it("preselects the member's default route", async () => {
    setDefault("claude", { route: "anthropic-api" });
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-api", "anthropic-subscription"], usableRoutes: ["anthropic-api", "anthropic-subscription"] }]));
    expect(routeSelect()!.value).toBe("anthropic-api");
    await click(launchBtn());
    expect(launchedWith().route).toBe("anthropic-api");
  });

  it("keeps the current route when the already selected agent is clicked again", async () => {
    setDefault("claude", { route: "anthropic-api" });
    const k: LiveKindAccess = { kind: "claude", routes: ["anthropic-api", "anthropic-subscription"], usableRoutes: ["anthropic-api", "anthropic-subscription"] };
    await render(["claude", "codex"], access([k, { kind: "codex" }]));
    expect(routeSelect()!.value).toBe("anthropic-api");
    const claudeBtn = [...document.querySelectorAll<HTMLButtonElement>("button.seg-btn")].find((b) => b.classList.contains("seg-btn--active"))!;
    await click(claudeBtn);
    expect(routeSelect()!.value).toBe("anthropic-api");
    // A route the member chose by hand survives the same click.
    await choose(routeSelect()!, "anthropic-subscription");
    await click(claudeBtn);
    expect(routeSelect()!.value).toBe("anthropic-subscription");
  });

  it("applies the default when synced prefs arrive while the dialog is open", async () => {
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-api", "anthropic-subscription"], usableRoutes: ["anthropic-api", "anthropic-subscription"] }]));
    expect(routeSelect()!.value).toBe("");
    await act(async () => void setDefault("claude", { route: "anthropic-api" }));
    await settle();
    expect(routeSelect()!.value).toBe("anthropic-api");
  });

  it("applies the default when the catalogue arrives after the prefs", async () => {
    await render(["claude"], access(null));
    expect(routeSelect()).toBeUndefined();
    await act(async () => void setDefault("claude", { route: "anthropic-api" }));
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-api", "anthropic-subscription"], usableRoutes: ["anthropic-api", "anthropic-subscription"] }]));
    expect(routeSelect()!.value).toBe("anthropic-api");
  });

  it("does not override a route the member chose before the prefs arrived", async () => {
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-api", "anthropic-subscription"], usableRoutes: ["anthropic-api", "anthropic-subscription"] }]));
    await choose(routeSelect()!, "anthropic-subscription");
    await act(async () => void setDefault("claude", { route: "anthropic-api" }));
    await settle();
    expect(routeSelect()!.value).toBe("anthropic-subscription");
  });

  it("preselects a Bedrock default with its profile and region once the profiles are read", async () => {
    ssmProfiles = [{ id: "p-marked", label: "Bedrock invoke", region: "eu-west-1", modelRoute: true }];
    setDefault("claude", { route: "bedrock", connection: "p-marked@us-west-2" });
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-subscription", "bedrock"], usableRoutes: ["bedrock"] }]));
    expect(routeSelect()!.value).toBe("bedrock");
    expect(document.querySelector<HTMLInputElement>('input[aria-label="Region"]')!.value).toBe("us-west-2");
    expect(document.body.textContent).not.toContain("Your default route cannot be used now");
  });

  it("says a Bedrock default whose profile is no longer marked cannot be used", async () => {
    ssmProfiles = [{ id: "p-other", label: "Other", region: "eu-west-1", modelRoute: true }];
    setDefault("claude", { route: "bedrock", connection: "p-marked@eu-west-1" });
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-subscription", "bedrock"], usableRoutes: ["bedrock"] }]));
    expect(routeSelect()!.value).toBe("");
    expect(document.body.textContent).toContain("Your default route cannot be used now");
  });

  it("never swaps a default that cannot be used for another route", async () => {
    setDefault("claude", { route: "anthropic-api" });
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-api", "anthropic-subscription"], usableRoutes: ["anthropic-subscription"] }]));
    const sel = routeSelect()!;
    expect(sel.value).toBe("");
    expect(document.body.textContent).toContain("Your default route cannot be used now");
    await click(launchBtn());
    expect(launchedWith().route).toBeUndefined();
  });

  it("launches nothing without a catalogue", async () => {
    await render(["claude"], access(null));
    expect(launchBtn().disabled).toBe(true);
  });
});

describe("the one line when nothing can be launched", () => {
  it("sends the member to an administrator when the policy allows nothing", async () => {
    await render([], access([], { noRoute: true }));
    expect(document.body.textContent).toContain("No model route is available to you. Ask your administrator.");
    expect(document.querySelector('[data-testid="cred-own-settings-line"]')).toBeNull();
  });

  it("offers the reset when the member's own switches turned everything off", async () => {
    await render([], access([], { allOff: true }));
    expect(document.body.textContent).toContain("You have switched off every agent.");
    expect(document.body.textContent).toContain("Reset my settings");
    expect(document.body.textContent).not.toContain("administrator");
  });

  it("says neither while the answer is still out, or when agents are simply not signed in", async () => {
    await render([], access([], { noRoute: true }), { settling: true });
    expect(document.body.textContent).toContain("Checking agent connections");
    await render([], access([]));
    expect(document.body.textContent).not.toContain("administrator");
    expect(document.body.textContent).not.toContain("switched off every agent");
  });
});

describe("policy refusals", () => {
  it("explains route_required in the dialog and asks for the catalogue again", async () => {
    const refresh = vi.fn();
    onLaunch = vi.fn<Launch>(async () => ({ ok: false, policy: "route_required" as const }));
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-subscription"], usableRoutes: ["anthropic-subscription"] }], { refresh }));
    await click(launchBtn());
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("Choose a model route for this agent");
    expect(refresh).toHaveBeenCalled();
  });
});

// ADR 0142 decision 7 (strip note): the route sits on the run-with strip, and what stops the launch
// is said there; Details stays folded for route reasons.
describe("the strip explains the route", () => {
  beforeEach(() => localStorage.removeItem("af.launch-open.details"));
  const detailsOpen = () =>
    [...document.querySelectorAll(".launch-sec-head")].find((h) => h.querySelector(".launch-sec-label")?.textContent === "Details")?.getAttribute("aria-expanded");
  const strip = () => document.querySelector('[data-testid="run-strip"]')!;
  const routeChip = () => strip().querySelector<HTMLElement>(".run-chip--route");

  it("stays quiet when nothing needs the member — negative control", async () => {
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-api", "anthropic-subscription"], usableRoutes: ["anthropic-api", "anthropic-subscription"] }]));
    expect(detailsOpen()).toBe("false");
    expect(strip().querySelector(".run-strip-why")).toBeNull();
    expect(routeChip()!.className).not.toContain("run-chip--warn");
    expect(routeChip()!.className).not.toContain("run-chip--accent");
  });

  it("says a route has to be chosen on the chip, and disables the launch", async () => {
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-api", "anthropic-subscription"], usableRoutes: ["anthropic-api"] }]));
    expect(detailsOpen()).toBe("false");
    expect(routeChip()!.className).toContain("run-chip--warn");
    expect(strip().querySelector(".run-strip-why")).toBeTruthy();
    expect((launchBtn() as HTMLButtonElement).disabled).toBe(true);
    expect(routeSelect()).toBeTruthy();
  });

  it("says the saved default route cannot be used", async () => {
    setDefault("claude", { route: "anthropic-api" });
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-api", "anthropic-subscription"], usableRoutes: ["anthropic-subscription"] }]));
    expect(detailsOpen()).toBe("false");
    expect(strip().textContent).toContain("Your default route cannot be used now");
  });

  it("accents a route other than the default, because the billed account changes", async () => {
    setDefault("claude", { route: "anthropic-api" });
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-api", "anthropic-subscription"], usableRoutes: ["anthropic-api", "anthropic-subscription"] }]));
    expect(routeChip()!.className).toContain("run-chip--accent");
    expect(routeChip()!.textContent).toContain("Anthropic API");
  });

  it("shows a policy refusal's reason under the strip, Details staying folded", async () => {
    onLaunch = vi.fn<Launch>(async () => ({ ok: false, policy: "route_required" as const }));
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-subscription"], usableRoutes: ["anthropic-subscription"] }]));
    expect(detailsOpen()).toBe("false");
    await click(launchBtn());
    expect(detailsOpen()).toBe("false");
    expect(strip().querySelector('[role="alert"]')?.textContent).toContain("Choose a model route for this agent");
  });

  it("does not warn about an expired Claude login when the launch runs on an API key", async () => {
    setDefault("claude", { route: "anthropic-api" });
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-api", "anthropic-subscription"], usableRoutes: ["anthropic-api", "anthropic-subscription"] }]), { claudeExpired: true });
    expect(document.body.textContent).not.toContain("Claude login has expired");
  });
});
