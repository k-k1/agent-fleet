// What the 登録済み catalogue draws for a row, and what fills a row that has nothing to draw
// (ADR 0088).
//
// The complaint this covers is not a crash: every card's title was the row id, so a page of them
// was unreadable, and nothing was red. These assertions are therefore about what is ON the card —
// the publisher's name as the title, the id kept underneath it, the example image, and the
// family as a heading rather than a tag in the middle of the body.
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

const named = {
  id: "meinamix_meinav11_5038",
  kind: "checkpoint",
  enabled: true,
  base_model: "sd15",
  display_name: "MeinaMix",
  version_name: "Meina V11",
  preview_url: "https://image.civitai.com/x/y/anim=false,width=1024/1.jpeg",
  thumb_url: "https://image.civitai.com/x/y/anim=false,width=256/1.jpeg",
  source: "civitai:5038",
};

const bare = { id: "abyssorangemix2_hard_8832", kind: "checkpoint", enabled: false, source: "civitai:8832" };

const imageRow = {
  key: "image",
  api: "images",
  provider: "comfy",
  managed: true,
  base_models: ["sdxl", "sd15"],
  model_rows: [named, bare],
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
  await act(async () => { root!.render(<EngineAddView engineKey="image" lora={false} initialView="registered" />); });
  await flush();
}

const card = (id: string) => document.querySelector<HTMLElement>(`.engine-registered-card[aria-label="${id}"]`);

const button = (label: string) => Array.from(document.querySelectorAll<HTMLButtonElement>("button"))
  .find((candidate) => candidate.textContent === label);

const labelled = (label: string) => Array.from(document.querySelectorAll<HTMLButtonElement>("button"))
  .find((candidate) => candidate.getAttribute("aria-label") === label);

async function click(element: HTMLElement | undefined) {
  expect(element).toBeTruthy();
  await act(async () => { element!.dispatchEvent(new MouseEvent("click", { bubbles: true })); });
  await flush();
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

describe("the registered catalogue card", () => {
  it("titles the card with the publisher's name and keeps the id on the card", async () => {
    mockEngineAPI([imageRow]);
    await mount();

    const header = card(named.id)!.querySelector(".engine-registered-name")!;
    expect(header.querySelector("strong")!.textContent).toBe("MeinaMix");
    expect(header.textContent).toContain("Meina V11");
    // 🔴 The id is the key every S3 path and the launch menu are written in. A card that dropped
    // it would be one an operator cannot match to the file in front of them.
    expect(card(named.id)!.querySelector(".engine-registered-id")!.textContent).toBe(named.id);
  });

  it("falls back to the id, and offers the button that fills the name in", async () => {
    mockEngineAPI([imageRow]);
    await mount();

    expect(card(bare.id)!.querySelector(".engine-registered-name strong")!.textContent).toBe(bare.id);
    expect(labelled(`配布元から名前を取る: ${bare.id}`)).toBeTruthy();
    // And not on the row that already has one: this is an upstream read, not a decoration.
    expect(labelled(`配布元から名前を取る: ${named.id}`)).toBeFalsy();
  });

  it("draws the card-sized example image and opens the large one", async () => {
    mockEngineAPI([imageRow]);
    await mount();

    const thumb = card(named.id)!.querySelector<HTMLImageElement>(".engine-registered-thumb img")!;
    expect(thumb.getAttribute("src")).toBe(named.thumb_url);
    // A row with no picture leaves no empty box behind.
    expect(card(bare.id)!.querySelector(".engine-registered-thumb")).toBeNull();

    await click(labelled(`作例を拡大: MeinaMix`));
    expect(document.querySelector<HTMLImageElement>(".engine-catalog-lightbox img")!.getAttribute("src"))
      .toBe(named.preview_url);
  });

  it("files the rows under family headings, unclassified first, and narrows to one", async () => {
    mockEngineAPI([imageRow]);
    await mount();

    const heads = Array.from(document.querySelectorAll(".engine-registered-group-head span:first-child"))
      .map((node) => node.textContent);
    expect(heads).toEqual(["分類なし", "sd15"]);

    await click(button("sd15 1"));
    expect(card(named.id)).toBeTruthy();
    expect(card(bare.id)).toBeNull();
  });

  // 🔴 The group of rows that declare no family is itself keyed "", so a "show everything"
  // sentinel of "" makes the two the same value: both chips drew as selected and the
  // unclassified one narrowed nothing (seen on the rendered screen).
  it("tells the unclassified chip apart from 'every family'", async () => {
    mockEngineAPI([imageRow]);
    await mount();

    const all = button("すべて 2")!;
    const none = button("分類なし 1")!;
    expect(all.getAttribute("aria-selected")).toBe("true");
    expect(none.getAttribute("aria-selected")).toBe("false");

    await click(none);
    expect(button("すべて 2")!.getAttribute("aria-selected")).toBe("false");
    expect(card(bare.id)).toBeTruthy();
    expect(card(named.id)).toBeNull();
  });

  it("reads the model pages of the rows that have no name, one at a time", async () => {
    mockEngineAPI([imageRow]);
    apiJSON.mockResolvedValue({ id: bare.id, display_name: "AbyssOrangeMix2", found: true });
    await mount();

    await click(button("名前と作例をまとめて取る（1 件）"));
    expect(apiJSON).toHaveBeenCalledWith(`api/admin/engines/image/models/${bare.id}/meta`, "POST");
    // The named row is not re-read: the press is offered for what is missing, not for all of it.
    expect(apiJSON).toHaveBeenCalledTimes(1);
  });

  // A `url:` row can never answer, and before this it aborted the run for every row behind it.
  it("keeps going past a row whose source is no page, and reports the failure once", async () => {
    mockEngineAPI([{ ...imageRow, model_rows: [bare, { ...bare, id: "second", source: "url:https://x/y.safetensors" }] }]);
    apiJSON.mockImplementation((path: string) => path.includes("/second/")
      ? Promise.resolve({ error: { code: "engine_no_source", message: "no page" } })
      : Promise.resolve({ id: bare.id, found: true }));
    await mount();

    await click(button("名前と作例をまとめて取る（2 件）"));
    expect(apiJSON).toHaveBeenCalledTimes(2);
    expect(document.querySelector(".engine-registered-note")!.textContent)
      .toBe("1 件に名前が入りました（取れなかったもの 1 件）。");
  });
});
