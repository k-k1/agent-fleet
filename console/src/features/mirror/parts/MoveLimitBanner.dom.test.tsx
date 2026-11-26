// The move offered at a usage limit (ADR 0115 P2b). Pinned: it shows for live and confirmed stopped usage limits,
// never for a moving or moved source, and only when the Agent's preview leaves a target
// that is not the source's own kind and not already at its cap; the action opens the move dialog
// flagged as coming from the limit.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let routes: Record<string, unknown> = {};
vi.mock("../../../core/api/client.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  // A function route runs per call: it may throw (a rejected fetch) or never settle.
  api: async (path: string) => {
    const r = routes[`GET ${path}`];
    return typeof r === "function" ? (r as () => unknown)() : (r ?? { error: { code: "unrouted" } });
  },
}));

const { MoveLimitBanner } = await import("./MoveLimitBanner.tsx");
const { useSessionUI } = await import("../../sessions/ui.ts");
const { useWorkspaceStore } = await import("../../../core/store/workspace.ts");
const { setCachedConns, clearCachedConns } = await import("../../repos/connsCache.ts");
const { t } = await import("../../../lib/i18n/index.ts");
type Session = import("../../../types/session.ts").Session;
type MovePreview = import("../../sessions/move.ts").MovePreview;

const PREVIEW = "GET api/sessions/s1/move/preview";
let root: Root | null = null;
let host: HTMLDivElement;
const src = (over: Partial<Session> = {}): Session => ({ name: "s1", kind: "claude", alive: true, state: "limited", title: "作業", ...over }) as Session;
const preview = (over: Partial<MovePreview> = {}): MovePreview => ({
  source: "s1",
  kind: "claude",
  state: "limited",
  targets: [
    { kind: "claude", eligible: false, reason: "same_kind" },
    { kind: "codex", eligible: true },
    { kind: "opencode", eligible: true },
  ],
  ...over,
});
// A fresh tree per call: a mounted banner keeps the usage it read, as it does in the app.
const render = async (s: Session, state: string | undefined = s.state, running = true) => {
  act(() => root!.unmount());
  root = createRoot(host);
  await act(async () => root!.render(<MoveLimitBanner session={s} state={state} running={running} />));
  for (let i = 0; i < 6; i++) await act(async () => await Promise.resolve());
};
const banner = () => host.querySelector(".mirror-move-limit");

