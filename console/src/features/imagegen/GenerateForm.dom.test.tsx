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

const SDXL: ImagegenModel = {
  id: "sdxl-base",
  family: "sdxl",
  knobs: ["steps", "cfg", "sampler", "scheduler", "negative", "strength"],
  ops: ["generate", "edit", "inpaint"],
};
// flux1 reads neither cfg nor a negative prompt; klein reads no scheduler either.
const FLUX: ImagegenModel = { id: "flux1-dev", family: "flux1", knobs: ["steps", "sampler", "scheduler", "strength"] };
const OLD: ImagegenModel = { id: "legacy", family: "sdxl" };
// ADR 0094: edit-only, no strength, no size candidates.
const QWEN_EDIT: ImagegenModel = {
  id: "qwen-edit-row",
  family: "qwen-image-edit-2509",
  knobs: ["steps", "cfg", "sampler", "scheduler", "negative"],
  ops: ["edit"],
  sizes: [],
};

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

  // 選ぶ前の「何が付いてくるか」。チップは選んだ後の答えで、選ぶかどうかを決めている最中には
  // 何も答えていなかった。ticked になったらチップ側が同じ語を押せる形で持つので、行からは消す
  // （押せない同じ一覧が 2 つ並ぶのは 1 つより悪い）。
  it("選ぶ前に行へ出て、選んだらチップに入れ替わる", async () => {
    await render(SDXL);
    const rowOf = (name: string) =>
      [...host.querySelectorAll(".igen-lora")].find((r) => r.querySelector(".igen-lora-pick")?.textContent?.includes(name))!;
    expect(rowOf("neon").querySelector(".igen-lora-trigger")?.textContent).toContain("neon_glow");
    expect(rowOf("neon").querySelector(".igen-lora-trigger")?.textContent).toContain("night");
    await tick("neon");
    expect(rowOf("neon").querySelector(".igen-lora-trigger")).toBeNull();
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

  // ADR 0094 決定 4: sizes が空の族は disabled ではなく、欄そのものを描かない。
  it("qwen-image-edit-2509 は sizes が空なので欄ごと出ない", async () => {
    await render(QWEN_EDIT, { op: "edit" });
    expect(fieldByLabel("大きさ") ?? fieldByLabel("Size")).toBeNull();
  });
});

// ADR 0094 decision 12: the op selector and the strength slider both read the AGENT's per-model
// word (`ops`, `knobs`), the same rule the other knobs already follow — absent means an old
// Agent and the form stays fully usable.
describe("ADR 0094: 編集専用モデルの op と strength", () => {
  const opOptions = (): string[] => {
    const select = [...host.querySelectorAll("select")].find((s) =>
      [...s.options].some((o) => o.value === "generate" || o.value === "edit"),
    ) as HTMLSelectElement;
    return [...select.options].map((o) => o.value);
  };

  it("model.ops が edit だけなら選択肢も edit だけ", async () => {
    await render(QWEN_EDIT, { op: "edit" });
    expect(opOptions()).toEqual(["edit"]);
  });

  it("model.ops が無ければ（古い Agent）3 つとも選べる", async () => {
    await render(OLD);
    expect(opOptions()).toEqual(["generate", "edit", "inpaint"]);
  });

  it("model.ops のある行では 3 つとも選べる（この行は generate/edit/inpaint すべて対応）", async () => {
    await render(SDXL);
    expect(opOptions()).toEqual(["generate", "edit", "inpaint"]);
  });

  it("strength を読まない族はスライダーごと出ない", async () => {
    await render(QWEN_EDIT, { op: "edit" });
    expect(host.textContent).not.toContain("元画像をどれだけ変えるか");
    expect(host.textContent).not.toContain("How much of the input to change");
  });

  it("strength を読む族はスライダーが出る（陽性対照）", async () => {
    await render(SDXL, { op: "edit" });
    const label = fieldByLabel("元画像をどれだけ変えるか") ?? fieldByLabel("How much of the input to change");
    expect(label).not.toBeNull();
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

// 🔴 The option's TEXT is the name and its VALUE is the id (ADR 0090). A member never sees an S3
// key, so the id buys them nothing — and after ADR 0089 a catalogue routinely holds two sizes of
// one model, whose ids differ by a few characters and say nothing about which is which. The value
// must not move: it is what the generation names.
describe("モデルの選択肢 (ADR 0090)", () => {
  const NAMED: ImagegenModel = { ...SDXL, id: "qwen-image_q8", label: "unsloth/Qwen-Image-GGUF Q8_0" };

  it("draws the label and still sends the id", async () => {
    await render(NAMED);
    const option = document.querySelector<HTMLOptionElement>(`option[value="${NAMED.id}"]`)!;
    expect(option).toBeTruthy();
    expect(option.textContent).toBe("unsloth/Qwen-Image-GGUF Q8_0");
    expect(option.value).toBe("qwen-image_q8");
  });

  it("falls back to the id for a model the catalogue composed no name for", async () => {
    await render(SDXL);
    expect(document.querySelector<HTMLOptionElement>(`option[value="${SDXL.id}"]`)!.textContent).toBe("sdxl-base");
  });
});
