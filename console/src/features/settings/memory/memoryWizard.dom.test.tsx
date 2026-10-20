// The shared wizard frame and grouped preview. Pinned here: the stepper marks the current step,
// Cancel leaves (and waits while an apply is in flight), a finished wizard offers Back instead,
// and the grouped preview draws one expandable group per non-empty status with its count, the
// row checkboxes only where the panel asks for them, and nothing for a status it does not know.
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { WizardFrame, GroupedPreview, WIZARD_STEPS, type WizardStep } from "./memoryWizard.tsx";
import { useEscLayer } from "../../../lib/escLayer.ts";
import { t } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const mount = async (el: React.ReactElement) => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => root!.render(el));
};
afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = host = null;
});
const buttons = () => Array.from(host!.querySelectorAll("button"));

const Inner = ({ onClose, busy }: { onClose: () => void; busy: boolean }) => (
  <WizardFrame title="T" step={busy ? "apply" : "confirm"} busy={busy} onClose={onClose}>
    b
  </WizardFrame>
);

describe("WizardFrame", () => {
  it("lists the five steps and marks only the current one", async () => {
    for (const step of WIZARD_STEPS) {
      await mount(<WizardFrame title="T" step={step}>body</WizardFrame>);
      const items = Array.from(host!.querySelectorAll(".mem-wiz-step"));
      expect(items.map((li) => li.textContent)).toEqual(WIZARD_STEPS.map((s) => t(("mem.wz_step_" + s) as "mem.wz_step_source")));
      expect(items.filter((li) => li.getAttribute("aria-current") === "step").map((li) => li.textContent)).toEqual([
        t(("mem.wz_step_" + step) as "mem.wz_step_source"),
      ]);
      act(() => root!.unmount());
      host!.remove();
    }
  });

  it("shows title, intro, body and result", async () => {
    await mount(<WizardFrame title="Title" intro="Intro text" step="result" result="3 imported">body-here</WizardFrame>);
    expect(host!.querySelector("h4")?.textContent).toBe("Title");
    expect(host!.textContent).toContain("Intro text");
    expect(host!.textContent).toContain("body-here");
    expect(host!.querySelector(".mem-wiz-result")?.textContent).toBe("3 imported");
  });

  it("Cancel calls onClose before the end, and Back replaces it once finished", async () => {
    const close = vi.fn();
    const steps: WizardStep[] = ["source", "preview", "confirm"];
    for (const step of steps) {
      await mount(<WizardFrame title="T" step={step} onClose={close}>b</WizardFrame>);
      expect(buttons().map((b) => b.textContent)).toEqual([t("common.cancel")]);
      act(() => root!.unmount());
      host!.remove();
    }
    await mount(<WizardFrame title="T" step="result" onClose={close}>b</WizardFrame>);
    expect(buttons().map((b) => b.textContent)).toEqual([t("mem.wz_back")]);
    await act(async () => buttons()[0].click());
    expect(close).toHaveBeenCalledTimes(1);
  });

  it("cannot be left while an apply is in flight", async () => {
    const close = vi.fn();
    await mount(<WizardFrame title="T" step="apply" busy onClose={close}>b</WizardFrame>);
    expect(buttons()[0].disabled).toBe(true);
    await act(async () => buttons()[0].click());
    expect(close).not.toHaveBeenCalled();
  });

  // The Settings dialog's own Escape layer sits underneath the wizard's.
  const esc = () => document.dispatchEvent(new KeyboardEvent("keydown", { key: "Escape", bubbles: true }));
  function Under({ outer, children }: { outer: () => void; children: React.ReactNode }) {
    useEscLayer(outer);
    return <>{children}</>;
  }

  // As in Settings: the dialog is open first, the wizard joins the stack on top of it.
  const mountLater = async (outer: () => void, wizard: React.ReactElement) => {
    await mount(<Under outer={outer}>{null}</Under>);
    await act(async () => root!.render(<Under outer={outer}>{wizard}</Under>));
  };

  it("Escape leaves to the hub when idle without reaching the dialog beneath", async () => {
    const close = vi.fn();
    const outer = vi.fn();
    await mountLater(outer, <Inner onClose={close} busy={false} />);
    act(() => esc());
    expect(close).toHaveBeenCalledTimes(1);
    expect(outer).not.toHaveBeenCalled();
  });

  it("Escape is consumed while an apply is in flight: neither the wizard nor the dialog closes", async () => {
    const close = vi.fn();
    const outer = vi.fn();
    await mountLater(outer, <Inner onClose={close} busy />);
    act(() => esc());
    expect(close).not.toHaveBeenCalled();
    expect(outer).not.toHaveBeenCalled();
  });

  it("draws no leave button without an onClose", async () => {
    await mount(<WizardFrame title="T" step="source">b</WizardFrame>);
    expect(buttons()).toHaveLength(0);
  });
});

describe("GroupedPreview", () => {
  interface Row {
    name: string;
    st: string;
  }
  const rows: Row[] = [
    { name: "a", st: "new" },
    { name: "b", st: "new" },
    { name: "c", st: "same" },
    { name: "d", st: "from-the-future" },
    { name: "e", st: "conflict" },
  ];
  const view = (selection?: React.ComponentProps<typeof GroupedPreview<Row>>["selection"]) => (
    <GroupedPreview
      items={rows}
      order={["new", "same", "conflict", "empty"]}
      statusOf={(r) => r.st}
      label={(st) => "L-" + st}
      badge={(st) => "ci-" + st}
      open={(st) => st !== "same"}
      keyOf={(r) => r.name}
      renderItem={(r) => <span className="row">{r.name}</span>}
      selection={selection}
    />
  );

  it("draws one group per non-empty status with its count, open as asked, and skips unknown statuses", async () => {
    await mount(view());
    const groups = Array.from(host!.querySelectorAll("details.mem-ci-group"));
    expect(groups.map((g) => g.querySelector("summary")?.textContent)).toEqual(["L-new 2", "L-same 1", "L-conflict 1"]);
    expect(groups.map((g) => (g as HTMLDetailsElement).open)).toEqual([true, false, true]);
    expect(groups[0].querySelector(".mem-badge")?.className).toContain("ci-new");
    expect(host!.textContent).not.toContain("from-the-future");
    expect(Array.from(host!.querySelectorAll(".row")).map((r) => r.textContent)).toEqual(["a", "b", "c", "e"]);
    expect(host!.querySelectorAll("input[type=checkbox]")).toHaveLength(0);
  });

  it("puts a checkbox only on the rows the selection asks for, and reports toggles", async () => {
    const toggle = vi.fn();
    await mount(
      view({
        shows: (_r, st) => st === "conflict",
        checked: () => true,
        disabled: () => false,
        label: (r) => "overwrite " + r.name,
        toggle,
      }),
    );
    const boxes = host!.querySelectorAll<HTMLInputElement>("input[type=checkbox]");
    expect(boxes).toHaveLength(1);
    expect(boxes[0].getAttribute("aria-label")).toBe("overwrite e");
    expect(boxes[0].checked).toBe(true);
    await act(async () => boxes[0].click());
    expect(toggle).toHaveBeenCalledWith(rows[4]);
  });
});
