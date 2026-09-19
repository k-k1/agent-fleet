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
import { clearCatalogMemory } from "./catalogMemory.ts";

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
    if (path.endsWith("/objects")) return Promise.resolve({ objects: [] });
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
  // 🔴 The catalogue's memory is module scope and every mount here shares one key
  //    (no paneId), so without this a case reads the previous one's page and the
  //    search it asserts is never sent.
  clearCatalogMemory();
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
          plan: {
            plan_token: "p", id: "adapter",
            files: [{ name: "adapter.gguf", action: "download", bytes: 1024 }],
            bytes_to_download: 1024,
          },
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
    expect(document.querySelector(".engine-operation-footer")?.textContent).toContain("ファミリーを選んでください");
  });

  // The card draws a 92x108 box and the lightbox fills the screen, so they must not load the
  // same file. Measured 2026-09-15 against the live CDN: the URL Civitai publishes is the
  // ORIGINAL, ~1-4 MB a row, and twenty of those is what fails to arrive on a phone — a failed
  // <img> is exactly the broken-image glyph the operator photographed.
  it("loads the card-sized example on the card and the large one only in the lightbox", async () => {
    mockEngineAPI([imageRow]);
    apiJSON.mockImplementation((path: string) => {
      if (path.endsWith("/ingest/search")) {
        return Promise.resolve({ hits: [{
          source: "civitai", ref: "22", model_ref: "7", name: "Example",
          preview_url: "https://image.civitai.com/a/b/anim=false,width=1024/1.jpeg",
          thumb_url: "https://image.civitai.com/a/b/anim=false,width=256/1.jpeg",
        }] });
      }
      return Promise.resolve({});
    });

    await mount("image");
    const thumb = document.querySelector<HTMLImageElement>(".engine-catalog-thumb img")!;
    expect(thumb.getAttribute("src")).toBe("https://image.civitai.com/a/b/anim=false,width=256/1.jpeg");

    await click(document.querySelector<HTMLButtonElement>(".engine-catalog-thumb")!);
    expect(document.querySelector(".engine-catalog-lightbox img")?.getAttribute("src"))
      .toBe("https://image.civitai.com/a/b/anim=false,width=1024/1.jpeg");
  });

  // Reaching the bottom asks for the next page by itself — and stops asking when the answer
  // brought nothing. Without that guard an upstream error that still answers a cursor turns one
  // landing at the end of the list into an endless request loop.
  it("loads the next page on reaching the end, and stops when a page adds no row", async () => {
    mockEngineAPI([imageRow]);
    let page = 0;
    let empty = false;
    apiJSON.mockImplementation((path: string) => {
      if (!path.endsWith("/ingest/search")) return Promise.resolve({});
      page += 1;
      return Promise.resolve({
        hits: empty ? [] : [{ source: "civitai", ref: `v${page}`, model_ref: `m${page}`, name: `Example ${page}` }],
        next_cursor: "more",
      });
    });

    const observers: (() => void)[] = [];
    class FakeObserver {
      constructor(private readonly fire: (entries: { isIntersecting: boolean }[]) => void) {
        observers.push(() => this.fire([{ isIntersecting: true }]));
      }
      observe() {}
      disconnect() {}
    }
    vi.stubGlobal("IntersectionObserver", FakeObserver);
    try {
      await mount("image");
      expect(page).toBe(1);

      const reachTheEnd = async () => {
        const fire = observers[observers.length - 1];
        expect(fire).toBeTruthy();
        await act(async () => { fire(); });
        await flush();
      };
      await reachTheEnd();
      expect(page).toBe(2);
      expect(document.querySelectorAll(".engine-catalog-card").length).toBe(2);

      // The page that answers nothing is the last automatic one, however often the end of the
      // list comes back into view.
      empty = true;
      await reachTheEnd();
      expect(page).toBe(3);
      await reachTheEnd();
      await reachTheEnd();
      expect(page).toBe(3);

      // The button is the deliberate way past that guard, and still works.
      await click(button("さらに読み込む"));
      expect(page).toBe(4);
    } finally {
      vi.unstubAllGlobals();
    }
  });
});
