// The two rules of ADR 0081 that only a rendered form can prove.
//
//  1. **What a family does not read is disabled, from the AGENT's word** (decision 4). The
//     table lives in the Agent; the Console must never grow a second one, so the test drives
//     `knobs` and nothing else. An ABSENT `knobs` is an old Agent and must leave the form
//     usable — greying everything out on an old workspace is the failure this direction
//     avoids.
//  2. **Trigger chips follow the LoRA selection** (decision 7). They are derived from what is
//     ticked, so unticking a LoRA takes away the words it brought and nothing else — the
//     failure being guarded is a stored chip list that outlives its LoRA.
import { afterEach, describe, expect, it } from "vitest";
import { useState } from "react";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { GenerateForm } from "./parts/GenerateForm.tsx";
import { emptyDraft, type ImagegenDraft } from "./draft.ts";
import { resolveFleetProvider, type ImagegenLora, type ImagegenModel, type ImagegenProvider } from "./wire.ts";

let host: HTMLDivElement;
let root: Root;

const SDXL: ImagegenModel = { id: "sdxl-base", family: "sdxl", knobs: ["steps", "cfg", "sampler", "scheduler", "negative"] };
// flux1 reads neither cfg nor a negative prompt; klein reads no scheduler either.
const FLUX: ImagegenModel = { id: "flux1-dev", family: "flux1", knobs: ["steps", "sampler", "scheduler"] };
const OLD: ImagegenModel = { id: "legacy", family: "sdxl" };

const LORAS: ImagegenLora[] = [
  { name: "detail", baseModel: "sdxl", trained_words: ["add_detail"], weight: 0.7 },
  { name: "neon", baseModel: "sdxl", trained_words: ["neon_glow", "night"] },
];

function Harness({
  model,
  initial,
  fleetProviders = [],
}: {
  model: ImagegenModel;
  initial?: Partial<ImagegenDraft>;
  fleetProviders?: ImagegenProvider[];
}) {
  const [draft, setDraft] = useState<ImagegenDraft>({ ...emptyDraft(), model: model.id, ...initial });
  return (
    <GenerateForm
      draft={draft}
      patch={(p) => setDraft((d) => ({ ...d, ...p }))}
      fleetProviders={fleetProviders}
      provider={resolveFleetProvider(fleetProviders, draft.providerId)}
      models={[model]}
      loras={LORAS}
      model={model}
      samplers={["euler", "dpmpp_2m"]}
      schedulers={["simple", "karras"]}
      loraWeightMax={2}
      alwaysNegative="nsfw"
      busy={false}
      trialFull={false}
      queueFull={false}
      onTrial={() => {}}
      onEnqueue={() => {}}
      onPromptHelp={() => {}}
    />
  );
}

const render = async (model: ImagegenModel, initial?: Partial<ImagegenDraft>, fleetProviders?: ImagegenProvider[]) => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(<Harness model={model} initial={initial} fleetProviders={fleetProviders} />);
  });
};

/** A field is located by the knob's own label text, not by index: the grid's order is
 *  layout, and pinning it here would make every future field a failing test. */
const fieldByLabel = (label: string): HTMLElement | null => {
  for (const l of host.querySelectorAll("label.igen-field")) {
    if (l.querySelector(".igen-label")?.textContent?.trim() === label) {
      return l.querySelector("input, select, textarea");
    }
  }
  return null;
};

const chips = (): string[] => [...host.querySelectorAll(".igen-triggers .igen-chip")].map((c) => c.textContent!.trim());

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.innerHTML = "";
});

describe("knobs に無い欄", () => {
  it("sdxl は 4 つとも触れる", async () => {
    await render(SDXL);
    for (const k of ["steps", "cfg", "sampler", "scheduler"]) {
      expect((fieldByLabel(k) as HTMLInputElement).disabled, k).toBe(false);
    }
    expect((host.querySelector(".igen-negative") as HTMLTextAreaElement).disabled).toBe(false);
  });

  it("flux1 は cfg とネガティブが無効で、理由が出る", async () => {
    await render(FLUX);
    expect((fieldByLabel("cfg") as HTMLInputElement).disabled).toBe(true);
    expect((fieldByLabel("steps") as HTMLInputElement).disabled).toBe(false);
    expect((host.querySelector(".igen-negative") as HTMLTextAreaElement).disabled).toBe(true);
    expect(host.textContent).toContain("flux1");
  });

  it("knobs を送らない古い Agent では全部使える（画面を殺さない）", async () => {
    await render(OLD);
    expect((fieldByLabel("cfg") as HTMLInputElement).disabled).toBe(false);
    expect((host.querySelector(".igen-negative") as HTMLTextAreaElement).disabled).toBe(false);
  });
});

