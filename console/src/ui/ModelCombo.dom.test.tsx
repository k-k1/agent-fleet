// ModelCombo is what a native <select> was traded away for, so the things <select> used to
// give for free are what this pins:
//   - the maker's mark actually reaches the row (the reason the trade was made at all),
//   - and it is ABSENT, not defaulted, when the Agent could not place the model,
//   - typing filters, the arrows move, Enter commits,
//   - the ARIA wiring a screen reader needs (role=combobox + aria-activedescendant into a
//     role=listbox), since nothing else in the Console asserts it.
//
// The marks are CSS masks, which jsdom does not render — asserting on a class is the most
// this layer can do (memory: a mask-image bug is invisible here and only headless Chromium
// sees it). What the class asserts is the join between the Agent's `provider` and the icon
// set: brandClass() is real, so a provider with no vendored asset falls back to a codicon and
// this test fails on the class name.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, useState } from "react";
import { createRoot, type Root } from "react-dom/client";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };

let respond: (body: unknown) => void = () => {};
vi.mock("../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return {
    ...real,
    api: () =>
      new Promise((resolve) => {
        respond = resolve;
      }),
  };
});

const { ModelPicker } = await import("./ModelPicker.tsx");

let root: Root | null = null;
let host: HTMLDivElement;
let picked: string[] = [];

const input = () => host.querySelector<HTMLInputElement>('input[role="combobox"]')!;
const rows = () => [...host.querySelectorAll<HTMLElement>('[role="option"]')];
const labels = () => rows().map((r) => r.querySelector(".model-combo-label")?.textContent || "");
const markClasses = () =>
  rows().map((r) => r.querySelector(".model-combo-mark > .codicon")?.className.replace("codicon ", "") || "");

