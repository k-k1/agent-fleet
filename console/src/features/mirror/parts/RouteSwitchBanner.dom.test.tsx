// The switch to the API route offered at a window stop (ADR 0128 decision 10). Pinned: it is
// offered at "limited" only (never at spend_limit, for a kind the Agent refuses, or
// when the Agent's preview does not allow it); the cost, prompt-cache and account-setting
// statements stand in the confirmation; a confirmed switch POSTs once and then sends the
// "carry on" as an ordinary message; every refusal code has its own text; a rollback, a
// refusal and an unconfirmed resume cancel stay visible until closed.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let routes: Record<string, unknown> = {};
const calls: Array<{ path: string; method: string; body?: unknown }> = [];
vi.mock("../../../core/api/client.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  api: async (path: string) => {
    const r = routes[`GET ${path}`];
    return typeof r === "function" ? (r as () => unknown)() : (r ?? { error: { code: "unrouted" } });
  },
  apiJSON: async (path: string, method: string, body?: unknown) => {
    calls.push({ path, method, body });
    const r = routes[`${method} ${path}`];
    return typeof r === "function" ? (r as () => unknown)() : (r ?? { error: { code: "unrouted" } });
  },
}));

const { RouteSwitchBanner, isWindowStop, safeSettingUrl } = await import("./RouteSwitchBanner.tsx");
const { t } = await import("../../../lib/i18n/index.ts");
const { errText } = await import("../../../core/api/client.ts");
type Session = import("../../../types/session.ts").Session;
type Preview = import("./RouteSwitchBanner.tsx").RouteSwitchPreview;

const PREVIEW = "GET api/sessions/s1/route-switch";
const SWITCH = "POST api/sessions/s1/route-switch";
const INPUT = "POST api/sessions/s1/input";
let root: Root | null = null;
let host: HTMLDivElement;
const src = (over: Partial<Session> = {}): Session => ({ name: "s1", kind: "claude", alive: true, state: "limited", title: "x", ...over }) as Session;
const allowed = (over: Partial<Preview> = {}): Preview => ({
  allowed: true,
  from: "anthropic-subscription",
  to: "anthropic-api",
  mayCostMore: true,
  cacheNotShared: true,
  accountSettingUrl: "https://claude.ai/settings/usage",
  accountSettingAffectsAll: true,
  ...over,
});
const settle = async () => {
  for (let i = 0; i < 8; i++) await act(async () => await Promise.resolve());
};
const render = async (s: Session, state: string | undefined = s.state) => {
  act(() => root!.unmount());
  root = createRoot(host);
  await act(async () => root!.render(<RouteSwitchBanner session={s} state={state} />));
  await settle();
};
const band = () => host.querySelector(".mirror-route-switch");
const click = async (label: string) => {
  const b = [...host.querySelectorAll("button")].find((x) => x.textContent === label);
  expect(b, `button ${label}`).toBeTruthy();
  await act(async () => b!.click());
  await settle();
};

beforeEach(() => {
  routes = { [PREVIEW]: allowed() };
  calls.length = 0;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  act(() => root?.unmount());
  host.remove();
  root = null;
});

