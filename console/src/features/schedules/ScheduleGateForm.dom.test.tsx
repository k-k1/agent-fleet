// The gate section of the schedule detail modal: it must round-trip an existing gate without
// dropping fields, show the last check, and survive a verdict it has never heard of.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const g = globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean };
g.IS_REACT_ACT_ENVIRONMENT = true;

vi.mock("./api.ts", () => ({ scheduleUpdate: vi.fn() }));
vi.mock("../workitems/api.ts", () => ({ workItemList: vi.fn() }));

const { ScheduleDetailModal } = await import("./ScheduleDetailModal.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { scheduleUpdate } = await import("./api.ts");
const { workItemList } = await import("../workitems/api.ts");

const update = scheduleUpdate as unknown as ReturnType<typeof vi.fn>;
const list = workItemList as unknown as ReturnType<typeof vi.fn>;

let root: Root | null = null;
let host: HTMLDivElement;

const base = { id: "sch_1", spec_kind: "cron", spec: "0 9 * * *", prompt: "hi", enabled: true, session_mode: "new" };
const payload = {
  items: [],
  sessions: [],
  queries: [
    { id: "q1", provider: "github", label: "Red PRs", query: "is:pr", enabled: true },
    { id: "q2", provider: "github", label: "Labelled", query: "label:x", enabled: true },
  ],
};

// Every call mounts a fresh modal: the form state is seeded once from the schedule.
async function render(extra: Record<string, unknown>): Promise<void> {
  await act(async () => root!.unmount());
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <ScheduleDetailModal s={{ ...base, ...extra } as never} onClose={() => {}} onSaved={() => {}} />
      </ToastProvider>,
    );
  });
}
const el = <T extends HTMLElement>(sel: string) => document.querySelector(sel) as T;
const save = () => el<HTMLButtonElement>("button[type=submit]");
async function pick(sel: string, value: string) {
  const node = el<HTMLSelectElement>(sel);
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLSelectElement.prototype, "value")!.set!;
    setter.call(node, value);
    node.dispatchEvent(new Event("change", { bubbles: true }));
  });
}

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  update.mockReset();
  update.mockResolvedValue({ ...base });
  list.mockReset();
  list.mockResolvedValue(payload);
});
afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

const gate = { kind: "work_items", query_id: "q1", fingerprint: "keys+updated", max_fires_per_day: 5, min_fire_gap_s: 90 };

