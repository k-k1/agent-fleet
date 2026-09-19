// Which search sources a deployment offers (control-plane/engine_civitai_red.go).
//
// 🔴 The tab strips are drawn from the engine list's `catalog_sources` and never from a literal,
// because the CP refuses a search for a source it does not offer: a strip that keeps its own list
// draws a tab whose every press is a 403. The refusal is the gate — this is only the half that
// keeps somebody from meeting it.
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

import { EngineAddView } from "./adminEngineAdd.tsx";
import { EngineModelsAdminView } from "./adminEngineModels.tsx";
import { clearCatalogMemory } from "./catalogMemory.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const imageRow = { key: "image", api: "images", provider: "sdcpp", managed: true, base_models: ["sdxl"], model_rows: [] };

/** `sources` omitted is the answer of a Control Plane too old to carry the field. */
function mockEngines(sources?: string[]) {
  api.mockImplementation((path: string) => {
    if (path === "api/admin/engines") {
      return Promise.resolve({
        super_admin: true, engines: [imageRow], ...(sources ? { catalog_sources: sources } : {}),
      });
    }
    if (path.endsWith("/objects")) return Promise.resolve({ objects: [], checked_at: "2026-09-15T03:42:00Z" });
    return Promise.resolve({});
  });
  apiJSON.mockResolvedValue({ hits: [] });
}

async function mount(node: React.ReactNode) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => { root!.render(node); });
  for (const _ of [0, 1, 2]) await act(async () => { await Promise.resolve(); });
}

const button = (label: string) => Array.from(document.querySelectorAll<HTMLButtonElement>("button"))
  .find((candidate) => candidate.textContent === label);
const click = async (element: HTMLElement | undefined) => {
  expect(element).toBeTruthy();
  await act(async () => { element!.dispatchEvent(new MouseEvent("click", { bubbles: true })); });
  await act(async () => { await Promise.resolve(); });
};
const lastSearch = () => apiJSON.mock.calls.filter(([path]) => String(path).includes("search")).at(-1);

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

describe("the catalogue's source tabs follow the deployment", () => {
  it("draws Civitai Red only where the deployment offers it, and searches it when pressed", async () => {
    mockEngines(["hf", "civitai", "civitai-red"]);
    await mount(<EngineAddView engineKey="image" lora={false} />);

    await click(button("Civitai Red"));
    const [path, method, body] = lastSearch()!;
    expect(String(path)).toContain("/ingest/search");
    expect(method).toBe("POST");
    expect((body as { source: string }).source).toBe("civitai-red");
  });

  it("hides it where the deployment does not", async () => {
    mockEngines(["hf", "civitai"]);
    await mount(<EngineAddView engineKey="image" lora={false} />);

    expect(button("Civitai Red")).toBeUndefined();
    expect(button("Civitai")).toBeTruthy();
  });

  // A Control Plane that does not send the list is not a Control Plane that means "all of them".
  // The safe half is assumed, for the same reason `super_admin` defaults to false.
  it("assumes the safe half when the answer says nothing", async () => {
    mockEngines(undefined);
    await mount(<EngineAddView engineKey="image" lora={false} />);

    expect(button("Civitai Red")).toBeUndefined();
    expect(button("Civitai")).toBeTruthy();
  });

  // 🔴 The switch can move while a screen is open. What must NOT survive it is the SEARCH: a
  // source picked before it went away would keep being sent, and the CP answers that 403.
  it("stops searching a source that has gone away", async () => {
    apiJSON.mockResolvedValue({ hits: [] });
    await mount(<EngineModelsAdminView sources={["hf", "civitai", "civitai-red"]} />);
    await click(button("画像（checkpoint）"));
    await click(button("Civitai Red"));
    await click(button("人気を見る"));
    expect((lastSearch()![2] as { source: string }).source).toBe("civitai-red");

    await act(async () => { root!.render(<EngineModelsAdminView sources={["hf", "civitai"]} />); });
    expect(button("Civitai Red")).toBeUndefined();
    await click(button("人気を見る"));
    expect((lastSearch()![2] as { source: string }).source).toBe("hf");
  });
});
