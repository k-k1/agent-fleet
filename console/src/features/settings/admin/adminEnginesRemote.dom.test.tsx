// The engine panel for a row borrowed from another Agent Fleet (ADR 0079 decision 10).
//
// A borrowed row arrives with the SAME HOLES as ADR 0076's external row — `managed:false`, a
// `url`, a `warm` flag, and no `state`, `desired`, `box`, `stop_eta`, `idle_secs`, `window_*` —
// and differs from it by one declared field, `lifecycle:"remote"`. What hangs off that field is
// everything this file pins: there is a fleet on the other end, with an admin panel of its own,
// and it is the one that starts the engine and owns the catalogue.
//
// 🔴 The pairing rule of adminEnginesExternal.dom.test.tsx applies twice over here, and the
// SECOND control is the one that matters. Every "is not on the screen" assertion below is paired
// with the same claim against a row that must still show it, and for the catalogue that row is
// the EXTERNAL one — also `managed:false`. Pairing only against a managed row would leave this
// file green if the controls were hidden for every unmanaged row, which would take ADR 0076's
// LAN ComfyUI down with it.
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

/** A second row, neither enabled nor started. Without one, "start with this" and "enable" are
 *  absent from every panel — the control is per row — and two of the write doors below would
 *  prove nothing on either side of the pairing. */
const MODEL2 = {
  id: "flux1-dev",
  kind: "checkpoint",
  enabled: false,
  selected: false,
  description: "FLUX.1 dev",
  base_model: "flux1",
};

const FAR_FLEET = "https://af.example.com";
const LAN_COMFY = "http://192.168.1.20:8188";

/** Decision 10's contract, field for field: the far fleet's BASE url (no engine path — the
 *  gateway composes that over there), a mirrored `warm`, and nothing about a box. */
const remoteAnswer = {
  super_admin: true,
  engines: [
    {
      key: "image",
      api: "images",
      provider: "comfy",
      managed: false,
      lifecycle: "remote",
      url: FAR_FLEET,
      warm: true,
      mode: "on",
      enabled: true,
      has_models: true,
      models: ["sdxl-base-1.0"],
      model_rows: [MODEL, MODEL2],
      base_models: ["sdxl", "flux1"],
      file_flags: ["", "--diffusion-model", "--vae"],
      // Read off the mirror and writable only over there: the route that would save it answers
      // 400 `engine_not_ours` (decision 7).
      negative_always: "lowres, watermark",
      negative_max: 500,
    },
  ],
};

/** ADR 0076's row — the OTHER unmanaged lifecycle. The control that proves "hidden because it is
 *  borrowed", not "hidden because nothing here owns it". */
const externalAnswer = {
  super_admin: true,
  engines: [{ ...remoteAnswer.engines[0], lifecycle: "external", url: LAN_COMFY }],
};

/** The same engine as ECS owns it. Everything a borrowed row must not show about a box is on
 *  this one. */
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
      negative_always: "lowres, watermark",
      negative_max: 500,
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
/** The captions beside the facts in the state block ("Models", "Endpoint" / "Borrowed from"). */
const factLabels = () =>
  Array.from(host?.querySelectorAll(".engines-fact-label") || []).map((e) => e.textContent?.trim());

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
});

