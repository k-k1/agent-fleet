import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { EngineAddView, savedObjectsForHit } from "./adminEngineAdd.tsx";
import { clearCatalogMemory } from "./catalogMemory.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const imageRow = {
  key: "image",
  api: "images",
  provider: "sdcpp",
  managed: true,
  file_flags: ["", "--vae", "--t5xxl"],
  base_models: ["sdxl"],
  model_rows: [{ id: "existing", enabled: true, file_rows: [{ s3Key: "image/checkpoints/existing.safetensors" }] }],
};
const llmRow = { key: "llm", api: "chat", provider: "llamacpp", managed: true, model_rows: [] };

/** The ledger route answers both tabs now (ADR 0085 decision 2): `GET …/storage` and
 *  `GET …/ingest` are no longer read by this screen, and a mock that still answers them would
 *  hide a call that went to the old route. */
function mockEngines(rows: Record<string, unknown>[], objects: Record<string, unknown>[] = []) {
  api.mockImplementation((path: string) => {
    if (path === "api/admin/engines") return Promise.resolve({ super_admin: true, engines: rows });
    if (path.endsWith("/objects")) return Promise.resolve({ objects, checked_at: "2026-09-15T03:42:00Z" });
    return Promise.resolve({});
  });
}

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => { root!.render(<EngineAddView engineKey="image" lora={false} />); });
  for (const _ of [0, 1, 2]) await act(async () => { await Promise.resolve(); });
}

async function mountLora() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => { root!.render(<EngineAddView engineKey="image" lora />); });
  for (const _ of [0, 1, 2]) await act(async () => { await Promise.resolve(); });
}

async function mountRegistered(engineKey = "image") {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => { root!.render(<EngineAddView engineKey={engineKey} lora={false} initialView="registered" />); });
  for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });
}

const button = (label: string) => Array.from(document.querySelectorAll<HTMLButtonElement>("button"))
  .find((candidate) => candidate.textContent === label);
const labelled = (label: string) => document.querySelector<HTMLButtonElement>(`button[aria-label="${label}"]`) || undefined;
/** Scoped to one dialog. Unscoped, 消す and 揃える both match the button on the row BEHIND the
 *  modal first, so the press lands on the screen nobody is looking at. */
const within = (selector: string, label: string) => Array.from(document.querySelectorAll<HTMLButtonElement>(`${selector} button`))
  .find((candidate) => candidate.textContent === label);
const click = async (element: HTMLElement | undefined) => {
  expect(element).toBeTruthy();
  await act(async () => { element!.dispatchEvent(new MouseEvent("click", { bubbles: true })); });
  await act(async () => { await Promise.resolve(); });
};
const acceptLicence = async () => {
  const licence = Array.from(document.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'))
    .find((input) => input.parentElement?.textContent?.includes("ライセンス"))!;
  expect(licence).toBeTruthy();
  await act(async () => { licence.click(); });
};

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  // 🔴 The catalogue's memory is module scope and every mount here shares one key
  //    (no paneId), so without this a case reads the previous one's page and the
  //    search it asserts is never sent.
  clearCatalogMemory();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
});

