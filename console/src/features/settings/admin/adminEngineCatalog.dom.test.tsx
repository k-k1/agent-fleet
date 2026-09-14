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

import { EngineAddView, exactReusableStorage, savedFilesForHit } from "./adminEngineAdd.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const imageRow = {
  key: "image",
  api: "images",
  provider: "sdcpp",
  managed: true,
  file_flags: ["", "--vae", "--t5xxl"],
  base_models: ["sdxl"],
  model_rows: [{ id: "existing", enabled: true, file_rows: [{ s3Key: "image/existing.safetensors" }] }],
};
const llmRow = { key: "llm", api: "chat", provider: "llamacpp", managed: true, model_rows: [] };

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
const click = async (element: HTMLElement | undefined) => {
  expect(element).toBeTruthy();
  await act(async () => { element!.dispatchEvent(new MouseEvent("click", { bubbles: true })); });
  await act(async () => { await Promise.resolve(); });
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
    api.mockImplementation((path: string) => {
      if (path === "api/admin/engines") return Promise.resolve({ super_admin: true, engines: [imageRow, llmRow] });
      if (path.endsWith("/storage")) return Promise.resolve({ files: [] });
      if (path.endsWith("/ingest")) return Promise.resolve({ jobs: [] });
      return Promise.resolve({});
    });
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

  it("renders genuinely role-specific image and LLM card facts", async () => {
    api.mockImplementation((path: string) => {
      if (path === "api/admin/engines") return Promise.resolve({ super_admin: true, engines: [imageRow, llmRow] });
      if (path.endsWith("/storage")) return Promise.resolve({ files: [] });
      if (path.endsWith("/ingest")) return Promise.resolve({ jobs: [] });
      return Promise.resolve({});
    });
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
    api.mockImplementation((path: string) => {
      if (path === "api/admin/engines") return Promise.resolve({ super_admin: true, engines: [imageRow] });
      if (path.endsWith("/storage")) return Promise.resolve({ files: [] });
      if (path.endsWith("/ingest")) return Promise.resolve({ jobs: [] });
      return Promise.resolve({});
    });
    apiJSON.mockResolvedValue({ hits: [{ source: "civitai", ref: "22", model_ref: "7", name: "Example", preview_url: "https://example.test/a.jpg" }] });
    await mount();
    const thumb = document.querySelector<HTMLButtonElement>(".engine-catalog-thumb")!;
    expect(thumb).toBeTruthy();
    expect(document.querySelectorAll(".engine-catalog-thumb")).toHaveLength(1);
    thumb.focus();
    await click(thumb);
    expect(document.querySelector(".engine-catalog-lightbox img")).toBeTruthy();
    await act(async () => { document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })); });
    await act(async () => { await Promise.resolve(); });
    expect(document.querySelector(".engine-catalog-lightbox")).toBeNull();
    expect(document.activeElement).toBe(thumb);
  });

  it("loads versions and files from one card operation instead of entering a step workflow", async () => {
    api.mockImplementation((path: string) => {
      if (path === "api/admin/engines") return Promise.resolve({ super_admin: true, engines: [imageRow] });
      if (path.endsWith("/storage")) return Promise.resolve({ files: [] });
      if (path.endsWith("/ingest")) return Promise.resolve({ jobs: [] });
      return Promise.resolve({});
    });
    apiJSON.mockImplementation((path: string) => {
      if (path.endsWith("/ingest/search")) return Promise.resolve({ hits: [{ source: "civitai", ref: "22", model_ref: "7", name: "Example" }] });
      if (path.endsWith("/ingest/versions")) return Promise.resolve({ versions: [{ ref: "22", name: "v2" }, { ref: "21", name: "v1" }] });
      if (path.endsWith("/ingest/files")) return Promise.resolve({ files: [{ name: "model.safetensors" }, { name: "vae.safetensors" }] });
      return Promise.resolve({});
    });
    await mount();
    const card = document.querySelector(".engine-catalog-card")!;
    expect(card.getAttribute("aria-label")).toBe("Example");
    expect(button("追加")?.getAttribute("aria-label")).toBe("追加: Example");
    expect(button("追加")?.classList.contains("ui-btn-primary")).toBe(true);
    await click(button("追加"));
    await act(async () => { await Promise.resolve(); });
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/ingest/versions", "POST", {
      source: "civitai", ref: "22", model_ref: "7",
    });
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/ingest/files", "POST", {
      source: { civitai: { versionId: 22, file: "" } },
    });
    expect(document.querySelector(".engines-wizard-rail")).toBeNull();
    expect(document.querySelector(".engine-catalog-operation")?.getAttribute("aria-labelledby")).toBeTruthy();
    expect(document.querySelector(".engine-catalog-operation .ui-modal-title")?.textContent).toContain("Example");
    expect(document.querySelectorAll(".engine-operation-grid select").length).toBeGreaterThan(1);
  });

  it("includes the LLM KV cache in the operation VRAM guard", async () => {
    const row = { ...llmRow, class: { vram_mib: 21000 } };
    api.mockImplementation((path: string) => {
      if (path === "api/admin/engines") return Promise.resolve({ super_admin: true, engines: [row] });
      if (path.endsWith("/storage")) return Promise.resolve({ files: [] });
      if (path.endsWith("/ingest")) return Promise.resolve({ jobs: [] });
      return Promise.resolve({});
    });
    apiJSON.mockImplementation((path: string) => {
      if (path.endsWith("/ingest/search")) return Promise.resolve({ hits: [{ source: "hf", ref: "org/model", model_ref: "org/model", name: "Large LLM" }] });
      if (path.endsWith("/ingest/versions")) return Promise.resolve({ versions: [{ ref: "main", name: "main" }] });
      if (path.endsWith("/ingest/files")) return Promise.resolve({ files: [{ name: "model.gguf", bytes: 18_556_689_568 }] });
      if (path.endsWith("/ingest/resolve")) return Promise.resolve({ bytes: 18_556_689_568, can_ingest: true, license: "apache-2.0", context_length: 262144, kv_mib_per_1k_tokens: 96 });
      return Promise.resolve({});
    });
    await mount();
    await click(button("追加"));
    for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });
    const fit = document.querySelector(".engine-operation-fit");
    expect(fit?.textContent).toContain("重み 17697 MiB");
    expect(fit?.textContent).toContain("KV キャッシュ 24576 MiB");
    expect(fit?.textContent).toContain("合計 42273 MiB");
    expect(document.querySelector(".engine-operation-check.warn")?.textContent).toContain("重みと KV キャッシュ");
  });

  it("keeps restrictions, VAE provenance, and parameter hints in the operation", async () => {
    api.mockImplementation((path: string) => {
      if (path === "api/admin/engines") return Promise.resolve({ super_admin: true, engines: [imageRow] });
      if (path.endsWith("/storage")) return Promise.resolve({ files: [] });
      if (path.endsWith("/ingest")) return Promise.resolve({ jobs: [] });
      return Promise.resolve({});
    });
    apiJSON.mockImplementation((path: string) => {
      if (path.endsWith("/ingest/search")) return Promise.resolve({ hits: [{ source: "civitai", ref: "22", model_ref: "7", name: "Restricted", restrictions: ["no_derivatives"], commercial_use: "no", login_required: "yes" }] });
      if (path.endsWith("/ingest/versions")) return Promise.resolve({ versions: [{ ref: "22", name: "v2" }] });
      if (path.endsWith("/ingest/files")) return Promise.resolve({ files: [{ name: "model.safetensors" }] });
      if (path.endsWith("/ingest/resolve")) return Promise.resolve({
        bytes: 2_000_000_000, can_ingest: false, commercial_use: "no", login_required: true,
        restrictions: ["no_derivatives"], gated_needs_acceptance: true,
        params_hint: { steps: 28, sampler: "dpmpp_2m" }, params_hint_quote: "Use 28 steps",
        vae_bundled: "no", family_vae: { repo: "org/vae", file: "vae.safetensors", bytes: 335_000_000, license: "mit", unreachable: true },
      });
      return Promise.resolve({});
    });
    await mount();
    const card = document.querySelector('[aria-label="Restricted"]')!;
    expect(card.textContent).toContain("派生不可");
    expect(card.textContent).toContain("非商用");
    expect(card.textContent).toContain("要ログイン");
    await click(button("追加"));
    for (const _ of [0, 1, 2, 3]) await act(async () => { await Promise.resolve(); });
    expect(document.body.textContent).toContain("ログイン済みのアカウント");
    expect(document.body.textContent).toContain("Use 28 steps");
    expect(document.body.textContent).toContain("org/vae/vae.safetensors");
    expect(document.body.textContent).toContain("335 MB");
    const vae = Array.from(document.querySelectorAll<HTMLInputElement>('input[type="checkbox"]')).find((input) => input.parentElement?.textContent?.includes("org/vae"));
    expect(vae?.disabled).toBe(true);
    const steps = Array.from(document.querySelectorAll("label")).find((label) => label.querySelector("span")?.textContent === "ステップ数")?.querySelector("input") as HTMLInputElement;
    expect(steps.value).toBe("28");
  });

  it("invalidates pagination when the visible query changes", async () => {
    api.mockImplementation((path: string) => {
      if (path === "api/admin/engines") return Promise.resolve({ super_admin: true, engines: [imageRow] });
      if (path.endsWith("/storage")) return Promise.resolve({ files: [] });
      if (path.endsWith("/ingest")) return Promise.resolve({ jobs: [] });
      return Promise.resolve({});
    });
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

  it("aggregates registered multipart storage from existence checks, not ingest jobs", async () => {
    const registered = {
      ...imageRow,
      model_rows: [{ id: "split", enabled: true, file_rows: [
        { s3Key: "image/split.safetensors", flag: "" },
        { s3Key: "image/vae.safetensors", flag: "--vae" },
      ] }],
    };
    api.mockImplementation((path: string) => {
      if (path === "api/admin/engines") return Promise.resolve({ super_admin: true, engines: [registered] });
      if (path.endsWith("/storage")) return Promise.resolve({ files: [
        { s3_key: "image/split.safetensors", state: "present", model_ids: ["split"] },
        { s3_key: "image/vae.safetensors", state: "missing", model_ids: ["split"] },
      ] });
      if (path.endsWith("/ingest")) return Promise.resolve({ jobs: [{ id: "old", model_id: "split", state: "done" }] });
      return Promise.resolve({});
    });
    await mountRegistered();
    const card = document.querySelector<HTMLElement>('.engine-registered-card[aria-label="split"]')!;
    expect(card).toBeTruthy();
    expect(card.querySelector("header .warn")?.textContent).toContain("1/2");
    expect(card.querySelector("header .warn")?.textContent).toContain("一部不足");
    expect(card.querySelectorAll(".engine-registered-parts li")).toHaveLength(2);
    expect(card.querySelector('[aria-label="編集: split"]')).toBeTruthy();
    expect(card.querySelector('[aria-label="ファイルと部品: split"]')).toBeTruthy();
    expect(document.querySelector(".engine-registered-search input")).toBeTruthy();
    await click(card.querySelector<HTMLButtonElement>('[aria-label="編集: split"]') || undefined);
    expect(document.querySelector(".engine-registered-edit .ui-modal-title")?.textContent).toContain("split");
    expect(document.querySelectorAll(".engine-registered-edit input").length).toBeGreaterThan(1);
  });

  it("requires an explicit VRAM confirmation before enabling an oversized registered model", async () => {
    const oversized = { ...imageRow, class: { vram_mib: 8192 }, model_rows: [{
      id: "large", kind: "model", enabled: false, vram_need_mib: 12288, vram_need_source: "declared",
      file_rows: [{ s3Key: "image/large.safetensors" }],
    }] };
    api.mockImplementation((path: string) => {
      if (path === "api/admin/engines") return Promise.resolve({ super_admin: true, engines: [oversized] });
      if (path.endsWith("/storage")) return Promise.resolve({ files: [{ s3_key: "image/large.safetensors", state: "present", model_ids: ["large"] }] });
      if (path.endsWith("/ingest")) return Promise.resolve({ jobs: [] });
      return Promise.resolve({});
    });
    apiJSON.mockResolvedValue({});
    await mountRegistered();
    await click(document.querySelector<HTMLButtonElement>('[aria-label="有効にする: large"]') || undefined);
    expect(apiJSON).not.toHaveBeenCalled();
    expect(document.querySelector(".engine-registered-confirm")?.textContent).toContain("12288");
    await click(button("承知のうえで有効にする"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/models/large", "PUT", { enabled: true, confirm_vram: true });
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
});

describe("catalogue storage identity", () => {
  const files = [
    { s3_key: "a", source: "hf:org/repo@abc/model.gguf", state: "present" as const, model_ids: [] },
    { s3_key: "legacy", source: "hf:org/repo/model.gguf", state: "present" as const, model_ids: [] },
    { s3_key: "gone", source: "civitai:22", state: "missing" as const, model_ids: [] },
    { s3_key: "ambiguous", source: "civitai:22", state: "present" as const, model_ids: [] },
    { s3_key: "civitai-exact", source: "civitai:22/model.safetensors", state: "present" as const, model_ids: [] },
  ];
  it("counts concrete source files but reuses only present immutable identities", () => {
    expect(savedFilesForHit({ source: "hf", ref: "org/repo", model_ref: "org/repo", name: "Repo" }, files)).toHaveLength(2);
    expect(exactReusableStorage(files, "hf", "org/repo", "abc", "model.gguf")?.s3_key).toBe("a");
    expect(exactReusableStorage(files, "hf", "org/repo", "", "model.gguf")).toBeUndefined();
    expect(exactReusableStorage(files, "civitai", "7", "22", "other.safetensors")).toBeUndefined();
    expect(exactReusableStorage(files.filter((file) => file.s3_key !== "civitai-exact"), "civitai", "7", "22", "model.safetensors")).toBeUndefined();
    expect(exactReusableStorage(files, "civitai", "7", "22", "model.safetensors")?.s3_key).toBe("civitai-exact");
  });
});
