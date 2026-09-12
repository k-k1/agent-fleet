// The reduced engine panel a granted tenant_admin sees (ADR 0072 open question 11, phase P5).
//
// The catalogue is one per deployment and the GPU box is shared, so a tenant that was granted
// `allow_engine_ingest` may ADD to the catalogue and nothing else. The panel is the same
// component either way — one screen, one set of fixes — so what has to be pinned is the list of
// things that DISAPPEAR, and that they reappear for the operator.
//
// 🔴 The positive control is not optional here. Every "not in the document" assertion below
// passes trivially against a panel that rendered nothing at all — a thrown error, a fixture the
// component could not read, a selector that never matched — so each one is run twice: once with
// `super_admin: false` (absent) and once with `super_admin: true` (present).
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

// The catalogue row as the CP sends it to each caller. The tenant_admin's is a strict SUBSET —
// the same trimming control-plane/engine_ingest_perm.go does — so this fixture is what the
// reduced panel actually has to work from, not a full row with a flag set.
const TENANT_MODEL = {
  id: "sdxl-base-1.0",
  kind: "checkpoint",
  enabled: true,
  description: "SDXL 1.0",
  base_model: "sdxl",
  license: "openrail++",
  license_name: "CreativeML Open RAIL++-M",
  commercial_use: "yes",
};

const SUPER_MODEL = {
  ...TENANT_MODEL,
  selected: true,
  source: "hf:stabilityai/stable-diffusion-xl-base-1.0",
  vram_need_mib: 9000,
  vram_need_source: "floor",
  license_accepted_by: "ops@example.com",
  license_accepted_at: "2026-09-10T00:00:00Z",
};

const tenantAnswer = {
  super_admin: false,
  engines: [
    {
      key: "image",
      api: "images",
      provider: "comfy",
      base_models: ["sdxl", "flux1"],
      file_flags: ["", "--diffusion-model", "--vae"],
      model_rows: [TENANT_MODEL],
    },
  ],
};

const superAnswer = {
  super_admin: true,
  engines: [
    {
      key: "image",
      api: "images",
      provider: "comfy",
      base_models: ["sdxl", "flux1"],
      file_flags: ["", "--diffusion-model", "--vae"],
      model_rows: [SUPER_MODEL],
      models: ["sdxl-base-1.0"],
      has_models: true,
      mode: "ondemand",
      enabled: true,
      managed: true,
      state: "stopped",
      desired: 0,
      classes: [{ id: "l4", label: "L4 24GB", vram_mib: 21000, types: ["g6.xlarge"] }],
      class: { id: "l4", label: "L4 24GB", vram_mib: 21000, types: ["g6.xlarge"] },
      class_default: "l4",
      class_is_default: true,
      window_secs: 300,
      idle_secs: 900,
    },
  ],
};

/** 🔴 Which SCREEN, as an argument. Since the panel was split, a granted tenant_admin reaches
 *  the models screen and only that — the other one buys and stops a GPU — so "what a tenant
 *  sees" is a question about this component, and the operator's half has to be asked of the
 *  other one. Defaulting to the models screen keeps every assertion below about the screen a
 *  tenant actually opens. */
