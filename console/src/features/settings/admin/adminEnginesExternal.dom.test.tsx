// The engine panel for a row this deployment does not own: a ComfyUI on the LAN (ADR 0076).
//
// The contract the control plane sends for such a row is decision 5's, and it is a row with
// HOLES: `managed:false`, `lifecycle:"external"`, a `url` and a `warm` flag, and NO `state`,
// `desired`, `box`, `stop_eta`, `idle_secs`, `window_*`, `last_demand` or GPU ladder. The
// fixture below is that exact shape — not a managed row with fields blanked — because the
// failure this pins is a panel that fills a hole with a confident zero.
//
// 🔴 Every "is not on the screen" assertion here passes just as well against a panel that
// rendered nothing at all, so each one is paired with the same claim against a MANAGED row.
// Without that pair, deleting the component's body would leave this file green.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import type { ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { EnginesAdminView } from "./adminEngines.tsx";
import { EngineModelsAdminView } from "./adminEngineModels.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const MODEL = {
  id: "sdxl-base-1.0",
  kind: "checkpoint",
  enabled: true,
  selected: true,
  description: "SDXL 1.0",
  base_model: "sdxl",
};

const COMFY_URL = "http://192.168.1.20:8188";

/** Decision 5's contract, field for field. */
const externalAnswer = {
  super_admin: true,
  engines: [
    {
      key: "image",
      api: "images",
      provider: "comfy",
      managed: false,
      lifecycle: "external",
      url: COMFY_URL,
      warm: true,
      mode: "on",
      enabled: true,
      has_models: true,
      models: ["sdxl-base-1.0"],
      model_rows: [MODEL],
      base_models: ["sdxl", "flux1"],
      file_flags: ["", "--diffusion-model", "--vae"],
    },
  ],
};

/** The same engine as ECS owns it — the positive control. Everything the external row must NOT
 *  show is on this one, so an assertion that stops working is visible here first. */
const managedAnswer = {
  super_admin: true,
  engines: [
    {
      key: "image",
      api: "images",
      provider: "comfy",
      managed: true,
      mode: "ondemand",
      enabled: true,
      has_models: true,
      models: ["sdxl-base-1.0"],
      model_rows: [MODEL],
      base_models: ["sdxl", "flux1"],
      file_flags: ["", "--diffusion-model", "--vae"],
      state: "running",
      desired: 1,
      warm: true,
      box: { id: "i-0abc", since: "2026-09-11T00:00:00Z", instance_type: "g6.xlarge", status: "ACTIVE" },
      classes: [{ id: "l4", label: "L4 24GB", vram_mib: 21000, types: ["g6.xlarge"] }],
      class: { id: "l4", label: "L4 24GB", vram_mib: 21000, types: ["g6.xlarge"] },
      class_default: "l4",
      class_is_default: true,
      idle_secs: 900,
      window_secs: 300,
      window_units: 3,
      last_demand: "2026-09-11T00:05:00Z",
    },
  ],
};

/** Which SCREEN. Everything an external row changes is on the machine screen — the mode, the
 *  box, the ladder — so that is the default; the one assertion about the catalogue mounts the
 *  models screen, because that is where a catalogue now is. */
async function mount(answer: unknown, View: () => ReactNode = EnginesAdminView) {
  api.mockImplementation((p: string) =>
    String(p).endsWith("/ingest") ? Promise.resolve({ jobs: [] }) : Promise.resolve(answer),
  );
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<View />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const text = () => host?.textContent || "";
const btn = (label: string) =>
  Array.from(host?.querySelectorAll("button") || []).find((b) => b.textContent?.trim() === label);

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
});

