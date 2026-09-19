// The repository card for a chat engine (ADR 0089): one card per quantisation repository, the
// sizes already taken in marked, the rest offered with a verdict and one press.
//
// The fault these cover is the one the operator reported: `unsloth/Qwen3.8-27B-GGUF` showed
// "KV キャッシュ 66560 MiB" whatever file was picked, which reads as "this can never run" about a
// model that runs on the same card at a sane window — and adding a second quantisation of a model
// already in the catalogue meant finding the repository again in the 探す tab.
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

const REPO = "unsloth/Qwen3.8-27B-GGUF";

// The real listing, read live on 2026-09-18. The sizes are the published ones and the geometry
// behind 260 MiB/1k is 65 blocks x 4 KV heads x (256+256) x 2 bytes.
const FILES = [
  { name: "imatrix_unsloth.gguf", bytes: 10_000_000, role: "imatrix" },
  { name: "mmproj-F16.gguf", bytes: 930_000_000, role: "projector" },
  { name: "Qwen3.8-27B-UD-IQ2_XXS.gguf", bytes: 7_270_000_000, role: "model" },
  { name: "Qwen3.8-27B-UD-IQ2_S.gguf", bytes: 8_370_000_000, role: "model" },
  { name: "Qwen3.8-27B-UD-IQ4_XS.gguf", bytes: 14_250_000_000, role: "model" },
];

const held = {
  id: "qwen3_8_27b_ud_iq2_xxs", kind: "gguf", enabled: true, default: true,
  display_name: REPO, context_tokens: 32768,
  source: `hf:${REPO}/Qwen3.8-27B-UD-IQ2_XXS.gguf`,
  file_rows: [{ s3Key: "llm/models/Qwen3.8-27B-UD-IQ2_XXS.gguf", bytes: 7_270_000_000 }],
};

const llmRow = {
  key: "llm",
  api: "chat",
  provider: "llamacpp",
  managed: true,
  class: { id: "g6.xlarge", label: "g6.xlarge", vram_mib: 22000, types: [] },
  classes: [
    { id: "g6.xlarge", label: "g6.xlarge", vram_mib: 22000, types: [] },
    { id: "g6e.xlarge", label: "g6e.xlarge", vram_mib: 46068, types: [] },
  ],
  model_rows: [held],
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

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => { root!.render(<EngineAddView engineKey="llm" lora={false} initialView="registered" />); });
  await flush();
}

const button = (label: string) => Array.from(document.querySelectorAll<HTMLButtonElement>("button"))
  .find((candidate) => candidate.textContent === label);

const rowFor = (quant: string) => Array.from(document.querySelectorAll<HTMLElement>(".engine-repo-quants li"))
  .find((li) => li.querySelector(".engine-repo-quant-name")?.textContent === quant);

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
  await flush();
}

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  apiJSON.mockImplementation((path: string) => path.endsWith("/ingest/files")
    ? Promise.resolve({ files: FILES, kv_mib_per_1k_tokens: 260, kv_from: "Qwen3.8-27B-UD-IQ4_XS.gguf" })
    : Promise.resolve({}));
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

describe("the repository card", () => {
  it("heads the group with the repository and says how many are taken in", async () => {
    mockEngineAPI([llmRow]);
    await mount();
    const head = document.querySelector(".engine-registered-group-head")!;
    expect(head.textContent).toContain(REPO);
    expect(head.textContent).toContain("取り込み済み 1 件");
  });

  // 🔴 Not on mount. A catalogue of eight repositories would open as eight upstream requests
  // nobody asked for, at a host that sheds load with a 503.
  it("reads the repository only when the ladder is opened", async () => {
    mockEngineAPI([llmRow]);
    await mount();
    expect(apiJSON).not.toHaveBeenCalled();

    await click(button("この配布元の他の量子化を見る"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/llm/ingest/files", "POST",
      { source: { hf: { repo: REPO } } });
  });

  it("marks what is here, offers what is not, and drops what is not a model", async () => {
    mockEngineAPI([llmRow]);
    await mount();
    await click(button("この配布元の他の量子化を見る"));

    expect(Array.from(document.querySelectorAll(".engine-repo-quant-name")).map((node) => node.textContent))
      .toEqual(["UD-IQ2_XXS", "UD-IQ2_S", "UD-IQ4_XS"]);
    expect(rowFor("UD-IQ2_XXS")!.className).toContain("held");
    expect(rowFor("UD-IQ2_S")!.className).not.toContain("held");
  });

  // The whole point: at the window the row actually declares, a 2-bit 27B fits a 22 GB card and
  // the 4-bit one does not. The old screen could say neither.
  it("prices every size against the class at the window the rows declare", async () => {
    mockEngineAPI([llmRow]);
    await mount();
    await click(button("この配布元の他の量子化を見る"));

    expect(rowFor("UD-IQ2_S")!.textContent).toContain("収まります");
    expect(rowFor("UD-IQ4_XS")!.textContent).toContain("ぎりぎり");
  });

  it("re-prices the ladder when the window is changed, without writing anything", async () => {
    mockEngineAPI([llmRow]);
    await mount();
    await click(button("この配布元の他の量子化を見る"));
    const window = document.querySelector<HTMLInputElement>(".engine-repo-ladder-head input")!;
    expect(window.value).toBe("32768");

    await typeInto(window, "262144");
    expect(rowFor("UD-IQ2_S")!.textContent).toContain("このクラスでは入りません");
    // 🔴 Exploring a window must not write one: a row's window belongs to that row.
    expect(apiJSON).toHaveBeenCalledTimes(1);
  });

  it("names the rung that would hold what this class will not", async () => {
    mockEngineAPI([llmRow]);
    await mount();
    await click(button("この配布元の他の量子化を見る"));
    const window = document.querySelector<HTMLInputElement>(".engine-repo-ladder-head input")!;
    await typeInto(window, "65536");
    expect(rowFor("UD-IQ2_S")!.textContent).toContain("g6e.xlarge");
  });

  it("opens the ingest dialog on the exact file, pre-filled from the repository", async () => {
    mockEngineAPI([llmRow]);
    await mount();
    await click(button("この配布元の他の量子化を見る"));
    await click(rowFor("UD-IQ2_S")!.querySelector<HTMLButtonElement>("button")!);

    // 🔴 The revision is `main`, not the URL it was parsed out of. `hit.ref` is the HF revision
    // field, so smuggling a blob URL through it sent `revision: "https://huggingface.co/…"`
    // upstream — which this test caught, and which is why the dialog takes its own `initialRef`.
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/llm/ingest/resolve", "POST", {
      kind: "gguf",
      source: { hf: { repo: REPO, file: "Qwen3.8-27B-UD-IQ2_S.gguf", revision: "main" } },
    });
  });
});