async function mount(answer: unknown, View: () => ReactNode = EngineModelsAdminView) {
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

describe("the engine panel a granted tenant_admin sees", () => {
  // The operator-only surfaces, each named by what a reader would look for, and each with the
  // SCREEN it belongs to. Kept as one table so that adding a control means deciding, in one
  // place, whether a tenant may see it.
  //
  // 🔴 The first three are on the machine screen, which a tenant admin has no rail entry to at
  // all. They are still asserted against the MODELS screen below, because what would go wrong
  // is one of them appearing there — and their positive control has to be taken on the screen
  // they actually live on, or it would be proving the absence of something from a page that
  // never had it.
  const OPERATOR_ONLY: [string, () => boolean, () => ReactNode][] = [
    // 1. The mode. It buys and stops a GPU for the whole deployment.
    ["the mode buttons", () => !!btn("オンデマンド") || !!btn("常時稼働") || !!btn("無効"), EnginesAdminView],
    // 2. The GPU ladder — which card the next box is, and what it costs per hour.
    ["the class picker", () => text().includes("L4 24GB"), EnginesAdminView],
    // 3. What the box is doing right now.
    ["the box state", () => text().includes("停止中") || text().includes("稼働中"), EnginesAdminView],
    // 4. Enabling / disabling a model, and choosing what the engine starts with.
    ["the model controls", () => !!btn("有効にする") || !!btn("無効にする") || !!btn("これで起動する"), EngineModelsAdminView],
    // 5. Forgetting a row (and, behind it, deleting the bytes).
    ["forget", () => !!btn("登録を消す"), EngineModelsAdminView],
    // 6. The deployment's Hugging Face token.
    ["the token panel", () => text().includes("Hugging Face のトークン"), EngineModelsAdminView],
  ];

  /** Between two mounts in one test. The afterEach cannot do it: these tests mount several
   *  times, and a left-over root goes on answering queries against a detached node. */
  const unmount = async () => {
    await act(async () => root?.unmount());
    host?.remove();
    root = null;
    host = null;
  };

  it("shows none of the six operator-only surfaces", async () => {
    for (const [name, present] of OPERATOR_ONLY) {
      await mount(tenantAnswer);
      expect(present(), `${name} must not be on a tenant_admin's panel`).toBe(false);
      await unmount();
    }
  });

  it("shows all six to the operator — the positive control for the test above", async () => {
    for (const [name, present, View] of OPERATOR_ONLY) {
      await mount(superAnswer, View);
      expect(present(), `${name} is missing from the operator's panel, so its absence proves nothing`).toBe(true);
      await unmount();
    }
  });

  it("still lists the catalogue, read-only, so an id is not taken in twice", async () => {
    await mount(tenantAnswer);
    // What the spec keeps: the id, the name, the family, the licence and on/off.
    expect(text()).toContain("sdxl-base-1.0");
    expect(text()).toContain("SDXL 1.0");
    expect(text()).toContain("sdxl");
    expect(text()).toContain("CreativeML Open RAIL++-M");
    // On or off is a badge, not the label of a button that would change it — which is the
    // whole reason the read-only list still reads correctly.
    expect(host?.querySelector(".engines-model-tag.on")).toBeTruthy();
  });

  it("keeps the ingest form, which is the one thing the grant is for", async () => {
    await mount(tenantAnswer);
    // The form starts collapsed, like it does for the operator. Opening it is what proves the
    // reduced row carried enough to build it.
    const opener = btn("Hugging Face などから取り込む");
    expect(opener).toBeTruthy();
    await act(async () => {
      opener!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
    });
    expect(host?.querySelector(".engines-ingest")).toBeTruthy();
    // And the two vocabularies the form is built from survived the trim: without base_models
    // there is no family to declare, and without file_flags a split model cannot be described
    // at all. Read off the form's own selects rather than off the fixture.
    const options = Array.from(host?.querySelectorAll("option") || []).map((o) => o.getAttribute("value"));
    expect(options).toContain("flux1");
    expect(options).toContain("--diffusion-model");
  });

  // 🔴 Both of these were found by RENDERING the panel, not by the table above: a sentence is
  // neither a control nor a field, so every "must be absent" assertion passed while two
  // paragraphs went on explaining buttons that are not on the screen. They are pinned here
  // because that is the failure mode this whole branch has — text outliving its control.
  it("drops the sentences that explain the operator's controls", async () => {
    await mount(tenantAnswer);
    expect(text()).not.toContain("選び直しは次の起動から効きます"); // explains "start with this one"
    expect(text()).not.toContain("オンデマンド"); // explains the mode segment
  });

  it("keeps those sentences for the operator, whose controls they describe", async () => {
    await mount(superAnswer);
    expect(text()).toContain("選び直しは次の起動から効きます");
    // The sentence that explains the mode segment lives with the segment, on the other screen.
    await act(async () => root?.unmount());
    host?.remove();
    await mount(superAnswer, EnginesAdminView);
    expect(text()).toContain("オンデマンド");
  });

  it("says what this panel is and that the catalogue is shared", async () => {
    await mount(tenantAnswer);
    // 🔴 The cost the grant accepts, stated where the grant is used. A tenant that reads this
    // screen as its own catalogue has been misled by it.
    expect(text()).toContain("どのテナントからも見えます");
  });

  it("says nothing of the sort to the operator, whose panel is not reduced", async () => {
    await mount(superAnswer);
    expect(text()).not.toContain("どのテナントからも見えます");
  });

  it("treats a Control Plane that sends no flag as not-super, rather than guessing", async () => {
    // An older CP answers the old envelope. Drawing the operator's controls off a missing
    // field is the failure that ends in a row of buttons that 403.
    await mount({ engines: tenantAnswer.engines }, EnginesAdminView);
    expect(btn("オンデマンド")).toBeFalsy();
  });
});
