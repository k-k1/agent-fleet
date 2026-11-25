// The launch button never clips the agent / model it names at narrow widths (#5185).
import "fake-indexeddb/auto";
import { IDBFactory } from "fake-indexeddb";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import type { Mock } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";

// What the server's synced ui-prefs say when the strip asks again (another device's edits).
let serverPrefs: unknown = {};
vi.mock("../../core/api/client.ts", () => ({
  apiJSON: async () => ({}),
  getTenant: () => "",
  getUser: () => "",
  api: async (url: string) =>
    url.includes("api/env/ui-prefs") ? Promise.resolve(serverPrefs) : url.includes("api/ssm/profiles") ? [] : url.includes("api/me/ui-state") ? {} : { branches: [] },
  repoPromptTemplates: async () => ({ groups: [] }),
  errText: (e: { message?: string }) => e?.message ?? "",
  errDetail: (e: { message?: string }) => e?.message ?? "",
  isTransientErr: () => false,
}));

const { LaunchModal, SpaceLaunchForm } = await import("./LaunchModal.tsx");
const { resetAttachDraftDB, useAttachDraft } = await import("../../lib/attachDraft.ts");
const { setSettings, cancelPrefsTimersForTest } = await import("../../lib/settings.ts");
const { resumePrefsTimersForTest } = await import("../../lib/settings/sync.ts");
const { ConfirmProvider } = await import("../../ui/ConfirmProvider.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
import { setLocale } from "../../lib/i18n/index.ts";
import type { LaunchAccess, LaunchOpts, LaunchResult } from "./LaunchModal.tsx";

type Launch = (o: LaunchOpts) => Promise<LaunchResult>;

let root: Root | null = null;
let host: HTMLDivElement;
let onLaunch: Mock<Launch>;

const REGISTERED = ["claude-opus-4-8", "claude-opus-4-7", "claude-sonnet-4-6", "claude-haiku-4-5", "claude-opus-4-1"];
const liveKinds = new Map<string, unknown>([
  ["claude", { kind: "claude", routes: ["anthropic-subscription"], usableRoutes: ["anthropic-subscription"] }],
  ["codex", { kind: "codex", routes: ["chatgpt-subscription"], usableRoutes: ["chatgpt-subscription"] }],
]);
const access: LaunchAccess = { kinds: liveKinds as LaunchAccess["kinds"], noRoute: false, allOff: false };

async function settle(): Promise<void> {
  for (let i = 0; i < 5; i++) await act(async () => void (await new Promise((r) => setTimeout(r, 0))));
}

function Hub({ kinds }: { kinds: string[] }) {
  const [prompt, setPrompt] = useState("");
  const attach = useAttachDraft(null);
  return (
    <SpaceLaunchForm
      kinds={kinds}
      access={access}
      shared={{ prompt, setPrompt, attach, loading: false, missing: 0 }}
      busy={false}
      setBusy={() => {}}
      onClose={() => {}}
      onLaunch={onLaunch}
    />
  );
}

async function renderDialog(kinds: string[]): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <LaunchModal repo="app" branch="main" initialWorktree kinds={kinds} access={access} onClose={() => {}} onLaunch={onLaunch} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await settle();
}
async function renderHub(kinds: string[]): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <Hub kinds={kinds} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await settle();
}


const goButton = () => document.querySelector<HTMLButtonElement>("button.launch-go")!;
import { readFileSync } from "node:fs";

// jsdom has no layout, so the viewport is simulated through matchMedia: a query "(max-width: Npx)"
// matches when the simulated width is at most N. The wrapping itself is a CSS rule, checked below.
function setWidth(px: number): void {
  window.matchMedia = ((query: string) => {
    const m = /max-width:\s*(\d+)px/.exec(query);
    return { matches: m ? px <= Number(m[1]) : false, media: query, addEventListener() {}, removeEventListener() {}, addListener() {}, removeListener() {}, dispatchEvent: () => false, onchange: null };
  }) as unknown as typeof window.matchMedia;
}
async function mountHome(): Promise<void> {
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ConfirmProvider>
          <LaunchModal home repo="" kinds={["claude"]} access={access} onClose={() => {}} onLaunch={onLaunch} />
        </ConfirmProvider>
      </ToastProvider>,
    );
  });
  await settle();
}

beforeEach(() => {
  localStorage.clear();
  globalThis.indexedDB = new IDBFactory();
  resetAttachDraftDB();
  setSettings({ claudeCustomModels: REGISTERED });
  cancelPrefsTimersForTest();
  resumePrefsTimersForTest();
  serverPrefs = {};
  onLaunch = vi.fn<Launch>(async () => ({ ok: true }));
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  setSettings({ claudeCustomModels: [] });
  setLocale("en");
});

