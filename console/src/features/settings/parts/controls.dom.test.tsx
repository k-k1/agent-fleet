// Select hands its callback the original option value (not the DOM string) and ignores a value
// that matches no option.
import { afterEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Choice, Select } from "./controls.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function mount(node: React.ReactElement) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => root!.render(node));
  return host;
}

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host?.remove();
  host = null;
});

function choose(sel: HTMLSelectElement, value: string) {
  // Set the value the way the browser does, then fire the event React listens to.
  const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
  act(() => {
    setter.call(sel, value);
    sel.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

describe("Select", () => {
  const options = [
    [2, "two"],
    [4, "four"],
  ] as const;

  it("returns the matching option's original (non-string) value", () => {
    const onChange = vi.fn();
    const h = mount(<Select value={2} options={options} onChange={onChange} />);
    choose(h.querySelector("select")!, "4");
    expect(onChange).toHaveBeenCalledTimes(1);
    expect(onChange).toHaveBeenCalledWith(4);
  });

  it("does not call onChange for a value that matches no option", () => {
    const onChange = vi.fn();
    const h = mount(<Select value={2} options={options} onChange={onChange} />);
    const sel = h.querySelector("select")!;
    // jsdom drops a value with no matching <option>, so add one the options list does not know.
    const stray = document.createElement("option");
    stray.value = "stray";
    sel.appendChild(stray);
    choose(sel, "stray");
    expect(onChange).not.toHaveBeenCalled();
  });
});

describe("Choice", () => {
  it("returns the clicked option's original value", () => {
    const onChange = vi.fn();
    const h = mount(<Choice value={false} options={[[true, "on"], [false, "off"]]} onChange={onChange} />);
    act(() => h.querySelectorAll("button")[0].dispatchEvent(new MouseEvent("click", { bubbles: true })));
    expect(onChange).toHaveBeenCalledWith(true);
  });
});
