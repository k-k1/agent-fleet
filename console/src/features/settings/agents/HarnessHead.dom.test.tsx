// The rows every harness card on Settings > Agents opens with (ADR 0138 D16): the member's on/off,
// the default route (D11b) and "Runs on". They read the live catalogue, so they offer only what the
// policy allows; a default route is saved with the connection the Agent holds a launch to.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: vi.fn(() => Promise.resolve({})),
  apiJSON: vi.fn(() => Promise.resolve({})),
}));

const { HarnessHead } = await import("./HarnessHead.tsx");
const { getSettings, setSettings, settingsDefaults } = await import("../../../lib/settings.ts");
const { t } = await import("../../../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(node));
}
const select = () => host!.querySelector<HTMLSelectElement>("select.ds-select");
const options = () => Array.from(select()?.options ?? []).map((o) => o.textContent);
const choose = async (label: string) => {
  const sel = select()!;
  const opt = Array.from(sel.options).find((o) => o.textContent === label)!;
  await act(async () => {
    sel.value = opt.value;
    sel.dispatchEvent(new Event("change", { bubbles: true }));
  });
};
const route = (r: string) => t(("cred.route." + r) as never);

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  setSettings(settingsDefaults());
});

describe("HarnessHead", () => {
  it("offers only the routes the catalogue allows, and saves the pick", async () => {
    await mount(
      <HarnessHead kind="claude" live={{ kind: "claude", routes: ["anthropic-api"] }} profiles={[]} onToggled={() => {}} />,
    );
    expect(options()).toEqual([t("agents.default_route_none"), route("anthropic-api")]);
    expect(options()).not.toContain(route("anthropic-subscription"));
    await choose(route("anthropic-api"));
    expect(getSettings().agentLaunchDefaults.claude.route).toEqual({ route: "anthropic-api" });
    // The row keeps what it does not set.
    expect(getSettings().agentLaunchDefaults.claude.model).toBe(settingsDefaults().agentLaunchDefaults.claude.model);
    await choose(t("agents.default_route_none"));
    expect(getSettings().agentLaunchDefaults.claude.route).toBeUndefined();
  });

  it("saves a Bedrock default with the marked profile it runs on", async () => {
    await mount(
      <HarnessHead
        kind="claude"
        live={{ kind: "claude", routes: ["bedrock"] }}
        profiles={[{ id: "work", label: "Work", region: "us-east-1" }]}
        onToggled={() => {}}
      />,
    );
    await choose(`${route("bedrock")} — Work`);
    expect(getSettings().agentLaunchDefaults.claude.route).toEqual({ route: "bedrock", connection: "work@us-east-1" });
  });

  it("saves a llama.cpp default with the endpoint's binding, and offers none without an endpoint", async () => {
    await mount(
      <HarnessHead kind="lcpp" live={{ kind: "lcpp", fixedRoutes: ["member-llamacpp"] }} profiles={null} ownSwitch onToggled={() => {}} />,
    );
    expect(select()).toBeNull();
    act(() => root?.unmount());
    host?.remove();
    await mount(
      <HarnessHead
        kind="lcpp"
        live={{ kind: "lcpp", fixedRoutes: ["member-llamacpp"] }}
        profiles={null}
        lcppBinding="member-llamacpp@mac123"
        ownSwitch
        onToggled={() => {}}
      />,
    );
    await choose(route("member-llamacpp"));
    expect(getSettings().agentLaunchDefaults.lcpp.route).toEqual({ route: "member-llamacpp", connection: "member-llamacpp@mac123" });
    // llama.cpp's own switch is on its card, not repeated here.
    expect(host!.textContent).not.toContain(t("agents.enabled"));
  });

  it("does not name a saved default the policy now hides, and says it cannot be used", async () => {
    setSettings({
      agentLaunchDefaults: {
        ...settingsDefaults().agentLaunchDefaults,
        claude: { ...settingsDefaults().agentLaunchDefaults.claude, route: { route: "anthropic-subscription" } },
      },
    });
    await mount(
      <HarnessHead kind="claude" live={{ kind: "claude", routes: ["anthropic-api"] }} profiles={[]} onToggled={() => {}} />,
    );
    expect(select()!.value).toBe("");
    expect(host!.textContent).toContain(t("agents.default_route_unusable"));
    expect(host!.textContent).not.toContain(route("anthropic-subscription"));
  });

  it("does not call a saved Bedrock default unusable while the profiles are still loading", async () => {
    setSettings({
      agentLaunchDefaults: {
        ...settingsDefaults().agentLaunchDefaults,
        claude: { ...settingsDefaults().agentLaunchDefaults.claude, route: { route: "bedrock", connection: "work@us-east-1" } },
      },
    });
    const live = { kind: "claude", routes: ["bedrock", "anthropic-api"] };
    await mount(<HarnessHead kind="claude" live={live} profiles={null} connectionsKnown={false} onToggled={() => {}} />);
    expect(host!.textContent).not.toContain(t("agents.default_route_unusable"));
    act(() => root?.unmount());
    host?.remove();
    // Loaded: the saved profile is chosen.
    await mount(
      <HarnessHead kind="claude" live={live} profiles={[{ id: "work", label: "Work", region: "us-east-1" }]} onToggled={() => {}} />,
    );
    expect(select()!.selectedOptions[0].textContent).toBe(`${route("bedrock")} — Work`);
    expect(host!.textContent).not.toContain(t("agents.default_route_unusable"));
    act(() => root?.unmount());
    host?.remove();
    // Loaded and the profile is gone: now it is unusable.
    await mount(<HarnessHead kind="claude" live={live} profiles={[]} onToggled={() => {}} />);
    expect(host!.textContent).toContain(t("agents.default_route_unusable"));
  });

  it("switches the harness off through agentEnabled and asks the page to refresh", async () => {
    const onToggled = vi.fn();
    await mount(<HarnessHead kind="codex" live={{ kind: "codex", fixedRoutes: ["openai-api"] }} profiles={[]} onToggled={onToggled} />);
    const off = Array.from(host!.querySelectorAll<HTMLButtonElement>(".seg-btn")).find((b) => b.textContent === t("common.off"))!;
    await act(async () => off.click());
    expect(getSettings().agentEnabled).toEqual({ codex: false });
    expect(onToggled).toHaveBeenCalledTimes(1);
    // Back on: the page asks again too, so its routes are not stale for a refresh period.
    const on = Array.from(host!.querySelectorAll<HTMLButtonElement>(".seg-btn")).find((b) => b.textContent === t("common.on"))!;
    await act(async () => on.click());
    expect(getSettings().agentEnabled).toEqual({});
    expect(onToggled).toHaveBeenCalledTimes(2);
  });

  it("says what the harness runs on now, by the catalogue's usable routes", async () => {
    await mount(
      <HarnessHead
        kind="opencode"
        live={{ kind: "opencode", fixedRoutes: ["anthropic-api", "opencode-zen"], usableRoutes: ["anthropic-api"] }}
        profiles={[]}
        onToggled={() => {}}
      />,
    );
    expect(host!.querySelector(".harness-runs-on")!.textContent).toContain(`${t("agents.runs_on")} ${route("anthropic-api")}`);
    expect(host!.querySelector(".harness-runs-on")!.textContent).not.toContain(route("opencode-zen"));
  });
});
