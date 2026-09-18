// DOM tests for the engine indicator (ADR 0084 decisions 4/5/11). Mounts the pure
// `EnginesPillView` directly with rows as a prop — no store, no api() mock needed (the same
// split JobList.dom.test.tsx uses for the imagegen queue).
//
// Every "no X" assertion here is paired with a positive control that goes through the same
// render path and DOES show X, per AGENTS.md "Verifying your own work": an empty result and a
// check that never ran read identically otherwise.
import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { EnginesPillView } from "./EnginesPill.tsx";
import type { EngineMemberRow } from "./wire.ts";

let host: HTMLDivElement;
let root: Root;

async function render(rows: EngineMemberRow[]) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(<EnginesPillView rows={rows} />);
  });
}

afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
});

const pills = () => Array.from(host.querySelectorAll<HTMLButtonElement>(".engine-pill"));
const openPopover = async (btn: HTMLButtonElement) => {
  await act(async () => {
    btn.click();
  });
};

describe("engine pill — decision 5, the four hide conditions", () => {
  // On the wire all four ("no engine of this role", mode=off, no enabled models, tenant not
  // allowed) collapse into the SAME fact Console sees: no row for the role at all
  // (engine_admin.go's row is only ever built for a role that exists, and P0-C's gate omits
  // the row instead of zeroing fields it — decision 5's "行を送らないことで表現する"). So the
  // one mechanism this side has to test is "no row -> no pill", exercised with an empty
  // payload and with a payload missing just the images role.
  it("renders nothing for an empty engines payload", async () => {
    await render([]);
    expect(pills().length).toBe(0);
  });

  it("shows no image pill when only a chat row exists (one role hidden, the other not)", async () => {
    await render([{ key: "llm", api: "chat", state: "running" }]);
    expect(pills().length).toBe(1);
    // Default locale is ja (lib/i18n's DEFAULT_LOCALE) — "engine.role_chat".
    expect(host.querySelector(".engine-pill[title*='チャット']")).not.toBeNull();
  });

  // Positive control: the same render path, with a row present, DOES draw a pill — proves
  // the assertions above are catching a real "hidden", not a component that renders nothing
  // no matter what it is given.
  it("positive control: a role WITH a row gets a pill", async () => {
    await render([{ key: "image", api: "images", state: "running" }]);
    expect(pills().length).toBe(1);
  });
});

describe("engine pill — decision 4, no countdown without stop_eta", () => {
  it("omits the countdown line when stop_eta is absent", async () => {
    await render([{ key: "image", api: "images", state: "running" }]);
    expect(host.querySelector(".engine-pill-countdown")).toBeNull();
  });

  // Positive control, same render path: a row WITH stop_eta DOES draw the countdown.
  it("positive control: stop_eta present draws the countdown", async () => {
    const future = new Date(Date.now() + 5 * 60_000).toISOString();
    await render([{ key: "image", api: "images", state: "running", stop_eta: future }]);
    expect(host.querySelector(".engine-pill-countdown")).not.toBeNull();
  });

  it("an external row never shows a countdown, even if stop_eta rode along on the wire", async () => {
    const future = new Date(Date.now() + 5 * 60_000).toISOString();
    await render([{ key: "lan", api: "images", lifecycle: "external", stop_eta: future }]);
    expect(host.querySelector(".engine-pill-countdown")).toBeNull();
  });
});

describe("engine pill — decision 4/11, an external row shows state only", () => {
  it("popover row for an external engine shows 'Available' and a lifecycle badge, no state/stop/idle lines", async () => {
    await render([
      { key: "lan", api: "images", lifecycle: "external", idle_secs: 999, stop_eta: new Date().toISOString() },
    ]);
    await openPopover(pills()[0]);
    const rowEl = host.querySelector(".engine-row")!;
    // Default locale is ja — "engine.state_available" / "engine.lifecycle_external".
    expect(rowEl.querySelector(".engine-row-state")?.textContent).toBe("利用可");
    expect(rowEl.querySelector(".engine-row-lifecycle")?.textContent).toBe("外部管理");
    expect(rowEl.querySelector(".engine-row-line")).toBeNull();
  });

  // Positive control: a self-managed stopped row (same popover render path) DOES draw an
  // idle-policy / cold-start line, so the assertion above is catching real suppression.
  it("positive control: a self-managed row with idle_secs draws the policy line", async () => {
    await render([{ key: "image", api: "images", state: "stopped", idle_secs: 1800 }]);
    await openPopover(pills()[0]);
    const lines = Array.from(host.querySelectorAll(".engine-row-line")).map((n) => n.textContent);
    expect(lines.some((t) => t?.includes("30 分"))).toBe(true); // idle_policy: dur(1800s) = 30 分
    expect(lines.some((t) => t?.includes("起動します"))).toBe(true); // cold_hint
  });
});

