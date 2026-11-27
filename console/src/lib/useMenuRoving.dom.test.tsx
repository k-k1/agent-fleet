import { act, useRef } from "react";
import { flushSync } from "react-dom";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { useMenuRoving } from "./useMenuRoving.ts";

function Harness({ labels, open }: { labels: string[]; open: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  useMenuRoving(ref, open);
  return (
    <>
      <button id="opener">Open</button>
      <div className="ui-menu" ref={ref}>
        {labels.map((label) => <button id={label} key={label} className="ui-menu-item">{label}</button>)}
      </div>
    </>
  );
}

let host: HTMLDivElement;
let root: Root;
let restoreLayout: () => void;
const button = (id: string) => host.querySelector<HTMLButtonElement>(`#${id}`)!;
const render = (labels: string[], open = true) => act(async () => root.render(<Harness labels={labels} open={open} />));

beforeEach(() => {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  const layout = vi.spyOn(HTMLElement.prototype, "offsetParent", "get").mockImplementation(function (this: HTMLElement) {
    return this.parentElement;
  });
  restoreLayout = () => layout.mockRestore();
});

afterEach(() => {
  act(() => root.unmount());
  host.remove();
  restoreLayout();
});

it("recovers when focusout fires before the focused item is detached", async () => {
  await render(["first", "middle", "last"]);
  const middle = button("middle");
  middle.focus();
  // Chromium sends focusout before removal. jsdom needs an explicit blur in the same turn.
  await act(async () => {
    middle.blur();
    flushSync(() => root.render(<Harness labels={["first", "last"]} open />));
  });
  expect(document.activeElement).toBe(button("last"));
});

it("follows the focused item's position across updates and clamps at the end", async () => {
  await render(["first", "second", "focused", "next", "last"]);
  button("focused").focus();
  await render(["second", "focused", "next", "last"]);
  await render(["second", "next", "last"]);
  expect(document.activeElement).toBe(button("next"));

  button("last").focus();
  await render(["second", "next"]);
  expect(document.activeElement).toBe(button("next"));
  await render([]);
  expect(document.activeElement).toBe(document.body);
});

it.each(["blur", "outside focus"] as const)("preserves %s after the former item is removed", async (departure) => {
  await render(["first", "last"]);
  const first = button("first");
  expect(document.activeElement).toBe(first);
  const destination = departure === "blur" ? document.body : button("opener");
  if (departure === "blur") first.blur();
  else destination.focus();
  await render(["first", "last"]);
  await render(["last"]);
  expect(document.activeElement).toBe(destination);
});

it("stops recovering focus when the menu closes", async () => {
  await render(["first", "last"]);
  expect(document.activeElement).toBe(button("first"));
  await render(["first", "last"], false);
  await render(["last"], false);
  expect(document.activeElement).toBe(document.body);
});
