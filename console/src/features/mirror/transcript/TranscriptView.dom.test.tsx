// TranscriptCaps の中心的な約束 —「能力が無い = その操作要素を出さない」— を jsdom で
// 押さえる。共有セッションビュー(docs/59)は所有者向けの能力をほぼ渡さないので、ここが
// 崩れると受信者に「押せるのに何も起きないボタン」が並ぶ。それは見た目の粗ではなく、
// 相手のワークスペースを開こうとする導線を出してしまうということでもある。
//
// あわせて、能力が無いときの代替表示(その場で展開する diff / プラン)が出ることも見る。
// 押せないボタンを消すだけでは、受信者は変更内容にたどり着けなくなる。
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

// MarkdownView は remark/rehype を丸ごと引き込むので、ここでは本文が出ることだけ確かめる。
vi.mock("../../viewer/MarkdownView.tsx", () => ({
  MarkdownView: ({ source }: { source?: string }) => <div className="markdown">{source}</div>,
}));

import { TranscriptView } from "./TranscriptView.tsx";
import { groupTurns } from "./model.ts";
import type { TranscriptCaps } from "./capabilities.ts";
import type { Turn } from "./types.ts";
import { t as tr } from "../../../lib/i18n/index.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

function render(turns: Turn[], caps: TranscriptCaps) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<TranscriptView groups={groupTurns(turns)} caps={caps} />));
  return host;
}

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

const EDIT_TURN: Turn[] = [
  { role: "user", text: "直して", idx: 1, anchorId: "u1", ts: "2026-08-13T10:00:00Z" },
  {
    role: "assistant",
    idx: 2,
    ts: "2026-08-13T10:01:00Z",
    parts: [
      // file は Agent が編集系ツールに必ず載せる座標（docs/68）。ここに無いと
      // 「このターンが直したファイル」のチップが出ない。
      { kind: "tool", tool: "Edit", info: "app.ts", file: "src/app.ts", edits: [{ old: "const a = 1", new: "const a = 2" }] },
      { kind: "text", text: "直しました" },
    ],
  },
];

const OWNER: TranscriptCaps = {
  agentName: "Claude",
  session: "s1",
  openDiff: () => {},
  openPlan: () => {},
  openFile: () => {},
  forkAt: () => {},
  onReauth: () => {},
};
// 受信者はこれだけ。共有ビューが実際に渡すものと同じ。
const RECIPIENT: TranscriptCaps = { agentName: "Claude" };

describe("TranscriptCaps: 能力が無ければ操作要素を出さない", () => {
  it("所有者には diff ペインを開くボタンと分岐導線が出る", () => {
    const el = render(EDIT_TURN, OWNER);
    expect(el.querySelector(".mt-tool-diff")).not.toBeNull();
    expect(el.querySelector(".mt-fork")).not.toBeNull();
  });

  it("所有者にはターン末尾に「このターンが直したファイル」のチップが出る（docs/68 P1）", () => {
    const el = render(EDIT_TURN, OWNER);
    const chips = el.querySelectorAll(".mtf-chip");
    expect(chips).toHaveLength(1);
    expect(chips[0].textContent).toContain("app.ts");
  });

  it("受信者にはチップを出さない（共有 DTO はパスを落とすので開く座標が無い）", () => {
    expect(render(EDIT_TURN, RECIPIENT).querySelector(".mirror-turn-files")).toBeNull();
  });

  it("受信者には分岐導線が出ない（叩ける相手のセッションが無い）", () => {
    const el = render(EDIT_TURN, RECIPIENT);
    expect(el.querySelector(".mt-fork")).toBeNull();
  });

  it("受信者の編集トレースは死んだボタンではなく、その場で展開する diff になる", () => {
    const el = render(EDIT_TURN, RECIPIENT);
    expect(el.querySelector(".mt-tool-diff")).toBeNull(); // ペインを開くボタンは出さない
    const head = el.querySelector<HTMLButtonElement>(".mt-tool-outhead");
    expect(head).not.toBeNull();
    expect(el.querySelector(".mt-tool-diff-inline")).toBeNull(); // 既定は畳んだ状態
    act(() => head!.click());
    const inline = el.querySelector(".mt-tool-diff-inline");
    expect(inline).not.toBeNull();
    // diff ペインと同じ lineDiff / dv-* で描くので、追加行と削除行が並ぶ
    expect(inline!.querySelectorAll(".dv-row.dv-add").length).toBe(1);
    expect(inline!.querySelectorAll(".dv-row.dv-del").length).toBe(1);
    expect(inline!.textContent).toContain("const a = 2");
  });

  it("受信者のプランはペインではなくその場で全文展開できる", () => {
    const turns: Turn[] = [
      {
        role: "assistant",
        idx: 1,
        parts: [{ kind: "plan", plan: "# 移行計画\n\n最初に棚卸しする", answer: "approved" }],
      },
    ];
    const owner = render(turns, OWNER);
    expect(owner.querySelector(".mt-plan-open")).not.toBeNull();
    expect(owner.querySelector(".mt-plan-body")).toBeNull(); // 所有者はペインで開く
    act(() => root?.unmount());
    host?.remove();

    const el = render(turns, RECIPIENT);
    const toggle = el.querySelector<HTMLButtonElement>(".mt-plan-open");
    expect(toggle).not.toBeNull();
    act(() => toggle!.click());
    expect(el.querySelector(".mt-plan-body")?.textContent).toContain("最初に棚卸しする");
  });

  it("受信者には所有者のエージェントを再認証させる導線を出さない", () => {
    const turns: Turn[] = [
      { role: "assistant", idx: 1, parts: [{ kind: "error", info: "OAuthError", text: "Please run /login", cause: "auth" }] },
    ];
    expect(render(turns, OWNER).querySelector(".mef-action")).not.toBeNull();
    act(() => root?.unmount());
    host?.remove();
    const el = render(turns, RECIPIENT);
    expect(el.querySelector(".mirror-error")).not.toBeNull(); // 失敗そのものは見える
    expect(el.querySelector(".mef-action")).toBeNull(); // 直しに行く導線は出さない
  });

  it("添付ファイルは開く先が無ければパネルごと出さない（パスは DTO で落ちている）", () => {
    const turns: Turn[] = [
      { role: "assistant", idx: 1, parts: [{ kind: "userfile", files: ["out/report.md"], caption: "結果" }] },
    ];
    expect(render(turns, OWNER).querySelector(".mt-files")).not.toBeNull();
    act(() => root?.unmount());
    host?.remove();
    expect(render(turns, RECIPIENT).querySelector(".mt-files")).toBeNull();
  });
});

