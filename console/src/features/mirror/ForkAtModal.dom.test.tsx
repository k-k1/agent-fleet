// The fork confirmation dialog. If this breaks, forking becomes unreachable as an operation,
// so three things are pinned in jsdom: the confirm posts fork with `at`, success hands back
// the new session name, and a failure keeps the dialog open with the reason. The last one
// matters most — closing on failure reads as "I pressed it and nothing happened".
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const apiJSON = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  errText: (e: { message?: string }) => e?.message || "",
}));

import { ForkAtModal } from "./ForkAtModal.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;
const done: Array<{ name: string; draft: string }> = [];
let closed = 0;

function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() =>
    root!.render(
      <ForkAtModal
        session="oc-1"
        target={{ anchorId: "msg_7", text: "やっぱり別の方法で", carried: 3 }}
        onDone={(name, opts) => done.push({ name, draft: opts.draft })}
        onClose={() => closed++}
      />,
    ),
  );
}

// The confirm button is the footer's primary. Its label is locale-dependent, so select by class.
function goButton() {
  const el = document.querySelector<HTMLButtonElement>(".ui-modal-foot .ui-btn-primary");
  if (!el) throw new Error("confirm button not rendered");
  return el;
}

// Mode switch (redo / continue): the radiogroup's second button is "continue".
function modeButton(mode: "redo" | "continue") {
  const els = Array.from(document.querySelectorAll<HTMLButtonElement>('[role="radiogroup"] .seg-btn'));
  if (els.length !== 2) throw new Error(`expected 2 mode buttons, got ${els.length}`);
  return mode === "redo" ? els[0] : els[1];
}

beforeEach(() => {
  apiJSON.mockReset();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  done.length = 0;
  closed = 0;
});

describe("ForkAtModal", () => {
  it("shows the branch point so the user can tell WHICH message they picked", () => {
    mount();
    expect(document.querySelector(".mirror-fork-preview")?.textContent).toContain("やっぱり別の方法で");
  });

  it("defaults to redo: posts include=false and seeds the prompt as a draft", async () => {
    apiJSON.mockResolvedValue({ name: "oc-2" });
    mount();
    await act(async () => {
      goButton().click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/sessions/oc-1/fork", "POST", { at: "msg_7", include: false });
    expect(done).toEqual([{ name: "oc-2", draft: "やっぱり別の方法で" }]);
    expect(closed).toBe(1);
  });

  it("continue mode posts include=true and seeds NO draft", async () => {
    // In continue mode the message is still in the forked conversation, so the same text in
    // the composer would read as a duplicate.
    apiJSON.mockResolvedValue({ name: "oc-3" });
    mount();
    await act(async () => {
      modeButton("continue").click();
    });
    await act(async () => {
      goButton().click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/sessions/oc-1/fork", "POST", { at: "msg_7", include: true });
    expect(done).toEqual([{ name: "oc-3", draft: "" }]);
  });

  it("keeps the dialog open and shows why when the fork is refused", async () => {
    apiJSON.mockRejectedValue({ code: "fork_bad_anchor", message: "この分岐点は使えません" });
    mount();
    await act(async () => {
      goButton().click();
    });
    expect(done).toEqual([]);
    expect(closed).toBe(0);
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("この分岐点は使えません");
  });

  it("surfaces the server's reason from a RESOLVED {error} body, not a generic message", async () => {
    // api() does not reject on failure — a 4xx/5xx *resolves* as {error:{code,message}}
    // (client.ts). Written with reject only, this test would pass while the real dialog
    // turned every reason into "no session in fork response".
    apiJSON.mockResolvedValue({ error: { code: "fork_bad_anchor", message: "エージェントの発言からは分岐できません" } });
    mount();
    await act(async () => {
      goButton().click();
    });
    expect(done).toEqual([]);
    expect(closed).toBe(0);
    expect(document.querySelector('[role="alert"]')?.textContent).toContain("エージェントの発言からは分岐できません");
  });

  it("does not report success when the response carries no session", async () => {
    // A 200 without a name means no fork happened. Accepting it would report success with
    // no pane to open.
    apiJSON.mockResolvedValue({});
    mount();
    await act(async () => {
      goButton().click();
    });
    expect(done).toEqual([]);
    expect(closed).toBe(0);
  });
});
