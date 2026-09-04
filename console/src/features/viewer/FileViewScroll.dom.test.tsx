// タブを切り替えて戻ってきたときに、読んでいた位置へ返ること（scrollMemory）。
//
// タブ表示は 1 セルに選ばれた 1 枚しか描かない（PaneHost）ので、別のタブへ移ると
// FileView は **unmount** される。ここで再現しているのはまさにそれ ——「同じ props で
// 描き直す」ではなく、いったん外して付け直す。
//
// jsdom にレイアウトは無いので、スクロール容器の寸法だけ器で与える（本物の
// 高さの積み上がり ——Markdown の innerHTML → ハイライト → 画像 —— は器では
// 再現できない。あれは実ブラウザでしか見えない・src/test/domSetup.ts）。
import { afterAll, afterEach, beforeAll, beforeEach, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { clearScrollPos } from "./scrollMemory.ts";

const CODE = Array.from({ length: 400 }, (_, i) => `line ${i + 1}`).join("\n");

vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async (path: string) => {
    if (path.startsWith("api/fs/linemarks")) return { error: { message: "none" } };
    const filePath = decodeURIComponent(path.split("path=")[1] || "");
    return {
      path: filePath,
      size: CODE.length,
      binary: false,
      truncated: false,
      editable: false,
      editabilityReason: "read_only_root",
      content: CODE,
    };
  }),
  downloadURL: (p: string) => `/dl/${p}`,
  isTransientErr: () => false,
  errText: (e: unknown) => String(e),
  rel: (p: string) => p,
}));

const { FileView } = await import("./FileView.tsx");

const VIEW_H = 400;
const CONTENT_H = 6000;

let root: Root | null = null;
let host: HTMLDivElement;

// 器: スクロール容器（.codeview）にだけ「画面より高い中身」を持たせる。
const scrollable = (el: HTMLElement) => el.classList?.contains("codeview");
beforeAll(() => {
  Object.defineProperty(HTMLElement.prototype, "clientHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return scrollable(this) ? VIEW_H : 0;
    },
  });
  Object.defineProperty(HTMLElement.prototype, "scrollHeight", {
    configurable: true,
    get(this: HTMLElement) {
      return scrollable(this) ? CONTENT_H : 0;
    },
  });
});
afterAll(() => {
  delete (HTMLElement.prototype as unknown as Record<string, unknown>).clientHeight;
  delete (HTMLElement.prototype as unknown as Record<string, unknown>).scrollHeight;
});

beforeEach(() => {
  clearScrollPos();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
  clearScrollPos();
});

async function open(props: { paneId?: string; filePath?: string } = {}): Promise<HTMLElement> {
  await act(async () => {
    root!.render(<FileView filePath={props.filePath ?? "repos/x/main.go"} paneId={props.paneId ?? "pane-1"} />);
  });
  await act(async () => {
    await Promise.resolve();
  });
  return host.querySelector(".codeview") as HTMLElement;
}

/** 別のタブへ移る＝この面は外される。 */
async function leave(): Promise<void> {
  await act(async () => {
    root!.render(<div />);
  });
}

/** 読み手がそこまで送った、を作る（scroll は容器から上がる）。 */
function scrollTo(el: HTMLElement, top: number): void {
  el.scrollTop = top;
  act(() => {
    el.dispatchEvent(new Event("scroll"));
  });
}

it("タブを切り替えて戻ると、読んでいた位置に返る", async () => {
  const first = await open();
  expect(first).not.toBeNull();
  scrollTo(first, 1800);

  await leave();
  const again = await open();

  expect(again).not.toBe(first); // 本当に付け直している（＝素なら 0 に戻る経路）
  expect(again.scrollTop).toBe(1800);
});

it("先頭まで戻して離れた人は、先頭のまま返る", async () => {
  const first = await open();
  scrollTo(first, 1800);
  scrollTo(first, 0);

  await leave();
  expect((await open()).scrollTop).toBe(0);
});

it("位置はペインとファイルを跨いで漏れない", async () => {
  scrollTo(await open({ paneId: "pane-1", filePath: "repos/x/main.go" }), 1800);
  await leave();

  // 同じファイルを別のペインで
  expect((await open({ paneId: "pane-2", filePath: "repos/x/main.go" })).scrollTop).toBe(0);
  await leave();
  // 同じペインで別のファイルを
  expect((await open({ paneId: "pane-1", filePath: "repos/x/other.go" })).scrollTop).toBe(0);
  await leave();
  // 元の組み合わせは覚えたまま
  expect((await open({ paneId: "pane-1", filePath: "repos/x/main.go" })).scrollTop).toBe(1800);
});

it("中身が短くなっていたら、行ける一番下まででやめる", async () => {
  // 位置は px なので、戻ってきたときにファイルが縮んでいれば目的地は存在しない。
  const first = await open();
  scrollTo(first, 5000);
  await leave();

  const shorter = 900;
  const spy = vi.spyOn(HTMLElement.prototype, "scrollHeight", "get").mockImplementation(function (this: HTMLElement) {
    return scrollable(this) ? shorter : 0;
  });
  try {
    expect((await open()).scrollTop).toBe(shorter - VIEW_H);
  } finally {
    spy.mockRestore();
  }
});