describe("engine pill — decision 11, headline and row count", () => {
  it("a warm row wins the headline even when another row is merely running", async () => {
    await render([
      { key: "a", api: "images", state: "running" },
      { key: "b", api: "images", state: "starting", warm: true },
    ]);
    // Default locale is ja — "engine.state_ready".
    expect(host.querySelector(".engine-pill-state")!.textContent).toContain("準備済み");
  });

  it("shows a row-count badge once a role has more than one row", async () => {
    await render([
      { key: "a", api: "images", state: "running" },
      { key: "b", api: "images", state: "stopped" },
    ]);
    expect(host.querySelector(".engine-pill-count")?.textContent).toBe("×2");
  });

  it("no count badge for a single-row role", async () => {
    await render([{ key: "a", api: "images", state: "running" }]);
    expect(host.querySelector(".engine-pill-count")).toBeNull();
  });
});

describe("engine pill — the model in VRAM and who is using it", () => {
  it("draws the readable name for the loaded model, not the id, when the wire carries one", async () => {
    await render([
      {
        key: "llm",
        api: "chat",
        state: "running",
        warm: true,
        warm_model: "qwen3.8-27b-ud-iq4_xs",
        warm_model_label: "Qwen3.8 27B IQ4_XS",
      },
    ]);
    await openPopover(pills()[0]);
    const lines = Array.from(host.querySelectorAll(".engine-row-line")).map((n) => n.textContent);
    expect(lines.some((t) => t?.includes("Qwen3.8 27B IQ4_XS"))).toBe(true);
    expect(lines.some((t) => t?.includes("qwen3.8-27b-ud-iq4_xs"))).toBe(false);
  });

  it("falls back to the id when no label rode along (ADR 0090 — absence is the old behaviour)", async () => {
    await render([{ key: "llm", api: "chat", state: "running", warm: true, warm_model: "qwen3.8-27b-ud-iq4_xs" }]);
    await openPopover(pills()[0]);
    expect(host.querySelector(".engine-row-line .mono")?.textContent).toBe("qwen3.8-27b-ud-iq4_xs");
  });

  it("omits the model line entirely for a row the CP said nothing about (cold engine)", async () => {
    await render([{ key: "llm", api: "chat", state: "stopped" }]);
    await openPopover(pills()[0]);
    const lines = Array.from(host.querySelectorAll(".engine-row-line")).map((n) => n.textContent);
    expect(lines.some((t) => t?.includes("モデル"))).toBe(false); // engine.warm_model's ja prefix
  });

  it("a warm engine with a request in flight says 'in use', not 'ready'", async () => {
    await render([{ key: "llm", api: "chat", state: "running", warm: true, queue: { count: 1 } }]);
    // Default locale is ja — "engine.state_in_use".
    expect(host.querySelector(".engine-pill-state")!.textContent).toContain("使用中");
  });

  it("positive control: the same row with nothing in flight reads 'ready'", async () => {
    await render([{ key: "llm", api: "chat", state: "running", warm: true, queue: { count: 0 } }]);
    expect(host.querySelector(".engine-pill-state")!.textContent).toContain("準備済み");
  });

  it("an idle warm row says how long ago it was last used; a busy one does not", async () => {
    const stopEta = new Date(Date.now() + 28 * 60_000).toISOString();
    await render([{ key: "llm", api: "chat", state: "running", warm: true, idle_secs: 1800, stop_eta: stopEta }]);
    await openPopover(pills()[0]);
    const idleLines = Array.from(host.querySelectorAll(".engine-row-line")).map((n) => n.textContent);
    expect(idleLines.some((t) => t?.includes("最後の利用 2 分前"))).toBe(true);

    await act(async () => root.unmount());
    host.remove();
    await render([
      { key: "llm", api: "chat", state: "running", warm: true, idle_secs: 1800, stop_eta: stopEta, queue: { count: 1 } },
    ]);
    await openPopover(pills()[0]);
    const busyLines = Array.from(host.querySelectorAll(".engine-row-line")).map((n) => n.textContent);
    expect(busyLines.some((t) => t?.includes("最後の利用"))).toBe(false);
  });
});

describe("engine pill — decision 6, queue count", () => {
  it("omits the queue line when nothing is certain", async () => {
    await render([{ key: "image", api: "images", state: "running" }]);
    expect(host.querySelector(".engine-pill-queue")).toBeNull();
  });

  it("positive control: a certain queue count is shown, summed across rows", async () => {
    await render([
      { key: "a", api: "images", state: "running", queue: { count: 2 } },
      { key: "b", api: "images", state: "stopped", queue: { count: 1 } },
    ]);
    expect(host.querySelector(".engine-pill-queue")?.textContent).toContain("3");
  });

  it("a fresh-counter 0 does not draw a confident queue line", async () => {
    await render([{ key: "image", api: "images", state: "running", queue: { count: 0, counted_secs: 0 } }]);
    expect(host.querySelector(".engine-pill-queue")).toBeNull();
  });
});