describe("model catalogue pane", () => {
  it("opens image on Civitai new arrivals and switches LLM to Hugging Face updated", async () => {
    mockEngines([imageRow, llmRow]);
    apiJSON.mockResolvedValue({ hits: [] });
    await mount();
    expect(document.querySelector(".engine-catalog-pane")?.classList.contains("engines-add-pane")).toBe(true);
    expect(document.querySelector(".engine-catalog-browser")?.getAttribute("aria-label")).toBe("画像モデルカタログ");
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/ingest/search", "POST", {
      q: "", source: "civitai", sort: "newest", lora: false,
    });
    expect(document.body.textContent).toContain("厳密な最終更新順ではありません");
    expect(Array.from(document.querySelectorAll(".engine-catalog-pane button")).every((item) => item.classList.contains("ui-btn"))).toBe(true);

    await click(button("文章"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/llm/ingest/search", "POST", {
      q: "", source: "hf", sort: "updated", lora: false,
    });
    expect(button("Civitai")).toBeUndefined();
  });

  // 🔴 The family filter sends the ENGINE's own word, from the vocabulary the CP served. The
  // upstreams spell the same architecture differently and answer a name they do not know with an
  // empty list, so a Console that sent "Anima" here would draw "no models" for a full family.
  it("narrows the browse to one family without anyone typing a repository name", async () => {
    mockEngines([{ ...imageRow, base_models: ["sdxl", "anima", "krea2"] }, llmRow]);
    apiJSON.mockResolvedValue({ hits: [] });
    await mount();
    const select = document.querySelector<HTMLSelectElement>(".engine-catalog-family select");
    expect(Array.from(select!.options).map((option) => option.value)).toEqual(["", "sdxl", "anima", "krea2"]);

    apiJSON.mockClear();
    await act(async () => {
      select!.value = "anima";
      select!.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await act(async () => { await Promise.resolve(); });
    expect(apiJSON).toHaveBeenLastCalledWith("api/admin/engines/image/ingest/search", "POST", {
      q: "", source: "civitai", sort: "newest", lora: false, family: "anima",
    });
  });

  it("renders genuinely role-specific image and LLM card facts", async () => {
    mockEngines([imageRow, llmRow]);
    apiJSON.mockImplementation((_path: string, _method: string, body: { source?: string }) => Promise.resolve(body.source === "civitai"
      ? { hits: [{ source: "civitai", ref: "22", model_ref: "7", name: "Image Example", base_model: "SDXL", preview_url: "https://example.test/i.jpg" }] }
      : { hits: [{ source: "hf", ref: "org/text", model_ref: "org/text", name: "Text Example", bytes: 4_200_000_000, context_length: 32768 }] }));
    await mount();
    expect(document.querySelector('[aria-label="Image Example"]')?.textContent).toContain("チェックポイント");
    expect(document.querySelector('[aria-label="Image Example"]')?.textContent).toContain("ファミリー: SDXL");
    expect(document.querySelector('[aria-label="Image Example"] .engine-catalog-thumb')).toBeTruthy();

    await click(button("文章"));
    for (const _ of [0, 1]) await act(async () => { await Promise.resolve(); });
    const llm = document.querySelector('[aria-label="Text Example"]');
    expect(llm?.textContent).toContain("4.2 GB");
    expect(llm?.textContent).toContain("コンテキスト: 32,768");
    expect(llm?.querySelector(".engine-catalog-thumb")).toBeNull();
  });

  it("keeps a small optional preview on the card and opens it in a keyboard-closeable modal", async () => {
    mockEngines([imageRow]);
    apiJSON.mockResolvedValue({ hits: [{ source: "civitai", ref: "22", model_ref: "7", name: "Example", preview_url: "https://example.test/a.jpg" }] });
    await mount();
    const thumb = document.querySelector<HTMLButtonElement>(".engine-catalog-thumb")!;
    expect(document.querySelectorAll(".engine-catalog-thumb")).toHaveLength(1);
    thumb.focus();
    await click(thumb);
    expect(document.querySelector(".engine-catalog-lightbox img")).toBeTruthy();
    await act(async () => { document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); });
    await act(async () => { await Promise.resolve(); });
    expect(document.querySelector(".engine-catalog-lightbox")).toBeNull();
    expect(document.activeElement).toBe(thumb);
  });

  // 🔴 ADR 0085 decision 3: one button on the card. `attach` and `replace` were two ways to land
  // in the wrong place from a screen that never said which one you were on.
  it("offers taking in as the only act on a search card", async () => {
    mockEngines([imageRow]);
    apiJSON.mockResolvedValue({ hits: [{ source: "civitai", ref: "22", model_ref: "7", name: "Example" }] });
    await mount();
    expect(button("追加")?.getAttribute("aria-label")).toBe("追加: Example");
    expect(button("部品を追加")).toBeUndefined();
    expect(button("置き換え")).toBeUndefined();
  });

  // 🔴 The whole of ADR 0085 decision 4 in one test: the CP answers a PLAN, the card shows what
  // each file costs, and the press carries the plan's token — no key, no role, no parts checkbox,
  // no attach/replace. Three parties deciding one destination key is what produced the 400
  // `s3Key must be empty or identical` on af-sandbox.
  it("takes a split family in with one press, carrying the CP's plan token and no key", async () => {
    mockEngines([{ ...imageRow, base_models: ["anima"] }]);
    let sent: Record<string, unknown> | undefined;
    apiJSON.mockImplementation((path: string, _method?: string, body?: Record<string, unknown>) => {
      if (path.endsWith("/ingest/search")) return Promise.resolve({ hits: [{ source: "hf", ref: "circlestone-labs/Anima", model_ref: "circlestone-labs/Anima", name: "Anima" }] });
      if (path.endsWith("/ingest/versions")) return Promise.resolve({ versions: [{ ref: "main", name: "main" }] });
      if (path.endsWith("/ingest/files")) return Promise.resolve({ files: [{ name: "split_files/diffusion_models/anima-aesthetic-v1.1.safetensors" }] });
      if (path.endsWith("/ingest/resolve")) return Promise.resolve({
        bytes: 4_180_000_000, can_ingest: true, license_name: "Anima licence", commercial_use: "no",
        plan: {
          plan_token: "plan-1", id: "anima-aesthetic-v1-1", base_model: "anima", main_flag: "--diffusion-model",
          files: [
            { flag: "--diffusion-model", name: "anima-aesthetic-v1.1.safetensors", bytes: 4_180_000_000, action: "download", source: "hf:circlestone-labs/Anima/anima-aesthetic-v1.1.safetensors", key: "image/diffusion_models/anima-aesthetic-v1.1.safetensors" },
            { flag: "--clip_l", name: "qwen_3_06b_base.safetensors", bytes: 1_190_000_000, action: "download", source: "hf:circlestone-labs/Anima/qwen_3_06b_base.safetensors", key: "image/text_encoders/qwen_3_06b_base.safetensors" },
            { flag: "--vae", name: "qwen_image_vae.safetensors", bytes: 253_800_000, action: "reuse", source: "image/vae/qwen_image_vae.safetensors", key: "image/vae/qwen_image_vae.safetensors" },
          ],
          bytes_to_download: 5_370_000_000,
          warnings: ["この族の VAE は既にこの配備にあります。"],
        },
      });
      if (path.endsWith("/ingest")) { sent = body; return Promise.resolve({ id: "job1", model_id: "anima-aesthetic-v1-1", state: "pending", action: "download" }); }
      return Promise.resolve({});
    });
    await mount();
    await click(button("追加"));
    for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });

    // Every file, with what it costs — and the one already here says "no download" instead of a
    // size, because a number beside bytes nobody will spend is what makes a total untrustworthy.
    const plan = document.querySelector(".engine-plan-files")!;
    expect(plan.textContent).toContain("--clip_l");
    expect(plan.textContent).toContain("取得なし（配備が持っています）");
    expect(document.querySelector(".engine-plan-total")?.textContent).toContain("5.4 GB");
    // 🔴 `source` carries two different things and the action is what says which: the upstream
    // for a download, the key the bytes sit at today for a reuse or a move.
    const lines = Array.from(plan.querySelectorAll("li"));
    expect(lines[0].querySelector(".engine-plan-source")?.textContent).toBe("hf:circlestone-labs/Anima/anima-aesthetic-v1.1.safetensors");
    expect(lines[2].querySelector(".engine-plan-source")?.textContent).toBe("いまの場所 image/vae/qwen_image_vae.safetensors");
    // The commercial-use verdict is said ONCE, as the sentence: a chip saying the same thing in
    // another wording beside it reads as two separate restrictions.
    expect(document.querySelector("p.form-err")?.textContent).toContain("非商用ライセンスです");
    expect(document.querySelector(".engine-operation-facts")?.textContent).not.toContain("商用");
    expect(document.body.textContent).toContain("この族の VAE は既にこの配備にあります。");
    // The questions ADR 0085 removed from this card.
    expect(document.body.textContent).not.toContain("ファイルの役割");
    expect(Array.from(document.querySelectorAll("select")).some((select) => Array.from(select.options).some((option) => option.value === "--vae"))).toBe(false);

    await acceptLicence();
    await click(button("取り込む"));
    expect(sent?.plan_token).toBe("plan-1");
    // 🔴 The same `kind` the resolve was asked with. The CP re-plans from this body and reads
    // `lora` out of it to choose the directory, so a press without it stages an adapter under
    // `checkpoints/` — or, with luck, only disagrees with the token and 409s.
    expect(sent?.kind).toBe("checkpoint");
    expect(sent?.id).toBe("anima-aesthetic-v1-1");
    expect(sent?.base_model).toBe("anima");
    expect(sent?.license_accepted).toBe(true);
    for (const gone of ["s3Key", "reuse_s3_key", "attach", "replace", "file_flag", "with_family_parts", "with_family_vae"]) {
      expect(sent?.[gone]).toBeUndefined();
    }
    expect(document.body.textContent).toContain("取り込みを開始しました");
  });

  // 🔴 The press re-plans on the CP, and `kind` is what tells it `image/loras/` from
  // `image/checkpoints/`. The plan's token cannot stand in for it: a body without it re-plans as
  // a checkpoint, which is a 409 if the deployment is lucky and an adapter in the wrong loader's
  // directory if it is not.
  it("sends the same kind the resolve was asked with when taking a LoRA in", async () => {
    mockEngines([imageRow]);
    let sent: Record<string, unknown> | undefined;
    const resolves: Record<string, unknown>[] = [];
    apiJSON.mockImplementation((path: string, _method?: string, body?: Record<string, unknown>) => {
      if (path.endsWith("/ingest/search")) return Promise.resolve({ hits: [{ source: "civitai", ref: "31", model_ref: "9", name: "Style LoRA" }] });
      if (path.endsWith("/ingest/versions")) return Promise.resolve({ versions: [{ ref: "31", name: "v1" }] });
      if (path.endsWith("/ingest/files")) return Promise.resolve({ files: [{ name: "style.safetensors" }] });
      if (path.endsWith("/ingest/resolve")) {
        resolves.push(body || {});
        return Promise.resolve({
          bytes: 220_000_000, can_ingest: true, trained_words: ["stylething"],
          plan: { plan_token: "lora-1", id: "style", base_model: "sdxl", files: [{ name: "style.safetensors", action: "download", bytes: 220_000_000, key: "image/loras/style.safetensors" }], bytes_to_download: 220_000_000 },
        });
      }
      if (path.endsWith("/ingest")) { sent = body; return Promise.resolve({ id: "job3", model_id: "style", state: "pending", action: "download" }); }
      return Promise.resolve({});
    });
    await mountLora();
    await click(button("追加"));
    for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });
    await acceptLicence();
    await click(button("取り込む"));
    expect(resolves[0]?.kind).toBe("lora");
    expect(sent?.kind).toBe("lora");
    expect(sent?.plan_token).toBe("lora-1");
    expect(sent?.trained_words).toEqual(["stylething"]);
  });

  // 🔴 The plan is a quote and the press is the purchase. A card can sit open for minutes, and
  // this is the call that spends money: when the CP re-plans and the answer differs it refuses
  // with the NEW plan, which is redrawn — licence tick included, because what was accepted is not
  // what would now be taken in.
  it("redraws the card from the fresh plan when the press is refused as stale", async () => {
    mockEngines([imageRow]);
    const sent: Record<string, unknown>[] = [];
    let stale = true;
    apiJSON.mockImplementation((path: string, _method?: string, body?: Record<string, unknown>) => {
      if (path.endsWith("/ingest/search")) return Promise.resolve({ hits: [{ source: "hf", ref: "org/model", model_ref: "org/model", name: "Example" }] });
      if (path.endsWith("/ingest/versions")) return Promise.resolve({ versions: [{ ref: "main", name: "main" }] });
      if (path.endsWith("/ingest/files")) return Promise.resolve({ files: [{ name: "model.safetensors" }] });
      if (path.endsWith("/ingest/resolve")) return Promise.resolve({
        bytes: 2_000_000_000, can_ingest: true,
        plan: { plan_token: "old", id: "example", base_model: "sdxl", files: [{ name: "model.safetensors", action: "download", bytes: 2_000_000_000 }], bytes_to_download: 2_000_000_000 },
      });
      if (path.endsWith("/ingest")) {
        sent.push(body || {});
        if (stale) {
          stale = false;
          return Promise.resolve({ error: { code: "engine_plan_stale", message: "the source changed", plan: {
            plan_token: "fresh", id: "example", base_model: "sdxl",
            files: [{ name: "model.safetensors", action: "download", bytes: 3_000_000_000 }], bytes_to_download: 3_000_000_000,
          } } });
        }
        // A press the CP answered with `reuse`: the bytes were already here, so nothing crosses
        // the network — and telling somebody to watch a download would be telling them to watch
        // for something that never appears.
        return Promise.resolve({ id: "job2", model_id: "example", state: "pending", action: "reuse" });
      }
      return Promise.resolve({});
    });
    await mount();
    await click(button("追加"));
    for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });
    await acceptLicence();
    await click(button("取り込む"));

    expect(sent[0]?.plan_token).toBe("old");
    expect(document.querySelector(".engine-plan-stale")?.textContent).toContain("作り直しました");
    expect(document.querySelector(".engine-plan-total")?.textContent).toContain("3.0 GB");
    // The licence has to be read again before the second press can happen at all.
    expect((button("取り込む") as HTMLButtonElement).disabled).toBe(true);
    await acceptLicence();
    await click(button("取り込む"));
    expect(sent[1]?.plan_token).toBe("fresh");
    expect(document.body.textContent).toContain("ダウンロードはありません");
  });

  it("asks for a family only when the CP could not read one, and only from its candidates", async () => {
    mockEngines([imageRow]);
    apiJSON.mockImplementation((path: string) => {
      if (path.endsWith("/ingest/search")) return Promise.resolve({ hits: [{ source: "hf", ref: "org/model", model_ref: "org/model", name: "Example" }] });
      if (path.endsWith("/ingest/versions")) return Promise.resolve({ versions: [{ ref: "main", name: "main" }] });
      if (path.endsWith("/ingest/files")) return Promise.resolve({ files: [{ name: "model.safetensors" }] });
      if (path.endsWith("/ingest/resolve")) return Promise.resolve({
        bytes: 1024, can_ingest: true,
        plan: { plan_token: "p", id: "example", base_model_candidates: ["sdxl", "sd35"], files: [{ name: "model.safetensors", action: "download", bytes: 1024 }], bytes_to_download: 1024 },
      });
      return Promise.resolve({});
    });
    await mount();
    await click(button("追加"));
    for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });
    const family = Array.from(document.querySelectorAll<HTMLSelectElement>(".engine-catalog-plan select"))
      .find((select) => Array.from(select.options).some((option) => option.value === "sd35"))!;
    expect(family).toBeTruthy();
    // Never a free string: an upstream display name stored as a family makes a row that looks
    // complete and will not generate.
    expect(family.tagName).toBe("SELECT");
    await acceptLicence();
    expect((button("取り込む") as HTMLButtonElement).disabled).toBe(true);
    expect(document.querySelector(".engine-operation-footer")?.textContent).toContain("ファミリーを選んでください");
  });

  // 🔴 The family the operator picks has to reach the PLAN, not only the press (ADR 0094 decision
  // 7 — "one press" only holds if the card shows what the press will do). The parts a split family
  // needs are planned from its family (engine_family_parts.go), so a card drawn before anybody
  // chose one lists the main file alone: the operator accepts a licence and a size for one file,
  // presses, and the CP re-plans into three — 409 `engine_plan_stale`, licence reset, press again.
  //
  // It is the qwen-image-edit families that made this reachable: they are the first with parts and
  // deliberately no guess rule (a wrong family here is worse than none — engine_family_guess.go).
  it("re-plans when the operator picks the family, so the card shows the parts the press will fetch", async () => {
    mockEngines([{ ...imageRow, base_models: ["sdxl", "qwen-image-edit-2511"] }]);
    const resolveBodies: (Record<string, unknown> | undefined)[] = [];
    apiJSON.mockImplementation((path: string, _method?: string, body?: Record<string, unknown>) => {
      if (path.endsWith("/ingest/search")) return Promise.resolve({ hits: [{ source: "hf", ref: "Comfy-Org/Qwen-Image-Edit_ComfyUI", model_ref: "Comfy-Org/Qwen-Image-Edit_ComfyUI", name: "Qwen-Image-Edit" }] });
      if (path.endsWith("/ingest/versions")) return Promise.resolve({ versions: [{ ref: "main", name: "main" }] });
      if (path.endsWith("/ingest/files")) return Promise.resolve({ files: [{ name: "split_files/diffusion_models/qwen_image_edit_2511_fp8mixed.safetensors" }] });
      if (path.endsWith("/ingest/resolve")) {
        resolveBodies.push(body);
        const family = String(body?.base_model || "");
        if (!family) {
          return Promise.resolve({
            bytes: 20_533_762_817, can_ingest: true,
            plan: {
              plan_token: "no-family", id: "qwen-image-edit-2511",
              base_model_candidates: ["sdxl", "qwen-image-edit-2511"],
              files: [{ name: "qwen_image_edit_2511_fp8mixed.safetensors", action: "download", bytes: 20_533_762_817 }],
              bytes_to_download: 20_533_762_817,
            },
          });
        }
        return Promise.resolve({
          bytes: 20_533_762_817, can_ingest: true,
          plan: {
            plan_token: "with-family", id: "qwen-image-edit-2511", base_model: family, main_flag: "--diffusion-model",
            files: [
              { flag: "--diffusion-model", name: "qwen_image_edit_2511_fp8mixed.safetensors", action: "download", bytes: 20_533_762_817 },
              { flag: "--clip_l", name: "qwen_2.5_vl_7b_fp8_scaled.safetensors", action: "reuse" },
              { flag: "--vae", name: "qwen_image_vae.safetensors", action: "reuse" },
            ],
            bytes_to_download: 20_533_762_817,
          },
        });
      }
      if (path.endsWith("/ingest")) return Promise.resolve({ id: "job1", model_id: "qwen-image-edit-2511", state: "pending", action: "download" });
      return Promise.resolve({});
    });
    await mount();
    await click(button("追加"));
    for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });

    // Before anybody picks: one file, and the selector asking for a family.
    expect(document.querySelectorAll(".engine-plan-files li")).toHaveLength(1);
    const family = Array.from(document.querySelectorAll<HTMLSelectElement>(".engine-catalog-plan select"))
      .find((select) => Array.from(select.options).some((option) => option.value === "qwen-image-edit-2511"))!;
    expect(family).toBeTruthy();

    await act(async () => {
      family.value = "qwen-image-edit-2511";
      family.dispatchEvent(new Event("change", { bubbles: true }));
    });
    for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });

    // The choice reached the CP, and the card now prices the three files the press will act on.
    expect(resolveBodies.some((sent) => sent?.base_model === "qwen-image-edit-2511")).toBe(true);
    const lines = Array.from(document.querySelectorAll(".engine-plan-files li")).map((li) => li.textContent);
    expect(lines).toHaveLength(3);
    expect(lines.join(" ")).toContain("--clip_l");
    expect(lines.join(" ")).toContain("--vae");
    // 🔴 And the licence is asked again: what is being accepted changed under it.
    expect((button("取り込む") as HTMLButtonElement).disabled).toBe(true);
    // 🔴 The selector is still there. The CP offers candidates only while it cannot name the
    // family, so the re-plan answers with none — and drawing the selector from that answer alone
    // would take it away from the person the moment they used it.
    const stillThere = Array.from(document.querySelectorAll<HTMLSelectElement>(".engine-catalog-plan select"))
      .find((select) => Array.from(select.options).some((option) => option.value === "qwen-image-edit-2511"));
    expect(stillThere?.value).toBe("qwen-image-edit-2511");

    await acceptLicence();
    await click(button("取り込む"));
    const press = apiJSON.mock.calls.find((call) => String(call[0]).endsWith("/ingest") && call[1] === "POST");
    expect((press![2] as { plan_token: string }).plan_token).toBe("with-family");
  });

  it("includes the LLM KV cache in the plan's VRAM fit line", async () => {
    mockEngines([{ ...llmRow, class: { vram_mib: 21000 } }]);
    apiJSON.mockImplementation((path: string) => {
      if (path.endsWith("/ingest/search")) return Promise.resolve({ hits: [{ source: "hf", ref: "org/model", model_ref: "org/model", name: "Large LLM" }] });
      if (path.endsWith("/ingest/versions")) return Promise.resolve({ versions: [{ ref: "main", name: "main" }] });
      if (path.endsWith("/ingest/files")) return Promise.resolve({ files: [{ name: "model.gguf", bytes: 18_556_689_568 }] });
      if (path.endsWith("/ingest/resolve")) return Promise.resolve({
        bytes: 18_556_689_568, can_ingest: true, license: "apache-2.0", context_length: 262144, kv_mib_per_1k_tokens: 96,
        plan: { plan_token: "p", id: "large-llm", files: [{ name: "model.gguf", action: "download", bytes: 18_556_689_568 }], bytes_to_download: 18_556_689_568 },
      });
      return Promise.resolve({});
    });
    await mount();
    await click(button("追加"));
    for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });
    const fit = document.querySelector(".engine-operation-fit");
    expect(fit?.textContent).toContain("重み 17697 MiB");
    // 🔴 The window is what the CARD can hold, not the model's published ceiling (ADR 0089).
    // 262,144 would cost 24,576 MiB of cache on top of 17,697 MiB of weights — 42,273 MiB on a
    // 21,000 MiB card — and the field used to open at exactly that, so this screen's answer for
    // every large model was "impossible". 17,697 MiB leaves 150 MiB under the comfortable line,
    // which buys 1,024 tokens and no more: the number is small because the model is nearly the
    // size of the card, which is the true and useful answer.
    expect(document.querySelector<HTMLInputElement>(".engine-operation-window input")?.value).toBe("1024");
    expect(fit?.textContent).toContain("KV キャッシュ 96 MiB");
    expect(fit?.textContent).toContain("合計 17793 MiB");
    expect(fit?.textContent).toContain("収まります");
    // And the ceiling is still on screen, said as what it is.
    expect(document.querySelector(".engine-operation-window")?.textContent).toContain("上限 262,144");
  });

  it("invalidates pagination when the visible query changes", async () => {
    mockEngines([imageRow]);
    apiJSON.mockResolvedValue({ hits: [{ source: "civitai", ref: "22", model_ref: "7", name: "First" }], next_cursor: "page-2" });
    await mount();
    expect(button("さらに読み込む")).toBeTruthy();
    const search = document.querySelector<HTMLInputElement>(".engine-catalog-search input")!;
    await act(async () => {
      Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!.call(search, "another");
      search.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(button("さらに読み込む")).toBeUndefined();
  });

  it("keeps browsing usable when no engines are registered", async () => {
    api.mockResolvedValue({ super_admin: true, engines: [] });
    apiJSON.mockResolvedValue({ hits: [{ source: "civitai", ref: "31", model_ref: "9", name: "Browse Only", base_model: "Flux" }] });
    await mount();
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/search?kind=checkpoint", "POST", {
      q: "", source: "civitai", sort: "newest", lora: false,
    });
    expect(document.querySelector('.engine-catalog-card[aria-label="Browse Only"]')).toBeTruthy();
    expect(document.querySelector('.engine-catalog-card[aria-label="Browse Only"] button.primary')).toBeNull();
    expect(document.body.textContent).toContain("閲覧だけです");
  });

  it("shows a loading indicator while a search is in flight and clears it once it lands", async () => {
    mockEngines([imageRow]);
    let resolveSearch: ((value: unknown) => void) | undefined;
    apiJSON.mockImplementation(() => new Promise((resolve) => { resolveSearch = resolve; }));
    await mount();
    expect(document.body.textContent).toContain("検索しています");
    expect(document.querySelector(".engine-catalog-loading .codicon-loading")).toBeTruthy();

    await act(async () => { resolveSearch!({ hits: [] }); });
    for (const _ of [0, 1]) await act(async () => { await Promise.resolve(); });
    expect(document.body.textContent).not.toContain("検索しています");
    expect(document.querySelector(".engine-catalog-loading")).toBeNull();
  });

  // 🔴 Civitai's search endpoint 503s under load (measured on af-sandbox). The panel used to
  // show nothing but a bare error banner with no way to tell "still loading" from "it failed" —
  // this pins that the banner carries the upstream's own status text and the spinner is gone.
  it("surfaces a Civitai 503 as an error banner and clears the loading indicator", async () => {
    mockEngines([imageRow]);
    apiJSON.mockResolvedValue({ error: { code: "source_error", message: "civitai.com answered 503 Service Unavailable" } });
    await mount();
    expect(document.body.textContent).toContain("取り込み元が想定外の応答を返しました: civitai.com answered 503 Service Unavailable");
    expect(document.querySelector(".engine-catalog-loading")).toBeNull();
  });

  // 🔴 The `id` field is inside the advanced fold. When the operator edits it, the press sends
  // the new id to the CP — but the plan token was computed without it in the earlier resolve,
  // so the CP re-plans and answers 409 `engine_plan_stale`. The fix: send `id` to resolve too,
  // committed on blur/Enter so the plan token already includes it before the press.
  it("re-plans with the operator's id when they commit it by leaving the field", async () => {
    mockEngines([{ ...imageRow, base_models: ["sdxl"] }]);
    const resolveBodies: (Record<string, unknown> | undefined)[] = [];
    apiJSON.mockImplementation((path: string, _method?: string, body?: Record<string, unknown>) => {
      if (path.endsWith("/ingest/search")) return Promise.resolve({ hits: [{ source: "hf", ref: "org/model", model_ref: "org/model", name: "Example" }] });
      if (path.endsWith("/ingest/versions")) return Promise.resolve({ versions: [{ ref: "main", name: "main" }] });
      if (path.endsWith("/ingest/files")) return Promise.resolve({ files: [{ name: "model.safetensors" }] });
      if (path.endsWith("/ingest/resolve")) {
        resolveBodies.push(body);
        const sentId = body?.id as string | undefined;
        return Promise.resolve({
          bytes: 1_000_000_000, can_ingest: true,
          plan: {
            plan_token: sentId ? `token-with-${sentId}` : "token-default",
            id: sentId || "model-v1",
            base_model: "sdxl",
            files: [{ name: "model.safetensors", action: "download", bytes: 1_000_000_000 }],
            bytes_to_download: 1_000_000_000,
          },
        });
      }
      if (path.endsWith("/ingest")) return Promise.resolve({ id: "job1", model_id: "my-custom-id", state: "pending", action: "download" });
      return Promise.resolve({});
    });
    await mount();
    await click(button("追加"));
    for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });

    // The plan card is showing; open advanced and change the id.
    const idInput = Array.from(document.querySelectorAll<HTMLInputElement>(".engine-operation-advanced input"))
      .find((input) => input.closest("label")?.textContent?.includes("id"))!;
    expect(idInput).toBeTruthy();
    const resolveCountBefore = resolveBodies.length;

    await act(async () => {
      Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!.call(idInput, "my-custom-id");
      idInput.dispatchEvent(new Event("input", { bubbles: true }));
    });
    for (const _ of [0, 1]) await act(async () => { await Promise.resolve(); });

    // Typing alone must not trigger a re-plan.
    expect(resolveBodies.length).toBe(resolveCountBefore);

    // Enter commits the id and triggers a re-plan (same as blur in the component handler).
    await act(async () => { idInput.dispatchEvent(new KeyboardEvent("keydown", { key: "Enter", bubbles: true })); });
    for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });

    expect(resolveBodies.some((sent) => sent?.id === "my-custom-id")).toBe(true);
    // The fresh plan token includes the id; the press carries it.
    await acceptLicence();
    await click(button("取り込む"));
    const press = apiJSON.mock.calls.find((call) => String(call[0]).endsWith("/ingest") && call[1] === "POST");
    expect((press![2] as { plan_token: string }).plan_token).toBe("token-with-my-custom-id");
  });

  // 🔴 The dangerous side of the id fix: id is typed character by character, so a naive
  // dependency on `id` would re-plan on every keystroke, resetting the licence checkbox each
  // time — 20 characters typed = 20 resets. Only a committed value (blur/Enter) may trigger
  // re-planning.
  it("does not re-plan while the operator is still typing the id", async () => {
    mockEngines([{ ...imageRow, base_models: ["sdxl"] }]);
    const resolveBodies: (Record<string, unknown> | undefined)[] = [];
    apiJSON.mockImplementation((path: string, _method?: string, body?: Record<string, unknown>) => {
      if (path.endsWith("/ingest/search")) return Promise.resolve({ hits: [{ source: "hf", ref: "org/model", model_ref: "org/model", name: "Example" }] });
      if (path.endsWith("/ingest/versions")) return Promise.resolve({ versions: [{ ref: "main", name: "main" }] });
      if (path.endsWith("/ingest/files")) return Promise.resolve({ files: [{ name: "model.safetensors" }] });
      if (path.endsWith("/ingest/resolve")) {
        resolveBodies.push(body);
        return Promise.resolve({
          bytes: 1_000_000_000, can_ingest: true,
          plan: {
            plan_token: "p1", id: "model-v1", base_model: "sdxl",
            files: [{ name: "model.safetensors", action: "download", bytes: 1_000_000_000 }],
            bytes_to_download: 1_000_000_000,
          },
        });
      }
      return Promise.resolve({});
    });
    await mount();
    await click(button("追加"));
    for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });

    await acceptLicence();
    const resolveCountAfterPlan = resolveBodies.length;

    // Open advanced and type three characters, one at a time, without blurring.
    const idInput = Array.from(document.querySelectorAll<HTMLInputElement>(".engine-operation-advanced input"))
      .find((input) => input.closest("label")?.textContent?.includes("id"))!;
    expect(idInput).toBeTruthy();

    for (const char of ["a", "ab", "abc"]) {
      await act(async () => {
        Object.getOwnPropertyDescriptor(window.HTMLInputElement.prototype, "value")!.set!.call(idInput, char);
        idInput.dispatchEvent(new Event("input", { bubbles: true }));
        idInput.dispatchEvent(new Event("change", { bubbles: true }));
      });
      for (const _ of [0, 1]) await act(async () => { await Promise.resolve(); });
    }

    // No extra resolve calls fired while typing — the licence checkbox stays intact.
    expect(resolveBodies.length).toBe(resolveCountAfterPlan);
    const licence = Array.from(document.querySelectorAll<HTMLInputElement>('input[type="checkbox"]'))
      .find((input) => input.parentElement?.textContent?.includes("ライセンス"))!;
    expect(licence.checked).toBe(true);
  });

  // 🔴 Opening the plan dialog must call ingest/resolve exactly once. The bug: syncing
  // committedId from plan.id in the resolve callback changes a dep and re-runs the effect,
  // which clears the card (setResolved/setPlan null) and hits the upstream a second time.
  // Image engines have a separate pre-existing second call via plannedFamily/setBaseModel,
  // so this test uses an LLM engine where plannedFamily is always "" and isolates the issue.
  it("calls ingest/resolve exactly once for an LLM when the dialog opens without any operator input", async () => {
    mockEngines([llmRow]);
    let resolveCount = 0;
    apiJSON.mockImplementation((path: string) => {
      if (path.endsWith("/ingest/search")) return Promise.resolve({ hits: [{ source: "hf", ref: "org/model", model_ref: "org/model", name: "Example LLM" }] });
      if (path.endsWith("/ingest/versions")) return Promise.resolve({ versions: [{ ref: "main", name: "main" }] });
      if (path.endsWith("/ingest/files")) return Promise.resolve({ files: [{ name: "model.gguf" }] });
      if (path.endsWith("/ingest/resolve")) {
        resolveCount++;
        return Promise.resolve({
          bytes: 4_000_000_000, can_ingest: true,
          plan: { plan_token: "p1", id: "example-llm", files: [{ name: "model.gguf", action: "download", bytes: 4_000_000_000 }], bytes_to_download: 4_000_000_000 },
        });
      }
      return Promise.resolve({});
    });
    await mount();
    await click(button("追加"));
    for (const _ of [0, 1, 2, 3, 4, 5]) await act(async () => { await Promise.resolve(); });

    expect(resolveCount).toBe(1);
  });
});

