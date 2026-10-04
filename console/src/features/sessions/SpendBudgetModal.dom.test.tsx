// The spend budget's dialog and chip (#1054). What is pinned: "raise and resume" writes the cap
// BEFORE it resumes (a resume first would run a turn the old cap then stops), refuses a cap that
// does not clear the spend, and the chip writes the spend as an approximation against the cap.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const calls: string[] = [];
type SpendFixture = {
  name: string; spendCapUsd: number; spendCapHitAt?: string; hardFactor: number; spendUsd: number;
  priced: boolean; unpriced?: boolean; children: never[]; childrenUsd: number;
};
const base: SpendFixture = { name: "sb1", spendCapUsd: 5, spendCapHitAt: "2026-10-04T00:00:00Z", hardFactor: 2, spendUsd: 5.2, priced: true, children: [], childrenUsd: 0 };
let spend: SpendFixture = { ...base };
// "ok" answers the fixture; "reject" fails the read; "pending" never answers.
let spendRead: "ok" | "reject" | "pending" = "ok";
// What the write answers with beyond the cap: the Agent re-prices, so it can still report a hit.
let capReply: Record<string, unknown> = {};
vi.mock("../../core/api/client.ts", async (orig) => ({
  ...(await orig<Record<string, unknown>>()),
  sessionSpend: async (name: string) => {
    calls.push("spend:" + name);
    if (spendRead === "reject") throw new Error("network");
    if (spendRead === "pending") return new Promise(() => {});
    return spend;
  },
  sessionSetSpendCap: async (name: string, usd: number) => {
    calls.push(`cap:${name}:${usd}`);
    return { spendCapUsd: usd, ...capReply };
  },
}));

const { SpendBudgetModal } = await import("./SpendBudgetModal.tsx");
const { SpendChip } = await import("../mirror/SpendChip.tsx");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");
const { useSessionsStore } = await import("./store.ts");
const { t } = await import("../../lib/i18n/index.ts");
// The catalogue's own strings, so the assertions hold in whichever locale the tests run.
const NO_PRICE = () => t("sess.budget_chip_unpriced");
const PARTLY = () => t("sess.budget_partly_unpriced");
const UNPRICED = () => t("sess.budget_unpriced");
type Session = import("../../types/session.ts").Session;

let root: Root | null = null;
let host: HTMLDivElement;

const flush = async () => {
  for (let i = 0; i < 5; i++) await act(async () => await Promise.resolve());
};

const render = async (node: React.ReactNode) => {
  await act(async () => root!.render(<ToastProvider>{node}</ToastProvider>));
  await flush();
};

const input = () => document.querySelector<HTMLInputElement>(".ui-modal-body input")!;
const submit = () => document.querySelector<HTMLButtonElement>(".ui-modal-foot button[type=submit]")!;
const type = async (v: string) => {
  await act(async () => {
    const set = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    set.call(input(), v);
    input().dispatchEvent(new Event("input", { bubbles: true }));
  });
};

beforeEach(() => {
  calls.length = 0;
  spend = { ...base };
  spendRead = "ok";
  capReply = {};
  useSessionsStore.setState({
    start: async (name: string) => {
      calls.push("start:" + name);
      return true;
    },
    refresh: async () => {},
  } as never);
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  document.body.innerHTML = "";
});

const stopped: Session = { name: "sb1", kind: "claude", alive: false, spendCapUsd: 5, spendCapHitAt: "2026-10-04T00:00:00Z" };