describe("an externally managed engine row (ADR 0076)", () => {
  // Each surface named by what an operator would look for, so that adding one to this panel
  // means deciding, in one place, whether a machine nobody here owns can be described by it.
  const ECS_ONLY: [string, () => boolean][] = [
    // The mode that buys and stops a box. There is nothing to buy (the API answers 400).
    ["the on-demand mode button", () => !!btn("オンデマンド")],
    // The GPU ladder — which card the next box is.
    ["the class picker", () => text().includes("L4 24GB")],
    // Which box is answering, and since when.
    ["the box", () => text().includes("i-0abc") || text().includes("g6.xlarge")],
    // The idle window the controller stops it against.
    ["the idle policy", () => text().includes("自動停止します")],
    // What the control loop counted. There is no control loop.
    ["the demand window", () => text().includes("直近") || text().includes("最後の要求")],
    // 14 days of samples written by that same tick.
    ["the uptime heatmap", () => text().includes("稼働実績")],
  ];

  it("draws none of the ECS-only surfaces", async () => {
    await mount(externalAnswer);
    // The panel did render — otherwise every line below would pass on an empty document.
    expect(text()).toContain("comfy (image)");
    for (const [name, present] of ECS_ONLY) {
      expect(present(), `${name} describes a box this deployment does not own`).toBe(false);
    }
  });

  it("draws all of them for a managed row — the positive control", async () => {
    await mount(managedAnswer);
    for (const [name, present] of ECS_ONLY) {
      expect(present(), `${name} is missing from the managed panel, so its absence proves nothing`).toBe(true);
    }
  });

  // Kept out of the table above because it hangs off the MODE, not off `managed`: the warning is
  // drawn for an always-on engine, and an external engine is always on by default — which is
  // exactly why it would otherwise greet every operator of a LAN ComfyUI with an hourly bill.
  it("does not warn about an hourly GPU bill it is not running up", async () => {
    await mount(externalAnswer); // mode: "on"
    expect(text()).not.toContain("時間単価");
  });

  it("still warns a managed engine pinned on — the positive control", async () => {
    await mount({
      super_admin: true,
      engines: [{ ...managedAnswer.engines[0], mode: "on" }],
    });
    expect(text()).toContain("時間単価");
  });

  it("offers off and on, and nothing else", async () => {
    await mount(externalAnswer);
    expect(btn("無効")).toBeTruthy();
    expect(btn("常時稼働")).toBeTruthy();
    expect(btn("オンデマンド")).toBeFalsy();
  });

  it("shows the URL it points at", async () => {
    await mount(externalAnswer);
    expect(text()).toContain(COMFY_URL);
  });

  it("says this deployment does not own the lifecycle, without naming docker", async () => {
    await mount(externalAnswer);
    expect(text()).toContain("外部管理");
    // 🔴 The TTS panel's wording ("standalone docker, etc.") is wrong here: a LAN ComfyUI may be
    // a process on somebody's desktop. Borrowing it back would leave this green only if the
    // borrowed string is the one that lost its docker.
    expect(text()).not.toContain("常駐 docker");
  });

  it("does not explain on-demand when no row has it", async () => {
    // The failure mode this panel has: text outliving its control. The footer sentence describes
    // the three-way segment, and on a deployment whose only engine is a URL there is no such
    // segment on the screen.
    await mount(externalAnswer);
    expect(text()).not.toContain("要求が来たときだけインスタンスを買い");
    expect(text()).toContain("経路を閉じるだけ");
  });

  it("keeps that explanation where the segment is", async () => {
    await mount(managedAnswer);
    expect(text()).toContain("要求が来たときだけインスタンスを買い");
    expect(text()).not.toContain("経路を閉じるだけ");
  });

  it("still lists the catalogue, which is where the model names come from", async () => {
    await mount(externalAnswer, EngineModelsAdminView);
    // Decision 6: the catalogue is a hand-written declaration either way, so nothing about it
    // changes for an external engine. It is the half of the panel that must NOT disappear.
    expect(text()).toContain("sdxl-base-1.0");
    expect(btn("無効にする")).toBeTruthy();
  });

  it("reads a row with no `managed` field as managed, not as external", async () => {
    // A control plane too old to send the flag, and a granted tenant_admin's trimmed row, both
    // arrive without it. Treating absence as "external" would strip the ECS half off the
    // operator's own panel — which is why the predicate is `managed === false`.
    const e = { ...managedAnswer.engines[0] } as Record<string, unknown>;
    delete e.managed;
    await mount({ super_admin: true, engines: [e] });
    expect(btn("オンデマンド")).toBeTruthy();
  });
});
