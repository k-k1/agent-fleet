// Render tests for the 新規フォルダ kind (取り込み元なし) in リポジトリを追加.
// The folder name is the one thing this path can get wrong in a way the user pays
// for later: the server refuses a collision with 409 and an out-of-charset name with
// 400, so the dialog must refuse them here rather than fire a request that fails.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../lib/i18n/index.ts";

const { NewRepoModal } = await import("./NewRepoModal.tsx");

let root: Root | null = null;
let host: HTMLDivElement;

// Modal renders through a portal, so everything below queries the document.
const segButtons = () => [...document.querySelectorAll<HTMLButtonElement>(".ui-seg .seg-btn")];
const segFor = (label: string) => segButtons().find((b) => b.textContent?.includes(label));
const nameInput = () => document.querySelector<HTMLInputElement>(".ui-modal-body input")!;
const submitButton = () => document.querySelector<HTMLButtonElement>(".ui-modal-foot button[type=submit]")!;

async function render(props: Partial<Parameters<typeof NewRepoModal>[0]> = {}): Promise<void> {
  await act(async () => {
    root!.render(<NewRepoModal onClone={() => {}} repos={[{ name: "docs" }]} {...props} />);
  });
}

async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
}

async function type(input: HTMLInputElement, value: string): Promise<void> {
  await act(async () => {
    const setter = Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!;
    setter.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

beforeEach(() => {
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("NewRepoModal — 新規フォルダ", () => {
  it("種類の選択肢は onInit を渡したときだけ出る", async () => {
    await render();
    expect(segFor(t("rp.vcs_new"))).toBeUndefined();
    await render({ onInit: vi.fn() });
    expect(segFor(t("rp.vcs_new"))).toBeDefined();
  });

  it("名前を入れて作成すると、その名前で onInit が呼ばれる", async () => {
    const onInit = vi.fn();
    const onClose = vi.fn();
    await render({ onInit, onClose });
    await click(segFor(t("rp.vcs_new"))!);
    await type(nameInput(), "  new-project  ");
    await click(submitButton());
    expect(onInit).toHaveBeenCalledWith("new-project"); // 前後の空白は落とす
    expect(onClose).toHaveBeenCalled();
  });

  it("既存の作業コピーと同じ名前では作成できない（サーバーは 409 で断る）", async () => {
    const onInit = vi.fn();
    await render({ onInit });
    await click(segFor(t("rp.vcs_new"))!);
    await type(nameInput(), "docs");
    expect(submitButton().disabled).toBe(true);
    await click(submitButton());
    expect(onInit).not.toHaveBeenCalled();
  });

  it("フォルダ名にできない文字列も作成できない", async () => {
    const onInit = vi.fn();
    await render({ onInit });
    await click(segFor(t("rp.vcs_new"))!);
    for (const bad of ["../escape", "a/b", ".hidden", "   "]) {
      await type(nameInput(), bad);
      expect(submitButton().disabled).toBe(true);
    }
    expect(onInit).not.toHaveBeenCalled();
  });
});
