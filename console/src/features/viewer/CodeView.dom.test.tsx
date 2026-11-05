// The read-only source grid is contentEditable for caret browsing, so every native edit has
// to be cancelled. jsdom never performs the edit itself; what is asserted is that a native
// beforeinput — the event a real browser cancels — is default-prevented for each input type,
// deletions included, which React's synthetic onBeforeInput never sees. IME composition cannot
// be cancelled, so a DOM edit is simulated and the grid is expected to put the text back.
import { afterEach, describe, expect, it } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { CodeView } from "./CodeView.tsx";

let host: HTMLDivElement;
let root: Root;

const render = async () => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root.render(<CodeView html={"hello world\nsecond line"} lines={2} lineNumbers />);
  });
  return host.querySelector(".codegrid") as HTMLElement;
};

const beforeInput = (el: HTMLElement, inputType: string) => {
  const ev = new InputEvent("beforeinput", { inputType, bubbles: true, cancelable: true });
  el.querySelector(".cl-code")!.dispatchEvent(ev);
  return ev.defaultPrevented;
};

afterEach(async () => {
  await act(async () => root.unmount());
  document.body.innerHTML = "";
});

describe("CodeView の読み取り専用ガード", () => {
  it.each([
    "deleteContentBackward",
    "deleteContentForward",
    "deleteWordBackward",
    "insertText",
    "insertParagraph",
    "insertFromPaste",
    "historyUndo",
  ])("ネイティブの beforeinput (%s) を取り消す", async (inputType) => {
    const grid = await render();
    expect(beforeInput(grid, inputType)).toBe(true);
  });

  const html = Array.from({ length: 30 }, (_, i) => `line ${i + 1} text`).join("\n");
  const renderLong = async () => {
    host = document.createElement("div");
    document.body.appendChild(host);
    root = createRoot(host);
    await act(async () => {
      root.render(<CodeView html={html} lines={30} lineNumbers />);
    });
    return host.querySelector(".codegrid") as HTMLElement;
  };
  const cell = (ln: number) => host.querySelector(`.cl-code[data-ln="${ln}"]`)!;

  // What a browser does on an IME commit: the composition's own (uncancellable) beforeinput,
  // then the text lands at the caret, then compositionend.
  const compose = async (grid: HTMLElement, ln: number, data: string) => {
    const ev = new InputEvent("beforeinput", { inputType: "insertCompositionText", data, bubbles: true, cancelable: false });
    cell(ln).dispatchEvent(ev);
    const text = cell(ln).firstChild as Text;
    text.insertData(text.length, data);
    return () =>
      act(async () => {
        grid.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data }));
      });
  };

  it("IME の確定後に行を元に戻し、途中行のキャレット・選択を編集前の位置へ戻す", async () => {
    const grid = await renderLong();
    grid.focus();
    const t = cell(17).firstChild as Text;
    document.getSelection()!.setBaseAndExtent(t, 7, t, 2); // backwards selection on line 17
    const end = await compose(grid, 17, "亜");
    await end();
    expect(host.querySelector(".codegrid")).toBe(grid);
    expect(cell(17).textContent).toBe("line 17 text");
    expect(document.activeElement).toBe(grid);
    const sel = document.getSelection()!;
    expect(sel.anchorNode).toBe(cell(17).firstChild);
    expect([sel.anchorOffset, sel.focusOffset]).toEqual([7, 2]);
  });

  it("変換中にフォーカスが他へ移っていたら、確定後もフォーカスと選択を奪わない", async () => {
    const grid = await renderLong();
    const other = document.createElement("input");
    document.body.appendChild(other);
    grid.focus();
    const t = cell(3).firstChild as Text;
    document.getSelection()!.collapse(t, 4);
    const end = await compose(grid, 3, "あ");
    other.focus();
    document.getSelection()!.collapse(document.body, 0);
    await end();
    expect(cell(3).textContent).toBe("line 3 text");
    expect(document.activeElement).toBe(other);
    expect(grid.contains(document.getSelection()!.anchorNode)).toBe(false);
  });

  it("行の構造まで壊れたら作り直し、フォーカスは元々あった場合だけ戻す", async () => {
    const grid = await renderLong();
    grid.focus();
    grid.appendChild(document.createTextNode("stray"));
    await act(async () => {
      grid.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertText", isComposing: false }));
    });
    const fresh = host.querySelector(".codegrid") as HTMLElement;
    expect(fresh).not.toBe(grid);
    expect(fresh.textContent).not.toContain("stray");
    expect(document.activeElement).toBe(fresh);

    const other = document.createElement("input");
    document.body.appendChild(other);
    other.focus();
    fresh.appendChild(document.createTextNode("stray"));
    await act(async () => {
      fresh.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertText", isComposing: false }));
    });
    expect(document.activeElement).toBe(other);
  });

  it("変換中の input では戻さない（確定まで IME を壊さない）", async () => {
    const grid = await renderLong();
    await compose(grid, 5, "あ");
    await act(async () => {
      grid.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertCompositionText", isComposing: true }));
    });
    expect(cell(5).textContent).toBe("line 5 textあ");
  });

  it("アンマウント後はリスナーを外す", async () => {
    const grid = await render();
    await act(async () => root.unmount());
    expect(beforeInput(grid, "deleteContentBackward")).toBe(false);
    root = createRoot(document.createElement("div"));
  });
});
