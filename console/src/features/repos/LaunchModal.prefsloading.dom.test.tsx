// The launch dialog while the synced ui-prefs are still being read: no launch on an unchosen
// route until the saved default has had its chance. Its own file because "prefs loaded" is module
// state that any earlier hydrate in the same file would have flipped for good.
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
const { getSettings, setSettings, hydrateUIPrefs } = await import("../../lib/settings.ts");
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

async function render(kinds: string[], a: LaunchAccess, extra: { settling?: boolean } = {}): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <LaunchModal repo="app" branch="main" kinds={kinds} access={a} settling={extra.settling} onClose={() => {}} onLaunch={onLaunch} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await settle();
}

const routeSelect = () =>
  [...document.querySelectorAll<HTMLSelectElement>("select")].find((s) => [...s.options].some((o) => o.value === "anthropic-api" || o.value === "bedrock"));
const launchBtn = () => [...document.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.includes("Start in a worktree"))!;
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

describe("prefs loading", () => {
  it("does not launch on an unchosen route while the synced prefs are still loading", async () => {
    let release!: (v: unknown) => void;
    prefsGate = new Promise((r) => (release = r));
    const loading = hydrateUIPrefs();
    await render(["claude"], access([{ kind: "claude", routes: ["anthropic-api", "anthropic-subscription"], usableRoutes: ["anthropic-api", "anthropic-subscription"] }]));
    expect(routeSelect()!.value).toBe("");
    expect(launchBtn().disabled).toBe(true);
    expect(document.body.textContent).toContain("Loading your saved settings");
    await act(async () => {
      release({});
      await loading;
    });
    await settle();
    prefsGate = null;
    // Loaded: the member may launch again, with no route chosen when the kind allows it.
    expect(launchBtn().disabled).toBe(false);
    expect(document.body.textContent).not.toContain("Loading your saved settings");
  });
});