describe("管理者のネガティブ", () => {
  it("固定のチップとして出て、テキスト欄には混ざらない", async () => {
    const withRow = { ...SDXL, negative: "worst quality" };
    await render(withRow);
    const fixed = [...host.querySelectorAll(".igen-fixed .igen-chip")].map((c) => c.textContent!.trim());
    expect(fixed.join(" ")).toContain("worst quality");
    expect(fixed.join(" ")).toContain("nsfw");
    expect((host.querySelector(".igen-negative") as HTMLTextAreaElement).value).toBe("");
  });
});

describe("トリガー語のチップ", () => {
  const tick = async (name: string) => {
    const box = [...host.querySelectorAll<HTMLInputElement>(".igen-lora-pick input")].find(
      (b) => b.parentElement?.textContent?.includes(name),
    )!;
    await act(async () => {
      box.click();
    });
  };

  it("選ぶと出て、外すとその LoRA の分だけ消える", async () => {
    await render(SDXL);
    expect(chips()).toEqual([]);
    await tick("detail");
    expect(chips()).toEqual(["add_detail"]);
    await tick("neon");
    expect(chips().sort()).toEqual(["add_detail", "neon_glow", "night"]);
    await tick("detail");
    expect(chips().sort()).toEqual(["neon_glow", "night"]);
  });

  it("チップは押されるまでプロンプトへ入らない（勝手に足さない）", async () => {
    await render(SDXL);
    await tick("detail");
    const box = host.querySelector(".igen-prompt") as HTMLTextAreaElement;
    expect(box.value).toBe("");
    await act(async () => {
      (host.querySelector(".igen-triggers .igen-chip") as HTMLButtonElement).click();
    });
    expect((host.querySelector(".igen-prompt") as HTMLTextAreaElement).value).toBe("add_detail");
  });
});

describe("編集のときの大きさ", () => {
  it("元画像の寸法が勝つので無効になり、理由が出る", async () => {
    await render(SDXL, { op: "edit" });
    const size = fieldByLabel("大きさ") ?? fieldByLabel("Size");
    expect((size as HTMLSelectElement).disabled).toBe(true);
  });
});

// ADR 0082 P1, unresolved question 2: two fleet rows must be choosable, not silently collapsed
// into "the first one" — but one row is not a choice, so the picker must not clutter the form
// on every ordinary, single-engine deployment (which is most of them).
describe("fleet が複数あるときの選択", () => {
  const IMAGE: ImagegenProvider = { id: "image", fleet: true, kind: "comfy" };
  const LAN: ImagegenProvider = { id: "comfy-lan", fleet: true, kind: "comfy" };

  const providerField = () => fieldByLabel("エンジン") ?? fieldByLabel("Engine");

  it("1 本しか無ければピッカーごと出さない", async () => {
    await render(SDXL, {}, [IMAGE]);
    expect(providerField()).toBeNull();
  });

  it("0 本でも出さない", async () => {
    await render(SDXL, {}, []);
    expect(providerField()).toBeNull();
  });

  it("2 本あれば選べる。値はそれぞれの行のキー", async () => {
    await render(SDXL, {}, [IMAGE, LAN]);
    const select = providerField() as HTMLSelectElement;
    expect(select).not.toBeNull();
    const values = [...select.options].map((o) => o.value);
    expect(values).toEqual(["image", "comfy-lan"]);
  });

  it("何も選んでいなければ最初の行が既定（今までと同じ挙動）", async () => {
    await render(SDXL, {}, [IMAGE, LAN]);
    expect((providerField() as HTMLSelectElement).value).toBe("image");
  });

  it("選ぶと draft.providerId に積まれ、選択が反映される", async () => {
    let seen: ImagegenDraft | null = null;
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    function Probe() {
      const [draft, setDraft] = useState<ImagegenDraft>({ ...emptyDraft(), model: SDXL.id });
      seen = draft;
      return (
        <GenerateForm
          draft={draft}
          patch={(p) => setDraft((d) => ({ ...d, ...p }))}
          fleetProviders={[IMAGE, LAN]}
          provider={resolveFleetProvider([IMAGE, LAN], draft.providerId)}
          models={[SDXL]}
          loras={[]}
          model={SDXL}
          samplers={[]}
          schedulers={[]}
          loraWeightMax={2}
          alwaysNegative=""
          busy={false}
          trialFull={false}
          queueFull={false}
          onTrial={() => {}}
          onEnqueue={() => {}}
          onPromptHelp={() => {}}
        />
      );
    }
    await act(async () => {
      root.render(<Probe />);
    });
    const select = providerField() as HTMLSelectElement;
    await act(async () => {
      select.value = "comfy-lan";
      select.dispatchEvent(new Event("change", { bubbles: true }));
    });
    expect(seen!.providerId).toBe("comfy-lan");
    expect((providerField() as HTMLSelectElement).value).toBe("comfy-lan");
  });

  // A row a stored draft named that no longer exists (removed, or a different deployment's
  // draft) must read the same as "no choice" — the first ready row — never as an error.
  it("消えた行を選んでいた draft は最初の行に読み替わる", async () => {
    await render(SDXL, { providerId: "gone" }, [IMAGE, LAN]);
    expect((providerField() as HTMLSelectElement).value).toBe("image");
  });
});
