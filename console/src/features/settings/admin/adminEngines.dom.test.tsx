// The self-hosted engine panel (ADR 0071).
//
// What is pinned here is the same distinction the VOICEVOX panel was audited for: the MODE is
// the administrator's intent and the STATE is what ECS is doing about it, and for the minute
// after "off" they disagree. A panel that echoed ECS back would report the opposite of the
// button just pressed.
//
// Plus the thing that is specific to this screen: a GPU box costs real money by the hour, so
// "always on" has to be visibly different from the other two rather than just another segment.
// 🔴 The hourly figure is NOT written into the message any more (ADR 0074): the box is
// selectable, so a number in the catalogue would be wrong for every deployment that moved off
// its default rung. The price comes from the ladder the operator declared, or not at all.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
// Only the transport is stubbed. errDetail is the REAL one, because how this panel words a
// refusal is part of what is under test: a hand-written stub that echoed `message` back would
// have reported the English developer text as a pass.
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { EnginesAdminView, engineIdFromFile } from "./adminEngines.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const row = (over: Record<string, unknown> = {}) => ({
  key: "image",
  api: "images",
  provider: "sdcpp",
  models: ["sdxl-base-1.0"],
  mode: "ondemand",
  enabled: true,
  managed: true,
  state: "stopped",
  desired: 0,
  ...over,
});

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<EnginesAdminView />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const seg = (label: string) =>
  Array.from(host!.querySelectorAll(".seg-btn")).find((b) => b.textContent === label) as
    | HTMLButtonElement
    | undefined;

const click = async (el: HTMLElement | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => {
    el!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
};

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  vi.useRealTimers();
});

// 🔴 A PROPOSAL, not an answer: it fills an empty id field and the field stays editable. What
// it has to get exactly right is the single-file checkpoint, which has no ambiguity at all —
// and it must drop the quantisation tag, which names the FILE and not the model (the same model
// at q4 and q8 is one model with two files).
describe("engineIdFromFile", () => {
  it("proposes the stem, without the quantisation that names the file", () => {
    expect(engineIdFromFile("flux1-dev.safetensors")).toBe("flux1-dev");
    expect(engineIdFromFile("qwen2.5-coder-0.5b-instruct-q4_k_m.gguf")).toBe("qwen2.5-coder-0.5b-instruct");
    // Two quantisations of one model propose one id — the CP then refuses the second as a
    // duplicate (409 model_id_exists), which is the correct conversation to have.
    expect(engineIdFromFile("qwen2.5-coder-0.5b-instruct-q8_0.gguf")).toBe(
      engineIdFromFile("qwen2.5-coder-0.5b-instruct-q4_k_m.gguf"),
    );
    expect(engineIdFromFile("Model-BF16.safetensors")).toBe("model");
    expect(engineIdFromFile("vae/diffusion_pytorch_model.safetensors")).toBe("diffusion_pytorch_model");
  });
});

