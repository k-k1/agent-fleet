// Late answers of a poll must not land after the thing they were read for changed (#4786): the
// effect-scoped guards these pollers had before moving onto useInterval, as generation checks.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

type Deferred = { path: string; resolve: (v: unknown) => void };
const pending: Deferred[] = [];
vi.mock("../../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  const defer = (path: string) => new Promise((resolve) => pending.push({ path, resolve }));
  return {
    ...real,
    api: (path: string) => defer(path),
    sessionSpend: (name: string) => defer("spend/" + name),
  };
});

const { useHandoffProposals } = await import("./HandoffProposal.tsx");
const { SpendChip } = await import("./SpendChip.tsx");

let root: Root;
let host: HTMLDivElement;
beforeEach(() => {
  vi.useFakeTimers();
  pending.length = 0;
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});
afterEach(async () => {
  await act(async () => root.unmount());
  host.remove();
  vi.useRealTimers();
});

const answer = (i: number, v: unknown) => act(async () => pending[i]!.resolve(v));

function Probe({ session }: { session: string }) {
  const [ps] = useHandoffProposals(session);
  return <div data-testid="ids">{ps.map((p) => p.id).join(",")}</div>;
}
const ids = () => host.querySelector("[data-testid=ids]")!.textContent;

describe("useHandoffProposals", () => {
  it("drops a read from an earlier visit to the same session (A -> B -> A)", async () => {
    await act(async () => root.render(<Probe session="a" />));
    await act(async () => root.render(<Probe session="b" />));
    await act(async () => root.render(<Probe session="a" />));
    expect(pending).toHaveLength(3);
    await answer(2, { proposals: [{ id: "new", created_at: 1 }] });
    await answer(0, { proposals: [{ id: "stale", created_at: 1 }] });
    expect(ids()).toBe("new");
  });

  it("keeps the newest read when a slow initial read returns after a poll", async () => {
    await act(async () => root.render(<Probe session="a" />));
    await act(async () => void vi.advanceTimersByTime(3000));
    expect(pending).toHaveLength(2);
    await answer(1, { proposals: [{ id: "poll", created_at: 1 }] });
    await answer(0, { proposals: [{ id: "initial", created_at: 1 }] });
    expect(ids()).toBe("poll");
  });
});

describe("SpendChip", () => {
  const sess = (name: string, cap: number) => ({ name, alive: true, spendCapUsd: cap }) as never;
  const spend = (name: string, usd: number) => ({ name, spendUsd: usd, priced: true, hardFactor: 1, children: [], childrenUsd: 0 });
  const chip = () => host.querySelector("[data-testid=spend-chip]")?.textContent ?? "";

  it("drops a poll answer that belongs to the previous session", async () => {
    await act(async () => root.render(<SpendChip s={sess("a", 0)} />));
    await act(async () => void vi.advanceTimersByTime(30_000));
    expect(pending).toHaveLength(2);
    await act(async () => root.render(<SpendChip s={sess("b", 0)} />));
    expect(pending).toHaveLength(3);
    await answer(2, spend("b", 2));
    await answer(1, spend("a", 9));
    expect(chip()).toContain("2");
    expect(chip()).not.toContain("9");
  });

  it("drops a poll answer that predates a cap change", async () => {
    await act(async () => root.render(<SpendChip s={sess("a", 0)} />));
    await act(async () => void vi.advanceTimersByTime(30_000));
    await act(async () => root.render(<SpendChip s={sess("a", 5)} />));
    await answer(2, { ...spend("a", 1), spendCapUsd: 5 });
    await answer(1, spend("a", 1));
    expect(chip()).toContain("/ $5.00");
  });
});
