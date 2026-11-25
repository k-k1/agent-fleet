import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act, type ReactNode } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Modal } from "./Modal.tsx";
import { FieldLabel } from "./FieldLabel.tsx";

(globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;

let host: HTMLDivElement;
let root: Root;
beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root.unmount());
  host.remove();
});
const render = (ui: ReactNode) => act(async () => root.render(ui));

describe("FieldLabel", () => {
  it("keeps the explanation out of sight until ⓘ is pressed, and wires aria-describedby", async () => {
    await render(
      <div>
        <FieldLabel htmlFor="x" info="Static explanation">
          Budget
        </FieldLabel>
        <input id="x" />
      </div>,
    );
    const btn = host.querySelector<HTMLButtonElement>("button")!;
    const panel = document.getElementById(btn.getAttribute("aria-describedby")!)!;
    expect(panel.textContent).toBe("Static explanation");
    expect(panel.hidden).toBe(true);
    expect(btn.getAttribute("aria-expanded")).toBe("false");
    await act(async () => btn.click());
    expect(panel.hidden).toBe(false);
    expect(btn.getAttribute("aria-expanded")).toBe("true");
    // Escape closes it even after focus has moved elsewhere, and the layer below sees none.
    const input = host.querySelector("input")!;
    input.focus();
    await act(async () => {
      input.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
    });
    expect(panel.hidden).toBe(true);
  });

  it("closes only the panel on the first Escape, the modal on the second", async () => {
    let closed = 0;
    await render(
      <Modal title="t" onClose={() => closed++}>
        <div className="ui-modal-body">
          <FieldLabel htmlFor="x" info="i">
            Name
          </FieldLabel>
          <input id="x" />
        </div>
      </Modal>,
    );
    const btn = document.querySelector<HTMLButtonElement>(".ui-field-info")!;
    await act(async () => btn.click());
    document.querySelector<HTMLInputElement>("#x")!.focus();
    const esc = () => act(async () => void document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true })));
    await esc();
    expect(closed).toBe(0);
    expect(btn.getAttribute("aria-expanded")).toBe("false");
    await esc();
    expect(closed).toBe(1);
  });

  it("gives each label its own panel id", async () => {
    await render(
      <div>
        <FieldLabel info="a">A</FieldLabel>
        <FieldLabel info="b">B</FieldLabel>
      </div>,
    );
    const ids = [...host.querySelectorAll("button")].map((b) => b.getAttribute("aria-describedby"));
    expect(new Set(ids).size).toBe(2);
  });

  it("is a plain label with no button without info", async () => {
    await render(<FieldLabel htmlFor="x">Budget</FieldLabel>);
    expect(host.querySelector("button")).toBeNull();
    expect(host.querySelector("label")!.textContent).toBe("Budget");
  });

  it("keeps the label bound to its control and puts trailing controls in the row", async () => {
    await render(
      <div>
        <FieldLabel htmlFor="x" info="i" trailing={<button type="button">Act</button>}>
          Name
        </FieldLabel>
        <input id="x" />
      </div>,
    );
    expect(host.querySelector("label")!.htmlFor).toBe("x");
    expect(host.querySelector(".ui-field-labelrow")!.textContent).toContain("Act");
  });
});