describe("共有ビューでもミラーと同じ形で畳まれる", () => {
  it("連続したツールは1行にまとまり、展開して中身を見られる", () => {
    const turns: Turn[] = [
      {
        role: "assistant",
        idx: 1,
        parts: [
          { kind: "tool", tool: "Read" },
          { kind: "tool", tool: "Read" },
          { kind: "tool", tool: "Bash" },
          { kind: "text", text: "終わりました" },
        ],
      },
    ];
    const el = render(turns, RECIPIENT);
    const runHead = el.querySelector<HTMLButtonElement>(".mt-toolrun-head");
    expect(runHead).not.toBeNull();
    // 畳んだ状態では個々のツール行は出ていない（旧実装はこれを延々と並べていた）
    expect(el.querySelectorAll(".mt-toolrun-body .mt-tool").length).toBe(0);
    expect(runHead!.textContent).toContain("Read×2");
    act(() => runHead!.click());
    expect(el.querySelectorAll(".mt-toolrun-body .mt-tool").length).toBe(3);
  });

  it("コンパクション要約は巨大なターンではなく畳んだブロックになる", () => {
    const turns: Turn[] = [
      { role: "user", text: "長い要約…", idx: 1, compact: true },
      { role: "assistant", text: "続けます", idx: 2 },
    ];
    const el = render(turns, RECIPIENT);
    expect(el.querySelector("details.mirror-compact")).not.toBeNull();
  });
});

describe("peer 着信の見え方（docs/58 §58.14）", () => {
  const peerTurn = (text: string): Turn[] => [{ role: "user", text, idx: 1, source: "peer" }];

  it("送信元と種別の2つのチップが出る", () => {
    const el = render(
      peerTurn("[agent-fleet:peer from=build-api intent=request reply=only-if-blocked] 直して"),
      RECIPIENT,
    );
    expect(el.querySelector(".mt-peer")?.textContent).toContain("build-api");
    const kind = el.querySelector(".mt-peer-kind");
    // 文言はロケール依存なので、訳が引けている（キーが素通しされていない）ことを見る。
    expect(kind?.textContent?.trim()).toBeTruthy();
    expect(kind?.textContent).not.toContain("mirror.peer_intent");
  });

  it("種別の無い旧い封筒でも送信元バッジは出る（チップだけ消える）", () => {
    const el = render(peerTurn("[agent-fleet:peer from=build-api] 直して"), RECIPIENT);
    expect(el.querySelector(".mt-peer")?.textContent).toContain("build-api");
    expect(el.querySelector(".mt-peer-kind")).toBeNull();
  });
});

// claude は AskUserQuestion / ExitPlanMode の tool_use を「訊いた時点」で転写に書く。
// 「転写に出ている＝決着済み」は成り立たないので、決着のバッジは tool_result が来て
// 初めて出す（これを決め打ちで出していたため、承認待ちのプランが「決定済み」を名乗った）。
describe("決着していない質問/プランは決定済みを名乗らない", () => {
  it("回答の無い質問カードに「回答済み」を出さない", () => {
    const turns: Turn[] = [
      {
        role: "assistant",
        idx: 1,
        parts: [
          {
            kind: "question",
            questions: [{ header: "方式", question: "どれにしますか？", options: [{ label: "案A" }, { label: "案B" }] }],
          },
        ],
      },
    ];
    const el = render(turns, RECIPIENT);
    expect(el.querySelector(".mt-question")).not.toBeNull(); // 問い自体は見える
    expect(el.querySelector(".mq-done")).toBeNull();
  });

  it("tool_result の無いプランカードに「決定済み」を出さない", () => {
    const turns: Turn[] = [{ role: "assistant", idx: 1, parts: [{ kind: "plan", plan: "# 移行計画\n\n棚卸しする" }] }];
    const el = render(turns, RECIPIENT);
    expect(el.querySelector(".mt-plan")).not.toBeNull();
    expect(el.querySelector(".mt-plan-badge")).toBeNull();
    expect(el.querySelector(".mt-plan.decided")).toBeNull();
  });

  it("却下の楽観マークが付いていれば tool_result より先に「却下」を出す", () => {
    const turns: Turn[] = [{ role: "assistant", idx: 1, parts: [{ kind: "plan", plan: "# 移行計画" }] }];
    const el = render(turns, { ...OWNER, isRejectedPlan: () => true });
    expect(el.querySelector(".mt-plan-badge")?.textContent).toBe(tr("mirror.rejected"));
  });
});
