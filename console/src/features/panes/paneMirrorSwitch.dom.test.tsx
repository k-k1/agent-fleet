// タブ付きグリッドで「ファイル → セッション（ミラー）」へタブを切り替えたとき、
// 生の TUI が一瞬だけ見えてからミラーに変わる回帰を押さえる。
//
// タブ表示では 1 セル = 1 つの Pane インスタンスが使い回され、タブを選び直しても
// pane プロパティが差し替わるだけで remount されない（PaneHost は key={cell.id}）。
// ミラーを出すか否かは Pane のローカル state なので、これを effect で追従させると
// 「古い state のまま 1 フレーム commit → ブラウザが描画 → effect が直す」となり、
// その 1 フレームに素の TerminalView が見えてしまう。
//
// jsdom にレイアウトは無いので「見えたか」は測れない。代わりに commit の履歴を
// MutationObserver で見る: 端末を包む .view が **hidden 無しで挿入されてから後で
// hidden を付けられた** 記録が残っていれば、それが素通しの 1 フレームそのもの。
import { describe, it, expect, afterEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { Pane } from "./Pane.tsx";
import { useSessionsStore } from "../sessions/store.ts";
import type { Cell, PaneView } from "../../layout/types.ts";
import type { Session } from "../../types/session.ts";

// 端末・ミラー・ファイルの中身はこのテストの関心事ではない（実物は PTY / SSE /
// fetch を張る）。どれが mount されたかだけ分かれば足りる。
vi.mock("../terminal/TerminalView.tsx", () => ({
  TerminalView: () => <div className="term-stub" />,
}));
vi.mock("../mirror/MirrorView.tsx", () => ({
  MirrorView: () => <div className="mirror-stub" />,
}));
vi.mock("../viewer/FileView.tsx", () => ({
  FileView: () => <div className="file-stub" />,
}));
// セッション操作メニューは Confirm/Toast プロバイダを要求する（タブの右クリック用で、
// ここでは開かない）。
vi.mock("../sessions/useSessionActions.tsx", () => ({
  useSessionActions: () => ({}),
}));

const SESSION: Session = { name: "s1", kind: "claude", driver: "tui", alive: true, title: "作業" };

const fileView: PaneView = { id: "v-file", session: null, content: { kind: "file", filePath: "/w/a.ts" }, wrap: null };
const termView: PaneView = { id: "v-term", session: "s1", content: { kind: "terminal", chat: true }, wrap: null };
const cellWith = (selectedViewId: string): Cell => ({ id: "c1", selectedViewId, views: [fileView, termView] });

const noop = () => {};

describe("tabbed pane: file tab → mirrored session tab", () => {
  let root: Root | null = null;
  let host: HTMLElement | null = null;

  afterEach(async () => {
    if (root) await act(async () => root!.unmount());
    host?.remove();
    root = null;
    host = null;
  });

  it("never commits a visible terminal on the way to the mirror", async () => {
    useSessionsStore.setState({ sessions: [SESSION] });
    host = document.createElement("div");
    document.body.appendChild(host);

    await act(async () => {
      root = createRoot(host!);
      root.render(
        <Pane
          cell={cellWith("v-file")}
          pane={fileView}
          tabbed
          onActivate={noop}
          onClose={noop}
          onSwap={noop}
          onDropSplit={noop}
        />,
      );
    });
    expect(host.querySelector(".file-stub")).not.toBeNull();

    // ここからがタブ切替。以後 DOM に起きたことを全部記録する。
    const seen: MutationRecord[] = [];
    const obs = new MutationObserver((rs) => seen.push(...rs));
    obs.observe(host, { childList: true, subtree: true, attributes: true, attributeOldValue: true });

    await act(async () => {
      root!.render(
        <Pane
          cell={cellWith("v-term")}
          pane={termView}
          sessionMeta={SESSION}
          tabbed
          onActivate={noop}
          onClose={noop}
          onSwap={noop}
          onDropSplit={noop}
        />,
      );
    });
    seen.push(...obs.takeRecords());
    const records = seen;
    obs.disconnect();

    // 落ち着いた先はミラー。端末はマウントされたまま（PTY とスクロールバックの保持）
    // 隠れている。
    const view = host.querySelector(".view") as HTMLElement | null;
    expect(host.querySelector(".mirror-stub")).not.toBeNull();
    expect(view).not.toBeNull();
    expect(view!.querySelector(".term-stub")).not.toBeNull();
    expect(view!.hasAttribute("hidden")).toBe(true);

    // 途中経過: .view の hidden は「挿入時から付いていた」のでなければならない。
    // 後から付けられた記録＝素の端末が見えていた commit がある、ということ。
    const lateHide = records.filter(
      (r) =>
        r.type === "attributes" &&
        r.attributeName === "hidden" &&
        (r.target as HTMLElement).classList?.contains("view"),
    );
    expect(lateHide).toEqual([]);
  });
});