beforeEach(() => {
  routes = { [PREVIEW]: preview() };
  // The live, policy-filtered catalogue the offered kinds are judged by (ADR 0138 D10a).
  routes["GET api/agents/kinds"] = {
    kinds: ["claude", "codex", "opencode", "cursor", "lcpp"].map((kind) => ({ kind })),
    access: { policyConfirmed: true },
  };
  useWorkspaceStore.setState({ state: "running" } as never);
  setCachedConns({ codex: { connected: true }, opencode: { connected: true }, muse: { connected: true } } as never);
  useSessionUI.setState({ move: null, moveFromLimit: false });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

describe("MoveLimitBanner", () => {
  it.each(["limited", "spend_limit"])("is shown for %s and opens the dialog from the limit", async (state) => {
    await render(src({ state }), state);
    expect(banner()).not.toBeNull();
    await act(async () => host.querySelector<HTMLElement>(".ma-btn")!.click());
    expect(useSessionUI.getState().move?.name).toBe("s1");
    expect(useSessionUI.getState().moveFromLimit).toBe(true);
  });

  it.each([
    { rateLimitResumeAt: new Date(Date.now() + 3600_000).toISOString() },
    { stoppedLimit: "limited" },
    { stoppedLimit: "spend_limit" },
  ] satisfies Partial<Session>[])("offers a move for a stopped hold %j until it clears", async (hold) => {
    await render(src({ state: "stopped", alive: false, ...hold }));
    expect(banner()).not.toBeNull();
    await act(async () => host.querySelector<HTMLElement>(".ma-btn")!.click());
    expect(useSessionUI.getState().moveFromLimit).toBe(true);
    await act(async () => root!.render(<MoveLimitBanner session={src({ state: "stopped", alive: false })} state="stopped" running />));
    expect(banner()).toBeNull();
  });

  it("ignores an expired booking on an otherwise stopped session", async () => {
    const request = vi.fn(() => preview());
    routes[PREVIEW] = request;
    await render(src({ state: "stopped", alive: false, rateLimitResumeAt: new Date(Date.now() - 60_000).toISOString() }));
    expect(banner()).toBeNull();
    expect(request).not.toHaveBeenCalled();
  });

  it("requires an available Workspace and an eligible preview for stopped holds", async () => {
    const stopped = src({ state: "stopped", alive: false, stoppedLimit: "limited" });
    const request = vi.fn(() => preview({ refusal: { code: "move_source_state", message: "x" } }));
    routes[PREVIEW] = request;
    await render(stopped, "stopped", false);
    expect(banner()).toBeNull();
    expect(request).not.toHaveBeenCalled();
    await render(stopped);
    expect(request).toHaveBeenCalledOnce();
    expect(banner()).toBeNull();
  });

  it("is not shown for a session that is not at a limit", async () => {
    await render(src({ state: "working" }));
    expect(banner()).toBeNull();
  });

  it("is not shown for a source that is moving or already moved", async () => {
    await render(src({ moving: "t9" }));
    expect(banner()).toBeNull();
    await render(src({ movedTo: "t9" }));
    expect(banner()).toBeNull();
  });

  it("is not shown while the Workspace is not running", async () => {
    await render(src(), "limited", false);
    expect(banner()).toBeNull();
  });

  it("is not shown when the preview leaves no eligible target, or refuses the source", async () => {
    routes[PREVIEW] = preview({ targets: [{ kind: "claude", eligible: false, reason: "same_kind" }] });
    await render(src());
    expect(banner()).toBeNull();
    routes[PREVIEW] = preview({ refusal: { code: "move_source_state", message: "x" } });
    await render(src());
    expect(banner()).toBeNull();
  });

  it("never offers the source's own kind, even if a preview listed it as eligible", async () => {
    routes[PREVIEW] = preview({ targets: [{ kind: "claude", eligible: true }] });
    await render(src());
    expect(banner()).toBeNull();
  });

  it("is not shown when every candidate is at its cap", async () => {
    routes["GET api/codex/usage"] = { ok: true, fiveHour: { pct: 100 } };
    routes[PREVIEW] = preview({ targets: [{ kind: "codex", eligible: true }] });
    await render(src());
    expect(banner()).toBeNull();
  });

  it("waits for the connection check, and stays hidden when it never answers", async () => {
    clearCachedConns();
    await render(src());
    expect(banner()).toBeNull();
  });

  it("re-reads the usage and the preview while it is open", async () => {
    vi.useFakeTimers();
    try {
      routes["GET api/codex/usage"] = { ok: true, fiveHour: { pct: 100 } };
      routes[PREVIEW] = preview({ targets: [{ kind: "codex", eligible: true }] });
      await render(src());
      expect(banner()).toBeNull();
      routes["GET api/codex/usage"] = { ok: true, fiveHour: { pct: 10 } };
      await act(async () => {
        await vi.advanceTimersByTimeAsync(61_000);
      });
      expect(banner()?.textContent).toContain("Codex");
      routes["GET api/codex/usage"] = { ok: true, fiveHour: { pct: 100 } };
      await act(async () => {
        await vi.advanceTimersByTimeAsync(61_000);
      });
      expect(banner()).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("drops a usage reading when the re-read rejects", async () => {
    vi.useFakeTimers();
    try {
      routes["GET api/codex/usage"] = { ok: true, fiveHour: { pct: 100 } };
      routes[PREVIEW] = preview({ targets: [{ kind: "codex", eligible: true }] });
      await render(src());
      expect(banner()).toBeNull();
      routes["GET api/codex/usage"] = () => {
        throw new Error("offline");
      };
      await act(async () => {
        await vi.advanceTimersByTimeAsync(61_000);
      });
      // unknown, not full: the band offers the kind again, unmarked.
      expect(banner()?.textContent).toContain(t("mirror.move_limit"));
    } finally {
      vi.useRealTimers();
    }
  });

  it("does not let the previous source's preview speak for the next one", async () => {
    await render(src());
    expect(banner()).not.toBeNull();
    routes["GET api/sessions/s2/move/preview"] = () => new Promise(() => {});
    await act(async () => root!.render(<MoveLimitBanner session={src({ name: "s2" })} state="limited" running />));
    for (let i = 0; i < 6; i++) await act(async () => await Promise.resolve());
    expect(banner()).toBeNull();
  });

  it("names the kinds with quota left, and still offers when no kind can be judged", async () => {
    routes["GET api/codex/usage"] = { ok: true, fiveHour: { pct: 20 } };
    await render(src());
    expect(banner()?.textContent).toContain("Codex");
    expect(banner()?.textContent).not.toContain("OpenCode");
    routes["GET api/codex/usage"] = { error: { code: "x" } };
    await render(src());
    expect(banner()?.textContent).toContain(t("mirror.move_limit"));
  });
});
