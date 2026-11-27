import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const model = { id: "fixture-model", label: "Fixture model", efforts: ["low", "high"], defaultEffort: "low" };
vi.mock("../../../core/api/client.ts", async (orig) => ({
  ...((await orig()) as object),
  api: async (path: string) => path.includes("/models") ? { models: [model] } : {},
}));

const { LaunchDefaults } = await import("./AgentCardParts.tsx");
const { getSettings, setSettings, cancelPrefsTimersForTest } = await import("../../../lib/settings.ts");

let host: HTMLDivElement;
let root: Root;
let saved: ReturnType<typeof getSettings>["agentLaunchDefaults"];

beforeEach(() => {
  saved = getSettings().agentLaunchDefaults;
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  setSettings({ agentLaunchDefaults: saved });
  cancelPrefsTimersForTest();
});

describe.each([
  { kind: "codex" as const, label: "Unspecified" },
  { kind: "opencode" as const, label: "Default (low)" },
])("$kind Settings effort", ({ kind, label }) => {
  it(`offers ${label} for the empty choice`, async () => {
    setSettings({ agentLaunchDefaults: { ...saved, [kind]: { ...saved[kind], model: model.id, effort: "high" } } });
    await act(async () => root.render(<LaunchDefaults kind={kind} />));
    const effortRow = [...host.querySelectorAll(".ps-row")].find((row) => row.querySelector(".ps-label")?.textContent === "Default effort");
    const buttons = [...effortRow!.querySelectorAll("button")];
    expect(buttons.map((button) => button.textContent)).toEqual([label, "low", "high"]);
    await act(async () => buttons[0].click());
    expect(getSettings().agentLaunchDefaults[kind].effort).toBe("");
  });
});