describe("EnginesAdminView", () => {
  it("switches an engine off through the per-engine route", async () => {
    api.mockResolvedValue({ engines: [row()] });
    apiJSON.mockResolvedValue(row({ mode: "off", enabled: false, state: "stopping" }));
    await mount();

    await click(seg("無効"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image", "PUT", { mode: "off" });
    // The answer, not a re-fetch, is what the row becomes — the same reason the TTS panel
    // does it: a poll landing in between would show the pre-click state.
    expect(host!.textContent).toContain("停止処理中");
  });

  it("shows the mode the admin chose, not what ECS is still doing", async () => {
    // The disagreement window: mode off, task still going away.
    api.mockResolvedValue({ engines: [row({ mode: "off", enabled: false, state: "stopping" })] });
    await mount();
    expect(seg("無効")?.className).toContain("active");
    expect(seg("常時稼働")?.className).not.toContain("active");
  });

  it("warns about the bill only while an engine is pinned on, and names no price of its own", async () => {
    api.mockResolvedValue({ engines: [row({ mode: "ondemand" })] });
    await mount();
    expect(host!.textContent).not.toContain("常時稼働は GPU");

    apiJSON.mockResolvedValue(row({ mode: "on", state: "running" }));
    await click(seg("常時稼働"));
    expect(host!.textContent).toContain("常時稼働は GPU");
    // 🔴 No hard-coded figure. The panel used to say "$1.26/時" here, which stopped being true
    // the moment the instance class became a choice — and a wrong price is worse than none.
    expect(host!.textContent).not.toContain("$1.26");
  });

  // --- the GPU class (ADR 0074) ---------------------------------------------------------

  const withClasses = (over: Record<string, unknown> = {}) =>
    row({
      classes: [
        { id: "l4", label: "L4 24GB", vram_mib: 21000, types: ["g6.xlarge"], usd_per_hour: 1.26 },
        { id: "l40s", label: "L40S 48GB", vram_mib: 44000, types: ["g6e.xlarge"] },
      ],
      class: { id: "l4", label: "L4 24GB", vram_mib: 21000, types: ["g6.xlarge"], usd_per_hour: 1.26 },
      class_default: "l4",
      class_is_default: true,
      ...over,
    });

  it("offers no class control at all where no ladder is declared", async () => {
    api.mockResolvedValue({ engines: [row()] });
    await mount();
    // Decision 3: a deployment that declares none must not see a control that does nothing.
    expect(host!.querySelector(".engines-class")).toBeNull();
  });

  it("shows the rungs with their VRAM, and a price only where one was declared", async () => {
    api.mockResolvedValue({ engines: [withClasses()] });
    await mount();
    const opts = Array.from(host!.querySelectorAll(".engines-class option")).map((o) => o.textContent);
    expect(opts[0]).toContain("L4 24GB");
    expect(opts[0]).toContain("$1.26/h");
    // 🔴 The rung the operator left without a price prints none, rather than $0.
    expect(opts[1]).not.toContain("$");
  });

  it("says permanently when the engine is not on the deployment's default rung", async () => {
    api.mockResolvedValue({
      engines: [withClasses({ class_is_default: false, class: { id: "l40s", label: "L40S 48GB", vram_mib: 44000, types: ["g6e.xlarge"] } })],
    });
    await mount();
    // The sentence that keeps "temporarily try a bigger box" from becoming a permanent bill.
    expect(host!.textContent).toContain("既定と違います");
    expect(host!.textContent).toContain("既定に戻す");
  });

  it("says the saved class has not reached the running box, and what replacing costs", async () => {
    api.mockResolvedValue({
      engines: [
        withClasses({
          class: { id: "l40s", label: "L40S 48GB", vram_mib: 44000, types: ["g6e.xlarge"] },
          class_is_default: false,
          state: "running",
          box: { id: "i-1", status: "ACTIVE", instance_type: "g6.xlarge" },
        }),
      ],
    });
    await mount();
    // The old box is named, because "changed" while the old card keeps answering is the
    // expensive lie this screen exists to avoid.
    expect(host!.textContent).toContain("g6.xlarge");
    expect(host!.textContent).toContain("いま入れ替える");
  });

  it("asks before enabling a model that does not fit the chosen card, and never guesses", async () => {
    api.mockResolvedValue({
      engines: [
        withClasses({
          model_rows: [
            { id: "flux-dev", enabled: false, vram_need_mib: 40000, vram_need_source: "declared" },
            { id: "mystery", enabled: false, vram_need_source: "unknown" },
          ],
        }),
      ],
    });
    await mount();
    // By its label, not by position: "start with this one" leads the row, and a positional
    // helper silently tested that button instead the moment the order changed.
    const enable = (id: string) =>
      Array.from(
        Array.from(host!.querySelectorAll(".engines-model"))
          .find((li) => li.textContent?.includes(id))
          ?.querySelectorAll("button") ?? [],
      ).find((b) => b.textContent === "有効にする") as HTMLButtonElement | undefined;

    await click(enable("flux-dev"));
    // Nothing was sent: the question comes first, with both numbers in it.
    expect(apiJSON).not.toHaveBeenCalled();
    expect(host!.textContent).toContain("40000");
    expect(host!.textContent).toContain("21000");

    apiJSON.mockResolvedValue(withClasses());
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "承知のうえで有効にする",
      ) as HTMLButtonElement,
    );
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/engines/image/models/flux-dev",
      "PUT",
      { enabled: true, confirm_vram: true },
    );

    // 🔴 A model nobody measured is NOT asked about: "unknown" is not "too big", and a panel
    // that asked about every unmeasured model would teach people to click through the one
    // that matters.
    apiJSON.mockClear();
    apiJSON.mockResolvedValue(withClasses());
    await click(enable("mystery"));
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/engines/image/models/mystery",
      "PUT",
      { enabled: true },
    );
  });

  // ⚠️ The comparison is a MAXIMUM, not a sum, and on comfy that is conservative rather than
  // exact: a checkpoint is chosen per request and loaded ones stay cached, so several can be
  // resident. Said in a sentence — turning the figure into a sum would warn on every start of a
  // deployment with four enabled models, which is the warning nobody reads.
  it("says a per-request engine may hold several models, and says it only there", async () => {
    const enabled = { model_rows: [{ id: "sdxl", enabled: true, vram_need_mib: 7000, vram_need_source: "declared" }], vram_need_mib: 7000, vram_need_source: "declared", vram_need_model: "sdxl", vram_fits: true };
    api.mockResolvedValue({ engines: [withClasses({ provider: "comfy", ...enabled })] });
    await mount();
    expect(host!.querySelector(".engines-class")!.textContent).toContain("複数が同時に載る");

    await act(async () => root!.unmount());
    api.mockResolvedValue({ engines: [withClasses({ provider: "sdcpp", ...enabled })] });
    await mount();
    // sd-server holds ONE checkpoint chosen at start, so the sentence would be false there.
    expect(host!.querySelector(".engines-class")!.textContent).not.toContain("複数が同時に載る");
  });

  // The licence facts of a row (ADR 0072 decision 10). All three are things the CP already
  // sends and the panel used to drop, and the last one is the reason this is not cosmetic: a
  // blank where every ingested row names a licence reads as "no restrictions".
  it("shows the licence, who accepted it, and says when nothing recorded one", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          model_rows: [
            {
              id: "flux-dev",
              enabled: true,
              license: "other",
              license_name: "flux-1-dev-non-commercial-license",
              commercial_use: "no",
              license_accepted_by: "ops@example.com",
              license_accepted_at: "2026-09-09T02:00:00Z",
            },
            { id: "seeded", enabled: true },
          ],
        }),
      ],
    });
    await mount();
    const li = (id: string) =>
      Array.from(host!.querySelectorAll(".engines-model")).find((e) =>
        e.textContent?.includes(id),
      ) as HTMLElement;
    // 🔴 In the head next to the state, not buried in the meta line: this is the one fact that
    // can make offering the model somebody's own breach.
    expect(li("flux-dev").querySelector(".engines-model-tag.warn")!.textContent).toBe("非商用");
    expect(li("flux-dev").textContent).toContain("flux-1-dev-non-commercial-license");
    expect(li("flux-dev").textContent).toContain("ops@example.com");
    // A seeded row cannot know a licence and the hand-registration form does not ask.
    expect(li("seeded").textContent).toContain("ライセンスの記録なし");
    expect(li("seeded").querySelector(".engines-model-tag.warn")).toBeNull();
  });

  it("says so rather than showing an empty screen when nothing is deployed", async () => {
    api.mockResolvedValue({ engines: [] });
    await mount();
    // No ENGINE control: there is no engine to switch off, on-demand or always-on. (The browse
    // below it has segments of its own — a read that needs no engine — so the count of every
    // .seg-btn on the page would no longer say anything about engines.)
    for (const mode of ["無効", "オンデマンド", "常時稼働"]) {
      expect(
        Array.from(host!.querySelectorAll(".seg-btn")).some((b) => b.textContent === mode),
      ).toBe(false);
    }
    expect(host!.textContent).toContain("動かしていません");
  });

  // The status block, and the rule it is written to: a field the CP has no answer for is
  // ABSENT, and the panel must not fill the hole. Each assertion below is a hole that would
  // otherwise be filled with something an operator would act on.
  it("shows when the box started and when it will stop by itself", async () => {
    const now = Date.now();
    api.mockResolvedValue({
      engines: [
        row({
          state: "running",
          desired: 1,
          warm: true,
          // The BOX's own clock, not the service's: `service_since` moves on a stack update
          // without a new box being bought, and it is the box that costs $1.26/hour.
          box: { id: "i-08a9", status: "ACTIVE", since: new Date(now - 3720_000).toISOString() },
          service_since: new Date(now - 99_000_000).toISOString(),
          stop_eta: new Date(now + 600_000).toISOString(),
          window_secs: 300,
          window_counted_secs: 300,
          window_units: 3,
          last_demand: new Date(now - 120_000).toISOString(),
        }),
      ],
    });
    await mount();
    const text = host!.textContent || "";
    expect(text).toContain("i-08a9");
    expect(text).toContain("1 時間 2 分 経過"); // the box's age, not the service's
    expect(text).toContain("あと 10 分");
    expect(text).toContain("直近 5 分の要求: 3 件");
    expect(text).toContain("（読み込み済）");
    // The count covers the whole window, so it must NOT be qualified — a warning that is
    // always on is a warning nobody reads.
    expect(text).not.toContain("この CP が数えているのは");
  });

  it("does not promise a stop for an engine that will not stop", async () => {
    // Pinned on. The CP omits stop_eta, and the panel must not substitute anything for it —
    // not even the idle policy, which does not apply while the engine is pinned.
    api.mockResolvedValue({
      engines: [
        row({
          mode: "on",
          state: "running",
          desired: 1,
          idle_secs: 900,
          window_secs: 300,
          window_counted_secs: 300,
        }),
      ],
    });
    await mount();
    expect(host!.textContent).not.toContain("自動停止");
  });

  it("states the idle window for a stopped on-demand engine, as a policy and not a time", async () => {
    // Nothing to stop, so there is no countdown — but the window itself is still worth knowing,
    // and it is a different claim ("30 minutes after the last request") from a clock time.
    api.mockResolvedValue({
      engines: [row({ mode: "ondemand", state: "stopped", desired: 0, idle_secs: 900 })],
    });
    await mount();
    const text = host!.textContent || "";
    expect(text).toContain("誰も使わなくなってから 15 分で自動停止します");
    expect(text).not.toContain("あと");
  });

  // 🔴 The one number on this panel that can be confidently wrong. The rolling count lives in
  // the control plane's memory, so a CP replaced two minutes ago answers "0 requests in the
  // last 5 minutes" while somebody is mid-conversation with the engine.
  it("says so when it has not been counting for a whole window", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          state: "running",
          desired: 1,
          window_secs: 300,
          window_counted_secs: 90,
          window_units: 0,
          last_demand: new Date(Date.now() - 60_000).toISOString(),
        }),
      ],
    });
    await mount();
    const text = host!.textContent || "";
    expect(text).toContain("直近 5 分の要求: 0 件");
    expect(text).toContain("この CP が数えているのは");
    // The last-request time is persisted, so it stays true across the restart the count did
    // not survive — which is what makes the 0 above readable rather than alarming.
    expect(text).toContain("最後の要求");
  });

  // The service events are the only place ECS writes down why a start failed, and an engine
  // stuck in `starting` is exactly when somebody needs them.
  it("shows why a start is stuck, only while it is stuck", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          state: "starting",
          desired: 1,
          events: ["(service af-image) was unable to place a task because no container instance met all of its requirements."],
        }),
      ],
    });
    await mount();
    expect(host!.textContent).toContain("no container instance met all of its requirements");
  });

  // <details> hides its children, it does not unmount them. Leaving the heatmap inside a closed
  // one fires a 14-day query per engine on load, for a section nobody opened.
  it("does not fetch the history until the section is opened", async () => {
    api.mockResolvedValue({ engines: [row(), row({ key: "llm" })] });
    await mount();
    const called = api.mock.calls.map((c) => String(c[0]));
    // The heatmap is a 14-day query per engine and nothing on screen shows it yet.
    expect(called.filter((p) => p.includes("/hourly"))).toEqual([]);
    // The ingest list IS fetched, once per engine: a download started before lunch has to be
    // visible on the panel that is opened after it, and the call is a cheap read that also
    // reconciles a finished job.
    // The token status rides along once for the whole deployment, not once per engine.
    expect(called.sort()).toEqual([
      "api/admin/engines",
      "api/admin/engines/hf-token",
      "api/admin/engines/image/ingest",
      "api/admin/engines/llm/ingest",
    ]);
  });

  it("lists every engine, each with its own control", async () => {
    api.mockResolvedValue({
      engines: [
        row({ key: "llm", api: "chat", provider: "llamacpp", models: ["qwen3-coder-30b-a3b"] }),
        row(),
      ],
    });
    await mount();
    // One panel per ENGINE. Counted by the panels that carry a mode control, because the
    // deployment-wide token panel is an .admin-panel too and is not one of these.
    expect(host!.querySelectorAll(".admin-panel:has(.seg-btn)").length).toBe(2);
    // Three segments each: off / on-demand / always-on.
    expect(host!.querySelectorAll(".seg-btn").length).toBe(6);
    expect(host!.textContent).toContain("qwen3-coder-30b-a3b");
    expect(host!.textContent).toContain("sdxl-base-1.0");
  });

  // --- the model catalogue (ADR 0072) ---------------------------------------

  // "may this deployment use it" and "this is the one the engine starts with" are different
  // questions, and the panel has to offer both: sd-server holds ONE checkpoint chosen by a
  // startup flag, so enabling a second one does not load it.
  it("selects a checkpoint through the model route, without restarting anything", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          has_models: true,
          model_rows: [
            { id: "sdxl-base-1.0", kind: "checkpoint", enabled: true, selected: true },
            { id: "sdxl-fine-tune", kind: "checkpoint", enabled: true },
          ],
        }),
      ],
    });
    apiJSON.mockResolvedValue(
      row({
        has_models: true,
        model_rows: [
          { id: "sdxl-base-1.0", kind: "checkpoint", enabled: true },
          { id: "sdxl-fine-tune", kind: "checkpoint", enabled: true, selected: true },
        ],
      }),
    );
    await mount();
    // Only the model that is NOT already the one started with offers the control.
    const select = Array.from(host!.querySelectorAll(".engines-model button")).filter(
      (b) => b.textContent === "これで起動する",
    );
    expect(select.length).toBe(1);
    await click(select[0] as HTMLElement);
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/engines/image/models/sdxl-fine-tune",
      "PUT",
      { selected: true },
    );
    // ⚠️ The panel must say that a running engine is not swapped. Without it an administrator
    // presses this mid-generation expecting an immediate change (ADR 0072 decision 4).
    expect(host!.textContent).toContain("次の起動から効きます");
  });

  // The llm role's equivalent is the model a request that named none gets, so the same button
  // sends a different field. Getting this wrong would set `selected` on an engine that has no
  // such concept and silently change nothing.
  it("sends default, not selected, for a chat engine", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          key: "llm",
          api: "chat",
          provider: "llamacpp",
          has_models: true,
          model_rows: [{ id: "qwen3", kind: "gguf", enabled: true }],
        }),
      ],
    });
    apiJSON.mockResolvedValue(row({ key: "llm", api: "chat", has_models: true, model_rows: [] }));
    await mount();
    const select = Array.from(host!.querySelectorAll(".engines-model button")).find(
      (b) => b.textContent === "これで起動する",
    );
    await click(select as HTMLElement);
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/llm/models/qwen3", "PUT", {
      default: true,
    });
  });

  // A disabled model stays on the panel — this is the only place it can be turned back on.
  it("keeps a disabled model reachable and offers to enable it", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          has_models: true,
          model_rows: [
            { id: "sdxl-base-1.0", kind: "checkpoint", enabled: true, selected: true },
            { id: "parked", kind: "checkpoint", enabled: false },
          ],
        }),
      ],
    });
    apiJSON.mockResolvedValue(row({ has_models: true, model_rows: [] }));
    await mount();
    expect(host!.textContent).toContain("parked");
    const enable = Array.from(host!.querySelectorAll(".engines-model button")).find(
      (b) => b.textContent === "有効にする",
    );
    await click(enable as HTMLElement);
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/parked", "PUT", {
      enabled: true,
    });
  });

  // 🔴 Which state a row is IN and what pressing its button WOULD DO are different sentences,
  // and only the second was ever written down: the state was carried by dimming the row to 0.6
  // opacity — indistinguishable from a disabled control, and in the light theme barely a
  // difference at all — leaving "有効にする" as the evidence for a row that is not enabled.
  it("says on or off in a badge, not only in the label of the button that would change it", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          has_models: true,
          model_rows: [
            { id: "sdxl-base-1.0", kind: "checkpoint", enabled: true, selected: true },
            { id: "parked", kind: "checkpoint", enabled: false },
          ],
        }),
      ],
    });
    await mount();
    const badge = (id: string) =>
      Array.from(host!.querySelectorAll(".engines-model"))
        .find((li) => li.querySelector(".engines-model-id")?.textContent === id)
        ?.querySelector(".engines-model-tag");
    expect(badge("sdxl-base-1.0")?.textContent).toBe("有効");
    expect(badge("sdxl-base-1.0")?.className).toContain("on");
    expect(badge("parked")?.textContent).toBe("無効");
    expect(badge("parked")?.className).toContain("off");

    // "Start with this one" leads: it is what somebody came to this list to do, and enabling
    // is implied by it. Forgetting the row is last.
    const row0 = Array.from(host!.querySelectorAll(".engines-model")).find(
      (li) => li.querySelector(".engines-model-id")?.textContent === "parked",
    )!;
    expect(
      Array.from(row0.querySelectorAll(".engines-model-actions button")).map((b) => b.textContent),
    ).toEqual(["これで起動する", "有効にする", "登録を消す"]);
  });

  // A LoRA is never something an engine is started with, so the control that would say so is
  // not offered — the check the Agent also makes when it builds the tool's enum.
  it("does not offer to start with a LoRA", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          has_models: true,
          model_rows: [
            { id: "sdxl-base-1.0", kind: "checkpoint", enabled: true, selected: true },
            { id: "watercolour", kind: "lora", enabled: true, base_model: "sdxl" },
          ],
        }),
      ],
    });
    await mount();
    const select = Array.from(host!.querySelectorAll(".engines-model button")).filter(
      (b) => b.textContent === "これで起動する",
    );
    expect(select.length).toBe(0);
    expect(host!.textContent).toContain("LoRA");
  });

  // An empty catalogue is the reason the controller refuses to start the engine, so it gets a
  // sentence. A blank area here reads as "still loading" and an administrator waits for a box
  // that is never coming.
  it("says why an engine with no catalogue will not start", async () => {
    api.mockResolvedValue({ engines: [row({ has_models: false, model_rows: [] })] });
    await mount();
    expect(host!.textContent).toContain("カタログは空です");
  });

  it("says when models exist but none is enabled", async () => {
    api.mockResolvedValue({
      engines: [
        row({ has_models: false, model_rows: [{ id: "parked", kind: "checkpoint", enabled: false }] }),
      ],
    });
    await mount();
    expect(host!.textContent).toContain("有効なモデルがありません");
  });

  // P0's definition of done is "switch the image checkpoint to another one without touching
  // CloudFormation", and the seed creates exactly ONE row per role — so there has to be a way
  // to add the second. This is not P4's ingest: it writes down a file that is already staged.
  it("registers a staged file as a catalogue row, disabled", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          has_models: true,
          model_rows: [{ id: "sdxl-base-1.0", kind: "checkpoint", enabled: true, selected: true }],
        }),
      ],
    });
    apiJSON.mockResolvedValue(row({ has_models: true, model_rows: [] }));
    await mount();
    const open = Array.from(host!.querySelectorAll("button")).find(
      (b) => b.textContent === "バケットのファイルを登録する",
    );
    await click(open as HTMLElement);

    // id, key, size, description, licence, licence URL. The WINDOW fields are chat-only, and
    // this is the image role.
    const inputs = Array.from(host!.querySelectorAll(".engines-model-add input"));
    expect(inputs.length).toBe(6);
    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(
          HTMLInputElement.prototype,
          "value",
        )!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    // ⚠️ The size belongs to its FILE and now sits with it, so the order is
    // id / key / size / description. A model can be several files (ADR 0072 decision 2) and a
    // single size field at the bottom of the form could not say which one it measured.
    await type(inputs[0], "juggernaut-xl-v9");
    await type(inputs[1], "image/checkpoints/juggernaut_xl_v9.safetensors");
    await type(inputs[3], "a photographic SDXL fine-tune");

    // ⚠️ The form must not imply the key was checked. The CP holds no S3 permission at all
    // (ADR 0072 review R3), so a typo only surfaces at the next cold start. Asserted while the
    // form is open, because submitting closes it.
    expect(host!.textContent).toContain("CP は S3 を見ません");

    const go = Array.from(host!.querySelectorAll(".engines-model-add button")).find(
      (b) => b.textContent === "登録する",
    );
    await click(go as HTMLElement);
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models", "POST", {
      id: "juggernaut-xl-v9",
      kind: "checkpoint",
      files: [{ flag: "", s3Key: "image/checkpoints/juggernaut_xl_v9.safetensors", bytes: 0 }],
      description: "a photographic SDXL fine-tune",
      // Empty because THIS engine declared no vocabulary: sd.cpp holds one checkpoint and never
      // reads a family, so the panel offered no choice and there is nothing to send.
      base_model: "",
      context_tokens: 0,
      max_output_tokens: 0,
      // 🔴 Empty because nobody typed one, and it is SENT empty: an unrecorded licence is a
      // state the row states ("licence not recorded"), not a gap the panel fills in.
      license_name: "",
      license_url: "",
    });
  });

  // The one route where a licence has to be typed — there is no source here to read one from
  // (ADR 0072 decision 6 vs. decision 10). Optional, and what it buys is the verdict: the CP
  // reads "may this be used commercially" off the words, exactly as the ingest does, so the
  // same model does not lose its mark by coming in through this door.
  it("takes a licence with a hand-registered row, and sends it as the terms", async () => {
    api.mockResolvedValue({ engines: [row({ has_models: true, model_rows: [] })] });
    apiJSON.mockResolvedValue(row({ has_models: true, model_rows: [] }));
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "バケットのファイルを登録する",
      ) as HTMLElement,
    );
    const inputs = Array.from(host!.querySelectorAll(".engines-model-add input"));
    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    await type(inputs[0], "flux-dev-local");
    await type(inputs[1], "image/checkpoints/flux1-dev.safetensors");
    await type(inputs[4], "flux-1-dev-non-commercial-license");
    await type(inputs[5], "https://example.com/LICENSE.md");
    await click(
      Array.from(host!.querySelectorAll(".engines-model-add button")).find(
        (b) => b.textContent === "登録する",
      ) as HTMLElement,
    );
    // 🔴 `license_name`, not `license`: the row reads `license_name || license`, and what a
    // person types here is the TERMS. Sent as `license` it would be shadowed by nothing and
    // read as Hugging Face's slug.
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models", "POST", {
      id: "flux-dev-local",
      kind: "checkpoint",
      files: [{ flag: "", s3Key: "image/checkpoints/flux1-dev.safetensors", bytes: 0 }],
      description: "",
      base_model: "",
      context_tokens: 0,
      max_output_tokens: 0,
      license_name: "flux-1-dev-non-commercial-license",
      license_url: "https://example.com/LICENSE.md",
    });
  });

  // "Forget" is the row, not the file: the CP has no s3:DeleteObject. The one the engine starts
  // with cannot be forgotten, or the role is left with no checkpoint at all.
  // 🔴 Measured on the dev deployment (2026-09-09): a row was forgotten and its 491 MB file was
  // still in the bucket afterwards, because the Console never asked for `?purge=1`. The CP had
  // implemented it — ADR 0072 decision 7's MODE=delete task exists precisely because the CP has
  // no s3:DeleteObject — and there was simply no way in from the UI.
  //
  // So the two acts are told apart BEFORE the press: forgetting alone leaves bytes nothing can
  // reach and that keep being paid for, and deleting them is a task that has to be started.
  it("tells forgetting the row apart from deleting the bytes, and can ask for both", async () => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs: [] }
        : {
            engines: [
              row({
                key: "llm",
                api: "chat",
                has_models: true,
                model_rows: [{ id: "qwen2.5-coder-0.5b", enabled: false }],
              }),
            ],
          },
    );
    await mount();

    // Pressing "forget" asks rather than acting: nothing has been sent yet.
    await click(
      Array.from(host!.querySelectorAll("button")).find((b) => b.textContent === "登録を消す") as HTMLElement,
    );
    expect(apiJSON).not.toHaveBeenCalled();
    // The default is the SAFE one, and it says what it leaves behind.
    const box = host!.querySelector(".engines-model-confirm input") as HTMLInputElement;
    expect(box.checked).toBe(false);
    expect(host!.textContent).toContain("バケットのファイルはそのまま残り");

    await act(async () => {
      box.click();
    });
    // Ticking it changes what the sentence promises, because the act is now destructive.
    expect(host!.textContent).toContain("バイト列を削除するタスクを起こします");

    apiJSON.mockResolvedValueOnce({ purge: "deleting llm/qwen2.5-coder-0.5b-instruct-q4_k_m.gguf" });
    await click(
      Array.from(host!.querySelectorAll(".engines-model-confirm button")).find(
        (b) => b.textContent === "消す",
      ) as HTMLElement,
    );
    const call = apiJSON.mock.calls.at(-1)!;
    expect(String(call[0])).toBe("api/admin/engines/llm/models/qwen2.5-coder-0.5b?purge=1");
    expect(String(call[1])).toBe("DELETE");
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    // The CP's own words: the deletion is a task that has been LAUNCHED, not a thing that has
    // already happened, and a row that just vanished would not say so.
    expect(host!.textContent).toContain("deleting llm/qwen2.5-coder-0.5b-instruct-q4_k_m.gguf");
  });

  // Leaving the box unticked must send NO purge — the destructive half has to be opt-in.
  it("forgets the row alone when the bytes were not asked for", async () => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs: [] }
        : {
            engines: [
              row({ key: "llm", api: "chat", has_models: true, model_rows: [{ id: "m1", enabled: false }] }),
            ],
          },
    );
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find((b) => b.textContent === "登録を消す") as HTMLElement,
    );
    apiJSON.mockResolvedValueOnce({});
    await click(
      Array.from(host!.querySelectorAll(".engines-model-confirm button")).find(
        (b) => b.textContent === "消す",
      ) as HTMLElement,
    );
    expect(String(apiJSON.mock.calls.at(-1)![0])).toBe("api/admin/engines/llm/models/m1");
  });

  it("forgets a row, and refuses to forget the one in use", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          has_models: true,
          model_rows: [
            { id: "sdxl-base-1.0", kind: "checkpoint", enabled: true, selected: true },
            { id: "parked", kind: "checkpoint", enabled: false },
          ],
        }),
      ],
    });
    apiJSON.mockResolvedValue(row({ has_models: true, model_rows: [] }));
    await mount();
    const forget = Array.from(host!.querySelectorAll(".engines-model")).map((li) =>
      Array.from(li.querySelectorAll("button")).find((b) => b.textContent === "登録を消す"),
    );
    expect((forget[0] as HTMLButtonElement).disabled).toBe(true);
    expect((forget[1] as HTMLButtonElement).disabled).toBe(false);
    await click(forget[1] as HTMLElement);
    // Forgetting now asks first (see the purge test above), and the default leaves the bytes.
    await click(
      Array.from(host!.querySelectorAll(".engines-model-confirm button")).find(
        (b) => b.textContent === "消す",
      ) as HTMLElement,
    );
    expect(apiJSON).toHaveBeenCalledWith(
      "api/admin/engines/image/models/parked",
      "DELETE",
      undefined,
    );
  });

  // ADR 0072 P1. A router role holds ONE model at a time, so two sessions on two models take
  // turns and every turn costs an unload plus 267 s of weights. The panel is where that price is
  // stated: "warm" alone describes a swapping engine and a settled one identically.
  it("names the model in VRAM and how often it has changed", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          key: "llm",
          api: "chat",
          provider: "llamacpp",
          state: "running",
          desired: 1,
          warm: true,
          warm_model: "qwen2.5-coder-1.5b",
          model_swaps: 3,
          has_models: true,
          model_rows: [
            { id: "qwen3-coder-30b-a3b", kind: "gguf", enabled: true, default: true, sync_secs: 179 },
            { id: "qwen2.5-coder-1.5b", kind: "gguf", enabled: true, sync_secs: 11 },
          ],
        }),
      ],
    });
    await mount();
    expect(host!.textContent).toContain("qwen2.5-coder-1.5b");
    expect(host!.textContent).toContain("モデル交替: 3 回");
    // The cold-start cost of each model, next to the toggle that adds it — and marked as an
    // estimate, because the control plane has never looked at the bucket.
    expect(host!.textContent).toContain("同期 +179 秒（推定）");
    expect(host!.textContent).toContain("同期 +11 秒（推定）");
  });

  // Nobody declared the sizes (every row the seed makes, and every row registered before the
  // field existed). "+0 s" would be a claim; nothing is the truth.
  it("says nothing about the sync when no size was declared", async () => {
    api.mockResolvedValue({
      engines: [
        row({ has_models: true, model_rows: [{ id: "sdxl-base-1.0", kind: "checkpoint", enabled: true }] }),
      ],
    });
    await mount();
    expect(host!.textContent).not.toContain("同期 +");
  });

  // ADR 0072 P1: a chat engine's row carries its OWN window, and this form is the only way to
  // declare one until P4's ingest reads it off the model card. A model registered without one
  // reaches opencode as context 0 — which switches auto-compaction off.
  it("declares a window and a size when registering a model for a chat engine", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          key: "llm",
          api: "chat",
          provider: "llamacpp",
          has_models: true,
          model_rows: [{ id: "qwen3-coder-30b-a3b", kind: "gguf", enabled: true, default: true }],
        }),
      ],
    });
    apiJSON.mockResolvedValue(row({ key: "llm", api: "chat", has_models: true, model_rows: [] }));
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "バケットのファイルを登録する",
      ) as HTMLElement,
    );
    // One field per ROW, each with its own label: eight labelled rows, not a strip of eight
    // look-alike boxes whose placeholder captions vanish as soon as somebody types into them.
    const rows = Array.from(host!.querySelectorAll(".engines-model-add-row"));
    expect(rows.length).toBe(8);
    expect(rows.every((r) => r.querySelector("span") && r.querySelector("input"))).toBe(true);
    const inputs = Array.from(host!.querySelectorAll(".engines-model-add input"));
    expect(inputs.length).toBe(8);
    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    await type(inputs[0], "qwen2.5-coder-1.5b");
    await type(inputs[1], "llm/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf");
    await type(inputs[2], "1117320768");
    await type(inputs[3], "small and quick");
    // 4 and 5 are the optional licence pair; the window is the last two.
    await type(inputs[6], "32768");
    await type(inputs[7], "4096");
    await click(
      Array.from(host!.querySelectorAll(".engines-model-add button")).find(
        (b) => b.textContent === "登録する",
      ) as HTMLElement,
    );
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/llm/models", "POST", {
      id: "qwen2.5-coder-1.5b",
      kind: "gguf",
      files: [{ flag: "", s3Key: "llm/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf", bytes: 1117320768 }],
      description: "small and quick",
      base_model: "",
      context_tokens: 32768,
      max_output_tokens: 4096,
      license_name: "",
      license_url: "",
    });
  });

  // A ComfyUI engine picks its workflow graph from the model's FAMILY and refuses to guess one
  // from a name, so the panel has to ask — and it asks with a CHOICE, because what a repository
  // calls a model ("SDXL 1.0") is a display name that names no graph.
  //
  // 🔴 Until this existed the form had one key field and no family at all, so the two things
  // ComfyUI needs were both unreachable from the Console: on af-sandbox the catalogue had to be
  // written by calling the admin API by hand (ADR 0072 P2 実機検証).
  it("asks a comfy engine for a family, and lets one model be several files", async () => {
    const comfy = row({
      provider: "comfy",
      base_models: ["sdxl", "sd35", "flux1", "flux2-klein", "zimage"],
      file_flags: ["", "--diffusion-model", "--clip_l", "--t5xxl", "--vae"],
      has_models: true,
      model_rows: [],
    });
    api.mockResolvedValue({ engines: [comfy] });
    apiJSON.mockResolvedValue(comfy);
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "バケットのファイルを登録する",
      ) as HTMLElement,
    );

    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    const pick = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("change", { bubbles: true }));
      });
    };
    const go = () =>
      Array.from(host!.querySelectorAll(".engines-model-add button")).find(
        (b) => b.textContent === "登録する",
      ) as HTMLButtonElement;

    let inputs = Array.from(host!.querySelectorAll(".engines-model-add input"));
    await type(inputs[0], "flux2-klein-4b");
    await type(inputs[1], "image/diffusion_models/flux-2-klein-4b.safetensors");

    // ⚠️ The family is not optional here, and the form says so by refusing rather than by
    // letting the CP answer 400 after the press.
    expect(go().disabled).toBe(true);

    const selects = () => Array.from(host!.querySelectorAll(".engines-model-add select"));
    await pick(selects()[0], "flux2-klein");
    expect(go().disabled).toBe(false);
    // The first file's part, then two more files with their own.
    await pick(selects()[1], "--diffusion-model");

    const more = () =>
      Array.from(host!.querySelectorAll(".engines-model-add button")).find(
        (b) => b.textContent === "ファイルを追加する",
      ) as HTMLElement;
    await click(more());
    await click(more());
    inputs = Array.from(host!.querySelectorAll(".engines-model-add input"));
    // id, then (key, size) per file, then the description and the optional licence pair: three
    // files is ten inputs.
    expect(inputs.length).toBe(10);
    await type(inputs[3], "image/text_encoders/qwen_3_4b_fp8_mixed.safetensors");
    await type(inputs[5], "image/vae/flux2-vae.safetensors");
    await pick(selects()[2], "--clip_l");
    await pick(selects()[3], "--vae");

    await click(go());
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models", "POST", {
      id: "flux2-klein-4b",
      kind: "checkpoint",
      files: [
        { flag: "--diffusion-model", s3Key: "image/diffusion_models/flux-2-klein-4b.safetensors", bytes: 0 },
        { flag: "--clip_l", s3Key: "image/text_encoders/qwen_3_4b_fp8_mixed.safetensors", bytes: 0 },
        { flag: "--vae", s3Key: "image/vae/flux2-vae.safetensors", bytes: 0 },
      ],
      description: "",
      base_model: "flux2-klein",
      context_tokens: 0,
      max_output_tokens: 0,
      license_name: "",
      license_url: "",
    });
  });

  // The row the SEED writes, and every row written before the CP validated one: complete in
  // every way this panel can see, and unable to generate. The CP states it because the panel
  // cannot know the vocabulary.
  it("says so on a row whose family names no workflow", async () => {
    api.mockResolvedValue({
      engines: [
        row({
          provider: "comfy",
          base_models: ["sdxl"],
          has_models: true,
          model_rows: [
            { id: "seeded", kind: "checkpoint", enabled: true, base_model_missing: true },
            { id: "declared", kind: "checkpoint", enabled: true, base_model: "sdxl" },
          ],
        }),
      ],
    });
    await mount();
    const warnings = Array.from(host!.querySelectorAll(".form-err")).filter((e) =>
      e.textContent?.includes("モデルファミリー"),
    );
    expect(warnings.length).toBe(1);

    // The FIX sits under the warning it answers, and only there — the row that already declares
    // one shows it in its meta line and needs no control.
    const pickers = Array.from(host!.querySelectorAll(".engines-model-family"));
    expect(pickers.length).toBe(1);

    // 🔴 One field, not the whole row. Before this the only way to give a row a family was to
    // register it again from scratch: that lands it disabled and, for a split model, means
    // re-typing three S3 keys to change one word.
    apiJSON.mockResolvedValue(row({ provider: "comfy", base_models: ["sdxl"], has_models: true, model_rows: [] }));
    const sel = pickers[0].querySelector("select")!;
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
      setter.call(sel, "sdxl");
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/seeded", "PUT", {
      base_model: "sdxl",
    });
  });

  // Half a window is worse than none: opencode reads an output cap of 0 as 32,000, so a 32k
  // context declared alone leaves 768 usable tokens. Both halves or neither.
  it("drops a context declared without an output cap", async () => {
    api.mockResolvedValue({
      engines: [row({ key: "llm", api: "chat", has_models: true, model_rows: [] })],
    });
    apiJSON.mockResolvedValue(row({ key: "llm", api: "chat", has_models: true, model_rows: [] }));
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "バケットのファイルを登録する",
      ) as HTMLElement,
    );
    const inputs = Array.from(host!.querySelectorAll(".engines-model-add input"));
    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    await type(inputs[0], "half-declared");
    await type(inputs[1], "llm/x.gguf");
    await type(inputs[3], "32768");
    await click(
      Array.from(host!.querySelectorAll(".engines-model-add button")).find(
        (b) => b.textContent === "登録する",
      ) as HTMLElement,
    );
    const body = apiJSON.mock.calls.at(-1)![2] as Record<string, number>;
    expect(body.context_tokens).toBe(0);
    expect(body.max_output_tokens).toBe(0);
  });

  // ADR 0072 decision 6 / 10. The order is the whole point: look the source up, SEE the licence
  // and the gating, and only then is there a checkbox to accept it — an acceptance offered
  // before the terms is not one.
  it("shows the licence before offering to accept it, and refuses a gated repo with no token", async () => {
    api.mockResolvedValue({
      engines: [row({ key: "llm", api: "chat", has_models: true, model_rows: [] })],
    });
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "Hugging Face などから取り込む",
      ) as HTMLElement,
    );
    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    const inputs = Array.from(host!.querySelectorAll(".engines-ingest .engines-model-add-row input"));
    await type(inputs[0], "black-forest-labs/FLUX.1-dev");
    await type(inputs[1], "flux1-dev.safetensors");
    await type(inputs[2], "flux1-dev");

    // Nothing to accept yet: the source has not been read.
    expect(host!.querySelector(".engines-ingest-accept")).toBe(null);

    apiJSON.mockResolvedValueOnce({
      sha256: "4610115bb0c89560703c892c59ac2742fa821e60ef5871b33493ba544683abd7",
      bytes: 23802932552,
      gated: true,
      license: "other",
      license_name: "flux-1-dev-non-commercial-license",
      commercial_use: "no",
      can_ingest: false,
    });
    await click(
      Array.from(host!.querySelectorAll(".engines-ingest button")).find(
        (b) => b.textContent === "調べる",
      ) as HTMLElement,
    );
    // The two licence fields, the size and both warnings — the non-commercial one because the
    // deployment may be charging, the gated one because it cannot be fetched at all here.
    expect(host!.textContent).toContain("flux-1-dev-non-commercial-license");
    expect(host!.textContent).toContain("23.8 GB");
    expect(host!.textContent).toContain("非商用ライセンス");
    expect(host!.textContent).toContain("トークンがありません");
    // 🔴 And the acceptance is unusable: pressing on would spend a Fargate task to earn a 401.
    const box = host!.querySelector(".engines-ingest-accept input") as HTMLInputElement;
    expect(box.disabled).toBe(true);
    const go = Array.from(host!.querySelectorAll(".engines-ingest button")).find(
      (b) => b.textContent === "取り込む",
    ) as HTMLButtonElement;
    expect(go.disabled).toBe(true);
  });

  it("starts an ingest once the licence is accepted, and shows the job", async () => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs: [] }
        : { engines: [row({ key: "llm", api: "chat", has_models: true, model_rows: [] })] },
    );
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "Hugging Face などから取り込む",
      ) as HTMLElement,
    );
    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    const inputs = Array.from(host!.querySelectorAll(".engines-ingest .engines-model-add-row input"));
    await type(inputs[0], "Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF");
    await type(inputs[1], "qwen2.5-coder-1.5b-instruct-q4_k_m.gguf");
    await type(inputs[2], "qwen2.5-coder-1.5b");
    await type(inputs[4], "32768");
    // The output cap is a select over fractions of the window, not a free number — 1/8 of
    // 32,768 is the 4,096 both models here were already being run at.
    await act(async () => {
      const sel = host!.querySelectorAll(".engines-ingest select")[0] as HTMLSelectElement;
      sel.value = "4096";
      sel.dispatchEvent(new Event("change", { bubbles: true }));
    });

    apiJSON.mockResolvedValueOnce({ sha256: "cc32", bytes: 1117320768, gated: false,
      license: "apache-2.0", commercial_use: "yes", can_ingest: true });
    await click(
      Array.from(host!.querySelectorAll(".engines-ingest button")).find(
        (b) => b.textContent === "調べる",
      ) as HTMLElement,
    );
    await act(async () => {
      const box = host!.querySelector(".engines-ingest-accept input") as HTMLInputElement;
      box.click();
    });
    apiJSON.mockResolvedValueOnce({ id: "j1", model_id: "qwen2.5-coder-1.5b", state: "running" });
    // After starting, the panel re-reads the job list — which is how a running download appears.
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs: [{ id: "j1", model_id: "qwen2.5-coder-1.5b", state: "running", source: "hf:Qwen/…", bytes: 1117320768 }] }
        : { engines: [row({ key: "llm", api: "chat", has_models: true, model_rows: [] })] },
    );
    await click(
      Array.from(host!.querySelectorAll(".engines-ingest button")).find(
        (b) => b.textContent === "取り込む",
      ) as HTMLElement,
    );
    const body = apiJSON.mock.calls.at(-1)!;
    expect(String(body[0])).toBe("api/admin/engines/llm/ingest");
    expect(body[2]).toMatchObject({
      id: "qwen2.5-coder-1.5b",
      kind: "gguf",
      s3Key: "llm/qwen2.5-coder-1.5b-instruct-q4_k_m.gguf",
      context_tokens: 32768,
      max_output_tokens: 4096,
      license_accepted: true,
      source: { hf: { repo: "Qwen/Qwen2.5-Coder-1.5B-Instruct-GGUF", file: "qwen2.5-coder-1.5b-instruct-q4_k_m.gguf", revision: "" } },
    });
    expect(host!.textContent).toContain("取り込み中");
  });

  /** The input of the form row with this label. By label rather than by index, because the
   *  filename row turns from an input into a select the moment a listing arrives. */
  const fieldByLabel = (label: string) =>
    Array.from(host!.querySelectorAll(".engines-ingest .engines-model-add-row")).find(
      (l) => l.querySelector("span")?.textContent === label,
    )!;

  // 🔴 The context length Hugging Face publishes is the ARCHITECTURE's ceiling, and this
  // deployment already runs a model well below it: unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF
  // says 262144 and is run at 32768, because 262k does not fit an L4. So a number somebody
  // entered has to outrank the one off the model card — silently replacing it is how a window
  // that was chosen for the GPU becomes one that was chosen by the publisher.
  it("never overwrites a window that was typed with the model's ceiling", async () => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs: [] }
        : { engines: [row({ key: "llm", api: "chat", has_models: true, model_rows: [] })] },
    );
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "Hugging Face などから取り込む",
      ) as HTMLElement,
    );
    const set = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    await set(fieldByLabel("リポジトリ").querySelector("input")!, "unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF");
    await set(fieldByLabel("ファイル名").querySelector("input")!, "Q4_K_M.gguf");
    // Chosen deliberately, for the GPU this deployment has.
    await set(fieldByLabel("コンテキストウィンドウ").querySelector("input")!, "32768");

    apiJSON.mockResolvedValueOnce({
      sha256: "a".repeat(64),
      bytes: 18556689568,
      gated: false,
      license: "apache-2.0",
      commercial_use: "yes",
      can_ingest: true,
      context_length: 262144,
    });
    await click(
      Array.from(host!.querySelectorAll(".engines-ingest button")).find(
        (b) => b.textContent === "調べる",
      ) as HTMLElement,
    );

    const ctx = fieldByLabel("コンテキストウィンドウ").querySelector("input") as HTMLInputElement;
    expect(ctx.value).toBe("32768");
    // Still SAID, because it is a fact worth knowing — just not one that overwrites a decision.
    expect(host!.textContent).toContain("モデルの上限 262144");
  });

  // 🔴 Observed on the dev deployment (2026-09-09): after a model was forgotten AND its bytes
  // purged, two finished jobs still sat under the ingest form — correctly, because a job is a
  // record of an event and "this ingest ran and finished" goes on being true. But undated, a
  // green "done" beside a model id reads as THAT MODEL's current state, i.e. as "ready to
  // use", which is the opposite of the truth for a model that no longer exists anywhere.
  it("dates the ingest history, so a finished job does not read as a model that is ready", async () => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? {
            jobs: [
              {
                id: "j1",
                model_id: "qwen2.5-coder-0.5b-instruct",
                state: "done",
                source: "hf:Qwen/Qwen2.5-Coder-0.5B-Instruct-GGUF/…",
                bytes: 491400064,
                created_at: "2026-09-09T02:59:30Z",
              },
            ],
          }
        : { engines: [row({ key: "llm", api: "chat", has_models: true, model_rows: [] })] },
    );
    await mount();
    // Headed as history, not as a section of the catalogue above it.
    expect(host!.textContent).toContain("取り込みの履歴");
    const when = host!.querySelector(".engines-ingest-when");
    expect(when).toBeTruthy();
    expect(when!.textContent).toBeTruthy();
    // The job survives a model that is not in the catalogue at all — that IS the case this
    // dating exists for, so the row has to still be here.
    expect(host!.textContent).toContain("qwen2.5-coder-0.5b-instruct");
  });

  // 🔴 Measured on the dev deployment (2026-09-09): the filename was free text, and one letter
  // short of `flux1-dev.safetensors` is refused correctly while looking exactly like a file
  // that is not there. So "look it up" with no filename asks the repository what it HOLDS, and
  // the answer becomes a picker.
  //
  // The two numbers ride along from the same answer: Hugging Face has already parsed the GGUF
  // header, so the window is offered rather than copied off a model card by hand, and the cap
  // follows at an eighth of it. Both stay editable — the window especially, because it is the
  // MODEL's ceiling and not what fits in this deployment's GPU.
  it("lists what the repository holds, and offers the window off the file that is picked", async () => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs: [] }
        : { engines: [row({ key: "llm", api: "chat", has_models: true, model_rows: [] })] },
    );
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "Hugging Face などから取り込む",
      ) as HTMLElement,
    );
    await act(async () => {
      const el = host!.querySelector(".engines-ingest .engines-model-add-row input")!;
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(el, "Qwen/Qwen2.5-Coder-0.5B-Instruct-GGUF");
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });

    // No filename yet, so the first call asks what there is — and starts nothing.
    apiJSON.mockResolvedValueOnce({
      files: [
        { name: "qwen2.5-coder-0.5b-instruct-q2_k.gguf", bytes: 415182720 },
        { name: "qwen2.5-coder-0.5b-instruct-q4_k_m.gguf", bytes: 491400064 },
      ],
    });
    await click(
      Array.from(host!.querySelectorAll(".engines-ingest button")).find(
        (b) => b.textContent === "調べる",
      ) as HTMLElement,
    );
    expect(String(apiJSON.mock.calls.at(-1)![0])).toBe("api/admin/engines/llm/ingest/files");
    const picker = host!.querySelector(".engines-ingest select") as HTMLSelectElement;
    expect(Array.from(picker.options).map((o) => o.value)).toEqual([
      "",
      "qwen2.5-coder-0.5b-instruct-q2_k.gguf",
      "qwen2.5-coder-0.5b-instruct-q4_k_m.gguf",
    ]);
    // The size is on the option, because "which quantisation" IS a question about size.
    expect(picker.textContent).toContain("491 MB");

    apiJSON.mockResolvedValueOnce({
      sha256: "1d9614638d18024d0fbb36575a15f1302a3adf044df10345688ec4f6e1c4ff32",
      bytes: 491400064,
      gated: false,
      license: "apache-2.0",
      commercial_use: "yes",
      can_ingest: true,
      context_length: 32768,
    });
    await act(async () => {
      picker.value = "qwen2.5-coder-0.5b-instruct-q4_k_m.gguf";
      picker.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => {
      await Promise.resolve();
    });

    // Picking resolves that file, and the resolve is where the licence comes from.
    expect(String(apiJSON.mock.calls.at(-1)![0])).toBe("api/admin/engines/llm/ingest/resolve");
    expect(apiJSON.mock.calls.at(-1)![2]).toMatchObject({
      source: { hf: { file: "qwen2.5-coder-0.5b-instruct-q4_k_m.gguf" } },
    });
    // Attributed as the MODEL's number, never presented as the window this deployment chose.
    expect(host!.textContent).toContain("モデルの上限 32768");

    const inputs = Array.from(host!.querySelectorAll(".engines-ingest .engines-model-add-row input")) as HTMLInputElement[];
    const ctxField = inputs.find((i) => i.value === "32768");
    expect(ctxField).toBeTruthy();
    const caps = Array.from(host!.querySelectorAll(".engines-ingest select")) as HTMLSelectElement[];
    expect(caps[caps.length - 1].value).toBe("4096"); // 1/8 of 32768
  });

  // 🔴 Measured on the dev deployment (2026-09-09): a filename typed one letter short answered
  // "the repository does not list flux1-dev.safetensor" — the CP's developer message, in
  // English, on a Japanese screen. Every code this panel can raise was a string literal in
  // engine_ingest.go, and the catalogue gate reads only the constants in errcodes.go, so none
  // of them had ever been checked for a translation.
  //
  // Both halves are pinned here: the sentence has to be Japanese, and the file the CP named has
  // to survive into it. Translating alone would have answered "そのリポジトリにそのファイルが
  // ありません" over a form with no way to tell WHICH file was wrong.
  it("says why an ingest was refused in the user's language, without losing the detail", async () => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs: [] }
        : { engines: [row({ key: "image", has_models: true, model_rows: [] })] },
    );
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "Hugging Face などから取り込む",
      ) as HTMLElement,
    );
    await act(async () => {
      const el = host!.querySelector(".engines-ingest .engines-model-add-row input")!;
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(el, "black-forest-labs/FLUX.1-dev");
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
    apiJSON.mockResolvedValueOnce({
      error: { code: "file_unknown", message: "the repository does not list flux1-dev.safetensor" },
    });
    await click(
      Array.from(host!.querySelectorAll(".engines-ingest button")).find(
        (b) => b.textContent === "調べる",
      ) as HTMLElement,
    );
    const shown = host!.querySelector(".engines-ingest .form-err")!.textContent!;
    expect(shown).toContain("そのリポジトリにそのファイルがありません");
    expect(shown).toContain("flux1-dev.safetensor");
  });

  // 🔴 Measured on the dev deployment (2026-09-09): the ingest finished in 72 seconds, the job
  // said 完了 — and the model list went on showing the two rows it already had. The row an
  // ingest creates is disabled by design, so it is precisely the row an administrator came here
  // to switch on, and it was reachable only by pressing refresh. Nothing else re-reads it: the
  // job poll reads only the job list, and the engine poll is off because an on-demand engine
  // parked at "stopped" is a settled state.
  it("re-reads the catalogue when an ingest finishes, so the new disabled row appears", async () => {
    vi.useFakeTimers();
    const catalogue = (extra: Record<string, unknown>[]) =>
      row({
        key: "llm",
        api: "chat",
        has_models: true,
        model_rows: [{ id: "qwen2.5-coder-1.5b", enabled: true }, ...extra],
      });
    let finished = false;
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? {
            jobs: [
              {
                id: "j1",
                model_id: "qwen2.5-coder-0.5b",
                state: finished ? "done" : "running",
                source: "hf:Qwen/Qwen2.5-Coder-0.5B-Instruct-GGUF/…",
                bytes: 491400064,
              },
            ],
          }
        : {
            engines: [
              catalogue(finished ? [{ id: "qwen2.5-coder-0.5b", enabled: false }] : []),
            ],
          },
    );
    await mount();

    // The catalogue rows only — the job list is the other <ul> and it names the model too, so
    // asserting on the panel's whole text would pass with the bug still in place.
    const catalogueIds = () =>
      Array.from(
        host!.querySelectorAll("ul.engines-model-list:not(.engines-ingest-jobs) .mono"),
      ).map((n) => n.textContent);
    expect(catalogueIds()).toEqual(["qwen2.5-coder-1.5b"]);
    expect(host!.textContent).toContain("取り込み中");

    finished = true;
    await act(async () => {
      await vi.advanceTimersByTimeAsync(10000);
    });

    expect(host!.textContent).toContain("完了");
    expect(catalogueIds()).toEqual(["qwen2.5-coder-1.5b", "qwen2.5-coder-0.5b"]);
    // Disabled, so what it offers is the switch-on — decision 6: taken in is not the same as
    // on offer.
    const fresh = Array.from(host!.querySelectorAll("li.engines-model")).find(
      (li) => li.querySelector(".mono")?.textContent === "qwen2.5-coder-0.5b",
    )!;
    expect(fresh.className).not.toContain("on");
    expect(fresh.textContent).toContain("有効にする");
  });

  // 🔴 ADR 0072 P2 欠落 6. An ingest wrote one unlabelled file, so a split model could not be
  // assembled by taking its parts in: on af-sandbox the four files of `flux1-dev-fp8` were
  // staged as three throwaway rows and the real row was re-typed key by key through
  // `POST /models` — with the throwaway rows left pointing at the same objects.
  //
  // Two halves are pinned here, and both were unreachable from this form: WHAT the file is
  // (which also decides its directory, and a text encoder under `image/checkpoints/` is
  // invisible to every loader that would read it), and that it joins the row that is already
  // there instead of being refused as a duplicate id.
  it("takes a part in and attaches it to the row that already holds the model", async () => {
    api.mockImplementation(async (p: string) =>
      p.endsWith("/ingest")
        ? { jobs: [] }
        : {
            engines: [
              row({
                provider: "comfy",
                base_models: ["sdxl", "sd35", "flux1", "flux2-klein", "zimage"],
                file_flags: ["", "--diffusion-model", "--clip_l", "--clip_g", "--t5xxl", "--vae"],
                has_models: true,
                model_rows: [{ id: "flux1-dev-fp8", enabled: false, base_model: "flux1" }],
              }),
            ],
          },
    );
    await mount();
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "Hugging Face などから取り込む",
      ) as HTMLElement,
    );
    const type = async (el: Element, v: string) => {
      await act(async () => {
        const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
        setter.call(el, v);
        el.dispatchEvent(new Event("input", { bubbles: true }));
      });
    };
    const inputs = Array.from(host!.querySelectorAll(".engines-ingest .engines-model-add-row input"));
    await type(inputs[0], "comfyanonymous/flux_text_encoders");
    await type(inputs[1], "clip_l.safetensors");
    // The id of the row this part belongs to, which the catalogue already holds.
    await type(inputs[2], "flux1-dev-fp8");

    // Said before anything is fetched: as it stands this ingest is the one the CP answers 409
    // to, and the button is not offered.
    expect(host!.textContent).toContain("この id はもう使われています");

    const partSelect = host!.querySelector(".engines-ingest select") as HTMLSelectElement;
    await act(async () => {
      partSelect.value = "--clip_l";
      partSelect.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => {
      const box = host!.querySelector(".engines-ingest-accept input") as HTMLInputElement;
      box.click();
    });

    apiJSON.mockResolvedValueOnce({
      sha256: "5555555555555555555555555555555555555555555555555555555555555555",
      bytes: 246144152,
      gated: false,
      license: "apache-2.0",
      commercial_use: "yes",
      can_ingest: true,
    });
    await click(
      Array.from(host!.querySelectorAll(".engines-ingest button")).find(
        (b) => b.textContent === "調べる",
      ) as HTMLElement,
    );
    await act(async () => {
      const boxes = Array.from(host!.querySelectorAll(".engines-ingest-accept input")) as HTMLInputElement[];
      boxes[boxes.length - 1].click(); // the licence
    });

    apiJSON.mockResolvedValueOnce({ id: "j2", model_id: "flux1-dev-fp8", state: "running" });
    await click(
      Array.from(host!.querySelectorAll(".engines-ingest button")).find(
        (b) => b.textContent === "取り込む",
      ) as HTMLElement,
    );
    const body = apiJSON.mock.calls.at(-1)!;
    expect(String(body[0])).toBe("api/admin/engines/image/ingest");
    expect(body[2]).toMatchObject({
      id: "flux1-dev-fp8",
      file_flag: "--clip_l",
      attach: true,
      // 🔴 text_encoders, not checkpoints: the directory is what puts the file in
      // DualCLIPLoader's menu at all.
      s3Key: "image/text_encoders/clip_l.safetensors",
    });
  });
});

