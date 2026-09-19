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

async function mount(engineKey: "llm" | "image" = "llm", lora = false) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => { root!.render(<EngineAddView engineKey={engineKey} lora={lora} initialView="registered" />); });
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

// The same ladders, reached from the ROW rather than from a group heading — and the image role's
// own, which had none at all. The entry point matters as much as the table: filing a GGUF under
// its repository needs a `display_name`, so every row nobody has read a model page for was filed
// under "" and could never reach the ladder, which is most of an older deployment's catalogue.
describe("the ladders a card opens", () => {
  const bare = {
    ...held, id: "qwen_bare", display_name: undefined,
    source: `hf:${REPO}/Qwen3.8-27B-UD-IQ2_S.gguf`,
  };

  const anima = {
    id: "animaika_v47", kind: "model", enabled: true, base_model: "sdxl",
    display_name: "Animalka", version_name: "v4.7", source: "civitai:5038",
    file_rows: [{ s3Key: "image/checkpoints/animaika_v47.safetensors", bytes: 4_200_000_000 }],
  };
  const imageRow = {
    key: "image", api: "images", provider: "comfy", managed: true, base_models: ["sdxl"],
    class: { id: "g6.xlarge", label: "g6.xlarge", vram_mib: 22000, types: [] },
    classes: [{ id: "g6.xlarge", label: "g6.xlarge", vram_mib: 22000, types: [] }],
    model_rows: [anima],
  };
  const VERSIONS = {
    model_ref: "4451",
    versions: [
      { ref: "5038", name: "v4.7", bytes: 4_200_000_000, published_at: "2026-08-01T00:00:00Z" },
      { ref: "6000", name: "v5.0", bytes: 4_300_000_000 },
      { ref: "7000", name: "v3.1" },
    ],
  };

  it("offers the other sizes from a row with no name, where no group heading can", async () => {
    mockEngineAPI([{ ...llmRow, model_rows: [bare] }]);
    await mount();
    expect(button("この配布元の他の量子化を見る")).toBeUndefined();

    await click(button("別サイズ…"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/llm/ingest/files", "POST",
      { source: { hf: { repo: REPO } } });
    expect(rowFor("UD-IQ2_XXS")).toBeTruthy();
    // Held is answered against the whole catalogue, so the size this row IS reads as taken in.
    expect(rowFor("UD-IQ2_S")!.className).toContain("held");
  });

  it("offers nothing where the row records no page to read", async () => {
    mockEngineAPI([{ ...llmRow, model_rows: [
      { ...held, id: "seeded", display_name: undefined, source: undefined },
      { ...held, id: "direct", display_name: undefined, source: "url:https://example.invalid/m.gguf" },
    ] }]);
    await mount();
    expect(button("別サイズ…")).toBeUndefined();
  });

  // An adapter is filed under the model it was trained against, not under a repository of sizes,
  // and the ladder prices a KV cache it does not have.
  it("offers nothing on a chat adapter", async () => {
    mockEngineAPI([{ ...llmRow, model_rows: [
      { ...held, id: "adapter", kind: "lora", base_model: "qwen3_8_27b_ud_iq2_xxs" },
    ] }]);
    await mount("llm", true);
    expect(button("別サイズ…")).toBeUndefined();
  });

  it("lists a checkpoint's other versions, marking the one this row is", async () => {
    mockEngineAPI([imageRow]);
    apiJSON.mockImplementation((path: string) => path.endsWith("/ingest/versions")
      ? Promise.resolve(VERSIONS) : Promise.resolve({}));
    await mount("image");

    await click(button("別バージョン…"));
    // 🔴 Asked with the VERSION id alone: a row records `civitai:<version>` and nothing else, and
    // the model id is a different number the CP resolves and sends back.
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/ingest/versions", "POST",
      { source: "civitai", ref: "5038" });
    const rows = Array.from(document.querySelectorAll<HTMLElement>(".engine-repo-quants li"));
    expect(rows.map((li) => li.querySelector(".engine-repo-quant-name")?.textContent)).toEqual(["v4.7", "v5.0", "v3.1"]);
    expect(rows[0].className).toContain("held");
    expect(rows[1].textContent).toContain("4.0 GiB");
    // No size from upstream, so no verdict either: a fit computed from a missing weight would
    // read as "it fits" about a model nobody measured.
    expect(rows[2].textContent).toContain("サイズ未取得");
    expect(rows[2].querySelector(".engines-model-tag")).toBeNull();
  });

  it("opens the plan dialog on the chosen version, as Civitai and not as a repository name", async () => {
    mockEngineAPI([imageRow]);
    apiJSON.mockImplementation((path: string) => path.endsWith("/ingest/versions")
      ? Promise.resolve(VERSIONS)
      : path.endsWith("/ingest/files")
        ? Promise.resolve({ files: [{ name: "animaika_v50.safetensors", bytes: 4_300_000_000 }] })
        : Promise.resolve({}));
    await mount("image");
    await click(button("別バージョン…"));
    const wanted = Array.from(document.querySelectorAll<HTMLElement>(".engine-repo-quants li"))
      .find((li) => li.querySelector(".engine-repo-quant-name")?.textContent === "v5.0")!;
    await click(wanted.querySelector<HTMLButtonElement>("button")!);

    // 🔴 The dialog inspects as CIVITAI. Handed the link without the source it would have opened
    // set to Hugging Face and sent the whole URL upstream as a repository name.
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/ingest/versions", "POST",
      { source: "civitai", ref: "6000", model_ref: "4451" });
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/image/ingest/files", "POST",
      { source: { civitai: { versionId: 6000, file: "" } } });
  });
});
