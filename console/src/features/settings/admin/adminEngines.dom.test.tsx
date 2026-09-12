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

import { EnginesAdminView, engineOfferResultKey } from "./adminEngines.tsx";

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

// The five results an attempt can end in (ADR 0075 decision 5), and the one thing the table must
// not do: swallow a code it does not know. The CP derives these from ECS service-event STRINGS, so
// AWS rewording a message ADDS a value here — and the raw word is then the only clue the next
// person has that the matching stopped working.
describe("engineOfferResultKey", () => {
  it("names each measured result and leaves an unknown one to be printed verbatim", () => {
    expect(engineOfferResultKey("active")).toBe("admin.engines_offer_result_active");
    expect(engineOfferResultKey("unfulfillable")).toBe("admin.engines_offer_result_unfulfillable");
    expect(engineOfferResultKey("insufficient")).toBe("admin.engines_offer_result_insufficient");
    expect(engineOfferResultKey("quota")).toBe("admin.engines_offer_result_quota");
    expect(engineOfferResultKey("budget")).toBe("admin.engines_offer_result_budget");
    expect(engineOfferResultKey("unusable")).toBe("admin.engines_offer_result_unusable");
    expect(engineOfferResultKey("interrupted")).toBe("admin.engines_offer_result_interrupted");
    // The two that are waited on differently must not collapse into one wording (decision 5).
    expect(engineOfferResultKey("unfulfillable")).not.toBe(engineOfferResultKey("insufficient"));
    expect(engineOfferResultKey("something_new")).toBe("");
    expect(engineOfferResultKey(undefined)).toBe("");
  });

});