describe("ScheduleDetailModal gate", () => {
  it("is absent for a schedule that cannot carry a gate", async () => {
    await render({ session_mode: "reuse" });
    expect(document.querySelector(".sched-gate")).toBeNull();
    expect(list).not.toHaveBeenCalled();
    await render({ spec_kind: "once" });
    expect(document.querySelector(".sched-gate")).toBeNull();
  });

  it("enables a gate from the saved queries and sends it whole", async () => {
    await render({});
    expect(el(".sched-gate")).not.toBeNull();
    await act(async () => el<HTMLInputElement>(".sched-gate input[type=checkbox]").click());
    expect(Array.from(document.querySelectorAll("#sched-gate-query option")).map((o) => o.textContent)).toContain("Red PRs");
    expect(save().disabled).toBe(true); // no query chosen yet
    await pick("#sched-gate-query", "q2");
    await act(async () => save().click());
    expect(update).toHaveBeenCalledWith("sch_1", { gate: { kind: "work_items", query_id: "q2", fingerprint: "keys" } });
  });

  it("round-trips an existing gate: no gate in the patch unless it was edited", async () => {
    await render({ gate, prompt: "hi" });
    expect(el<HTMLSelectElement>("#sched-gate-query").value).toBe("q1");
    expect(el<HTMLInputElement>("#sched-gate-max").value).toBe("5");
    expect(el<HTMLInputElement>("#sched-gate-gap").value).toBe("90");
    expect(save().disabled).toBe(true); // clean form
    // Editing another field leaves the gate out of the patch.
    await act(async () => {
      const ta = el<HTMLInputElement>("#sched-label");
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(ta, "x");
      ta.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => save().click());
    expect(update).toHaveBeenLastCalledWith("sch_1", { spec_label: "x" });
  });

  it("keeps the caps and mode when only the query changes", async () => {
    await render({ gate });
    await pick("#sched-gate-query", "q2");
    await act(async () => save().click());
    expect(update).toHaveBeenCalledWith("sch_1", {
      gate: { kind: "work_items", query_id: "q2", fingerprint: "keys+updated", max_fires_per_day: 5, min_fire_gap_s: 90 },
    });
  });

  it("keeps a stored query selectable when the list does not hold it", async () => {
    list.mockResolvedValue({ ...payload, queries: [] });
    await render({ gate });
    expect(el<HTMLSelectElement>("#sched-gate-query").value).toBe("q1");
  });

  it("removes the gate with kind none", async () => {
    await render({ gate });
    await act(async () => el<HTMLInputElement>(".sched-gate input[type=checkbox]").click());
    await act(async () => save().click());
    expect(update).toHaveBeenCalledWith("sch_1", { gate: { kind: "none" } });
  });

  it("refuses out-of-range caps", async () => {
    await render({ gate });
    await act(async () => {
      const i = el<HTMLInputElement>("#sched-gate-max");
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(i, "99");
      i.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(save().disabled).toBe(true);
  });

  it("shows the last check, and an unknown verdict as the CP sent it", async () => {
    await render({ gate: { ...gate, last_eval_at: new Date().toISOString(), last_verdict: "clear" } });
    expect(el(".sched-gate-last").textContent).toContain("該当なし");
    await render({ gate: { ...gate, last_eval_at: new Date().toISOString(), last_verdict: "something_new" } });
    expect(el(".sched-gate-last").textContent).toContain("something_new");
    await render({ gate });
    expect(el(".sched-gate-last").textContent).toContain("まだ確認されていません");
  });

  it("never rewrites a gate of a kind it does not know", async () => {
    await render({ gate: { kind: "command", last_verdict: "fire" } });
    expect(document.querySelector("#sched-gate-query")).toBeNull();
    expect(document.querySelector(".sched-gate input[type=checkbox]")).toBeNull();
    // Another field is edited and saved: the unknown gate must stay out of the patch.
    await act(async () => {
      const i = el<HTMLInputElement>("#sched-label");
      Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(i, "y");
      i.dispatchEvent(new Event("input", { bubbles: true }));
    });
    await act(async () => save().click());
    expect(update).toHaveBeenLastCalledWith("sch_1", { spec_label: "y" });
  });

  it("ignores a half-set gate once the schedule becomes a once", async () => {
    await render({});
    await act(async () => el<HTMLInputElement>(".sched-gate input[type=checkbox]").click());
    await pick("#sched-kind", "once");
    expect(document.querySelector(".sched-gate")).toBeNull();
    await act(async () => save().click());
    expect(update).toHaveBeenLastCalledWith("sch_1", { spec_kind: "once" });
  });

  it("blocks a gated schedule becoming a once while the gate is on, and allows it with the gate off", async () => {
    await render({ gate });
    await pick("#sched-kind", "once");
    expect(save().disabled).toBe(true);
    expect(el(".sched-gate [data-invalid]")).not.toBeNull();
    await act(async () => el<HTMLInputElement>(".sched-gate input[type=checkbox]").click());
    await act(async () => save().click());
    expect(update).toHaveBeenLastCalledWith("sch_1", { spec_kind: "once", gate: { kind: "none" } });
  });

  it("marks a missing query as an error", async () => {
    await render({});
    await act(async () => el<HTMLInputElement>(".sched-gate input[type=checkbox]").click());
    expect(el(".sched-gate [data-invalid]")).not.toBeNull();
  });
});
