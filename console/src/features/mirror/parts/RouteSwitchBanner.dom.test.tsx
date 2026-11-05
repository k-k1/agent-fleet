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

const { RouteSwitchBanner, isWindowStop, safeSettingUrl, resetRouteSwitchOutcomes } = await import("./RouteSwitchBanner.tsx");
const { t } = await import("../../../lib/i18n/index.ts");
const { setTenant } = await import("../../../core/api/client.ts");
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
// Same tree, new props: the real app re-renders a mounted banner as the session's state moves.
const rerender = async (s: Session, state: string | undefined = s.state) => {
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
  resetRouteSwitchOutcomes();
  setTenant("alpha");
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(() => {
  setTenant("");
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

describe("RouteSwitchBanner evidence is scoped", () => {
  it("does not show an earlier episode's allowed preview when the limit returns", async () => {
    await render(src());
    expect(band()).not.toBeNull();
    await rerender(src({ state: "working" }));
    expect(band()).toBeNull();
    let release!: (v: unknown) => void;
    routes[PREVIEW] = () => new Promise((r) => (release = r)); // the new episode's read is still out
    await rerender(src());
    expect(band()).toBeNull();
    await act(async () => release({ allowed: false, code: "route_switch_no_limit" }));
    await settle();
    expect(band()).toBeNull();
  });

  it("does not bring the offer back from the old preview after a switch is closed", async () => {
    routes[SWITCH] = { session: {} };
    routes[INPUT] = { queued: "s1" };
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    let release!: (v: unknown) => void;
    routes[PREVIEW] = () => new Promise((r) => (release = r));
    await click(t("mirror.route_switch_close"));
    expect(band()).toBeNull(); // still limited, but nothing read since
    await act(async () => release({ allowed: false, code: "route_switch_not_subscription" }));
    await settle();
    expect(band()).toBeNull();
  });

  it("drops the preview when the session's route changes", async () => {
    await render(src({ route: { route: "anthropic-subscription" } }));
    expect(band()).not.toBeNull();
    routes[PREVIEW] = () => new Promise(() => {});
    await rerender(src({ route: { route: "anthropic-api" } }));
    expect(band()).toBeNull();
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
    expect(text).toContain(t("mirror.route_switch_credits")); // and it says that route costs extra too
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
    routes[INPUT] = { queued: "s1" };
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    expect(calls.map((c) => `${c.method} ${c.path}`)).toEqual([SWITCH, INPUT]);
    expect(calls[1].body).toEqual({ prompt: t("mirror.route_switch_carry_on"), when_ready: true });
    expect(band()?.textContent).toContain(t("mirror.route_switch_done"));
    // The Agent's 202 {queued} is acceptance, not delivery: the wording says so.
    expect(band()?.textContent).toContain(t("mirror.route_switch_carry_queued"));
    expect(band()?.textContent).not.toContain(t("mirror.route_switch_carry_failed"));
  });

  it("shows progress while the switch is running and offers no second click", async () => {
    let release!: (v: unknown) => void;
    routes[SWITCH] = () => new Promise((r) => (release = r));
    routes[INPUT] = { queued: "s1" };
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
    routes[INPUT] = { queued: "s1" };
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    await act(async () => root!.render(<RouteSwitchBanner session={src({ state: "working" })} state="working" />));
    expect(band()?.textContent).toContain(t("mirror.route_switch_cancel_pending"));
    await click(t("mirror.route_switch_close"));
    expect(band()).toBeNull();
  });

  it("shows route_switch_failed as incomplete, never promising a restored session, and sends no carry-on", async () => {
    routes[SWITCH] = { error: { code: "route_switch_failed", message: "the new route would not start; rolled back" } };
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    const text = band()?.textContent ?? "";
    expect(text).toContain(t("mirror.route_switch_incomplete", { detail: "" }).split(":")[0]);
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
    expect(text).not.toContain(t("mirror.route_switch_incomplete", { detail: "" }).split(":")[0]);
  });

  it.each([
    ["a lost response", () => Promise.reject(new Error("offline"))],
    ["a gateway timeout", { error: { code: "gateway_timeout", message: "504" } }],
    ["an unknown code", { error: { code: "something_new", message: "?" } }],
  ])("treats %s as an unknown outcome, never as nothing changed", async (_n, answer) => {
    routes[SWITCH] = answer;
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    const text = band()?.textContent ?? "";
    expect(text).toContain(t("mirror.route_switch_unknown", { detail: "" }).split(":")[0]);
    expect(text).not.toContain(t("mirror.route_switch_refused", { detail: "" }).split(":")[0]);
    expect(calls.map((c) => c.method + " " + c.path)).toEqual([SWITCH]);
  });

  it("finishes the carry-on when the mirror is closed while the switch is pending, and shows the outcome on return", async () => {
    let release!: (v: unknown) => void;
    routes[SWITCH] = () => new Promise((r) => (release = r));
    routes[INPUT] = { queued: "s1" };
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    act(() => root!.unmount()); // the pane moved to another session
    await act(async () => release({ session: {} }));
    await settle();
    expect(calls.map((c) => c.method + " " + c.path)).toEqual([SWITCH, INPUT]);
    root = createRoot(host);
    await act(async () => root!.render(<RouteSwitchBanner session={src({ state: "working" })} state="working" />));
    await settle();
    expect(band()?.textContent).toContain(t("mirror.route_switch_done"));
  });

  it("says the Agent refused the carry-on only when it answered with an error", async () => {
    routes[SWITCH] = { session: {} };
    routes[INPUT] = { error: { code: "x" } };
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    expect(band()?.textContent).toContain(t("mirror.route_switch_carry_failed"));
  });

  it.each([
    ["a lost response", () => Promise.reject(new Error("offline"))],
    ["a gateway error", { error: { code: "http_502", status: 502, message: "bad gateway" } }],
  ])("does not claim the carry-on was refused after %s: it may already be queued", async (_n, answer) => {
    routes[SWITCH] = { session: {} };
    routes[INPUT] = answer;
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    const text = band()?.textContent ?? "";
    expect(text).toContain(t("mirror.route_switch_carry_unknown"));
    expect(text).not.toContain(t("mirror.route_switch_carry_failed"));
  });

  it("does not send the carry-on into another workspace's same-named session", async () => {
    let release!: (v: unknown) => void;
    routes[SWITCH] = () => new Promise((r) => (release = r));
    routes[INPUT] = { queued: "s1" };
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    setTenant("beta"); // the Console is switched while the switch is pending
    await act(async () => release({ session: {} }));
    await settle();
    expect(calls.map((c) => c.method + " " + c.path)).toEqual([SWITCH]);
    await rerender(src());
    expect(band()?.textContent ?? "").not.toContain(t("mirror.route_switch_done")); // beta's s1 shows nothing of alpha's action
    setTenant("alpha");
    await rerender(src());
    expect(band()?.textContent).toContain(t("mirror.route_switch_carry_skipped"));
  });

  it("does not show one workspace's outcome for the same-named session in another", async () => {
    routes[SWITCH] = { session: {} };
    routes[INPUT] = { queued: "s1" };
    await render(src());
    await click(t("mirror.route_switch_action"));
    await click(t("mirror.route_switch_do"));
    expect(band()?.textContent).toContain(t("mirror.route_switch_done"));
    setTenant("beta");
    routes[PREVIEW] = { allowed: false, code: "route_switch_no_limit" };
    await rerender(src());
    expect(band()).toBeNull();
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