describe("RouteSwitchBanner offer", () => {
  it("is offered at a window stop when the Agent's preview allows it", async () => {
    await render(src());
    expect(band()?.textContent).toContain(t("mirror.route_switch_offer"));
    expect(host.textContent).toContain(t("mirror.route_switch_action"));
  });

  it("names the reset time when one is booked", async () => {
    await render(src({ rateLimitResumeAt: new Date(Date.now() + 3600_000).toISOString() }));
    expect(band()?.textContent).not.toContain("{at}");
    expect(band()?.textContent?.length).toBeGreaterThan(10);
  });

  it.each([
    ["a spend limit", src({ state: "spend_limit" })],
    ["a working session", src({ state: "working" })],
    ["a moving source", src({ moving: "t9" })],
    ["a moved source", src({ movedTo: "t9" })],
  ])("is not offered for %s", async (_n, s) => {
    await render(s);
    expect(band()).toBeNull();
  });

  it("is not offered when the preview refuses, fails, or reports an unfinished switch", async () => {
    routes[PREVIEW] = { allowed: false, code: "route_switch_not_subscription", reason: "x" };
    await render(src());
    expect(band()).toBeNull();
    routes[PREVIEW] = { error: { code: "x" } };
    await render(src());
    expect(band()).toBeNull();
    routes[PREVIEW] = allowed({ pending: true });
    await render(src());
    expect(band()).toBeNull();
  });

  it("is not offered for a kind the Agent says has no metered route", async () => {
    routes[PREVIEW] = { allowed: false, code: "route_switch_unsupported", reason: "x" };
    await render(src({ kind: "codex" }));
    expect(band()).toBeNull();
  });

  it("re-reads the preview while it is open", async () => {
    vi.useFakeTimers();
    try {
      routes[PREVIEW] = { allowed: false, code: "route_switch_no_limit" };
      await render(src());
      expect(band()).toBeNull();
      routes[PREVIEW] = allowed();
      await act(async () => {
        await vi.advanceTimersByTimeAsync(61_000);
      });
      expect(band()).not.toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });

  it("isWindowStop is limited only; the setting link is https only", () => {
    expect(isWindowStop("limited")).toBe(true);
    expect(isWindowStop("spend_limit")).toBe(false);
    expect(safeSettingUrl("https://claude.ai/settings/usage")).toBe("https://claude.ai/settings/usage");
    expect(safeSettingUrl("javascript:alert(1)")).toBeNull();
    expect(safeSettingUrl("http://x.test/")).toBeNull();
    expect(safeSettingUrl(undefined)).toBeNull();
  });
});

describe("RouteSwitchBanner confirmation", () => {
  it("states the cost, the prompt cache and that the account setting affects every session", async () => {
    await render(src());
    await click(t("mirror.route_switch_action"));
    const text = host.textContent ?? "";
    expect(text).toContain(t("mirror.route_switch_cost"));
    expect(text).toContain(t("mirror.route_switch_cache"));
    expect(text).toContain(t("mirror.route_switch_credits_all"));
    const a = host.querySelector("a");
    expect(a?.getAttribute("href")).toBe("https://claude.ai/settings/usage");
    expect(a?.getAttribute("rel")).toContain("noopener");
    expect(calls).toHaveLength(0); // opening the confirmation changes nothing
  });

  it("omits the link, and its all-sessions claim, when the Agent gives no URL", async () => {
    routes[PREVIEW] = allowed({ accountSettingUrl: undefined });
    await render(src());
    await click(t("mirror.route_switch_action"));
    expect(host.querySelector("a")).toBeNull();
    expect(host.textContent).not.toContain(t("mirror.route_switch_credits_all"));
  });

  it("cancel goes back to the offer without calling the Agent", async () => {
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_cancel"));
    expect(host.textContent).toContain(t("mirror.route_switch_action"));
    expect(calls).toHaveLength(0);
  });
});

describe("RouteSwitchBanner switching", () => {
  it("switches once, then sends the carry-on as an ordinary message", async () => {
    routes[SWITCH] = { session: { name: "s1" }, from: "anthropic-subscription", to: "anthropic-api" };
    routes[INPUT] = { ok: true };
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual([SWITCH, INPUT]);
    expect(calls[1].body).toEqual({ prompt: t("mirror.route_switch_carry_on"), when_ready: true });
    expect(band()?.textContent).toContain(t("mirror.route_switch_done"));
    expect(band()?.textContent).not.toContain(t("mirror.route_switch_carry_failed"));
  });

  it("shows progress while the switch is running and offers no second click", async () => {
    let release!: (v: unknown) => void;
    routes[SWITCH] = () => new Promise((r) => (release = r));
    routes[INPUT] = { ok: true };
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    expect(band()?.getAttribute("aria-busy")).toBe("true");
    expect(band()?.textContent).toContain(t("mirror.route_switch_progress"));
    expect(host.querySelectorAll("button")).toHaveLength(0);
    await act(async () => release({ session: {} }));
    await settle();
    expect(band()?.textContent).toContain(t("mirror.route_switch_done"));
  });

  it("says when the scheduled resume could not be cancelled yet, and when the carry-on was not sent", async () => {
    routes[SWITCH] = { session: {}, bookingCancelPending: true };
    routes[INPUT] = { error: { code: "x" } };
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    const text = band()?.textContent ?? "";
    expect(text).toContain(t("mirror.route_switch_cancel_pending"));
    expect(text).toContain(t("mirror.route_switch_carry_failed"));
  });

  it("keeps the outcome after the session is no longer limited, until it is closed", async () => {
    routes[SWITCH] = { session: {}, bookingCancelPending: true };
    routes[INPUT] = { ok: true };
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    await act(async () => root!.render(<RouteSwitchBanner session={src({ state: "working" })} state="working" />));
    expect(band()?.textContent).toContain(t("mirror.route_switch_cancel_pending"));
    await click(t("mirror.route_switch_close"));
    expect(band()).toBeNull();
  });

  it("shows a rollback as rolled back, with the Agent's detail, and sends no carry-on", async () => {
    routes[SWITCH] = { error: { code: "route_switch_failed", message: "the new route would not start; rolled back" } };
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    const text = band()?.textContent ?? "";
    expect(text).toContain(t("mirror.route_switch_rolled_back", { detail: "" }).split(":")[0]);
    expect(text).toContain("the new route would not start");
    expect(text).not.toContain(t("mirror.route_switch_refused", { detail: "" }).split(":")[0]); // not "nothing changed"
    expect(calls.map((c) => c.method + " " + c.path)).toEqual([SWITCH]);
  });

  it("shows a refusal after the preview as nothing changed", async () => {
    routes[SWITCH] = { error: { code: "route_switch_no_limit", message: "no record" } };
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    const text = band()?.textContent ?? "";
    expect(text).toContain(t("mirror.route_switch_refused", { detail: "" }).split(":")[0]);
    expect(text).toContain(errText({ code: "route_switch_no_limit", message: "" }));
    expect(text).not.toContain(t("mirror.route_switch_rolled_back", { detail: "" }).split(":")[0]);
  });

  it("treats a network failure as nothing changed, not as a rollback", async () => {
    routes[SWITCH] = () => Promise.reject(new Error("offline"));
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    expect(band()?.textContent).toContain(t("mirror.route_switch_refused", { detail: "" }).split(":")[0]);
  });
});

describe("refusal codes", () => {
  it.each([
    "route_switch_unsupported",
    "route_switch_not_subscription",
    "route_switch_route_unproven",
    "route_switch_no_limit",
    "route_switch_spend_limit",
    "route_switch_state",
    "route_switch_busy",
    "route_switch_failed",
    "route_switch_booking",
  ])("%s has its own text", (code) => {
    const text = errText({ code, message: "raw agent sentence" });
    expect(text).not.toBe("raw agent sentence");
    expect(text.length).toBeGreaterThan(10);
  });
});
