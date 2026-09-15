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

async function mountRegistered() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => { root!.render(<EngineAddView engineKey="image" lora={false} initialView="registered" />); });
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
    expect(sent?.id).toBe("anima-aesthetic-v1-1");
    expect(sent?.base_model).toBe("anima");
    expect(sent?.license_accepted).toBe(true);
    for (const gone of ["s3Key", "reuse_s3_key", "attach", "replace", "file_flag", "with_family_parts", "with_family_vae"]) {
      expect(sent?.[gone]).toBeUndefined();
    }
    expect(document.body.textContent).toContain("取り込みを開始しました");
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
    expect(document.querySelector(".engine-operation-footer")?.textContent).toContain("モデル族");
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
    expect(fit?.textContent).toContain("KV キャッシュ 24576 MiB");
    expect(fit?.textContent).toContain("合計 42273 MiB");
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
    expect(card.querySelectorAll(".engine-registered-parts li")).toHaveLength(2);
    expect(card.querySelector('[aria-label="編集: split"]')).toBeTruthy();
    // The surfaces ADR 0085 decision 8 removed.
    expect(card.querySelector('[aria-label="ファイルと部品: split"]')).toBeNull();
    expect(button("既存の S3 ファイルを登録")).toBeUndefined();
    expect(api).not.toHaveBeenCalledWith("api/admin/engines/image/storage");
    expect(api).not.toHaveBeenCalledWith("api/admin/engines/image/ingest");
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
