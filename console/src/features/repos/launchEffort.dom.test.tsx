import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import type { RepoRowProps } from "./RepoRow.tsx";
import type { RepoRailContext } from "./useRepoRail.ts";

const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...((await orig()) as object),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));
vi.mock("../sessions/open.ts", () => ({
  openSessionChat: vi.fn(), openSessionChatSplit: vi.fn(),
  openSessionTerminal: vi.fn(), openSessionTerminalSplit: vi.fn(),
}));

let row: RepoRowProps;
vi.mock("./RepoRow.tsx", () => ({
  RepoRow: (props: RepoRowProps) => {
    row = props;
    return null;
  },
}));

const { RepoRowConnected } = await import("./RepoRowConnected.tsx");
const { getSettings, setSettings, cancelPrefsTimersForTest } = await import("../../lib/settings.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

const repo = { name: "app", path: "/repos/app" };
const effortKey = "af.repo-effort.codex.app";
const ctx: RepoRailContext = {
  launchKinds: ["codex"],
  access: {
    kinds: new Map([["codex", { kind: "codex", fixedRoutes: ["chatgpt-subscription"], usableRoutes: ["chatgpt-subscription"] }]]),
    hiddenByOwn: false, noRoute: false, allOff: false, addable: new Set(), refresh: async () => {},
  },
  connsSettling: false, running: true, claudeExpired: false,
  activeRepo: null, scmRepo: null, rPanes: null, sPanes: null, multiPane: false, activeSession: null,
};

let root: Root;
let host: HTMLDivElement;
let launchDefaults: ReturnType<typeof getSettings>["agentLaunchDefaults"];
beforeEach(async () => {
  localStorage.clear();
  launchDefaults = getSettings().agentLaunchDefaults;
  apiJSON.mockReset().mockResolvedValue({ name: "s1" });
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => root.render(<ToastProvider><RepoRowConnected r={repo} ctx={ctx} /></ToastProvider>));
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  setSettings({ agentLaunchDefaults: launchDefaults });
  cancelPrefsTimersForTest();
});

function expectLaunch(driver: string, effort: string) {
  const body = apiJSON.mock.calls.filter(([path, method]) => path === "api/sessions" && method === "POST").at(-1)?.[2];
  expect(body).toMatchObject({ kind: "codex", dir: repo.path, driver });
  if (effort) expect(body.effort).toBe(effort);
  else expect(body).not.toHaveProperty("effort");
}

describe.each(["managed", "tui"])("Codex Start work with %s", (driver) => {
  it.each([
    { effort: "", choice: "" },
    { effort: "high", choice: "" },
    { effort: "low", choice: "low" },
  ])("sends effort '$effort' and remembers choice '$choice' on success", async ({ effort, choice }) => {
    localStorage.setItem(effortKey, "medium");
    await act(async () => {
      expect(await row.onStartWork({
        kind: "codex", driver, model: "", effort, effortChoice: choice, startMode: "normal",
        prompt: "", title: "", images: [], subdir: "", base: "", newBranch: "", worktree: false,
      })).toMatchObject({ ok: true });
    });
    expectLaunch(driver, effort);
    expect(localStorage.getItem(effortKey)).toBe(choice || "@af:default");
  });
});

describe("Codex repository quick launch", () => {
  it("applies the current setting and keeps following it on the next launch", async () => {
    localStorage.setItem(effortKey, "default");
    await act(async () => setSettings({ agentLaunchDefaults: { ...launchDefaults, codex: { ...launchDefaults.codex, effort: "high" } } }));
    await act(async () => void (await row.onLaunch("codex", false)));
    expectLaunch("managed", "high");
    expect(localStorage.getItem(effortKey)).toBe("@af:default");
    await act(async () => setSettings({ agentLaunchDefaults: { ...launchDefaults, codex: { ...launchDefaults.codex, effort: "" } } }));
    await act(async () => void (await row.onLaunch("codex", false)));
    expectLaunch("managed", "");
  });

  it("sends a remembered concrete effort when the setting is unspecified", async () => {
    localStorage.setItem(effortKey, "low");
    await act(async () => void (await row.onLaunch("codex", false)));
    expectLaunch("managed", "low");
    expect(localStorage.getItem(effortKey)).toBe("low");
  });
});
