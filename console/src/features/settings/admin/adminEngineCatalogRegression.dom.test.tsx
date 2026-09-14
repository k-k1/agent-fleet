import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { EngineAddView } from "./adminEngineAdd.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const imageRow = {
  key: "image",
  api: "images",
  provider: "sdcpp",
  managed: true,
  file_flags: ["", "--vae"],
  base_models: ["sdxl"],
  model_rows: [],
};

const llmRow = {
  key: "llm",
  api: "chat",
  provider: "llamacpp",
  managed: true,
  file_flags: [],
  model_rows: [{ id: "qwen3", kind: "gguf", enabled: true }],
};

function mockEngineAPI(rows: Record<string, unknown>[]) {
  api.mockImplementation((path: string) => {
    if (path === "api/admin/engines") return Promise.resolve({ super_admin: true, engines: rows });
    if (path.endsWith("/storage")) return Promise.resolve({ files: [] });
    if (path.endsWith("/ingest")) return Promise.resolve({ jobs: [] });
    return Promise.resolve({});
  });
}

async function flush(turns = 4) {
  for (let i = 0; i < turns; i += 1) {
    await act(async () => { await Promise.resolve(); });
  }
}

async function mount(engineKey: "image" | "llm", lora = false) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => { root!.render(<EngineAddView engineKey={engineKey} lora={lora} />); });
  await flush();
}

const button = (label: string) => Array.from(document.querySelectorAll<HTMLButtonElement>("button"))
  .find((candidate) => candidate.textContent === label);

async function click(element: HTMLElement | undefined) {
  expect(element).toBeTruthy();
  await act(async () => { element!.dispatchEvent(new MouseEvent("click", { bubbles: true })); });
  await flush();
}

async function typeInto(element: HTMLInputElement, value: string) {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(element, value);
    element.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function select(element: HTMLSelectElement, value: string) {
  await act(async () => {
    element.value = value;
    element.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

function deferred<T>() {
  let resolve!: (value: T) => void;
  const promise = new Promise<T>((done) => { resolve = done; });
  return { promise, resolve };
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  delete (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT;
});

describe("model catalogue operation regressions", () => {
  it.each([
    ["the compact version reference", "civitai:782002"],
    ["the version-only model page", "https://civitai.com/models/?modelVersionId=782002"],
    ["the civitai.red version-only model page", "https://civitai.red/models/?modelVersionId=782002"],
  ])("keeps legacy Civitai input working for %s", async (_case, source) => {
    mockEngineAPI([imageRow]);
    apiJSON.mockImplementation((path: string) => {
      if (path.endsWith("/ingest/search")) return Promise.resolve({ hits: [] });
      if (path.endsWith("/ingest/files")) {
        return Promise.resolve({ files: [{ name: "model.safetensors" }, { name: "vae.safetensors" }] });
      }
      if (path.endsWith("/ingest/versions")) {
        return Promise.resolve({ error: { code: "bad_source", message: "model_ref is unavailable" } });
      }
      return Promise.resolve({});
    });

    await mount("image");
    await click(button("URL・リポジトリを指定"));
    await select(document.querySelector<HTMLSelectElement>(".engine-operation-manual select")!, "civitai");
    const input = document.querySelector<HTMLInputElement>(".engine-operation-manual input")!;
    await typeInto(input, source);
    await click(button("調べる"));

    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/ingest/files", "POST", {
      source: { civitai: { versionId: 782002, file: "" } },
    });
  });

  it("keeps the latest version's files when older requests finish last", async () => {
    mockEngineAPI([imageRow]);
    const currentFiles = deferred<{ files: { name: string }[] }>();
    const olderFiles = deferred<{ files: { name: string }[] }>();
    apiJSON.mockImplementation((path: string, _method: string, body: {
      source?: { civitai?: { versionId?: number } };
    }) => {
      if (path.endsWith("/ingest/search")) {
        return Promise.resolve({ hits: [{ source: "civitai", ref: "22", model_ref: "7", name: "Example" }] });
      }
      if (path.endsWith("/ingest/versions")) {
        return Promise.resolve({ versions: [{ ref: "22", name: "current" }, { ref: "21", name: "older" }] });
      }
      if (path.endsWith("/ingest/files")) {
        return body.source?.civitai?.versionId === 22 ? currentFiles.promise : olderFiles.promise;
      }
      return Promise.resolve({});
    });

    await mount("image");
    await click(button("追加"));
    const version = Array.from(document.querySelectorAll<HTMLSelectElement>(".engine-catalog-operation select"))
      .find((candidate) => Array.from(candidate.options).some((option) => option.value === "21"))!;
    expect(version).toBeTruthy();
    await select(version, "21");

    await act(async () => {
      olderFiles.resolve({ files: [{ name: "older-a.safetensors" }, { name: "older-b.safetensors" }] });
      await Promise.resolve();
    });
    await flush();
    expect(document.querySelector("option[value='older-a.safetensors']")).toBeTruthy();

    await act(async () => {
      currentFiles.resolve({ files: [{ name: "current-a.safetensors" }, { name: "current-b.safetensors" }] });
      await Promise.resolve();
    });
    await flush();
    expect(document.querySelector("option[value='older-a.safetensors']")).toBeTruthy();
    expect(document.querySelector("option[value='current-a.safetensors']")).toBeNull();
  });

  it("requires a registered model for an LLM LoRA instead of accepting a family suggestion", async () => {
    mockEngineAPI([llmRow]);
    apiJSON.mockImplementation((path: string) => {
      if (path.endsWith("/ingest/search")) {
        return Promise.resolve({ hits: [{ source: "hf", ref: "org/adapter", model_ref: "org/adapter", name: "Adapter" }] });
      }
      if (path.endsWith("/ingest/versions")) return Promise.resolve({ versions: [{ ref: "main", name: "main" }] });
      if (path.endsWith("/ingest/files")) return Promise.resolve({ files: [{ name: "adapter.gguf" }] });
      if (path.endsWith("/ingest/resolve")) {
        return Promise.resolve({
          bytes: 1024,
          can_ingest: true,
          license: "apache-2.0",
          base_model_suggest: "qwen-family",
        });
      }
      return Promise.resolve({});
    });

    await mount("llm", true);
    await click(button("追加"));
    await flush(8);
    const base = Array.from(document.querySelectorAll<HTMLSelectElement>(".engine-catalog-operation select"))
      .find((candidate) => Array.from(candidate.options).some((option) => option.value === "qwen3"))!;
    expect(base).toBeTruthy();
    expect(base.value).toBe("");

    const accept = Array.from(document.querySelectorAll<HTMLInputElement>(".engine-operation-check input"))
      .find((candidate) => candidate.parentElement?.textContent?.includes("ライセンス"))!;
    await click(accept);

    expect(button("取り込む")?.disabled).toBe(true);
    expect(document.querySelector(".engine-operation-footer")?.textContent).toContain("モデル族");
  });
});
