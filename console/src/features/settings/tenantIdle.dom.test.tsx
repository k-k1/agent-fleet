// 名簿に「いつ止まるか / なぜ止まらないか」を出す（docs/log/75 P4）。
//
// この画面が存在する理由は、自動停止が効かないときに運用者へ見えるものが何も無かった
// こと（reaper はログを出すだけで、調べる唯一の手段が他人のコンテナへ docker exec して
// status ファイルを読むことだった）。だから固定したいのは次の 3 点:
//   ①「止まらない」は「予定が出ていない」と別物として見えること
//   ② 止めている主体（セッション名・ピン・在席）が言えること
//   ③ 稼働していない Workspace に停止予定を出さないこと
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: vi.fn(),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
}));
vi.mock("../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));

import { MembersPanel } from "./tenantMembers.tsx";

const SIZING = { mem_meaning: "cap", cpu_effective: true };
const inHours = (h: number) => new Date(Date.now() + h * 3600_000).toISOString();

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mountRoster(members: unknown[]) {
  api.mockImplementation((p: string) =>
    p === "api/admin/workspace-sizing" ? Promise.resolve(SIZING) : Promise.resolve({ members }),
  );
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<MembersPanel slug="acme" isSuper={false} onOpenMember={() => {}} />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const rowText = (i: number) => (document.querySelectorAll(".member-row")[i]?.textContent || "").replace(/\s+/g, " ");

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  vi.clearAllMocks();
});

describe("メンバー名簿の自動停止の見通し", () => {
  it("止まる予定は残り時間で出る", async () => {
    await mountRoster([
      {
        user_key: "a",
        role: "member",
        state: "running",
        idle: { enabled: true, stopAt: inHours(1.5), observedAt: new Date().toISOString() },
      },
    ]);
    expect(rowText(0)).toMatch(/1h(2[0-9]|30)m/); // 「あと 1h30m で停止」
  });

  it("★止まらない行は理由と主体を出し、予定時刻では埋めない", async () => {
    await mountRoster([
      {
        user_key: "a",
        role: "member",
        state: "running",
        idle: {
          enabled: true,
          stopAt: inHours(1),
          holders: [{ kind: "working", session: "s5" }, { kind: "watching" }],
          observedAt: new Date().toISOString(),
        },
      },
    ]);
    const t = rowText(0);
    expect(t).toContain("s5"); // 誰が止めているか
    expect(t).not.toMatch(/1h/); // 予定は出さない（出すと「もうすぐ止まる」と誤読される）
    expect(document.querySelector(".mr-idle.hold")).toBeTruthy(); // 注意色（費用が出続けている）
  });

  it("自動停止が無効なテナントは「無効」と言う（予定なしと区別する）", async () => {
    await mountRoster([
      { user_key: "a", role: "member", state: "running", idle: { enabled: false, observedAt: new Date().toISOString() } },
    ]);
    expect(document.querySelector(".mr-idle")?.textContent).toBeTruthy();
    expect(document.querySelector(".mr-idle.hold")).toBeFalsy();
  });

  it("停止中の Workspace には何も出さない（止める予定は存在しない）", async () => {
    await mountRoster([
      {
        user_key: "a",
        role: "member",
        state: "stopped",
        idle: { enabled: true, stopAt: inHours(1), observedAt: new Date().toISOString() },
      },
      { user_key: "b", role: "member", state: "running" }, // 観測がまだ無い行
    ]);
    expect(document.querySelectorAll(".mr-idle").length).toBe(0);
  });
});
