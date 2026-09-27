// Which verb acts on which unit (ADR 0081 decision 12).
//
// Pause / resume / skip / abort are GROUP operations — the group is what the person
// submitted and what they think in — and the ✕ on a line cancels exactly one picture. Get
// that wrong in either direction and someone loses thirty-nine pictures they wanted, or
// keeps thirty-nine they did not; nothing in the types stops it, so it is checked here.
import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { JobList } from "./parts/JobList.tsx";
import { foldGroups } from "./jobs.ts";
import type { Job, JobGroup } from "./wire.ts";

let host: HTMLDivElement;
let root: Root;
const groupOps: [string, string][] = [];
const queueOps: string[] = [];
const cancelled: string[] = [];

const JOBS: Job[] = [
  { id: "j1", group: "g1", state: "done" },
  { id: "j2", group: "g1", state: "running", started_at: "2026-09-13T00:00:00Z", typical_ms: 30_000 },
  { id: "j3", group: "g1", state: "queued", position: 2 },
];
const GROUPS: JobGroup[] = [{ id: "g1", label: "cfg sweep", state: "running", done: 1, failed: 0, total: 40 }];

const render = async (paused = false, studioId?: string, jobs: Job[] = JOBS) => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  const groups = paused ? [{ ...GROUPS[0], state: "paused" as const }] : GROUPS;
  await act(async () => {
    root.render(
      <JobList
        rows={foldGroups(jobs, groups)}
        queuePaused={false}
        queued={3}
        queueMax={200}
        now={Date.parse("2026-09-13T00:00:15Z")}
        onGroupOp={(id, op) => groupOps.push([id, op])}
        onQueueOp={(op) => queueOps.push(op)}
        onCancelJob={(id) => cancelled.push(id)}
        studioId={studioId}
      />,
    );
  });
};

const byText = (t: string): HTMLButtonElement | undefined =>
  [...host.querySelectorAll<HTMLButtonElement>("button")].find((b) => b.textContent?.trim() === t);

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.innerHTML = "";
  groupOps.length = 0;
  queueOps.length = 0;
  cancelled.length = 0;
});

describe("待ち行列の行", () => {
  it("40 枚のグループは 1 行に畳まれ、件数は Agent のもの", async () => {
    await render();
    expect(host.querySelectorAll(".igen-qrow")).toHaveLength(1);
    expect(host.querySelector(".igen-qrow-counts")?.textContent).toContain("40");
    expect(host.querySelector(".igen-qrow-label")?.textContent).toBe("cfg sweep");
  });

  it("バーは done / failed / running の 3 区間", async () => {
    await render();
    expect(host.querySelector(".igen-bar-done")).toBeTruthy();
    expect(host.querySelector(".igen-bar-failed")).toBeTruthy();
    expect(host.querySelector(".igen-bar-running")).toBeTruthy();
  });
});

describe("取消の単位", () => {
  it("一時停止・飛ばし・中断はグループへ届く", async () => {
    await render();
    await act(async () => byText("一時停止")?.click());
    await act(async () => byText("今の 1 枚を飛ばす")?.click());
    await act(async () => byText("中断")?.click());
    expect(groupOps).toEqual([
      ["g1", "pause"],
      ["g1", "skip"],
      ["g1", "cancel"],
    ]);
    expect(cancelled).toEqual([]);
  });

  it("行の ✕ はそのジョブだけを取り消す（グループには触らない）", async () => {
    await render();
    const xs = [...host.querySelectorAll<HTMLButtonElement>(".igen-job button")];
    // The finished job has no ✕: there is nothing left to cancel.
    expect(xs).toHaveLength(2);
    await act(async () => xs[0].click());
    expect(cancelled).toEqual(["j2"]);
    expect(groupOps).toEqual([]);
  });

  it("止めたグループは「再開」を出す", async () => {
    await render(true);
    expect(byText("再開")).toBeTruthy();
    expect(byText("一時停止")).toBeUndefined();
  });

  it("行列全体の一時停止はグループ操作ではない", async () => {
    await render();
    await act(async () => byText("すべて一時停止")?.click());
    expect(queueOps).toEqual(["pause"]);
    expect(groupOps).toEqual([]);
  });
});

describe("他のスタジオのジョブ", () => {
  const mixed: Job[] = [
    ...JOBS.map((j) => ({ ...j, studio: "B" })),
    { id: "mine", studio: "A", state: "done" },
    { id: "sess", state: "done" },
  ];

  it("自分の行は外に、他のスタジオとスタジオ外の行は畳んだ中に薄く出る", async () => {
    await render(false, "A", mixed);
    const others = host.querySelector("details.igen-queue-others");
    expect(others).toBeTruthy();
    expect(others?.hasAttribute("open")).toBe(false);
    expect(others?.querySelector("summary")?.textContent).toContain("1 件が進行中");
    expect(others?.querySelectorAll(".igen-qrow.other")).toHaveLength(2);
    const badges = [...(others?.querySelectorAll(".igen-badge.other") || [])].map((b) => b.textContent);
    expect(badges).toEqual(["他のスタジオ", "スタジオ外"]);
    const outside = [...host.querySelectorAll(".igen-qrow")].filter((r) => !r.closest("details"));
    expect(outside).toHaveLength(1);
    expect(outside[0].classList.contains("other")).toBe(false);
  });

  it("スタジオを渡さなければ全部を自分の行として出す", async () => {
    await render(false, undefined, mixed);
    expect(host.querySelector("details.igen-queue-others")).toBeNull();
    expect(host.querySelectorAll(".igen-qrow")).toHaveLength(3);
  });
});
