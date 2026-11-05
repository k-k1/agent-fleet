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

  const tamper = (grid: HTMLElement) => {
    const code = grid.querySelector(".cl-code")!;
    code.textContent = "hello亜 world";
    return code;
  };

  it("IME 変換の確定後に DOM へ入った文字を元に戻し、フォーカスを保つ", async () => {
    const grid = await render();
    grid.focus();
    tamper(grid);
    await act(async () => {
      grid.dispatchEvent(new CompositionEvent("compositionend", { bubbles: true, data: "亜" }));
    });
    const fresh = host.querySelector(".codegrid") as HTMLElement;
    expect(fresh.querySelector(".cl-code")!.textContent).toBe("hello world");
    expect(document.activeElement).toBe(fresh);
  });

  it("変換中でない input で DOM が変わったら元に戻す", async () => {
    const grid = await render();
    tamper(grid);
    await act(async () => {
      grid.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertText", isComposing: false }));
    });
    expect(host.querySelector(".cl-code")!.textContent).toBe("hello world");
  });

  it("変換中の input では作り直さない（確定まで IME を壊さない）", async () => {
    const grid = await render();
    const code = tamper(grid);
    await act(async () => {
      grid.dispatchEvent(new InputEvent("input", { bubbles: true, inputType: "insertCompositionText", isComposing: true }));
    });
    expect(host.querySelector(".cl-code")).toBe(code);
  });

  it("アンマウント後はリスナーを外す", async () => {
    const grid = await render();
    await act(async () => root.unmount());
    expect(beforeInput(grid, "deleteContentBackward")).toBe(false);
    root = createRoot(document.createElement("div"));
  });
});
