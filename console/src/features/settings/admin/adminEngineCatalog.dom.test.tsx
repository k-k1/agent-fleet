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

    await click(button("文章"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/llm/ingest/search", "POST", {
      q: "", source: "hf", sort: "updated", lora: false,
    });
    expect(button("Civitai")).toBeUndefined();
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
    expect(document.querySelector(".engines-model-storage.partial")?.textContent).toContain("1/2");
    expect(document.querySelector(".engines-model-storage.partial")?.textContent).toContain("一部不足");
  });
});

describe("catalogue storage identity", () => {
  const files = [
    { s3_key: "a", source: "hf:org/repo@abc/model.gguf", state: "present" as const, model_ids: [] },
    { s3_key: "legacy", source: "hf:org/repo/model.gguf", state: "present" as const, model_ids: [] },
    { s3_key: "gone", source: "civitai:22", state: "missing" as const, model_ids: [] },
  ];
  it("counts concrete source files but reuses only present immutable identities", () => {
    expect(savedFilesForHit({ source: "hf", ref: "org/repo", model_ref: "org/repo", name: "Repo" }, files)).toHaveLength(2);
    expect(exactReusableStorage(files, "hf", "org/repo", "abc", "model.gguf")?.s3_key).toBe("a");
    expect(exactReusableStorage(files, "hf", "org/repo", "", "model.gguf")).toBeUndefined();
    expect(exactReusableStorage(files, "civitai", "7", "22", "model.safetensors")).toBeUndefined();
  });
});