describe("SpendBudgetModal", () => {
  it("raises the cap, then resumes", async () => {
    await render(<SpendBudgetModal s={stopped} onClose={() => {}} />);
    expect(input().value).toBe("8"); // max(5 × 1.5, 5.2 × 1.25) rounded up
    await act(async () => submit().click());
    await flush();
    expect(calls.filter((c) => !c.startsWith("spend:"))).toEqual(["cap:sb1:8", "start:sb1"]);
  });

  it("will not resume on a cap that leaves the spend at or over it", async () => {
    await render(<SpendBudgetModal s={stopped} onClose={() => {}} />);
    await type("5.20");
    expect(submit().disabled).toBe(true);
    await type("0"); // removing the budget is always allowed
    expect(submit().disabled).toBe(false);
  });

  it("only saves for a running session", async () => {
    await render(<SpendBudgetModal s={{ ...stopped, alive: true, spendCapHitAt: undefined }} onClose={() => {}} />);
    expect(input().value).toBe("5");
    await type("12.5");
    await act(async () => submit().click());
    await flush();
    expect(calls.filter((c) => !c.startsWith("spend:"))).toEqual(["cap:sb1:12.5"]);
  });

  it.each(["reject", "pending"] as const)("will not resume on a positive cap when the spend read is %s", async (mode) => {
    spendRead = mode;
    await render(<SpendBudgetModal s={stopped} onClose={() => {}} />);
    await type("100");
    expect(submit().disabled).toBe(true);
    await type("0");
    expect(submit().disabled).toBe(false);
  });

  it("does not resume when the write still reports the crossing", async () => {
    capReply = { spendCapHitAt: "2026-10-04T00:00:00Z", spendUsd: 8.5 };
    await render(<SpendBudgetModal s={stopped} onClose={() => {}} />);
    await act(async () => submit().click());
    await flush();
    expect(calls.filter((c) => !c.startsWith("spend:"))).toEqual(["cap:sb1:8"]);
    expect(document.querySelector("[data-testid=spend-budget-spent]")!.textContent).toContain("≈$8.50");
    expect(submit().disabled).toBe(true); // 8 is now at or under the fresh spend
  });

  it("says when part of the spend has no price, and when none of it has", async () => {
    spend = { ...base, unpriced: true };
    await render(<SpendBudgetModal s={stopped} onClose={() => {}} />);
    const mixed = document.querySelector("[data-testid=spend-budget-spent]")!.textContent!;
    expect(mixed).toContain("≈$5.20");
    expect(mixed).toContain(PARTLY());
    act(() => root!.unmount());
    root = createRoot(host);
    spend = { ...base, spendUsd: 0, priced: false, unpriced: true };
    await render(<SpendBudgetModal s={{ ...stopped, alive: true }} onClose={() => {}} />);
    expect(document.querySelector("[data-testid=spend-budget-spent]")!.textContent).toBe(UNPRICED());
    expect(document.querySelector("[data-testid=spend-budget-spent]")!.textContent).not.toContain("≈$");
  });

  it("rejects what the Agent would", async () => {
    await render(<SpendBudgetModal s={{ ...stopped, alive: true }} onClose={() => {}} />);
    await type("1.234");
    expect(submit().disabled).toBe(true);
  });
});

describe("SpendChip", () => {
  it("shows the spend as an approximation against the cap, red once over", async () => {
    await render(<SpendChip s={{ ...stopped, alive: true }} />);
    const chip = document.querySelector<HTMLElement>("[data-testid=spend-chip]")!;
    expect(chip.textContent).toBe("≈$5.20 / $5.00");
    expect(chip.className).toContain("cb-spend-over");
  });

  it("never shows unpriced spend as $0", async () => {
    spend = { ...base, spendUsd: 0, priced: false, unpriced: true };
    await render(<SpendChip s={{ ...stopped, alive: true }} />);
    const chip = document.querySelector<HTMLElement>("[data-testid=spend-chip]")!;
    expect(chip.textContent).toBe(NO_PRICE() + " / $5.00");
    expect(chip.title).toContain(UNPRICED());
  });

  it("marks a figure that leaves unpriced turns out", async () => {
    spend = { ...base, spendUsd: 1.2, unpriced: true };
    await render(<SpendChip s={{ ...stopped, alive: true }} />);
    const chip = document.querySelector<HTMLElement>("[data-testid=spend-chip]")!;
    expect(chip.textContent).toBe("≈$1.20+ / $5.00");
    expect(chip.title).toContain(PARTLY());
  });

  it("shows unpriced spend even without a budget", async () => {
    spend = { ...base, spendCapUsd: 0, spendUsd: 0, priced: false, unpriced: true };
    await render(<SpendChip s={{ ...stopped, alive: true, spendCapUsd: 0 }} />);
    expect(document.querySelector("[data-testid=spend-chip]")!.textContent).toBe(NO_PRICE());
  });

  it("shows the children's spend beside it", async () => {
    spend = { ...base, spendUsd: 4, childrenUsd: 3.5 };
    await render(<SpendChip s={{ ...stopped, alive: true }} />);
    expect(document.querySelector("[data-testid=spend-chip]")!.className).toContain("cb-spend-warn");
    expect(document.querySelector(".cb-spend-kids")!.textContent).toContain("≈$3.50");
  });
});