// The operator's Hugging Face token (ADR 0072 decision 6 as revised, phase P5).
//
// The panel exists because the alternative was a CloudFormation round trip. What it must not do
// is imply it holds more than it does: the CP has `PutSecretValue` and no `GetSecretValue`, so
// there is no current value to show, and a field that looked like it had been pre-filled would
// be a lie the deployment cannot back.
describe("EnginesAdminView / the Hugging Face token", () => {
  const byPath = (hf: Record<string, unknown>) => (path: string) =>
    Promise.resolve(path === "api/admin/engines/hf-token" ? hf : { engines: [row()] });

  // React tracks an input's value on the node, so assigning `.value` directly is invisible to
  // it — the state stays empty and the button stays disabled, which looks exactly like a
  // broken form.
  const typeInto = async (el: HTMLInputElement, value: string) => {
    await act(async () => {
      Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!.call(el, value);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };

  const tokenInput = () =>
    host!.querySelector('input[type="password"]') as HTMLInputElement | null;

  const button = (label: string) =>
    Array.from(host!.querySelectorAll("button")).find((b) => b.textContent === label) as
      | HTMLButtonElement
      | undefined;

  it("registers a token and reports who and when, never the value", async () => {
    api.mockImplementation(byPath({ available: true, configured: false }));
    apiJSON.mockResolvedValue({
      available: true,
      configured: true,
      updated_by: "admin1",
      updated_at: "2026-09-09T12:00:00Z",
    });
    await mount();

    const input = tokenInput()!;
    expect(input).toBeTruthy();
    expect(host!.textContent).toContain("未登録");
    // Empty is not a removal: the register button stays disabled until something is typed.
    expect(button("登録する")?.disabled).toBe(true);

    await typeInto(input, "hf_typed_value");
    await click(button("登録する"));

    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/hf-token", "PUT", {
      token: "hf_typed_value",
    });
    expect(host!.textContent).toContain("登録済み");
    expect(host!.textContent).toContain("admin1");
    // 🔴 The token is not on the screen afterwards, in any field: the CP cannot read it back,
    // so anything the panel showed would be its own copy of a secret.
    expect(host!.innerHTML).not.toContain("hf_typed_value");
    expect(tokenInput()!.value).toBe("");
  });

  it("offers no field on a stack that keeps the token itself, and says gated still works", async () => {
    api.mockImplementation(byPath({ available: false, configured: true, stack_token: true }));
    await mount();
    expect(tokenInput()).toBeNull();
    // Not "no token": this deployment HAS one, from a CloudFormation parameter. Reading as
    // "unregistered" would send somebody to fix what is not broken.
    expect(host!.textContent).toContain("CloudFormation");
    expect(host!.textContent).not.toContain("未登録");
  });

  it("says which half failed, in Japanese, when the secret refuses the write", async () => {
    api.mockImplementation(byPath({ available: true, configured: false }));
    apiJSON.mockResolvedValue({
      error: { code: "hf_token_put_failed", message: "AccessDeniedException: PutSecretValue" },
    });
    await mount();
    const input = tokenInput()!;
    await typeInto(input, "hf_typed_value");
    await click(button("登録する"));

    // Scoped to the token panel: the engine panels above carry their own .form-err (an empty
    // catalogue), and an unscoped query would pass while this panel said nothing at all.
    const panel = Array.from(host!.querySelectorAll(".admin-panel")).at(-1)!;
    expect(panel.querySelector(".form-err")?.textContent).toContain("配備の秘密");
    // Still not registered, and the typed value is kept so it can be tried again rather than
    // retyped from wherever it came from.
    expect(host!.textContent).toContain("未登録");
    expect(tokenInput()!.value).toBe("hf_typed_value");
  });
});

