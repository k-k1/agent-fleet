// タブ切替でペインが「別セッションの画面」を映す回帰（ターミナルを開くと別セッションの
// tmux にアタッチされたように見える）を押さえる。
//
// タブ表示では 1 セル = 1 つの TerminalView が使い回され、選択タブが変わっても
// コンテナ div は remount されない。xterm の open() は append なので、対策が無いと
// 1 つの div に前のタブの .xterm が残ったまま新しい .xterm が下へ積まれ、画面には
// 前のセッションが（tmux ステータス行ごと）映り続ける。ヘッダも PTY も打鍵も新しい
// セッションのものなので、見えている端末と繋がっている端末が食い違う。
//
// jsdom にレイアウトは無いので「どちらが見えているか」は測れない。代わりに原因側の
// 不変条件（1 コンテナに .xterm はちょうど 1 つ＝選択中ペインのもの）を検証する。
import { describe, it, expect, afterEach } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { ensureTerm, disposeTerm } from "./term.ts";
import { TerminalView } from "../features/terminal/TerminalView.tsx";

const termOf = (el: HTMLElement) => Array.from(el.querySelectorAll(".xterm"));

describe("terminal container ownership", () => {
  afterEach(() => {
    for (const id of ["p1", "p2"]) disposeTerm(id);
  });

  it("hands one container to exactly one pane", () => {
    const el = document.createElement("div");
    document.body.appendChild(el);

    const first = ensureTerm("p1", el);
    expect(termOf(el)).toEqual([first!.element]);

    // 同じ div を別ペインが取る（＝タブ切替）。前のタブの .xterm は残ってはならない。
    const second = ensureTerm("p2", el);
    expect(termOf(el)).toEqual([second!.element]);
    expect(second).not.toBe(first);

    // 戻ったら元のインスタンス（＝スクロールバックと PTY を保ったまま）が返る。
    expect(ensureTerm("p1", el)).toBe(first);
    expect(termOf(el)).toEqual([first!.element]);

    el.remove();
  });

  it("keeps a reused TerminalView container on the selected pane only", async () => {
    const host = document.createElement("div");
    document.body.appendChild(host);
    let root: Root | null = null;
    await act(async () => {
      root = createRoot(host);
      // session=null: PTY は張らない（ここで見たいのはコンテナの所有権だけ）。
      root.render(<TerminalView paneId="p1" session={null} />);
    });
    const container = host.querySelector(".term-body .terminal") as HTMLElement;
    expect(termOf(container)).toHaveLength(1);

    // タブ切替＝paneId だけが変わる再レンダリング。TerminalView は remount されない。
    await act(async () => {
      root!.render(<TerminalView paneId="p2" session={null} />);
    });
    expect(host.querySelector(".term-body .terminal")).toBe(container); // 同じ div のまま
    expect(termOf(container)).toHaveLength(1);

    await act(async () => root!.unmount());
    host.remove();
  });
});
