// 共有セッションでも「引き継ぎ」の中身が読めること。
//
// propose_session_handoff が転写に残すのはツール行と定型の完了文だけで、次セッションへ渡す
// 本文は所有者側の別ストアにある。ミラーはそれをカードとして会話へ差し込むが、共有ビューは
// 転写しか取っていなかったので、共有先には「引き継いだらしい」ことしか見えなかった。
//
// あわせて docs/log/59 §3 の約束 —「能力が無い操作要素は描画しない」— も押さえる: 共有先は
// 編集も破棄も起動もできないので、そのボタンが出てはいけない。
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

// MarkdownView は remark/rehype を丸ごと引き込む。ここで見たいのは引き継ぎカードなので素通し。
vi.mock("../viewer/MarkdownView.tsx", () => ({
  MarkdownView: ({ source }: { source?: string }) => <div className="markdown">{source}</div>,
}));

const api = vi.fn();
vi.mock("../../core/api/client.ts", () => ({
  api: (path: string) => api(path),
  apiJSON: vi.fn(async () => ({})),
  errText: (e: unknown) => String((e as { message?: string })?.message ?? e),
  // マーカー（docs/log/69）が読み手の login id を tenant ストア経由で引くので、その初期化に要る。
  getTenant: () => "",
}));

import { SharedSessionView } from "./SharedSessionView.tsx";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

const MESSAGES = {
  cursor: 10,
  firstLine: 0,
  hasMore: false,
  status: "idle",
  messages: [
    { role: "user", text: "続きを頼む", idx: 1, anchorId: "u1", ts: "2026-08-18T10:00:00Z" },
    { role: "assistant", idx: 2, ts: "2026-08-18T10:01:00Z", parts: [{ kind: "text", text: "引き継ぎを用意しました" }] },
  ],
};
const PROPOSALS = {
  proposals: [{ id: "hp_1", title: "残作業の続き", prompt: "未完了: 表示の実装。次はここから。", created_at: Date.parse("2026-08-18T10:02:00Z") }],
};

beforeEach(() => {
  (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT = true;
  api.mockReset();
  api.mockImplementation(async (path: string) => {
    if (path.includes("/handoff-proposals")) return PROPOSALS;
    if (path.includes("/messages")) return MESSAGES;
    return { sessions: [] };
  });
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
  delete (globalThis as { IS_REACT_ACT_ENVIRONMENT?: boolean }).IS_REACT_ACT_ENVIRONMENT;
});

async function render() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<SharedSessionView sharedSessionId="catalog-1" />);
  });
  // 転写と提案はそれぞれ別のポーリングで入ってくる。
  await act(async () => {
    await Promise.resolve();
  });
  return host;
}

describe("SharedSessionView の引き継ぎ提案", () => {
  it("提案の本文とタイトルを出す", async () => {
    const el = await render();
    const card = el.querySelector(".mirror-handoff");
    expect(card).toBeTruthy();
    expect(card!.textContent).toContain("残作業の続き");
    expect(card!.querySelector(".mirror-handoff-prompt")?.textContent).toBe("未完了: 表示の実装。次はここから。");
  });

  it("編集・破棄・起動は出さない(共有先にその能力は無い)", async () => {
    const el = await render();
    expect(el.querySelector(".mirror-handoff-actions")).toBeNull();
    expect(el.querySelector(".mirror-handoff")!.querySelectorAll("button, textarea, input").length).toBe(0);
  });

  it("会話より後の時点に置く(末尾固定にすると以後の会話が隠れる)", async () => {
    const el = await render();
    const nodes = [...el.querySelectorAll(".mirror-turn, .mirror-handoff")];
    expect(nodes.at(-1)?.classList.contains("mirror-handoff")).toBe(true);
    expect(nodes.length).toBeGreaterThan(1);
  });
});