describe("an engine borrowed from another fleet (ADR 0079)", () => {
  it("says it is borrowed, and not merely that it is external", async () => {
    await mount(remoteAnswer);
    expect(text()).toContain("借用");
    // 🔴 The whole point of decision 10: "externally managed" is TRUE of this row and useless,
    // because there is a panel on the other end that the operator's next act belongs to. If both
    // labels were drawn, the operator would read the one that names nothing to do.
    expect(text()).not.toContain("外部管理");
  });

  it("still says 'externally managed' for a LAN row — the positive control", async () => {
    await mount(externalAnswer);
    expect(text()).toContain("外部管理");
    expect(text()).not.toContain("借用");
  });

  it("never claims a state this deployment cannot observe", async () => {
    await mount(remoteAnswer);
    // `state` does not arrive at all (decision 10), so neither word may appear — including the
    // "stopped" a switch on `state` would fall through to, which is the failure mode of reusing
    // the managed branch: a borrowed engine that is up would be reported dead.
    expect(text()).not.toContain("稼働中");
    expect(text()).not.toContain("停止中");
  });

  it("shows the fleet it is borrowed from, labelled as such", async () => {
    await mount(remoteAnswer);
    expect(text()).toContain(FAR_FLEET);
    // The label is not the external row's "Endpoint": this URL names a DEPLOYMENT, which is the
    // only answer this screen has to "whose GPU is this model running on".
    // 🔴 Read off the label ELEMENT, not off the page text: "借用元" also occurs inside the warm
    // sentence above, so a whole-page includes() stays green with the label left as "接続先".
    expect(factLabels()).toContain("借用元");
    expect(factLabels()).not.toContain("接続先");
  });

  it("labels a LAN row's URL as an endpoint — the positive control", async () => {
    await mount(externalAnswer);
    expect(factLabels()).toContain("接続先");
  });

  it("reports warm as the lending deployment's observation, not its own", async () => {
    await mount(remoteAnswer);
    // Decision 10: a borrowed `warm` is mirrored off the far catalogue. The bare "(loaded)" is
    // this deployment's own claim about a box it neither probes nor pays for.
    expect(text()).toContain("借用元が最後に見た時点で読み込み済");
    expect(text()).not.toContain("（読み込み済）");
  });

  it("makes that claim in its own words for a managed row — the positive control", async () => {
    await mount(managedAnswer);
    expect(text()).toContain("（読み込み済）");
  });

  const ECS_ONLY: [string, () => boolean][] = [
    // There is nothing here to buy or stop; the API answers 400 (decision 10).
    ["the on-demand mode button", () => !!btn("オンデマンド")],
    ["the class picker", () => text().includes("L4 24GB")],
    ["the box", () => text().includes("i-0abc") || text().includes("g6.xlarge")],
    ["the idle policy", () => text().includes("自動停止します")],
    ["the demand window", () => text().includes("直近") || text().includes("最後の要求")],
    ["the uptime heatmap", () => text().includes("稼働実績")],
    // 🔴 The catalogue's engine-wide negative prompt. It ARRIVES on a borrowed row (it is
    // mirrored), so this is the one ECS-shaped surface that would render happily and then 400 on
    // save — the shape decision 7 forbids.
    ["the negative-prompt editor", () => text().includes("全画像から除外する語")],
  ];

  it("draws none of the surfaces about a box it does not own", async () => {
    await mount(remoteAnswer);
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

  it("offers off and on, and nothing else", async () => {
    await mount(remoteAnswer);
    expect(btn("無効")).toBeTruthy();
    expect(btn("常時稼働")).toBeTruthy();
    // The far fleet's own control loop stops its box. `ondemand` is refused with 400 here for the
    // same reason it is for an external row.
    expect(btn("オンデマンド")).toBeFalsy();
  });

  it("explains that off closes the local route only", async () => {
    await mount(remoteAnswer);
    expect(text()).toContain("向こうの箱は止めません");
    // The external sentence is about a URL changed by restarting the Control Plane, which
    // describes a LAN box nobody wakes — not this.
    expect(text()).not.toContain("URL の変更は Control Plane の再起動です");
  });

  it("keeps the external sentence where an external row is — the positive control", async () => {
    await mount(externalAnswer);
    expect(text()).toContain("URL の変更は Control Plane の再起動です");
  });
});

describe("the catalogue of a borrowed engine (ADR 0079 decision 7)", () => {
  /** Every door into a catalogue write. The CP answers all five with 400 `engine_not_ours`, so
   *  none of them may be on the screen — 🔴 and each is checked against the EXTERNAL row below,
   *  where it must still be, because `managed:false` alone must not take them away. */
  const WRITE_DOORS: [string, () => boolean][] = [
    ["disable", () => !!btn("無効にする")],
    ["start with this", () => !!btn("これで起動する")],
    ["forget the row", () => !!btn("登録を消す")],
    ["register a file already in the bucket", () => text().includes("バケットのファイルを登録する")],
    ["the ingest form", () => text().includes("Hugging Face などから取り込む")],
  ];

  it("lists the borrowed rows and offers no way to change them", async () => {
    await mount(remoteAnswer, EngineModelsAdminView);
    // The mirror is the reason the list is worth showing at all: these are the model ids a
    // session may ask for.
    expect(text()).toContain("sdxl-base-1.0");
    for (const [name, present] of WRITE_DOORS) {
      expect(present(), `${name} is a 400 engine_not_ours waiting to happen`).toBe(false);
    }
    // ...and the sentence that replaces them says where the catalogue IS edited, by name.
    expect(text()).toContain("この画面からは変更できません");
    expect(text()).toContain(FAR_FLEET);
  });

  it("offers every one of them for a LAN row — the positive control", async () => {
    await mount(externalAnswer, EngineModelsAdminView);
    for (const [name, present] of WRITE_DOORS) {
      expect(present(), `${name} is missing for an external row too, so its absence proves nothing`).toBe(true);
    }
    expect(text()).not.toContain("この画面からは変更できません");
  });

  it("does not explain a button it is not offering", async () => {
    // The failure this panel has form for: text outliving its control (see the external file's
    // footer case). "Re-selecting takes effect at the next start" describes a button that is not
    // drawn for a borrowed row.
    await mount(remoteAnswer, EngineModelsAdminView);
    expect(text()).not.toContain("選び直しは次の起動から効きます");
  });
});