describe("registered rows and the bucket", () => {
  const anima = {
    ...imageRow,
    base_models: ["anima"],
    model_rows: [{
      id: "anima-aesthetic", enabled: false, base_model: "anima", kind: "model",
      files_missing: ["--clip_l", "--vae"],
      file_rows: [{ s3Key: "image/diffusion_models/anima.safetensors", flag: "--diffusion-model" }],
    }],
  };
  const declaredObject = {
    key: "image/diffusion_models/anima.safetensors", bytes: 4_180_000_000, role_dir: "diffusion_models",
    placement: "ok", state: "present", declared_by: [{ model_id: "anima-aesthetic", flag: "--diffusion-model" }],
    source: "hf:circlestone-labs/Anima/anima.safetensors",
  };

  // 揃える with nothing to ask about just acts: the check answers, the CP is told to do it, and
  // the note says a move is not a download.
  it("completes a row without a dialog when there is nothing to choose or to pay for", async () => {
    mockEngines([anima], [declaredObject]);
    const bodies: unknown[] = [];
    apiJSON.mockImplementation((path: string, _method?: string, body?: Record<string, unknown>) => {
      if (path.endsWith("/complete")) { bodies.push(body); return Promise.resolve({ action: "moving", files: [], bytes_to_download: 0 }); }
      return Promise.resolve({ hits: [] });
    });
    await mountRegistered();
    await click(labelled("揃える: anima-aesthetic"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/anima-aesthetic/complete", "POST", { check: true });
    expect(bodies).toEqual([{ check: true }, {}]);
    // A move is not a download, and the note must not tell anybody to watch for one.
    expect(document.body.textContent).toContain("ダウンロードはありません");
  });

  // The one place a person picks a part — and they pick it FOR this checkpoint, from what the
  // ledger holds. Choosing a part and then its destination is the shape ADR 0085 removed.
  it("asks which file to use only when the CP offers several candidates", async () => {
    mockEngines([anima], [declaredObject]);
    let ran: Record<string, unknown> | undefined;
    apiJSON.mockImplementation((path: string, _method?: string, body?: Record<string, unknown>) => {
      if (path.endsWith("/complete")) {
        if (body?.check) {
          return Promise.resolve({
            action: "choose", bytes_to_download: 0,
            files: [{ flag: "--vae", action: "choose", candidates: [{ key: "image/vae/a.safetensors", bytes: 300_000_000 }, { key: "image/vae/b.safetensors" }] }],
          });
        }
        ran = body;
        return Promise.resolve({ action: "attached", files: [] });
      }
      return Promise.resolve({ hits: [] });
    });
    await mountRegistered();
    await click(labelled("揃える: anima-aesthetic"));
    const picker = document.querySelector<HTMLSelectElement>('.engine-complete select')!;
    expect(picker).toBeTruthy();
    expect((within(".engine-complete", "揃える") as HTMLButtonElement).disabled).toBe(true);
    await act(async () => {
      picker.value = "image/vae/b.safetensors";
      picker.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await click(within(".engine-complete", "揃える"));
    expect(ran).toEqual({ choices: { "--vae": "image/vae/b.safetensors" } });
  });

  // 🔴 ONE candidate still asks. `--clip_l` / `--clip_g` / `--t5xxl` share `text_encoders/`, so
  // the CP will not guess which role a loose encoder fills — a Console that treated a single
  // candidate as obvious would attach it to the wrong flag without saying anything.
  it("opens the dialog for a single candidate too, because the CP asked", async () => {
    mockEngines([anima], [declaredObject]);
    let ran: Record<string, unknown> | undefined;
    apiJSON.mockImplementation((path: string, _method?: string, body?: Record<string, unknown>) => {
      if (path.endsWith("/complete")) {
        if (body?.check) {
          return Promise.resolve({
            action: "choose", bytes_to_download: 0,
            files: [{ flag: "--clip_l", action: "choose", candidates: [{ key: "image/text_encoders/qwen_3_06b_base.safetensors", bytes: 1_190_000_000 }] }],
          });
        }
        ran = body;
        return Promise.resolve({ action: "attached", files: [] });
      }
      return Promise.resolve({ hits: [] });
    });
    await mountRegistered();
    await click(labelled("揃える: anima-aesthetic"));
    const picker = document.querySelector<HTMLSelectElement>(".engine-complete select")!;
    expect(picker).toBeTruthy();
    expect(Array.from(picker.options).map((option) => option.value))
      .toEqual(["", "image/text_encoders/qwen_3_06b_base.safetensors"]);
    await act(async () => {
      picker.value = "image/text_encoders/qwen_3_06b_base.safetensors";
      picker.dispatchEvent(new Event("change", { bubbles: true }));
    });
    await click(within(".engine-complete", "揃える"));
    expect(ran).toEqual({ choices: { "--clip_l": "image/text_encoders/qwen_3_06b_base.safetensors" } });
  });

  // 🔴 `register` starts no download of its own: a part that lives only upstream comes back as
  // `download` in the answer's own `complete`, and the row is the subject of that. Left as a note
  // it is exactly "I registered it and do not know what to do".
  it("carries a register's unfinished business straight into the complete dialog", async () => {
    mockEngines([anima], [
      { key: "image/checkpoints/split_files/diffusion_models/krea2.safetensors", bytes: 13_100_000_000, role_dir: "other", placement: "misplaced", state: "present", declared_by: [] },
    ]);
    let ran: Record<string, unknown> | undefined;
    apiJSON.mockImplementation((path: string, _method?: string, body?: Record<string, unknown>) => {
      if (path.endsWith("/objects/register")) {
        return Promise.resolve({
          model_id: "krea2", moved: true, jobs: [],
          complete: {
            action: "job_started", bytes_to_download: 1_190_000_000,
            files: [{ flag: "--clip_l", action: "download", bytes: 1_190_000_000, source: "hf:krea-ai/krea2" }],
          },
        });
      }
      if (path.endsWith("/complete")) { ran = body; return Promise.resolve({ action: "job_started", files: [] }); }
      return Promise.resolve({ hits: [] });
    });
    await mountRegistered();
    await click(labelled("登録: image/checkpoints/split_files/diffusion_models/krea2.safetensors"));
    expect(document.querySelector(".engine-complete .ui-modal-title")?.textContent).toContain("krea2");
    // Bytes to pay for, so the licence is read before the press — on the row the register made.
    expect((within(".engine-complete", "揃える") as HTMLButtonElement).disabled).toBe(true);
    await acceptLicence();
    await click(within(".engine-complete", "揃える"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/krea2/complete", "POST", { license_accepted: true });
    expect(ran).toEqual({ license_accepted: true });
  });

  // Swapping a part a row already has is the same dialog on a filled frame, and the CP is told
  // so: without `replace` it refuses, which is the refusal that used to arrive with no way out.
  it("swaps a filled slot from the same dialog and says so with replace", async () => {
    mockEngines([anima], [declaredObject]);
    let ran: Record<string, unknown> | undefined;
    apiJSON.mockImplementation((path: string, _method?: string, body?: Record<string, unknown>) => {
      if (path.endsWith("/complete")) {
        if (body?.check) {
          return Promise.resolve({
            action: "attached", bytes_to_download: 900_000_000,
            files: [{ flag: "--vae", action: "declare", key: "image/vae/a.safetensors", candidates: [{ key: "image/vae/a.safetensors" }, { key: "image/vae/b.safetensors" }] }],
          });
        }
        ran = body;
        return Promise.resolve({ action: "job_started", files: [], jobs: [{ id: "j9" }] });
      }
      return Promise.resolve({ hits: [] });
    });
    await mountRegistered();
    await click(labelled("揃える: anima-aesthetic"));
    const picker = document.querySelector<HTMLSelectElement>('.engine-complete select')!;
    expect(Array.from(picker.options)[0].textContent).toBe("今のまま");
    await act(async () => {
      picker.value = "image/vae/b.safetensors";
      picker.dispatchEvent(new Event("change", { bubbles: true }));
    });
    // Bytes to pay for means the licence is read again before the press.
    expect((within(".engine-complete", "揃える") as HTMLButtonElement).disabled).toBe(true);
    await acceptLicence();
    await click(within(".engine-complete", "揃える"));
    expect(ran).toEqual({ choices: { "--vae": "image/vae/b.safetensors" }, replace: true, license_accepted: true });
  });

  // 🔴 The af-sandbox hole, with a road out of it: after the rows were forgotten the bytes stayed
  // (about 24 GB) and no screen could show them, because every repair was computed FROM a row.
  it("lists the bucket, sorts orphans and misplaced objects first, and registers a main file", async () => {
    mockEngines([anima], [
      declaredObject,
      { key: "image/text_encoders/loose.safetensors", bytes: 1_190_000_000, role_dir: "text_encoders", placement: "ok", state: "present", declared_by: [] },
      { key: "image/checkpoints/split_files/diffusion_models/krea2.safetensors", bytes: 13_100_000_000, role_dir: "other", placement: "misplaced", state: "present", declared_by: [] },
    ]);
    apiJSON.mockImplementation((path: string) => {
      if (path.endsWith("/objects/register")) return Promise.resolve({ model_id: "krea2", moved: true, jobs: [], complete: { action: "attached" } });
      return Promise.resolve({ hits: [] });
    });
    await mountRegistered();
    const rows = Array.from(document.querySelectorAll(".engine-ledger-row")).map((row) => row.getAttribute("aria-label"));
    expect(rows[0]).toBe("image/checkpoints/split_files/diffusion_models/krea2.safetensors");
    expect(rows).toHaveLength(3);
    expect(document.querySelector(".engine-ledger-row")?.textContent).toContain("誤配置");

    // A misplaced MAIN file carries 登録; an orphan part carries only 消す, because a part is
    // never the subject — it is attached by the 揃える of whichever checkpoint reads it.
    expect(labelled("登録: image/checkpoints/split_files/diffusion_models/krea2.safetensors")).toBeTruthy();
    expect(labelled("登録: image/text_encoders/loose.safetensors")).toBeUndefined();
    expect(labelled("消す: image/text_encoders/loose.safetensors")).toBeTruthy();
    // An object a row declares has no button at all.
    expect(labelled("消す: image/diffusion_models/anima.safetensors")).toBeUndefined();

    await click(labelled("登録: image/checkpoints/split_files/diffusion_models/krea2.safetensors"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/objects/register", "POST", {
      key: "image/checkpoints/split_files/diffusion_models/krea2.safetensors",
    });
    expect(document.body.textContent).toContain("krea2 として登録しました");
  });

  // 🔴 Reported from the panel on 2026-09-19: three GGUFs under `llm/` that no row declared, each
  // drawn with 消す and nothing else, while the image tab had 登録 on the same kind of orphan. The
  // rule was written as ComfyUI's two loader directories, and the llm layout is FLAT — so the only
  // act the screen offered on a chat model this deployment is paying for was to throw it away.
  it("offers 登録 on a chat engine's flat orphan, and only 消す on its adapters and shards", async () => {
    mockEngines([llmRow], [
      { key: "llm/Qwen3.8-27B-Uncensored-Q4_K_M.gguf", bytes: 17_900_000_000, role_dir: "other", placement: "ok", state: "present", declared_by: [] },
      { key: "llm/loras/style-v1.gguf", bytes: 120_000_000, role_dir: "other", placement: "ok", state: "present", declared_by: [] },
      { key: "llm/qwen3-30b/model-00001-of-00002.gguf", bytes: 9_000_000_000, role_dir: "other", placement: "ok", state: "present", declared_by: [] },
      // Bytes in the bucket that are not a model file at all: listed, never registrable.
      { key: "llm/notes.json", bytes: 4_096, role_dir: "other", placement: "ok", state: "present", declared_by: [] },
    ]);
    apiJSON.mockImplementation((path: string) => {
      if (path.endsWith("/objects/register")) {
        return Promise.resolve({ model_id: "qwen3.8-27b-uncensored", moved: false, jobs: [], complete: { action: "none" } });
      }
      return Promise.resolve({ hits: [] });
    });
    await mountRegistered("llm");
    expect(labelled("登録: llm/Qwen3.8-27B-Uncensored-Q4_K_M.gguf")).toBeTruthy();
    // An adapter is attached by the 揃える of the model that reads it; one shard is not a file;
    // and a `.json` is not a model however flat it sits.
    for (const key of ["llm/loras/style-v1.gguf", "llm/qwen3-30b/model-00001-of-00002.gguf", "llm/notes.json"]) {
      expect(labelled(`登録: ${key}`)).toBeUndefined();
      expect(labelled(`消す: ${key}`)).toBeTruthy();
    }

    await click(labelled("登録: llm/Qwen3.8-27B-Uncensored-Q4_K_M.gguf"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/llm/objects/register", "POST", {
      key: "llm/Qwen3.8-27B-Uncensored-Q4_K_M.gguf",
    });
    expect(document.body.textContent).toContain("qwen3.8-27b-uncensored として登録しました");
  });

  it("deletes an orphan object only after saying what cannot be undone", async () => {
    mockEngines([anima], [
      { key: "image/text_encoders/loose.safetensors", bytes: 1_190_000_000, role_dir: "text_encoders", placement: "ok", state: "present", declared_by: [] },
    ]);
    apiJSON.mockResolvedValue({ deleting: "image/text_encoders/loose.safetensors" });
    await mountRegistered();
    await click(labelled("消す: image/text_encoders/loose.safetensors"));
    expect(document.querySelector(".engine-ledger-confirm")?.textContent).toContain("取り消せません");
    await click(within(".engine-ledger-confirm", "消す"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/objects", "DELETE", {
      key: "image/text_encoders/loose.safetensors",
    });
  });

  // 🔴 Measured on af-sandbox, during a krea2 ingest: the bucket already held the key, so the
  // ledger kept `state: "present"` and only the JOB said `uploading` — the row offered 登録, and
  // pressing it answered 409 `already declared by`. The refusal was right; the button was not.
  it("presses nothing on a key a task is writing, however the row spells it", async () => {
    vi.useFakeTimers();
    try {
      const key = "image/diffusion_models/krea2_raw_fp8_scaled.safetensors";
      let listed: Record<string, unknown>[] = [{
        key, bytes: 13_100_000_000, role_dir: "diffusion_models", placement: "ok",
        // present AND uploading: what a re-ingest over existing bytes looks like.
        state: "present", declared_by: [],
        job: { id: "job-11", state: "uploading", created_at: "2026-09-15T08:00:00Z" },
      }];
      let reads = 0;
      api.mockImplementation((path: string) => {
        if (path === "api/admin/engines") return Promise.resolve({ super_admin: true, engines: [anima] });
        if (path.endsWith("/objects")) { reads += 1; return Promise.resolve({ objects: listed, checked_at: "2026-09-15T08:00:00Z" }); }
        return Promise.resolve({});
      });
      apiJSON.mockResolvedValue({ hits: [] });
      await mountRegistered();
      const row = document.querySelector<HTMLElement>(`.engine-ledger-row[aria-label="${key}"]`)!;
      expect(row.textContent).toContain("取り込み中");
      expect(row.textContent).toContain("取り込みのタスクが走っています");
      expect(labelled(`登録: ${key}`)).toBeUndefined();
      expect(labelled(`消す: ${key}`)).toBeUndefined();

      // And the ledger follows the task to its end rather than waiting for somebody to reload.
      const before = reads;
      await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
      expect(reads).toBeGreaterThan(before);

      listed = [{ key, bytes: 13_100_000_000, role_dir: "diffusion_models", placement: "ok", state: "present",
        declared_by: [{ model_id: "krea2-v2", flag: "--diffusion-model" }] }];
      await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
      const done = document.querySelector<HTMLElement>(`.engine-ledger-row[aria-label="${key}"]`)!;
      expect(done.textContent).toContain("krea2-v2");
      expect(done.textContent).not.toContain("取り込み中");
      const settled = reads;
      await act(async () => { await vi.advanceTimersByTimeAsync(10000); });
      expect(reads).toBe(settled);
    } finally {
      vi.useRealTimers();
    }
  });

  // The refusal the operator actually saw, with the button they could not: 登録 on a key a row
  // already declares answers 409 with holder=row and next=complete, and that is a press.
  it("draws a register refusal's next act as the row's 揃える", async () => {
    mockEngines([anima], [
      { key: "image/diffusion_models/krea2.safetensors", bytes: 13_100_000_000, role_dir: "diffusion_models",
        placement: "ok", state: "present", declared_by: [] },
    ]);
    const calls: string[] = [];
    apiJSON.mockImplementation((path: string, method?: string) => {
      calls.push(`${method} ${path}`);
      if (path.endsWith("/objects/register")) {
        return Promise.resolve({ error: {
          code: "engine_bad_body", message: "already declared by krea2_raw_fp8_scaled",
          holder: { kind: "row", id: "krea2_raw_fp8_scaled", key: "image/diffusion_models/krea2.safetensors" },
          next: { act: "complete", target: "krea2_raw_fp8_scaled" },
        } });
      }
      if (path.endsWith("/complete")) return Promise.resolve({ action: "attached", files: [] });
      return Promise.resolve({ hits: [] });
    });
    await mountRegistered();
    await click(labelled("登録: image/diffusion_models/krea2.safetensors"));
    const refusal = document.querySelector(".engine-refusal")!;
    expect(refusal.textContent).toContain("already declared by krea2_raw_fp8_scaled");
    expect(refusal.textContent).toContain("押さえているのは 登録済みの行: krea2_raw_fp8_scaled");
    const next = refusal.querySelector<HTMLButtonElement>(".engine-refusal-next")!;
    expect(next.textContent).toBe("揃える");
    await click(next);
    expect(calls).toContain("POST api/admin/engines/image/models/krea2_raw_fp8_scaled/complete");
  });

  // 🔴 Measured on af-sandbox: 消す "did nothing". The CP holds no `s3:DeleteObject` (ADR 0072
  // decision 7), so the route starts a TASK and answers `{deleting}` — the object goes on being
  // listed as `present` for as long as that takes, and the screen said nothing about it.
  it("draws an accepted delete as deleting and keeps reloading until the bucket drops it", async () => {
    vi.useFakeTimers();
    try {
      const loose = { key: "image/text_encoders/loose.safetensors", bytes: 1_190_000_000, role_dir: "text_encoders", placement: "ok", state: "present", declared_by: [] };
      let listed: Record<string, unknown>[] = [loose];
      let reads = 0;
      api.mockImplementation((path: string) => {
        if (path === "api/admin/engines") return Promise.resolve({ super_admin: true, engines: [anima] });
        if (path.endsWith("/objects")) { reads += 1; return Promise.resolve({ objects: listed, checked_at: "2026-09-15T03:42:00Z" }); }
        return Promise.resolve({});
      });
      apiJSON.mockResolvedValue({ deleting: "image/text_encoders/loose.safetensors" });
      await mountRegistered();
      await click(labelled("消す: image/text_encoders/loose.safetensors"));
      await click(within(".engine-ledger-confirm", "消す"));

      // The object is still in the bucket, and the row says why rather than looking untouched.
      const row = document.querySelector<HTMLElement>('.engine-ledger-row[aria-label="image/text_encoders/loose.safetensors"]')!;
      expect(row.className).toContain("deleting");
      expect(row.textContent).toContain("削除中");
      expect(row.textContent).toContain("削除のタスクが走っています");
      expect(labelled("消す: image/text_encoders/loose.safetensors")).toBeUndefined();

      const before = reads;
      await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
      expect(reads).toBeGreaterThan(before);
      expect(document.querySelector('.engine-ledger-row[aria-label="image/text_encoders/loose.safetensors"]')).toBeTruthy();

      // The listing dropping the key is the only thing that ends it.
      listed = [];
      await act(async () => { await vi.advanceTimersByTimeAsync(5000); });
      expect(document.querySelector('.engine-ledger-row[aria-label="image/text_encoders/loose.safetensors"]')).toBeNull();
      const settled = reads;
      await act(async () => { await vi.advanceTimersByTimeAsync(10000); });
      expect(reads).toBe(settled);
    } finally {
      vi.useRealTimers();
    }
  });

  // A row pointing at bytes that are not there: the ledger names the row, and the act is that
  // row's 揃える — the object side has nothing anybody could press.
  it("sends a missing object's holder to its own row's complete, and hides an unclaimed one", async () => {
    mockEngines([anima], [
      { key: "image/diffusion_models/anima.safetensors", role_dir: "diffusion_models", placement: "ok", state: "missing",
        declared_by: [{ model_id: "anima-aesthetic", flag: "--diffusion-model" }] },
      // Nobody declares it and there are no bytes: neither a subject nor an act. The CP stopped
      // sending these; one that arrives anyway is dropped rather than drawn.
      { key: "image/vae/ghost.safetensors", role_dir: "vae", placement: "ok", state: "missing", declared_by: [] },
    ]);
    apiJSON.mockImplementation((path: string) => {
      if (path.endsWith("/complete")) return Promise.resolve({ action: "job_started", files: [], bytes_to_download: 0 });
      return Promise.resolve({ hits: [] });
    });
    await mountRegistered();
    const row = document.querySelector<HTMLElement>('.engine-ledger-row[aria-label="image/diffusion_models/anima.safetensors"]')!;
    expect(row.textContent).toContain("anima-aesthetic がこのキーを指していますが");
    expect(document.querySelector('.engine-ledger-row[aria-label="image/vae/ghost.safetensors"]')).toBeNull();

    await click(labelled("揃える: image/diffusion_models/anima.safetensors"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/anima-aesthetic/complete", "POST", { check: true });
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/anima-aesthetic/complete", "POST", {});
  });

  // A failed job is a failed entry on its destination key with one act (ADR 0085 decision 6):
  // there is no history tab to find it in any more.
  it("shows a failed ingest on its object with the task's message and dismisses it", async () => {
    mockEngines([anima], [
      { key: "image/vae/half.safetensors", role_dir: "vae", placement: "ok", state: "failed", declared_by: [],
        job: { id: "job-7", state: "failed", message: "403 from the source", created_at: "2026-09-15T03:42:00Z" } },
    ]);
    apiJSON.mockResolvedValue({ jobs: [] });
    await mountRegistered();
    expect(document.querySelector(".engine-ledger-message")?.textContent).toContain("403 from the source");
    expect(document.querySelector(".engine-catalog-jobs")).toBeNull();
    await click(labelled("消す: image/vae/half.safetensors"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/ingest/job-7", "DELETE");
  });

  // 🔴 ADR 0085 decision 5. "The S3 key … is already recorded; choose a new destination" was true
  // and unusable: the Console now draws the CP's own `next` as the button on the error line.
  it("turns a refusal's next act into the button beside it", async () => {
    mockEngines([anima], [
      { key: "image/vae/orphan.safetensors", role_dir: "vae", placement: "ok", state: "present", declared_by: [] },
    ]);
    const calls: string[] = [];
    apiJSON.mockImplementation((path: string, method?: string) => {
      calls.push(`${method} ${path}`);
      if (path.endsWith("/objects") && method === "DELETE") {
        return Promise.resolve({ error: {
          code: "engine_object_declared", message: "another row declares this key",
          holder: { kind: "row", id: "anima-aesthetic" },
          next: { act: "complete", target: "anima-aesthetic" },
        } });
      }
      if (path.endsWith("/complete")) return Promise.resolve({ action: "attached", files: [] });
      return Promise.resolve({ hits: [] });
    });
    await mountRegistered();
    await click(labelled("消す: image/vae/orphan.safetensors"));
    await click(within(".engine-ledger-confirm", "消す"));
    const refusal = document.querySelector(".engine-refusal")!;
    expect(refusal.textContent).toContain("another row declares this key");
    expect(refusal.textContent).toContain("押さえているのは 登録済みの行: anima-aesthetic");
    await click(refusal.querySelector<HTMLButtonElement>(".engine-refusal-next") || undefined);
    expect(calls).toContain("POST api/admin/engines/image/models/anima-aesthetic/complete");
    expect(document.body.textContent).toContain("ダウンロードは発生していません");
  });

  it("reads a row's file state off the ledger rather than a second existence check", async () => {
    const split = {
      ...imageRow,
      model_rows: [{ id: "split", enabled: true, kind: "model", file_rows: [
        { s3Key: "image/checkpoints/split.safetensors", flag: "" },
        { s3Key: "image/vae/vae.safetensors", flag: "--vae" },
      ] }],
    };
    mockEngines([split], [
      { key: "image/checkpoints/split.safetensors", role_dir: "checkpoints", placement: "ok", state: "present", declared_by: [{ model_id: "split" }] },
      { key: "image/vae/vae.safetensors", role_dir: "vae", placement: "ok", state: "missing", declared_by: [{ model_id: "split", flag: "--vae" }] },
    ]);
    apiJSON.mockResolvedValue({ hits: [] });
    await mountRegistered();
    const card = document.querySelector<HTMLElement>('.engine-registered-card[aria-label="split"]')!;
    expect(card.querySelector("header .warn")?.textContent).toContain("1/2");
    const parts = card.querySelectorAll<HTMLElement>(".engine-registered-parts li");
    expect(parts).toHaveLength(2);
    // The chip is the mark of a line that needs a hand: the header already counts the parts, so
    // a present one carries none and the missing one is the only thing lit.
    expect(parts[0].querySelector(".engines-model-tag")).toBeNull();
    expect(parts[1].querySelector(".engines-model-tag")?.textContent).toBe("不足");
    expect(card.querySelector('[aria-label="編集: split"]')).toBeTruthy();
    // The surfaces ADR 0085 decision 8 removed.
    expect(card.querySelector('[aria-label="ファイルと部品: split"]')).toBeNull();
    expect(button("既存の S3 ファイルを登録")).toBeUndefined();
    expect(api).not.toHaveBeenCalledWith("api/admin/engines/image/storage");
    expect(api).not.toHaveBeenCalledWith("api/admin/engines/image/ingest");
  });

  // 🔴 The chip a present line drops must not drop for "nobody could look". A bucket that cannot
  // be listed is every line's state, and it is the one the card's header cannot count either.
  it("keeps the unchecked chip on every part when the bucket cannot be listed", async () => {
    const solo = { ...imageRow, model_rows: [{ id: "solo", enabled: true, kind: "model", file_rows: [{
      s3Key: "image/checkpoints/solo.safetensors", flag: "",
      bytes: 4_200_000_000, source_url: "https://civitai.com/model-versions/5038",
    }] }] };
    api.mockImplementation((path: string) => {
      if (path === "api/admin/engines") return Promise.resolve({ super_admin: true, engines: [solo] });
      if (path.endsWith("/objects")) return Promise.resolve({ error: { code: "engine_objects_unreadable", message: "cannot list" } });
      return Promise.resolve({});
    });
    apiJSON.mockResolvedValue({ hits: [] });
    await mountRegistered();
    const part = document.querySelector<HTMLElement>('.engine-registered-card[aria-label="solo"] .engine-registered-parts li')!;
    expect(part.querySelector(".engines-model-tag")?.textContent).toBe("未確認");
    // The size and the page are at the right end of the flag's line, in one span — the narrow
    // card folds the key under them instead of scattering them down its own rows.
    const meta = part.querySelector<HTMLElement>(".engine-registered-part-meta")!;
    expect(meta.textContent).toContain("4.2 GB");
    expect(meta.querySelector("a")?.textContent).toBe("配布元を見る");
  });

  it("requires an explicit VRAM confirmation before enabling an oversized registered model", async () => {
    mockEngines([{ ...imageRow, class: { vram_mib: 8192 }, model_rows: [{
      id: "large", kind: "model", enabled: false, vram_need_mib: 12288, vram_need_source: "declared",
      file_rows: [{ s3Key: "image/checkpoints/large.safetensors" }],
    }] }], [
      { key: "image/checkpoints/large.safetensors", role_dir: "checkpoints", placement: "ok", state: "present", declared_by: [{ model_id: "large" }] },
    ]);
    apiJSON.mockResolvedValue({});
    await mountRegistered();
    await click(labelled("有効にする: large"));
    expect(apiJSON).not.toHaveBeenCalled();
    expect(document.querySelector(".engine-registered-confirm")?.textContent).toContain("12288");
    await click(button("承知のうえで有効にする"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/large", "PUT", { enabled: true, confirm_vram: true });
  });
});

// A tabbed cell renders only the selected view, so leaving the catalogue for another tab unmounts
// it. These are about what has to survive that — and about what must NOT happen on the way back.
describe("catalogue memory", () => {
  const mountPane = async (paneId: string, view?: "search" | "registered") => {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => {
      root!.render(<EngineAddView engineKey="image" lora={false} paneId={paneId} initialView={view} />);
    });
    for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });
  };
  const leave = async () => {
    await act(async () => { root?.unmount(); });
    host?.remove();
    root = null;
    host = null;
  };
  const searches = () => apiJSON.mock.calls.filter((call) => String(call[0]).endsWith("/ingest/search")).length;
  const settle = async () => { for (const _ of [0, 1, 2]) await act(async () => { await Promise.resolve(); }); };

  // 🔴 The point of the memory is not that the page reappears — it is that the return costs
  // NOTHING. A search is an upstream request to Civitai or Hugging Face, and this screen used to
  // send one every time somebody looked at another tab and came back.
  it("brings the page back from another tab without searching again", async () => {
    mockEngines([imageRow, llmRow]);
    apiJSON.mockResolvedValue({ hits: [{ source: "civitai", ref: "22", model_ref: "7", name: "Image Example" }] });
    await mountPane("pane-1");
    expect(document.querySelector('[aria-label="Image Example"]')).toBeTruthy();
    expect(searches()).toBe(1);

    await leave();
    await mountPane("pane-1");
    expect(document.querySelector('[aria-label="Image Example"]')).toBeTruthy();
    expect(searches()).toBe(1);
  });

  it("keeps each role's own page across 文章 ⇄ 画像", async () => {
    mockEngines([imageRow, llmRow]);
    apiJSON.mockImplementation((path: string) => Promise.resolve(String(path).includes("/llm/")
      ? { hits: [{ source: "hf", ref: "org/text", model_ref: "org/text", name: "Text Example" }] }
      : { hits: [{ source: "civitai", ref: "22", model_ref: "7", name: "Image Example" }] }));
    await mountPane("pane-2");
    expect(document.querySelector('[aria-label="Image Example"]')).toBeTruthy();

    await click(button("文章"));
    await settle();
    expect(document.querySelector('[aria-label="Text Example"]')).toBeTruthy();
    const asked = searches();

    await click(button("画像"));
    await settle();
    expect(document.querySelector('[aria-label="Image Example"]')).toBeTruthy();
    expect(searches()).toBe(asked);
  });

  it("comes back to the registered face with its filter still in the box", async () => {
    mockEngines([{ ...imageRow, model_rows: [
      { id: "harbor", enabled: true, kind: "model", base_model: "sdxl", file_rows: [] },
      { id: "meadow", enabled: true, kind: "model", base_model: "sdxl", file_rows: [] },
    ] }], []);
    apiJSON.mockResolvedValue({ hits: [] });
    await mountPane("pane-3", "registered");
    const filter = document.querySelector<HTMLInputElement>(".engine-registered-search input")!;
    await act(async () => {
      // Through the prototype setter: React tracks the value it wrote, and a plain assignment
      // looks to it like no change at all.
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(filter, "harbor");
      filter.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await settle();
    expect(document.querySelector('.engine-registered-card[aria-label="meadow"]')).toBeNull();

    await leave();
    await mountPane("pane-3", "registered");
    expect(document.querySelector<HTMLInputElement>(".engine-registered-search input")?.value).toBe("harbor");
    expect(document.querySelector('.engine-registered-card[aria-label="harbor"]')).toBeTruthy();
    expect(document.querySelector('.engine-registered-card[aria-label="meadow"]')).toBeNull();
  });

  // Two panes are two catalogues: one reader searching Flux must not decide what the other sees.
  it("gives each pane its own memory", async () => {
    mockEngines([imageRow, llmRow]);
    apiJSON.mockResolvedValue({ hits: [{ source: "civitai", ref: "22", model_ref: "7", name: "Image Example" }] });
    await mountPane("pane-a");
    expect(searches()).toBe(1);
    await leave();
    await mountPane("pane-b");
    expect(searches()).toBe(2);
  });
});

describe("catalogue ledger identity", () => {
  const objects = [
    { key: "a", source: "hf:org/repo@abc/model.gguf", artifact_identity: "hf:org/repo@abc/model.gguf#sha256:a", role_dir: "checkpoints" as const, placement: "ok" as const, state: "present" as const },
    { key: "legacy", source: "hf:org/repo/model.gguf", role_dir: "checkpoints" as const, placement: "ok" as const, state: "present" as const },
    { key: "gone", source: "civitai:22", role_dir: "checkpoints" as const, placement: "ok" as const, state: "missing" as const },
    { key: "civitai-exact", source: "civitai:22/model.safetensors", role_dir: "checkpoints" as const, placement: "ok" as const, state: "present" as const },
  ];
  it("counts present objects of one source and never promotes an absent one", () => {
    expect(savedObjectsForHit({ source: "hf", ref: "org/repo", model_ref: "org/repo", name: "Repo" }, objects)).toHaveLength(2);
    expect(savedObjectsForHit({ source: "civitai", ref: "22", model_ref: "7", name: "V" }, objects).map((object) => object.key)).toEqual(["civitai-exact"]);
  });
});

// ADR 0089 follow-up. The edit dialog was three raw number boxes: an operator correcting a
// window had to price the KV cache in their own head, which is the arithmetic nobody should be
// asked to do — and is how a row came to declare 262,144 on a card that holds a quarter of it.
// The same verdict the ingest form draws, on the row as it already is, plus the fitted window
// as one press.
describe("editing a registered LLM row", () => {
  const registered = (extra: Record<string, unknown> = {}) => ({
    ...llmRow,
    class: { vram_mib: 22000 },
    classes: [{ id: "l4", label: "l4", vram_mib: 22000, types: [] }],
    model_rows: [{
      id: "qwen", kind: "gguf", enabled: true, context_tokens: 32768, max_output_tokens: 8192,
      file_rows: [{ s3Key: "llm/qwen.gguf", bytes: 12_040_883_104 }],
      kv_mib_per_1k_tokens: 64, context_length: 262144, ...extra,
    }],
  });

  it("prices the stored window and offers the largest the class holds", async () => {
    mockEngines([registered()]);
    apiJSON.mockResolvedValue({ hits: [] });
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => { root!.render(<EngineAddView engineKey="llm" lora={false} initialView="registered" />); });
    for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });

    await click(labelled("編集: qwen"));
    const fit = document.querySelector(".engine-registered-edit .engine-operation-fit");
    // 11,483 MiB of weights + 64 MiB/1k x 32,768 = 2,048 MiB of cache.
    expect(fit?.textContent).toContain("重み 11483 MiB");
    expect(fit?.textContent).toContain("KV キャッシュ 2048 MiB");
    expect(fit?.textContent).toContain("収まります");

    // 22,000 x 0.85 = 18,700, less 11,483 of weights = 7,217 MiB of room: 65,536 tokens cost
    // 4,096 and 131,072 would cost 8,192. The press writes the window AND the output cap.
    const refit = button("このクラスに収まる最大 65,536 にする")!;
    expect(refit).toBeTruthy();
    await click(refit);
    const inputs = document.querySelectorAll<HTMLInputElement>(".engine-registered-edit .engine-operation-grid input");
    expect(Array.from(inputs).map((i) => i.value)).toContain("65536");
    expect(Array.from(inputs).map((i) => i.value)).toContain("8192");
  });

  it("offers no re-fit when the row has no geometry, rather than one computed from nothing", async () => {
    mockEngines([registered({ kv_mib_per_1k_tokens: undefined, context_length: undefined })]);
    apiJSON.mockResolvedValue({ hits: [] });
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => { root!.render(<EngineAddView engineKey="llm" lora={false} initialView="registered" />); });
    for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });

    await click(labelled("編集: qwen"));
    expect(Array.from(document.querySelectorAll("button")).some((b) => b.textContent?.includes("収まる最大"))).toBe(false);
    expect(document.querySelector(".engine-registered-edit")?.textContent).toContain("KV キャッシュは読めなかった");
  });
});
