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

type LiveProps = { working?: boolean; autoCollapseWork?: boolean };

function render(turns: Turn[], caps: TranscriptCaps, props: LiveProps = {}) {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  act(() => root!.render(<TranscriptView groups={groupTurns(turns)} caps={caps} {...props} />));
  return host;
}

// 同じ root へ描き直す（React が再マウントせず更新する経路）＝ポーリングで status が
// 動くたびに実際に起きること。新しい root で描き直すと状態が消えて何も検出できない。
function rerender(turns: Turn[], caps: TranscriptCaps, props: LiveProps) {
  act(() => root!.render(<TranscriptView groups={groupTurns(turns)} caps={caps} {...props} />));
  return host!;
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

// ミラーの working は生きたポーリング値なので、1 ターンの最中でも往復する（claude の Stop
// フックで一瞬 idle を拾う／operator・定時実行・peer の新しいプロンプトが転写へ届く前に
// working が立つ）。そのたびに作業過程が展開⇄畳みで入れ替わると、読んでいる最中に本文の
// 高さが跳ねて位置がズレる。畳み込みは片道・開閉は読者のもの、を固定する。
describe("作業過程の畳み込みは往復しない", () => {
  const WORK_TURN: Turn[] = [
    { role: "user", text: "調べて", idx: 1, ts: "2026-08-22T10:00:00Z" },
    {
      role: "assistant",
      idx: 2,
      ts: "2026-08-22T10:01:00Z",
      parts: [
        { kind: "tool", tool: "Read" },
        { kind: "tool", tool: "Bash" },
        { kind: "text", text: "調べ終わりました。原因は設定ミスです。" },
      ],
    },
  ];
  const workState = (el: HTMLElement) => {
    const head = el.querySelector<HTMLButtonElement>(".mt-work-head");
    return head ? (head.getAttribute("aria-expanded") === "true" ? "open" : "closed") : "unfolded";
  };

  it("完了で畳んだあと status が working へ戻っても開き直さない", () => {
    // 末尾を追いながら実行中を眺めている：作業過程は畳まずそのまま出ている。
    const el = render(WORK_TURN, OWNER, { working: true, autoCollapseWork: true });
    expect(workState(el)).toBe("unfolded");
    // 完了 → 畳む（末尾追従中なので閉じた要約になる）。
    expect(workState(rerender(WORK_TURN, OWNER, { working: false, autoCollapseWork: true }))).toBe("closed");
    // ここで status がまた working を名乗っても、畳んだものは畳んだまま。
    expect(workState(rerender(WORK_TURN, OWNER, { working: true, autoCollapseWork: true }))).toBe("closed");
    expect(workState(rerender(WORK_TURN, OWNER, { working: false, autoCollapseWork: true }))).toBe("closed");
  });

  it("読者が開いた作業過程は、その後の status/追従の変化で閉じない", () => {
    const el = render(WORK_TURN, OWNER, { working: false, autoCollapseWork: true });
    expect(workState(el)).toBe("closed");
    act(() => el.querySelector<HTMLButtonElement>(".mt-work-head")!.click());
    expect(workState(el)).toBe("open");
    // 追従が切れた／戻った、実行中に見えた — どれも読者の選択を上書きしない。
    expect(workState(rerender(WORK_TURN, OWNER, { working: true, autoCollapseWork: false }))).toBe("open");
    expect(workState(rerender(WORK_TURN, OWNER, { working: false, autoCollapseWork: true }))).toBe("open");
  });

  it("セッションを持ち替えたら畳み込みは持ち越さない（ミラーは再マウントされない）", () => {
    const el = render(WORK_TURN, OWNER, { working: false, autoCollapseWork: true });
    expect(workState(el)).toBe("closed");
    // 同じ idx を持つ別セッションのターン。React が使い回すと、実行中の作業過程が
    // 前のセッションの「畳んだ」状態を引き継いで最初から隠れてしまう。
    const other = rerender(WORK_TURN, { ...OWNER, session: "s2" }, { working: true, autoCollapseWork: true });
    expect(workState(other)).toBe("unfolded");
  });

  it("上へスクロールして読んでいる最中に完了したターンは開いたまま畳まれる", () => {
    // autoCollapseWork=false ＝末尾から離れて読んでいる。要約行は出すが中身は閉じない。
    const el = render(WORK_TURN, OWNER, { working: true, autoCollapseWork: false });
    expect(workState(el)).toBe("unfolded");
    expect(workState(rerender(WORK_TURN, OWNER, { working: false, autoCollapseWork: false }))).toBe("open");
    // 途中で末尾へ戻っても、いま読んでいるものを閉じにはいかない。
    expect(workState(rerender(WORK_TURN, OWNER, { working: false, autoCollapseWork: true }))).toBe("open");
  });
});

// 開いた作業過程／思考は画面数枚ぶんの高さになるので、畳む操作が見出しにしか無いと
// 「読み終えた位置から見出しまで戻る」までたたむ手段が無い。本文の最下部にも同じトグルを置く。
describe("開いた作業過程・思考は最下部からも閉じられる", () => {
  const WORK_TURN: Turn[] = [
    { role: "user", text: "調べて", idx: 1 },
    {
      role: "assistant",
      idx: 2,
      parts: [
        { kind: "tool", tool: "Read" },
        { kind: "text", text: "調べ終わりました。" },
      ],
    },
  ];
  const THINK_TURN: Turn[] = [
    { role: "user", text: "考えて", idx: 1 },
    {
      role: "assistant",
      idx: 2,
      parts: [
        { kind: "thinking", text: "まず前提を確かめる。" },
        { kind: "text", text: "こうです。" },
      ],
    },
  ];

  it("作業過程：最下部の閉じるで畳む（見出しのトグルと同じ状態になる）", () => {
    const el = render(WORK_TURN, OWNER, { working: false, autoCollapseWork: true });
    const head = el.querySelector<HTMLButtonElement>(".mt-work-head")!;
    act(() => head.click());
    expect(head.getAttribute("aria-expanded")).toBe("true");
    const foot = el.querySelector<HTMLButtonElement>(".mt-work-body .mirror-disclosure-foot")!;
    expect(foot.textContent).toContain(tr("mirror.collapse_section"));
    act(() => foot.click());
    expect(head.getAttribute("aria-expanded")).toBe("false");
  });

  it("思考：最下部の閉じるで畳む", () => {
    const el = render(THINK_TURN, OWNER);
    const head = el.querySelector<HTMLButtonElement>(".mirror-thinking-head")!;
    act(() => head.click());
    expect(head.getAttribute("aria-expanded")).toBe("true");
    act(() => el.querySelector<HTMLButtonElement>(".mirror-thinking-body .mirror-disclosure-foot")!.click());
    expect(head.getAttribute("aria-expanded")).toBe("false");
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

  // 由来タグが落ちていても封筒があればバッジは出す。タグは別ストア由来で、記録より前に
  // 取ってきたターン（ミラーは持っているターンを取り直さない）や、上限で押し出された
  // 古い記録では消える。ここで諦めると、着信の唯一の可視化が黙って無くなる。
  it("source が落ちていても封筒があればバッジは出る", () => {
    const el = render(
      [{ role: "user", text: "[agent-fleet:peer from=build-api intent=notice reply=none] 出た", idx: 1 }],
      RECIPIENT,
    );
    expect(el.querySelector(".mt-peer")?.textContent).toContain("build-api");
    expect(el.querySelector(".mt-peer-kind")?.textContent?.trim()).toBeTruthy();
  });

  it("封筒も由来タグも無い自分の入力にはバッジを出さない", () => {
    const el = render([{ role: "user", text: "自分で打った指示", idx: 1 }], RECIPIENT);
    expect(el.querySelector(".mt-peer")).toBeNull();
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
