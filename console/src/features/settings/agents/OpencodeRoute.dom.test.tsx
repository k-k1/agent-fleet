// The opencode card's two controls (docs/log/103). What the single 4-way "usage" control
// could not say, and what these pin:
//   - "off" governs the KIND, so nothing below it is offered while it is selected;
//   - the route governs opencode.ai ONLY, so on "own" the parts that only opencode.ai can use
//     (the account sign-in, its shared API key) are not offered either;
//   - the route the Agent ACTUALLY applied is shown when it differs from the chosen one —
//     Catalog's empty-menu rescue used to swap Go for the metered ids in silence.
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

const { OpencodeCard } = await import("./OpencodeCard.tsx");
const { setSettings, settingsDefaults, getSettings } = await import("../../../lib/settings.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

/** The launch catalog the Agent answers with, plus the route it says it shaped it by. Every
 *  test names its own: `route` lives in a module-level variable inside agentModels.ts, so a
 *  test that left it unset would read whichever answer the previous one happened to fetch. */
function catalog(models: string[], route: string) {
  api.mockImplementation((path: string) => {
    if (path.startsWith("api/agents/opencode/models")) {
      return Promise.resolve({ models: models.map((id) => ({ id, label: id })), route });
    }
    return Promise.resolve({});
  });
}

async function mount(st: Record<string, unknown> = {}) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<OpencodeCard running st={st} reload={() => {}} agents={{}} updateAgents={() => {}} />);
  });
  // The model fetch and the state update it queues.
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

const routes = () => [...host!.querySelectorAll<HTMLElement>('[role="radio"][data-route]')];
const routeValues = () => routes().map((b) => b.dataset.route);
const presetValues = () => [...(host!.querySelector("select")?.options ?? [])].map((o) => o.value);
const warning = () => host!.querySelector(".ps-note-warn");

async function click(el: Element | undefined) {
  await act(async () => {
    el!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
}

beforeEach(() => {
  setSettings(settingsDefaults());
  catalog(["opencode/grok-code"], "zen");
  apiJSON.mockImplementation(() => Promise.resolve({}));
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  setSettings(settingsDefaults());
});

describe("opencode の「使う」と「opencode.ai への課金」", () => {
  it("既定はオフ。枠の選択肢もキーの登録欄も出さない", async () => {
    await mount();
    expect(getSettings().opencodeCatalog).toBe("off");
    expect(routes()).toHaveLength(0);
    // Off means everything below is ignored, so a form that still invites a key would be
    // telling the user the opposite.
    expect(host!.querySelector("select")).toBeNull();
  });

  it("オンにすると「使わない（自分の鍵だけ）」から始まる — 何も選ばないまま外に出ない", async () => {
    await mount();
    const seg = host!.querySelectorAll(".p-body .choice-seg button");
    await click(seg[1]); // off | on
    expect(getSettings().opencodeCatalog).toBe("own");
    expect(routeValues()).toEqual(["own", "free", "go", "zen"]);
  });

  it("own では opencode.ai のものを出さない（サインインも、共通キーのプリセットも）", async () => {
    setSettings({ opencodeCatalog: "own" });
    catalog(["anthropic/claude-opus-5"], "own");
    await mount({ envs: ["ANTHROPIC_API_KEY"] });

    expect(presetValues()).not.toContain("go"); // the OPENCODE_API_KEY preset
    expect(presetValues()).toContain("anthropic");
    // The account sign-in block is opencode.ai's: its connect button must not be offered.
    expect(host!.querySelector(".p-opts:not(.p-opts-col)")).toBeNull();
  });

  it("own で保存済みの OPENCODE_API_KEY は「注入しません」と添える（消さずに、使っていないと言う）", async () => {
    setSettings({ opencodeCatalog: "own" });
    catalog(["anthropic/claude-opus-5"], "own");
    await mount({ envs: ["ANTHROPIC_API_KEY", "OPENCODE_API_KEY"] });
    expect(host!.querySelector(".oc-key-idle")).toBeTruthy();
  });

  it("Go を選んでも Go のモデルが無ければ、Zen で出していると言う", async () => {
    setSettings({ opencodeCatalog: "go" });
    // What the Agent answers when the rescue fired: metered ids, and `route` saying so.
    catalog(["opencode/deepseek-v4-pro"], "zen");
    await mount({ envs: ["OPENCODE_API_KEY"] });

    const note = warning();
    expect(note).toBeTruthy();
    expect(note!.textContent).toMatch(/Go/);
    expect(note!.textContent).toMatch(/Zen/);
  });

  it("枠どおりに出せているときは何も言わない", async () => {
    setSettings({ opencodeCatalog: "go" });
    catalog(["opencode-go/glm-5.2"], "go");
    await mount({ envs: ["OPENCODE_API_KEY"] });
    expect(warning()).toBeNull();
  });
});
