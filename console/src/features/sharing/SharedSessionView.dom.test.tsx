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
import { useHandoffStore, type HandoffOffer } from "./handoffStore.ts";
import { ToastProvider } from "../../ui/ToastProvider.tsx";

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

// メンバーから受け取った引き継ぎ（docs/log/77）。通知「引き継ぎが届きました」の行き先はこの面
// なので、受け取る口がここに無いと押した人はどこにも辿り着けない（実際、唯一の口はレール
// 見出しのアイコンだった＝「開始するボタンが見当たらない」）。
describe("SharedSessionView の受け取った引き継ぎ", () => {
  const OFFER: HandoffOffer = {
    id: "ho_1",
    sessionId: "catalog-1", // 共有カタログ id = この面の sharedSessionId
    sessionName: "sess-a",
    recipientUserKey: "b@example.com",
    ownerUserKey: "a@example.com",
    title: "残作業の続き",
    status: "pending",
    branch: "feature/x",
    repoRemote: "https://github.com/k-k1/agent-fleet.git",
    headSha: "0123456789ab",
    prompt: "未完了: 表示の実装。次はここから。",
  };
  const withOffer = (offer: HandoffOffer | null) =>
    api.mockImplementation(async (path: string) => {
      if (path.includes("session-handoff-offers/received")) return { offers: offer ? [offer] : [] };
      if (path.includes("session-handoff-offers")) return { offers: [] };
      if (path.includes("/handoff-proposals")) return { proposals: [] };
      if (path.includes("/messages")) return MESSAGES;
      return { sessions: [] };
    });

  // 在庫はこの面自身が取りに行く（レールが無い切り離しタブでも帯が出る）ので、ストアは
  // テストごとに戻す。
  afterEach(() => useHandoffStore.setState({ owned: [], received: [] }));

  async function renderWithToast() {
    host = document.createElement("div");
    document.body.append(host);
    root = createRoot(host);
    await act(async () => {
      // 受諾は起動導線に載るので、行は toast を要求する（受信箱と同じ文脈で描く）。
      root!.render(
        <ToastProvider>
          <SharedSessionView sharedSessionId="catalog-1" />
        </ToastProvider>,
      );
    });
    // 転写・提案・受信箱はそれぞれ別のポーリングで入ってくる（受信箱は 2 本を Promise.all）。
    await act(async () => {
      for (let i = 0; i < 4; i++) await Promise.resolve();
    });
    return host;
  }

  it("自分宛の未処理があると帯と受諾の口を出す", async () => {
    withOffer(OFFER);
    const el = await renderWithToast();
    const banner = el.querySelector(".shared-view-handoff");
    expect(banner).toBeTruthy();
    expect(banner!.textContent).toContain("a@example.com");
    // 帯のボタンから受信箱（受諾/辞退）に辿り着けること。Modal はポータルで body 直下。
    await act(async () => {
      banner!.querySelector("button")!.click();
    });
    const modal = document.body.querySelector(".ui-modal");
    expect(modal).toBeTruthy();
    expect(modal!.textContent).toContain("未完了: 表示の実装。次はここから。");
    // 受諾と辞退の 2 つ（文言はロケール依存なので構造で見る）。
    expect(modal!.querySelectorAll(".handoff-inbox-actions button").length).toBe(2);
  });

  it("別のセッション宛の引き継ぎでは出さない", async () => {
    withOffer({ ...OFFER, sessionId: "catalog-2" });
    const el = await renderWithToast();
    expect(el.querySelector(".shared-view-handoff")).toBeNull();
  });
});

// 保留中の AskUserQuestion。所有者の Agent はモーダルが開いているあいだ、その質問を
// 転写(messages)から外してカーソルも手前で止め、pendingQuestions として別枠で返す。
// ここを描かないと、共有先は「質問が出ているあいだだけ何も見えない」ことになる。
describe("SharedSessionView の保留中の質問", () => {
  const PENDING = {
    ...MESSAGES,
    // 保留中の質問は messages に入っていない、という実際の形。
    pendingText: "方式が2つあります",
    pendingQuestions: [
      {
        header: "方式",
        question: "どちらにしますか",
        options: [
          { label: "A 案", description: "既存を拡張する" },
          { label: "B 案", description: "作り直す", preview: "+--+\n|  |\n+--+" },
        ],
      },
    ],
  };
  const withPending = () =>
    api.mockImplementation(async (path: string) => {
      if (path.includes("/handoff-proposals")) return { proposals: [] };
      if (path.includes("/messages")) return PENDING;
      return { sessions: [] };
    });

  it("質問文と選択肢(preview 込み)を出す", async () => {
    withPending();
    const el = await render();
    const card = el.querySelector(".mt-question");
    expect(card).toBeTruthy();
    expect(card!.textContent).toContain("どちらにしますか");
    expect([...card!.querySelectorAll(".mq-opt")].map((o) => o.textContent)).toEqual([
      expect.stringContaining("A 案"),
      expect.stringContaining("B 案"),
    ]);
    expect(card!.querySelector(".mq-opt-preview")?.textContent).toContain("+--+");
    // 質問の直前に流れていた地の文も一緒に(カードだけだと何の話か分からない)。
    expect(el.textContent).toContain("方式が2つあります");
  });

  it("答える口は出さない(答えるのは所有者)", async () => {
    withPending();
    const el = await render();
    const card = el.querySelector(".mt-question")!;
    expect(card.querySelector(".mq-submit")).toBeNull();
    expect(card.querySelector(".mq-freetext")).toBeNull();
    expect([...card.querySelectorAll("button")].every((b) => (b as HTMLButtonElement).disabled)).toBe(true);
    // 決着していないので「回答済み」バッジも出ない。
    expect(card.querySelector(".mq-done")).toBeNull();
  });

  it("転写が空でも「履歴なし」で潰さない", async () => {
    api.mockImplementation(async (path: string) => {
      if (path.includes("/handoff-proposals")) return { proposals: [] };
      if (path.includes("/messages")) return { ...PENDING, messages: [] };
      return { sessions: [] };
    });
    const el = await render();
    expect(el.querySelector(".mt-question")).toBeTruthy();
    expect(el.querySelector(".mirror-empty")).toBeNull();
  });

  it("決着したら出しっぱなしにしない", async () => {
    withPending();
    vi.useFakeTimers();
    try {
      const el = await render();
      expect(el.querySelector(".mt-question")).toBeTruthy();
      // 次の poll に pendingQuestions が入っていない = もうモーダルは出ていない。前回の値を
      // 残すと、所有者が答えたあとも共有先にだけカードが居座る。
      api.mockImplementation(async (path: string) => {
        if (path.includes("/handoff-proposals")) return { proposals: [] };
        if (path.includes("/messages")) return MESSAGES;
        return { sessions: [] };
      });
      await act(async () => {
        await vi.advanceTimersByTimeAsync(3100); // POLL_IDLE
      });
      expect(el.querySelector(".mt-question")).toBeNull();
    } finally {
      vi.useRealTimers();
    }
  });
});