describe("EnginesAdminView", () => {
  it("switches an engine off through the per-engine route", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [row()] });
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
    api.mockResolvedValue({ super_admin: true, engines: [row({ mode: "off", enabled: false, state: "stopping" })] });
    await mount();
    expect(seg("無効")?.className).toContain("active");
    expect(seg("常時稼働")?.className).not.toContain("active");
  });

  it("warns about the bill only while an engine is pinned on, and names no price of its own", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [row({ mode: "ondemand" })] });
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
    api.mockResolvedValue({ super_admin: true, engines: [row()] });
    await mount();
    // Decision 3: a deployment that declares none must not see a control that does nothing.
    expect(host!.querySelector(".engines-class")).toBeNull();
  });

  it("shows the rungs with their VRAM, and a price only where one was declared", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [withClasses()] });
    await mount();
    const opts = Array.from(host!.querySelectorAll(".engines-class option")).map((o) => o.textContent);
    expect(opts[0]).toContain("L4 24GB");
    expect(opts[0]).toContain("$1.26/h");
    // 🔴 The rung the operator left without a price prints none, rather than $0.
    expect(opts[1]).not.toContain("$");
  });

  it("says permanently when the engine is not on the deployment's default rung", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [withClasses({ class_is_default: false, class: { id: "l40s", label: "L40S 48GB", vram_mib: 44000, types: ["g6e.xlarge"] } })],
    });
    await mount();
    // The sentence that keeps "temporarily try a bigger box" from becoming a permanent bill.
    expect(host!.textContent).toContain("既定と違います");
    expect(host!.textContent).toContain("既定に戻す");
  });

  it("says the saved class has not reached the running box, and what replacing costs", async () => {
    api.mockResolvedValue({
      super_admin: true,
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

  // 🔴 A rung that was SAVED but not APPLIED must be recoverable from this screen.
  //
  // The CP stores the choice before it writes it to the capacity provider (deliberately — that
  // is what lets the panel say the card was not applied), so after a refusal the picker already
  // shows the rung nothing was written for, and re-picking it fires no change event at all.
  // Until this button existed the only recovery was a detour through another rung (ADR 0074,
  // 直さなかったが分かっていること).
  it("offers a retry when the class was saved but could not be applied", async () => {
    let served: Record<string, unknown> = withClasses();
    api.mockImplementation(() => Promise.resolve({ super_admin: true, engines: [served] }));
    await mount();

    // What the CP holds after the refusal: the new rung stored, and why it did not land.
    served = withClasses({
      class: { id: "l40s", label: "L40S 48GB", vram_mib: 44000, types: ["g6e.xlarge"] },
      class_is_default: false,
      class_apply_error: "AccessDeniedException: ecs:UpdateCapacityProvider",
    });
    apiJSON.mockResolvedValue({
      error: { code: "engine_ecs_error", message: "AccessDeniedException: ecs:UpdateCapacityProvider" },
    });
    const sel = () => host!.querySelector(".engines-class select") as HTMLSelectElement;
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
      setter.call(sel(), "l40s");
      sel().dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/class", "PUT", { class: "l40s" });

    // The refusal is RE-READ rather than assumed to have changed nothing: the picker shows what
    // the CP stored, not the rung that was on screen before the press.
    expect(sel().value).toBe("l40s");
    // In the provider's own words — a missing IAM grant and a throttle need different things
    // from whoever is reading this.
    expect(host!.textContent).toContain("キャパシティプロバイダへの書き込みに失敗");
    expect(host!.textContent).toContain("ecs:UpdateCapacityProvider");

    // And the retry sends the rung the select can no longer produce a change event for.
    apiJSON.mockClear();
    apiJSON.mockResolvedValue(served);
    await click(
      Array.from(host!.querySelectorAll(".engines-class button")).find(
        (b) => b.textContent === "もう一度適用する",
      ) as HTMLElement,
    );
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/class", "PUT", { class: "l40s" });
  });

  it("says nothing about applying when the Control Plane reports no failure", async () => {
    // Absence is "no claim", not "it was applied": the CP's note is in memory, so a restarted
    // one has nothing to say and must not be drawn as either verdict.
    api.mockResolvedValue({ super_admin: true, engines: [withClasses()] });
    await mount();
    expect(host!.textContent).not.toContain("もう一度適用する");
  });

  // --- the purchase offers (ADR 0075) ---------------------------------------------------
  //
  // Contract B of the ADR's "実装の分け方（P0）": `offers`, `offer` and `offer_trail` ride on the
  // same row, `class` / `classes` / `class_default` / `class_is_default` are untouched, and one
  // meaning moves — `class_is_default: false` means PINNED where offers arrive (decision 8).
  //
  // 🔴 Every assertion below is paired with the no-offers case, because this contract lands
  // before the control plane that serves it: the ADR 0074 screen has to come back pixel for
  // pixel from a CP that sends none of the three fields.

  // Declared DEAREST FIRST on purpose. The operator's convention is cheap-first, but the order is
  // theirs and the try order is the declared one — a panel that sorted by price would put the
  // $0.67 Spot row at the top and quietly overrule the sequence they wrote (decision 1).
  const OFFERS = [
    { id: "l4", label: "L4 24GB", vram_mib: 21000, types: ["g6.xlarge"], usd_per_hour: 1.26, buy: "od" },
    { id: "l4-spot", label: "L4 24GB", vram_mib: 21000, types: ["g6.xlarge"], usd_per_hour: 0.67, buy: "spot" },
    { id: "l40s", label: "L40S 48GB", vram_mib: 44000, types: ["g6e.xlarge"], buy: "od" },
  ];
  const withOffers = (over: Record<string, unknown> = {}) => withClasses({ offers: OFFERS, ...over });
  const offerRows = () => Array.from(host!.querySelectorAll(".engines-offers li"));

  it("draws the ADR 0074 ladder unchanged when the Control Plane sends no offers", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [withClasses({ class_is_default: false })] });
    await mount();
    // No offers list, no current-offer line, no automatic entry — and the badge keeps the ADR
    // 0074 wording, because without offers there is nothing to be pinned against.
    expect(host!.querySelector(".engines-offers")).toBeNull();
    expect(host!.querySelector(".engines-offer-now")).toBeNull();
    const opts = Array.from(host!.querySelectorAll(".engines-class option")).map((o) => o.textContent);
    expect(opts).toHaveLength(2);
    expect(opts.join("|")).not.toContain("自動");
    expect(host!.textContent).toContain("既定と違います");
    expect(host!.textContent).not.toContain("固定されています");
    // The purchase form is not invented for a rung that never carried one.
    expect(opts.join("|")).not.toContain("オンデマンド");

    // The positive control: the same row with the three fields draws all of it.
    await act(async () => root!.unmount());
    api.mockResolvedValue({
      super_admin: true,
      engines: [withOffers({ offer: { id: "l4-spot", buy: "spot" } })],
    });
    await mount();
    expect(host!.querySelector(".engines-offers")).not.toBeNull();
    expect(host!.querySelector(".engines-offer-now")).not.toBeNull();
    expect(host!.textContent).toContain("自動（既定）");
  });

  it("lists the offers in the order they were declared, with the purchase form and declared price", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [withOffers()] });
    await mount();
    const rows = offerRows().map((li) => li.textContent || "");
    expect(rows).toHaveLength(3);
    // 🔴 Declaration order, not price order: the $1.26 on-demand row was declared first and stays
    // first above the $0.67 Spot row.
    expect(rows[0]).toContain("オンデマンド");
    expect(rows[0]).toContain("$1.26/h");
    expect(rows[1]).toContain("Spot");
    expect(rows[1]).toContain("$0.67/h");
    // The offer whose price the operator left out prints none, rather than $0 — the same rule the
    // rungs have (ADR 0074), and here it matters more: a Spot row's billed price starts out as
    // "not measured yet" by definition.
    expect(rows[2]).toContain("L40S 48GB");
    expect(rows[2]).not.toContain("$");
    expect(rows[2]).toContain("44000");
  });

  it("says which offer is answering, and only when the Control Plane said so", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [withOffers()] });
    await mount();
    // Decision 11 reads it from the service's strategy; a CP that has not looked yet omits the
    // field, and a panel that guessed "the first one" would be wrong exactly when rule 2 fell
    // through — the case this line exists for.
    expect(host!.querySelector(".engines-offer-now")).toBeNull();

    await act(async () => root!.unmount());
    api.mockResolvedValue({
      super_admin: true,
      engines: [withOffers({ offer: { id: "l4-spot", buy: "spot" } })],
    });
    await mount();
    const now = host!.querySelector(".engines-offer-now")!.textContent || "";
    expect(now).toContain("いまの提案");
    expect(now).toContain("L4 24GB");
    expect(now).toContain("Spot");
    // Marked on the row it is, rather than by moving it to the top: that it is the SECOND offer
    // is the fact worth seeing.
    expect(offerRows()[1].className).toContain("on");
    expect(offerRows()[0].className).not.toContain("on");
  });

  it("shows how far down the list this demand walked, and why each offer was left", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [
        withOffers({
          offer: { id: "l4", buy: "od" },
          offer_trail: [
            { id: "l4-spot", buy: "spot", result: "unfulfillable" },
            { id: "l40s", buy: "od", result: "insufficient" },
            { id: "l4", buy: "od", result: "active" },
            // A code this Console does not know is printed as it came: the CP reads these out of
            // ECS event strings, so a new one is exactly what nobody would otherwise see.
            { id: "l4", buy: "od", result: "something_new" },
          ],
        }),
      ],
    });
    await mount();
    const trail = host!.querySelector(".engines-offer-trail")!.textContent || "";
    expect(trail).toContain("試した順");
    expect(trail).toContain("要求の設定で買えず");
    expect(trail).toContain("在庫なし");
    expect(trail).toContain("取れた");
    expect(trail).toContain("something_new");
    expect(host!.querySelectorAll(".engines-offer-try")).toHaveLength(4);
  });

  it("says nothing about a trail that has one entry or none", async () => {
    // One entry is "it was bought on the first offer", which the line above already says — and a
    // trail of one reads as a fallback that did not happen.
    api.mockResolvedValue({
      super_admin: true,
      engines: [withOffers({ offer_trail: [{ id: "l4", buy: "od", result: "active" }] })],
    });
    await mount();
    expect(host!.querySelector(".engines-offer-trail")).toBeNull();
  });

  // 🔴 Decision 8: not choosing IS the choice, and it has to be reachable. Without an entry of its
  // own the only way back from a pin would be to select the offer that happens to be the default,
  // which is a different thing entirely — it would still refuse to fall through to the next one.
  it("offers automatic as the first choice and unpins by storing an empty class", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [withOffers({ class_is_default: true })] });
    await mount();
    const sel = () => host!.querySelector(".engines-class select") as HTMLSelectElement;
    const opts = Array.from(host!.querySelectorAll(".engines-class option"));
    expect(opts[0].textContent).toBe("自動（既定）");
    expect((opts[0] as HTMLOptionElement).value).toBe("");
    expect(opts).toHaveLength(4);
    // Automatic is what the picker sits on while nothing is pinned — `class` then names the CP's
    // own pick, and showing that as the administrator's choice would turn a start into a pin.
    expect(sel().value).toBe("");
    expect(host!.textContent).not.toContain("固定されています");

    // Pinning one sends its id.
    apiJSON.mockResolvedValue(withOffers({ class_is_default: false, class: OFFERS[2] }));
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
      setter.call(sel(), "l40s");
      sel().dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/class", "PUT", { class: "l40s" });
  });

  it("states a pin permanently and takes it off in one click", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [withOffers({ class_is_default: false, class: OFFERS[2] })],
    });
    await mount();
    // The ADR 0074 decision 7 badge, re-worded: what a pinned role gives up is the fall-through,
    // not the default rung.
    expect(host!.textContent).toContain("自動ではなく固定されています");
    expect(host!.textContent).not.toContain("既定と違います");
    expect((host!.querySelector(".engines-class select") as HTMLSelectElement).value).toBe("l40s");

    apiJSON.mockResolvedValue(withOffers({ class_is_default: true }));
    await click(
      Array.from(host!.querySelectorAll(".engines-class button")).find(
        (b) => b.textContent === "自動に戻す",
      ) as HTMLElement,
    );
    // Empty, not the default offer's id: the CP reads "" as automatic (decision 8), and sending
    // the default's id would leave the role pinned to it.
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/class", "PUT", { class: "" });
  });

  // ⚠️ The comparison is a MAXIMUM, not a sum, and on comfy that is conservative rather than
  // exact: a checkpoint is chosen per request and loaded ones stay cached, so several can be
  // resident. Said in a sentence — turning the figure into a sum would warn on every start of a
  // deployment with four enabled models, which is the warning nobody reads.
  it("says a per-request engine may hold several models, and says it only there", async () => {
    const enabled = { model_rows: [{ id: "sdxl", enabled: true, vram_need_mib: 7000, vram_need_source: "declared" }], vram_need_mib: 7000, vram_need_source: "declared", vram_need_model: "sdxl", vram_fits: true };
    api.mockResolvedValue({ super_admin: true, engines: [withClasses({ provider: "comfy", ...enabled })] });
    await mount();
    expect(host!.querySelector(".engines-class")!.textContent).toContain("複数が同時に載る");

    await act(async () => root!.unmount());
    api.mockResolvedValue({ super_admin: true, engines: [withClasses({ provider: "sdcpp", ...enabled })] });
    await mount();
    // sd-server holds ONE checkpoint chosen at start, so the sentence would be false there.
    expect(host!.querySelector(".engines-class")!.textContent).not.toContain("複数が同時に載る");
  });

  it("says so rather than showing an empty screen when nothing is deployed", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [] });
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
      super_admin: true,
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
      super_admin: true,
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
      super_admin: true,
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
      super_admin: true,
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
      super_admin: true,
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
    api.mockResolvedValue({ super_admin: true, engines: [row(), row({ key: "llm" })] });
    await mount();
    const called = api.mock.calls.map((c) => String(c[0]));
    // The heatmap is a 14-day query per engine and nothing on screen shows it yet.
    expect(called.filter((p) => p.includes("/hourly"))).toEqual([]);
    // 🔴 And ONE read in total, whatever the deployment runs. Since the panel was split, the
    // ingest lists and the token status are the other screen's reads — this one used to make
    // four calls on mount for two engines, three of which were about models it no longer draws.
    expect(called.sort()).toEqual(["api/admin/engines"]);
  });

  it("lists every engine, each with its own control", async () => {
    api.mockResolvedValue({
      super_admin: true,
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

  // ADR 0072 P1. A router role holds ONE model at a time, so two sessions on two models take
  // turns and every turn costs an unload plus 267 s of weights. The panel is where that price is
  // stated: "warm" alone describes a swapping engine and a settled one identically.
  it("names the model in VRAM and how often it has changed", async () => {
    api.mockResolvedValue({
      super_admin: true,
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
    // What each model costs the next cold start is a fact about the CATALOGUE and is asserted
    // on the models screen. Here it must be ABSENT: this screen draws no rows.
    expect(host!.textContent).not.toContain("同期 +179 秒（推定）");
  });

});

// The engine-wide exclusion list (ADR 0072 follow-up, negative prompts): one text box on the
// machine panel, applied to every image this engine makes. A draft with an explicit save — every
// keystroke would otherwise be a PUT that fans out to every running workspace, and a half-typed
// exclusion list excludes the wrong thing.
describe("EnginesAdminView / the exclusion list", () => {
  const button = (label: string) =>
    Array.from(host!.querySelectorAll("button")).find((b) => b.textContent === label) as
      | HTMLButtonElement
      | undefined;
  const typeInto = async (el: Element, v: string) => {
    await act(async () => {
      const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
      setter.call(el, v);
      el.dispatchEvent(new Event("input", { bubbles: true }));
    });
  };

  it("saves it, and says beside it that it is not a filter", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [row({ provider: "comfy", negative_always: "", negative_max: 500 })],
    });
    await mount();

    // 🔴 The note is part of the control. Without it a box called "excluded from every image"
    // reads as a content filter, which this is not: it is a negative prompt, and three of the
    // five checkpoint families sample where it cannot matter at all.
    const note = host!.querySelector(".engines-negative .muted")!;
    expect(note.textContent).toContain("フィルタではなく誘導");

    // Nothing typed yet, so there is nothing to save: the button is not a no-op waiting to be
    // pressed.
    expect(button("保存")!.disabled).toBe(true);
    await typeInto(host!.querySelector(".engines-negative input")!, "explicit, gore");
    apiJSON.mockResolvedValue(row({ negative_always: "explicit, gore" }));
    await click(button("保存"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/negative", "PUT", {
      negative: "explicit, gore",
    });
  });

  // A chat engine has nothing to exclude, and the CP says so by omitting the field. Drawing the
  // box anyway would offer a setting that reaches nothing.
  it("is absent on an engine that makes no images", async () => {
    api.mockResolvedValue({
      super_admin: true,
      engines: [row({ key: "llm", api: "chat", provider: "llamacpp" })],
    });
    await mount();
    expect(host!.querySelector(".engines-negative")).toBeNull();
  });
});