async function type(value: string) {
  const el = input();
  await act(async () => {
    // React tracks the previous value on the node, so assigning through the prototype setter
    // is what makes it see a real change rather than swallowing the event.
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(el, value);
    el.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

async function key(k: string) {
  await act(async () => {
    input().dispatchEvent(new KeyboardEvent("keydown", { key: k, bubbles: true }));
  });
}

// One entry per case that matters: two makers the resolver places, and one it cannot.
const CATALOG = {
  models: [
    { id: "opencode-go/glm-5.2", label: "GLM 5.2 (Go)", provider: "zhipuai" },
    { id: "opencode/claude-sonnet-4-6", label: "Claude Sonnet 4.6", provider: "anthropic" },
    { id: "opencode/big-pickle", label: "Big Pickle", provider: "" },
  ],
};

// The picker is controlled, so the harness has to hold the value: with a fixed `model` prop
// a committed pick would never come back and "what the closed field shows" could not be
// tested at all.
function Harness() {
  const [model, setModel] = useState("");
  return (
    <ModelPicker
      kind="opencode"
      model={model}
      onChange={(m) => {
        picked.push(m);
        setModel(m);
      }}
    />
  );
}

// primaryCoarsePointer() is a live media query read at render time, so which kind of device
// this is has to be decided before mounting. jsdom's shell always answers false (= a mouse),
// which is what every test that does not say otherwise runs as.
const realMatchMedia = window.matchMedia;
function setPointer(coarse: boolean) {
  window.matchMedia = ((q: string) => ({
    matches: coarse && q === "(pointer: coarse)",
    media: q,
    onchange: null,
    addEventListener() {},
    removeEventListener() {},
    addListener() {},
    removeListener() {},
    dispatchEvent: () => false,
  })) as unknown as typeof window.matchMedia;
}

/** Mount, settle the catalog fetch and focus the field (which is what opens the list). */
async function mount() {
  await act(async () => {
    root!.render(<Harness />);
  });
  await act(async () => {
    respond(CATALOG);
    await Promise.resolve();
    await Promise.resolve();
  });
  await act(async () => input().focus());
}

beforeEach(() => {
  picked = [];
  g.IS_REACT_ACT_ENVIRONMENT = true;
  setPointer(false);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
  window.matchMedia = realMatchMedia;
  delete g.IS_REACT_ACT_ENVIRONMENT;
});

describe("ModelCombo", () => {
  it("draws each maker's mark, and nothing at all for a model it could not place", async () => {
    await mount();
    expect(labels()).toEqual(["Default", "GLM 5.2 (Go)", "Claude Sonnet 4.6", "Big Pickle"]);
    expect(markClasses()).toEqual([
      "", // Default is not a model
      "brandicon bi-provider-zhipuai",
      "brandicon bi-provider-anthropic",
      "", // unplaceable: no mark rather than a stand-in
    ]);
  });

  it("filters on both the label and the id, so a pasted id still finds its row", async () => {
    await mount();
    await type("sonnet");
    expect(labels()).toEqual(["Claude Sonnet 4.6"]);
    await type("opencode-go/");
    expect(labels()).toEqual(["GLM 5.2 (Go)"]);
    await type("zzz");
    expect(rows()).toEqual([]);
    expect(host.querySelector(".model-combo-empty")).not.toBeNull();
  });

  it("commits the arrowed-to row on Enter", async () => {
    await mount();
    await type("glm");
    await key("Enter"); // the first match is active from the moment the query changes
    expect(picked).toEqual(["opencode-go/glm-5.2"]);
    expect(rows()).toEqual([]); // committing closes the list
  });

  it("moves with the arrows and wraps", async () => {
    await mount();
    await key("ArrowUp"); // from the selected row (Default, index 0) to the last
    expect(rows()[3].className).toContain("sel");
    await key("ArrowDown");
    expect(rows()[0].className).toContain("sel");
  });

  it("wires the combobox to the listbox the way a screen reader reads it", async () => {
    await mount();
    const list = host.querySelector('[role="listbox"]')!;
    expect(input().getAttribute("aria-expanded")).toBe("true");
    expect(input().getAttribute("aria-controls")).toBe(list.id);
    // The active row is pointed at by id; DOM focus stays in the input, which is what lets
    // typing and arrowing work at the same time.
    await key("ArrowDown");
    expect(input().getAttribute("aria-activedescendant")).toBe(rows()[1].id);
    expect(document.activeElement).toBe(input());
  });

  it("shows the selection when closed rather than an empty box", async () => {
    await mount();
    await type("glm");
    await key("Enter");
    expect(input().value).toBe("GLM 5.2 (Go)");
  });
});

// Reported from a phone: opening the picker raised GBoard, GBoard scrolled the dialog to keep
// the field visible, and the list stayed where it had been drawn.
describe("ModelCombo on a device with an on-screen keyboard", () => {
  const filterRow = () => host.querySelector<HTMLButtonElement>(".model-combo-filter");

  it("opens the list without making the field typable, so no keyboard is summoned", async () => {
    setPointer(true);
    await mount();
    expect(rows().length).toBe(4); // the list really is open
    expect(input().readOnly).toBe(true);
    // Read-only must not blank the field: without a query to show, it shows the selection.
    expect(input().value).toBe("Default");
    expect(filterRow()).not.toBeNull();
  });

  it("keeps the field typable where there is no on-screen keyboard to raise", async () => {
    await mount(); // beforeEach set a fine pointer
    expect(input().readOnly).toBe(false);
    expect(filterRow()).toBeNull();
  });

  it("hands the keyboard over on an explicit tap, without closing the list", async () => {
    setPointer(true);
    await mount();
    await act(async () => filterRow()!.click());
    expect(input().readOnly).toBe(false);
    // The tap blurs and refocuses the field to make a browser open the keyboard; the blur
    // must not be read as "tapped elsewhere" and close the list.
    expect(rows().length).toBe(4);
    expect(document.activeElement).toBe(input());
    expect(filterRow()).toBeNull(); // and the row is spent
    await type("glm");
    expect(labels()).toEqual(["GLM 5.2 (Go)"]);
  });

  // The defect behind the report: .ui-modal-body is a scroll container, and a scroll there
  // does not bubble to window — only a capture-phase listener sees it. jsdom has no layout,
  // so the anchor's rect is stubbed and what is measured is that the popup was placed from
  // the CURRENT rect rather than the one it opened with.
  it("re-anchors when the dialog scrolls underneath it", async () => {
    await mount();
    const pop = host.querySelector<HTMLElement>(".model-combo-pop")!;
    const rect = (top: number) =>
      ({ top, bottom: top + 28, left: 40, right: 240, width: 200, height: 28, x: 40, y: top }) as DOMRect;

    input().getBoundingClientRect = () => rect(100);
    await act(async () => window.dispatchEvent(new Event("resize")));
    expect(pop.style.top).toBe("130px"); // 100 + 28 + the 2px gap

    // A scroll of an inner container, dispatched where it really happens.
    input().getBoundingClientRect = () => rect(300);
    await act(async () => host.dispatchEvent(new Event("scroll")));
    expect(pop.style.top).toBe("330px");
  });
});
