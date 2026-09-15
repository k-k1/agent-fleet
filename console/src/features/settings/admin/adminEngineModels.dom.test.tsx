// The engine-less catalogue screen (ADR 0072 decision 11, kept by ADR 0085 P3): a deployment
// that has adopted no engine still gets to look at what there is to run.
//
// Everything a deployment WITH an engine does — the plan card, the registered rows, the bucket —
// is the catalogue pane and is tested in adminEngineCatalog.dom.test.tsx. Nothing in this screen
// reads an engine, a job or the bucket any more, and the last test here is what says so.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
// Only the transport is stubbed. errDetail is the REAL one, because how this panel words a
// refusal is part of what is under test: a hand-written stub that echoed `message` back would
// have reported the English developer text as a pass.
vi.mock("../../../core/api/client.ts", async (importActual) => ({
  ...(await importActual<typeof import("../../../core/api/client.ts")>()),
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
}));

import { EngineModelsAdminView } from "./adminEngineModels.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<EngineModelsAdminView />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const click = async (el: HTMLElement | undefined) => {
  expect(el).toBeTruthy();
  await act(async () => {
    el!.dispatchEvent(new MouseEvent("click", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
};

const ui = () => document.body;

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  api.mockReset();
  apiJSON.mockReset();
  vi.useRealTimers();
});

// 🔴 A deployment that has not adopted 60-engines has an EMPTY panel, and "there is nothing
// here" is the worst possible answer to "what could I run?". Looking at what Hugging Face has
// needs no engine at all — no token, no bucket, no task — so the browse stays.
describe("EnginesAdminView / browsing with no engine deployed", () => {
  const button = (label: string) =>
    Array.from(ui().querySelectorAll("button")).find((b) => b.textContent === label) as
      | HTMLButtonElement
      | undefined;
  it("still offers a look at what there is, and says it cannot take anything in", async () => {
    apiJSON.mockResolvedValue({
      hits: [{ source: "hf", ref: "unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF", name: "unsloth/Qwen3-Coder-30B-A3B-Instruct-GGUF", downloads: 12623435 }],
    });
    await mount();
    // The "nothing deployed" sentence stays — the browse is added beside it, not instead of it.
    expect(ui().textContent).toContain("動かしていません");

    await click(button("人気を見る"));
    // The keyless route, with the kind stated rather than derived: there is no engine to
    // derive it from.
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/search?kind=gguf", "POST", {
      q: "",
      source: "hf",
      sort: "downloads",
    });
    expect(ui().querySelector(".engines-search-hits")!.textContent).toContain("Qwen3-Coder-30B");
    expect(ui().textContent).toContain("12.6M");
    // 🔴 A hit is NOT clickable here: picking one opens a plan, and this deployment has no role
    // to take anything into. The note says so instead of offering a button that cannot work.
    expect(ui().querySelector(".engines-search-hits li button")).toBeNull();
    expect(ui().textContent).toContain("閲覧だけです");
  });

  it("asks for the kind, because there is no engine to derive it from", async () => {
    apiJSON.mockResolvedValue({ hits: [] });
    await mount();
    await click(button("画像（checkpoint）"));
    await click(button("人気を見る"));
    expect(apiJSON).toHaveBeenCalledWith("api/admin/engines/search?kind=checkpoint", "POST", {
      q: "",
      source: "hf",
      sort: "downloads",
    });

    // Civitai is offered here too, and only for checkpoints.
    await click(button("Civitai"));
    await click(button("人気を見る"));
    expect(apiJSON).toHaveBeenLastCalledWith("api/admin/engines/search?kind=checkpoint", "POST", {
      q: "",
      source: "civitai",
      sort: "downloads",
    });
    // Going back to GGUF takes the source with it: Civitai hosts no GGUFs, and a search that
    // can only answer nothing reads as a broken one.
    await click(button("LLM（GGUF）"));
    expect(button("Civitai")).toBeUndefined();
    await click(button("人気を見る"));
    expect(apiJSON).toHaveBeenLastCalledWith("api/admin/engines/search?kind=gguf", "POST", {
      q: "",
      source: "hf",
      sort: "downloads",
    });
  });

  // 🔴 The routes ADR 0085 P3 removed from the CP. This screen is the one that used to read them
  // on every mount (`GET …/ingest`, `GET …/storage`, one per engine), and a call left behind here
  // would only be noticed as a 404 on a deployment with an engine — which is not the deployment
  // this screen is for.
  it("reads no engine route at all, not even the engine list", async () => {
    apiJSON.mockResolvedValue({ hits: [] });
    await mount();
    expect(api).not.toHaveBeenCalled();
    expect(apiJSON).not.toHaveBeenCalled();
  });
});
