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
import { EnginesPillView, type MemberChatConn } from "./EnginesPill.tsx";
import type { EngineMemberRow } from "./wire.ts";

let host: HTMLDivElement;
let root: Root;

async function render(rows: EngineMemberRow[], memberChat?: MemberChatConn) {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(<EnginesPillView rows={rows} memberChat={memberChat} />);
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
  it("popover for an external engine shows 'Available' and a lifecycle badge, no state/stop/idle lines", async () => {
    await render([
      { key: "lan", api: "images", lifecycle: "external", idle_secs: 999, stop_eta: new Date().toISOString() },
    ]);
    await openPopover(pills()[0]);
    // A single-row role carries its state (and the lifecycle word) in the popover header — the
    // row's own head would only repeat the role label with an operator's key beside it.
    const headEl = host.querySelector(".engine-popover-head")!;
    // Default locale is ja — "engine.state_available" / "engine.lifecycle_external".
    expect(headEl.querySelector(".engine-row-state")?.textContent).toBe("利用可");
    expect(headEl.querySelector(".engine-row-lifecycle")?.textContent).toBe("外部管理");
    expect(host.querySelector(".engine-row .engine-row-line")).toBeNull();
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

describe("engine pill — where the popover puts the state and the engine key", () => {
  it("a single-row role: state in the header, no key and no row head at all", async () => {
    await render([{ key: "llm", api: "chat", state: "running", warm: true }]);
    await openPopover(pills()[0]);
    const headEl = host.querySelector(".engine-popover-head")!;
    expect(headEl.textContent).toContain("チャット"); // engine.role_chat
    expect(headEl.querySelector(".engine-row-state")?.textContent).toBe("準備済み");
    expect(host.querySelector(".engine-row-head")).toBeNull();
    expect(host.querySelector(".engine-row-key")).toBeNull();
  });

  // Positive control for the assertions above: two rows DO get a key and a per-row state, and
  // then the header carries neither — the key is the only thing telling the rows apart
  // (decision 11), and one header badge could only ever describe one of them.
  it("a two-row role: a key and a state per row, and nothing in the header", async () => {
    await render([
      { key: "comfy-l4", api: "images", state: "running", warm: true },
      { key: "lan", api: "images", lifecycle: "external" },
    ]);
    await openPopover(pills()[0]);
    const keys = Array.from(host.querySelectorAll(".engine-row-key")).map((n) => n.textContent);
    expect(keys).toEqual(["comfy-l4", "lan"]);
    const states = Array.from(host.querySelectorAll(".engine-row .engine-row-state")).map((n) => n.textContent);
    expect(states).toEqual(["準備済み", "利用可"]);
    expect(host.querySelector(".engine-popover-head .engine-row-state")).toBeNull();
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

describe("engine pill — the member's own lcpp connection (docs/log/107 follow-up)", () => {
  // Acceptance condition: with no member connection, the render is IDENTICAL to before this
  // feature existed — same row, same pill, the CP's own chat state untouched.
  it("no member connection: the chat pill reads the CP's engines row exactly as before", async () => {
    await render([{ key: "llm", api: "chat", state: "running", warm: true }]);
    expect(pills().length).toBe(1);
    // Default locale is ja — "engine.state_ready" (CP vocabulary), not any member wording.
    expect(host.querySelector(".engine-pill-state")!.textContent).toContain("準備済み");
  });

  // A member connection overrides the chat pill EVEN WHEN a CP chat row is also present
  // (decision 1's "member's setting always wins", carried onto the display) — the CP's
  // "running"/"warm" state must not leak through once a member connection exists.
  it("a member connection overrides the CP's chat row entirely", async () => {
    await render(
      [{ key: "llm", api: "chat", state: "running", warm: true }],
      { url: "http://box:9931", reachable: true },
    );
    expect(pills().length).toBe(1);
    const stateEl = host.querySelector(".engine-pill-state")!;
    expect(stateEl.textContent).toContain("接続中"); // engine.state_member_reachable
    expect(stateEl.textContent).not.toContain("準備済み"); // the CP's own state_ready must not show
  });

  // A member connection draws a chat pill even with NO chat row from the CP at all (e.g. this
  // deployment runs no engines) — a direct connection needs nothing from the deployment.
  it("a member connection draws a chat pill with no CP engines payload at all", async () => {
    await render([], { url: "http://box:9931", reachable: true });
    expect(pills().length).toBe(1);
    expect(host.querySelector(".engine-pill[title*='チャット']")).not.toBeNull();
  });

  it("reachable=false reads as not-reachable, not as unknown", async () => {
    await render([], { url: "http://box:9931", reachable: false });
    expect(host.querySelector(".engine-pill-state")!.textContent).toContain("届いていません");
  });

  // The core distinction this whole feature exists to preserve: "never observed" must draw
  // its OWN word, not the same one as a real, failed dial.
  it("reachable=undefined (never observed) reads as unknown, not as unreachable", async () => {
    await render([], { url: "http://box:9931" });
    const text = host.querySelector(".engine-pill-state")!.textContent;
    expect(text).toContain("確認中"); // engine.state_member_unknown
    expect(text).not.toContain("届いていません");
  });

  it("the popover shows the connection's own URL, so it reads as 'mine'", async () => {
    await render([], { url: "http://192.168.0.113:28080", reachable: true });
    await openPopover(pills()[0]);
    const lines = Array.from(host.querySelectorAll(".engine-row-line")).map((n) => n.textContent);
    expect(lines.some((t) => t?.includes("192.168.0.113:28080"))).toBe(true);
  });

  // The images role is untouched by any of this — only `chat` is ever overridden.
  it("an images row is unaffected by a member chat connection", async () => {
    await render([{ key: "image", api: "images", state: "running" }], { url: "http://box:9931", reachable: true });
    expect(pills().length).toBe(2);
    const imagesPill = host.querySelector(".engine-pill[title*='画像']"); // engine.role_images
    expect(imagesPill).not.toBeNull();
  });
});

describe("engine pill — the model behind the member connection (docs/log/107, 2026-09-21 addendum)", () => {
  // A member can swap the LAN box under the SAME saved URL, and a single-model llama-server
  // does not read the request's own `model` field — so this is the only thing on screen that
  // can catch a swap. It rides in the tooltip (title) so it is visible without opening.
  it("a single observed model rides in the tooltip beside the URL", async () => {
    await render([], { url: "http://box:9931", reachable: true, model: "gemma-4-12b-it-q4_k_m" });
    const title = pills()[0].getAttribute("title") || "";
    expect(title).toContain("box:9931");
    expect(title).toContain("gemma-4-12b-it-q4_k_m");
  });

  it("shows a count when the observation found more than one model (a router)", async () => {
    await render([], { url: "http://box:9931", reachable: true, model: "gemma-4-12b-it-q4_k_m", modelCount: 3 });
    const title = pills()[0].getAttribute("title") || "";
    expect(title).toMatch(/ほか 2 件|\+2 more/);
  });

  // Positive control for the count guard: a single model must not draw a "+0 more"/"ほか 0 件".
  it("draws no count suffix for a single model", async () => {
    await render([], { url: "http://box:9931", reachable: true, model: "gemma-4-12b-it-q4_k_m", modelCount: 1 });
    const title = pills()[0].getAttribute("title") || "";
    expect(title).not.toMatch(/ほか|more/);
  });

  // No model observed yet: nothing model-shaped rides in the tooltip.
  it("omits the model entirely when unknown", async () => {
    await render([], { url: "http://box:9931", reachable: true });
    const title = pills()[0].getAttribute("title") || "";
    expect(title).not.toMatch(/モデル|Model:/);
  });

  it("the popover also shows the model, on its own line", async () => {
    await render([], { url: "http://box:9931", reachable: true, model: "gemma-4-e4b-uncensored-hauhaucs-balanced-q4_k_m" });
    await openPopover(pills()[0]);
    const lines = Array.from(host.querySelectorAll(".engine-row-line")).map((n) => n.textContent);
    expect(lines.some((t) => t?.includes("gemma-4-e4b-uncensored-hauhaucs-balanced-q4_k_m"))).toBe(true);
  });
});