// The repository picker (ADR 0072 decision 11).
//
// P4 turned the free-text FILENAME into a picker; the repository name above it was still
// "look it up in another window and paste it". What these pin is that a result is a
// DESTINATION — picking one fills the field somebody would have typed into, and the existing
// resolve → accept → ingest road runs unchanged.
describe("EnginesAdminView / searching for a model", () => {
  const openIngest = async () => {
    await click(
      Array.from(host!.querySelectorAll("button")).find(
        (b) => b.textContent === "Hugging Face などから取り込む",
      ) as HTMLButtonElement,
    );
  };

  const typeInto = async (el: HTMLInputElement, value: string) => {
    await act(async () => {
      Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!.call(el, value);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };

  const field = (label: string) =>
    (Array.from(host!.querySelectorAll("label.engines-model-add-row, label.engines-search-row")).find(
      (l) => l.querySelector("span")?.textContent === label,
    )?.querySelector("input") || null) as HTMLInputElement | null;

  const button = (label: string) =>
    Array.from(host!.querySelectorAll("button")).find((b) => b.textContent === label) as
      | HTMLButtonElement
      | undefined;

  // React delegates onBlur from `focusout`; a raw non-bubbling `blur` never reaches it.
  const leave = async (el: HTMLInputElement) => {
    await act(async () => {
      el.dispatchEvent(new FocusEvent("focusout", { bubbles: true }));
    });
  };

  // 🔴 An address is what a person actually has in hand, and pasting one used to leave the
  // form looking untouched: the URL was taken apart only when the request was built, so what
  // would be fetched was never on screen, and the file it named was not the one the picker
  // showed. Splitting it into the fields leaves one source of truth.
  it("takes a pasted model-page URL apart into the fields it names", async () => {
    api.mockResolvedValue({ engines: [row()] });
    await mount();
    await openIngest();

    await typeInto(
      field("リポジトリ")!,
      "https://huggingface.co/stabilityai/stable-diffusion-xl-base-1.0/blob/main/sd_xl_base_1.0.safetensors",
    );
    await leave(field("リポジトリ")!);
    expect(field("リポジトリ")!.value).toBe("stabilityai/stable-diffusion-xl-base-1.0");
    expect(field("ファイル名")!.value).toBe("sd_xl_base_1.0.safetensors");

    // The file is named, so "look it up" resolves it rather than asking what the repo holds —
    // and the revision the URL carried survives the split (dropping it resolves `main`, which
    // is a different file whenever the URL pointed at anything else).
    apiJSON.mockResolvedValue({ sha256: "a".repeat(64), bytes: 6_939_000_000, license: "openrail++" });
    await click(button("調べる"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/ingest/resolve", "POST", {
      source: {
        hf: { repo: "stabilityai/stable-diffusion-xl-base-1.0", file: "sd_xl_base_1.0.safetensors", revision: "main" },
      },
    });
    // …and the id is proposed off that file, exactly as it is for one picked from a list.
    expect(field("id")!.value).toBe("sd_xl_base_1.0");
  });

  // The version id, not the model id in the path next to it: an ingest takes the former and
  // the two are different numbers on the same page.
  it("turns a pasted Civitai page into its version id", async () => {
    api.mockResolvedValue({ engines: [row()] });
    await mount();
    await openIngest();
    await typeInto(field("リポジトリ")!, "https://civitai.com/models/133005?modelVersionId=782002");
    await leave(field("リポジトリ")!);
    expect(field("リポジトリ")!.value).toBe("civitai:782002");
  });

  // What a field ASKS FOR has to be something that field can take. On the image role the
  // repository and the id offered a GGUF example (`Qwen/…-GGUF`, `qwen2.5-coder-1.5b`) — not a
  // hint but a wrong answer, since sd-server cannot load one and following it costs a resolve
  // and a refusal.
  it("offers examples that belong to this engine's role", async () => {
    api.mockResolvedValue({ engines: [row()] }); // api: "images"
    await mount();
    await openIngest();
    expect(field("探す")!.placeholder).toBe("sdxl");
    expect(field("リポジトリ")!.placeholder).toBe("stabilityai/stable-diffusion-xl-base-1.0");
    expect(field("ファイル名")!.placeholder).toBe("name.safetensors");
    expect(field("id")!.placeholder).toBe("sdxl-base-1.0");
  });

  // 🔴 With a plain https URL above it, the same box is not the file name — it is the sha256,
  // the only thing that can verify a download nothing else describes. A label and an example
  // that still say "name.safetensors" there ask for the one value it must not be given.
  it("relabels the file box as the sha256 when the source is a plain url", async () => {
    api.mockResolvedValue({ engines: [row()] });
    await mount();
    await openIngest();
    await typeInto(field("リポジトリ")!, "https://example.com/some-model.safetensors");
    expect(field("ファイル名")).toBeNull();
    expect(field("sha256")!.placeholder).toBe("64 桁の 16 進");
  });

  it("fills the repository field from a hit, and says gated before anything is started", async () => {
    api.mockResolvedValue({ engines: [row()] });
    apiJSON.mockResolvedValue({
      hits: [
        {
          source: "hf",
          ref: "black-forest-labs/FLUX.1-dev",
          name: "black-forest-labs/FLUX.1-dev",
          downloads: 790579,
          likes: 14538,
          trending: 316,
          gated: true,
          license: "other",
          license_name: "flux-1-dev-non-commercial-license",
        },
      ],
    });
    await mount();
    await openIngest();

    await typeInto(field("探す")!, "flux");
    await click(button("検索"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/ingest/search", "POST", {
      q: "flux",
      source: "hf",
      sort: "downloads",
    });
    // The verdict rides with the row: gated and the real licence name, so the choice is made
    // before a resolve, not after a refusal.
    const hit = host!.querySelector(".engines-search-hits li")!;
    expect(hit.textContent).toContain("gated");
    expect(hit.textContent).toContain("flux-1-dev-non-commercial-license");
    expect(hit.textContent).toContain("791k");
    // All three numbers, not only the one the list was ordered by: "everybody uses it" and
    // "people are looking at it this week" are different answers to "why is this here".
    expect(hit.textContent).toContain("15k");
    expect(hit.textContent).toContain("316");

    await click(hit.querySelector("button") as HTMLButtonElement);
    // Picking fills the field AND asks what is in that repository: the choice has already been
    // made, so 「調べる」 was a second confirmation of it. Nothing is STARTED — this is the
    // same read-only listing the button ran.
    expect(field("リポジトリ")!.value).toBe("black-forest-labs/FLUX.1-dev");
    expect(host!.querySelector(".engines-search-hits")).toBeNull();
    expect(apiJSON).toHaveBeenCalledTimes(2);
    expect(apiJSON).toHaveBeenLastCalledWith("api/admin/engines/image/ingest/files", "POST", {
      source: { hf: { repo: "black-forest-labs/FLUX.1-dev", file: "", revision: "" } },
    });
  });

  // 🔴 The trap that comes with resolving on the pick: `repo` still holds the PREVIOUS pick in
  // the handler that set the new one, so a request built from the state asks about the model
  // somebody chose a moment ago — with the new name on screen and no error anywhere.
  it("asks about the repository just picked, not the one still in the field", async () => {
    const hits = ["a/first", "b/second"].map((ref) => ({ source: "hf", ref, name: ref }));
    api.mockResolvedValue({ engines: [row()] });
    apiJSON.mockResolvedValue({ hits });
    await mount();
    await openIngest();

    await typeInto(field("探す")!, "x");
    await click(button("検索"));
    await click(host!.querySelector(".engines-search-hits li button") as HTMLButtonElement);
    expect(apiJSON).toHaveBeenLastCalledWith("api/admin/engines/image/ingest/files", "POST", {
      source: { hf: { repo: "a/first", file: "", revision: "" } },
    });

    await click(button("検索"));
    const second = host!.querySelectorAll(".engines-search-hits li")[1];
    await click(second.querySelector("button") as HTMLButtonElement);
    expect(field("リポジトリ")!.value).toBe("b/second");
    expect(apiJSON).toHaveBeenLastCalledWith("api/admin/engines/image/ingest/files", "POST", {
      source: { hf: { repo: "b/second", file: "", revision: "" } },
    });
  });

  // The three kinds of fact on a hit are told apart by kind, not by a "・": twenty results as
  // one line each — name, counts, licence, size at the same weight, wrapping into one another
  // — are a wall of text with nothing to scan by.
  it("splits a hit into a name, the numbers and the terms", async () => {
    api.mockResolvedValue({ engines: [row()] });
    apiJSON.mockResolvedValue({
      hits: [
        {
          source: "hf",
          ref: "stabilityai/stable-diffusion-xl-base-1.0",
          name: "stabilityai/stable-diffusion-xl-base-1.0",
          downloads: 1632949,
          likes: 6612,
          license: "openrail++",
          bytes: 6_939_000_000,
        },
      ],
    });
    await mount();
    await openIngest();
    await click(button("人気を見る"));

    const hit = host!.querySelector(".engines-hit")!;
    expect(hit.querySelector(".engines-hit-name")!.textContent).toBe(
      "stabilityai/stable-diffusion-xl-base-1.0",
    );
    // The counts carry their unit but are not joined to the licence and the size.
    expect(Array.from(hit.querySelectorAll(".engines-hit-stat")).map((s) => s.textContent)).toEqual([
      "1.6MDL",
      "7kいいね",
    ]);
    // …which ride as their own badges, so a card with neither draws no empty row.
    expect(Array.from(hit.querySelectorAll(".engines-hit-tags .engines-model-tag")).map((s) => s.textContent)).toEqual([
      "openrail++",
      "6.9 GB",
    ]);
  });

  it("rounds a fractional trending score instead of printing its float noise", async () => {
    api.mockResolvedValue({ engines: [row()] });
    apiJSON.mockResolvedValue({
      hits: [
        {
          source: "hf",
          ref: "John6666/wai-nsfw-illustrious-v80-sdxl",
          name: "John6666/wai-nsfw-illustrious-v80-sdxl",
          downloads: 1884,
          // What the live API answers — it is a score, not a count.
          trending: 0.7000000000000001,
        },
      ],
    });
    await mount();
    await openIngest();
    await typeInto(field("探す")!, "WAI");
    await click(button("検索"));
    const hit = host!.querySelector(".engines-search-hits li")!;
    expect(hit.textContent).toContain("0.7");
    expect(hit.textContent).not.toContain("0.7000000000000001");
  });

  it("puts a Civitai hit in as its version id, which is what an ingest takes", async () => {
    api.mockResolvedValue({ engines: [row()] });
    apiJSON.mockResolvedValue({
      hits: [{ source: "civitai", ref: "1759168", name: "Juggernaut XL — Ragnarok", base_model: "SDXL 1.0" }],
    });
    await mount();
    await openIngest();
    await click(button("Civitai"));
    await typeInto(field("探す")!, "juggernaut");
    await click(button("検索"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/ingest/search", "POST", {
      q: "juggernaut",
      source: "civitai",
      sort: "downloads",
    });

    await click(host!.querySelector(".engines-search-hits li button") as HTMLButtonElement);
    // 🔴 `civitai:<versionId>`, the form the source parser reads. The model id on the page's
    // URL is a different number and resolves to nothing.
    expect(field("リポジトリ")!.value).toBe("civitai:1759168");
  });

  it("offers Civitai to the image role only", async () => {
    api.mockResolvedValue({ engines: [row({ key: "llm", api: "chat", provider: "llamacpp" })] });
    await mount();
    await openIngest();
    // The CP answers the llm role nothing from Civitai (it hosts image models), so a source
    // switch there is a button that can only disappoint.
    expect(button("Civitai")).toBeUndefined();
    expect(field("探す")).toBeTruthy();
  });

  it("says so instead of leaving the box empty when nothing matches", async () => {
    api.mockResolvedValue({ engines: [row()] });
    apiJSON.mockResolvedValue({ hits: [] });
    await mount();
    await openIngest();
    await typeInto(field("探す")!, "zzzz");
    await click(button("検索"));
    expect(host!.textContent).toContain("見つかりませんでした");
    // And the way in that never needed a search is still there.
    expect(field("リポジトリ")).toBeTruthy();
  });
  it("browses a ranking with no words typed, and the button says so", async () => {
    api.mockResolvedValue({ engines: [row()] });
    apiJSON.mockResolvedValue({
      hits: [{ source: "hf", ref: "stabilityai/sdxl-turbo", name: "stabilityai/sdxl-turbo", downloads: 4176022 }],
    });
    await mount();
    await openIngest();

    // Nothing typed: the button offers the ranking rather than sitting disabled, because
    // "show me what people use" is the only way in for somebody with no name in hand.
    const go = button("人気を見る")!;
    expect(go).toBeTruthy();
    expect(go.disabled).toBe(false);

    await click(button("話題"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/ingest/search", "POST", {
      q: "",
      source: "hf",
      sort: "trending",
    });
    expect(host!.querySelector(".engines-search-hits li")!.textContent).toContain("stabilityai/sdxl-turbo");
    // Pressing a ranking searches at once — it is a question, not a setting that waits for a
    // second click somewhere else.
    expect(apiJSON).toHaveBeenCalledTimes(1);
  });
});

// 🔴 A deployment that has not adopted 60-engines has an EMPTY panel, and "there is nothing
// here" is the worst possible answer to "what could I run?". Looking at what Hugging Face has
// needs no engine at all — no token, no bucket, no task — so the browse stays.
describe("EnginesAdminView / browsing with no engine deployed", () => {
  const button = (label: string) =>
    Array.from(host!.querySelectorAll("button")).find((b) => b.textContent === label) as
      | HTMLButtonElement
      | undefined;

  it("still offers a look at what there is, and says it cannot take anything in", async () => {
    api.mockResolvedValue({ engines: [] });
    apiJSON.mockResolvedValue({
      hits: [{ source: "hf", ref: "unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF", name: "unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF", downloads: 12623435 }],
    });
    await mount();
    // The "nothing deployed" sentence stays — the browse is added beside it, not instead of it.
    expect(host!.textContent).toContain("動かしていません");

    await click(button("人気を見る"));
    // The keyless route, with the kind stated rather than derived: there is no engine to
    // derive it from.
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/search?kind=gguf", "POST", {
      q: "",
      source: "hf",
      sort: "downloads",
    });
    expect(host!.querySelector(".engines-search-hits")!.textContent).toContain("Qwen3-Coder-30B");
    expect(host!.textContent).toContain("12.6M");
    // 🔴 A hit is NOT clickable here: picking one fills an ingest form, and this deployment has
    // no role to ingest into. The note says so instead of offering a button that cannot work.
    expect(host!.querySelector(".engines-search-hits li button")).toBeNull();
    expect(host!.textContent).toContain("閲覧だけです");
  });

  it("asks for the kind, because there is no engine to derive it from", async () => {
    api.mockResolvedValue({ engines: [] });
    apiJSON.mockResolvedValue({ hits: [] });
    await mount();
    await click(button("画像（checkpoint）"));
    await click(button("人気を見る"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/search?kind=checkpoint", "POST", {
      q: "",
      source: "hf",
      sort: "downloads",
    });

    // Civitai is offered here too, and only for checkpoints.
    await click(button("Civitai"));
    await click(button("人気を見る"));
    expect(apiJSON).toHaveBeenLastCalledWith("api/admin/engines/search?kind=checkpoint", "POST", {
      q: "",
      source: "civitai",
      sort: "downloads",
    });
    // Going back to GGUF takes the source with it: Civitai hosts no GGUFs, and a search that
    // can only answer nothing reads as a broken one.
    await click(button("LLM（GGUF）"));
    expect(button("Civitai")).toBeUndefined();
    await click(button("人気を見る"));
    expect(apiJSON).toHaveBeenLastCalledWith("api/admin/engines/search?kind=gguf", "POST", {
      q: "",
      source: "hf",
      sort: "downloads",
    });
  });
});
