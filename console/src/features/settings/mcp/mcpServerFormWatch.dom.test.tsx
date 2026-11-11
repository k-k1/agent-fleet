// The Watch source checkbox follows the tenant switch for member-added watch sources: disabled
// with a line naming who decides while it is off, read-only for a server that already has it,
// and usable again as soon as the switch is on (the prop changes; no remount, no reload).
import { afterEach, beforeEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ServerForm } from "./mcpServerForm.tsx";
import { bodyOf, emptyForm } from "./mcpWire.ts";
import { setLocale } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

beforeEach(() => setLocale("en"));

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

function render(watch: boolean, blocked: boolean) {
  root!.render(
    <ServerForm
      form={{ ...emptyForm(), id: watch ? "mcp-abc" : "", watch }}
      setForm={() => {}}
      onSave={async () => true}
      onTest={async () => {}}
      egress={null}
      onProposed={() => {}}
      submitLabel="save"
      watchBlocked={blocked}
    />,
  );
}

function mount(watch: boolean, blocked: boolean) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  act(() => render(watch, blocked));
}

const box = () =>
  [...host!.querySelectorAll<HTMLLabelElement>(".mcp-check")].find((l) => l.textContent === "Watch source")!.querySelector("input")!;
const note = () => host!.querySelector(".mcp-watch-blocked");

describe("ServerForm watch source checkbox", () => {
  it("is enabled with no note while the tenant allows it", () => {
    mount(false, false);
    expect(box().disabled).toBe(false);
    expect(note()).toBeNull();
  });

  it("is disabled with a tenant-administrator line while the tenant blocks it", () => {
    mount(false, true);
    expect(box().disabled).toBe(true);
    expect(note()?.textContent).toMatch(/tenant administrator/);
  });

  it("shows an existing watch server read-only (checked, disabled) with the clearing warning", () => {
    mount(true, true);
    expect(box().checked).toBe(true);
    expect(box().disabled).toBe(true);
    expect(note()?.textContent).toMatch(/clears the mark/);
  });

  it("re-enables in place when the switch is turned on", () => {
    mount(false, true);
    expect(box().disabled).toBe(true);
    act(() => render(false, false));
    expect(box().disabled).toBe(false);
    expect(note()).toBeNull();
  });
});

describe("bodyOf with the watch source blocked", () => {
  it("omits targets.watch so the save is not refused, and keeps it otherwise", () => {
    const f = { ...emptyForm(), watch: true };
    expect(bodyOf(f, true).targets).not.toHaveProperty("watch");
    expect(bodyOf(f).targets).toHaveProperty("watch", true);
  });
});