describe.each([
  ["en", "Claude Code"],
  ["ja", "Claude Code"],
] as const)("launch button at narrow widths (%s)", (lang, agent) => {
  describe.each([360, 420])("%i px", (width) => {
    describe.each([
      ["the dialog", renderDialog],
      ["the hub's space form", renderHub],
      ["the home variant", async () => mountHome()],
    ] as const)("%s", (_n, mount) => {
      it("names the agent and the model in full, with the fixed words shortened", async () => {
        setLocale(lang);
        setWidth(width);
        await mount(["claude"]);
        const text = goButton().textContent!;
        expect(text).toContain(agent);
        expect(text).toMatch(/Sonnet/);
        expect(text).toBe(lang === "en" ? "Start · Claude Code · Sonnet" : "Claude Code · Sonnet を始める");
        // The short form drops the place words; the tooltip keeps the whole sentence.
        expect(text).not.toMatch(/worktree|スペース|space/);
        expect(goButton().title).toContain(agent);
        // The tooltip keeps the words the short label dropped.
        if (_n !== "the home variant") expect(goButton().title).toMatch(/worktree|スペース|space/);
      });
    });
  });

  it("keeps the full sentence on a wide viewport", async () => {
    setLocale(lang);
    setWidth(1200);
    await renderHub(["claude"]);
    expect(goButton().textContent).toMatch(lang === "en" ? /Start in a new space with/ : /新しいスペースで/);
  });
});

describe("launch button style", () => {
  it("wraps instead of clipping: no ellipsis, no nowrap", () => {
    const css = readFileSync("src/features/repos/repos.css", "utf8");
    const rule = /\n\.ui-btn\.launch-go \{([^}]*)\}/.exec(css)![1];
    expect(rule).not.toMatch(/text-overflow/);
    expect(rule).not.toMatch(/overflow:\s*hidden/);
    expect(rule).toMatch(/white-space:\s*normal/);
  });
});

// The body of the "@media (max-width: 520px)" block that holds the launch-foot rules, by brace
// matching, as { selector: declarations }; plus whatever of .launch-foot sits outside any media block.
function narrowFootRules(css: string): { inMedia: Record<string, string>; outside: string } {
  const head = "@media (max-width: 520px) {";
  let from = 0;
  const inMedia: Record<string, string> = {};
  let outside = css;
  for (;;) {
    const at = css.indexOf(head, from);
    if (at < 0) break;
    let depth = 1;
    let i = at + head.length;
    while (depth > 0 && i < css.length) depth += css[i] === "{" ? 1 : css[i] === "}" ? -1 : 0, i++;
    const body = css.slice(at + head.length, i - 1);
    if (body.includes("launch-foot")) {
      for (const m of body.matchAll(/([^{}]+)\{([^}]*)\}/g)) inMedia[m[1].trim().replace(/\s+/g, " ")] = m[2];
      outside = css.slice(0, at) + css.slice(i);
    }
    from = i;
  }
  return { inMedia, outside };
}

describe("launch dialog footer at narrow widths", () => {
  it("stacks in a column inside the narrow media block only, with a full-width primary", () => {
    const { inMedia, outside } = narrowFootRules(readFileSync("src/features/repos/repos.css", "utf8"));
    expect(Object.keys(inMedia).sort()).toEqual([
      "footer.launch-foot",
      "footer.launch-foot .launch-back",
      "footer.launch-foot .ui-btn.launch-go",
    ]);
    const foot = inMedia["footer.launch-foot"];
    expect(foot).toMatch(/flex-direction:\s*column;/);
    expect(foot).toMatch(/align-items:\s*stretch/);
    expect(inMedia["footer.launch-foot .launch-back"]).toMatch(/margin-right:\s*0/);
    expect(inMedia["footer.launch-foot .ui-btn.launch-go"]).toMatch(/max-width:\s*none/);
    // Nothing of it leaks to wide viewports, and no reordering that would split Tab from the screen.
    expect(outside).not.toMatch(/launch-foot/);
    expect(foot).not.toMatch(/reverse|order:/);
  });
  it.each([360, 420])("the dialog's footer carries launch-foot at %i px, primary last in DOM (= Tab = visual) order", async (w) => {
    setWidth(w);
    await renderDialog(["claude"]);
    const foot = document.querySelector<HTMLElement>("footer.ui-modal-foot")!;
    expect(foot.classList.contains("launch-foot")).toBe(true);
    const btns = [...foot.querySelectorAll("button")];
    expect(btns[btns.length - 1].classList.contains("launch-go")).toBe(true);
  });
});
