// はじめる ハブの「新しいフォルダで始める」ステージ。芯は 1 つ:
// 作ったフォルダを**そのまま起動へ渡す**こと —— ここで onPickRepo に繋がっていないと、
// 作業コピーだけができて利用者は左ペインから起動ボタンを探し直すことになる（クローンの
// 「このまま はじめる」を足した理由と同じ穴）。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { t } from "../../lib/i18n/index.ts";

const initRepo = vi.fn();
vi.mock("./clone.ts", () => ({
  cloneRepo: vi.fn(),
  svnCheckout: vi.fn(),
  initRepo: (...a: unknown[]) => initRepo(...a),
}));
vi.mock("../chat/api.ts", () => ({ assistantList: vi.fn(async () => ({ assistants: [] })) }));
vi.mock("../../core/api/client.ts", () => ({
  api: vi.fn(async () => []),
  apiJSON: vi.fn(async () => ({})),
  errText: (e: { message?: string }) => e?.message || "",
  errDetail: (e: { message?: string }) => e?.message || "",
  pasteImage: vi.fn(),
}));

const { StartModal } = await import("./StartModal.tsx");
const { useReposStore } = await import("./store.ts");
const { ToastProvider } = await import("../../ui/ToastProvider.tsx");

let root: Root | null = null;
let host: HTMLDivElement;
const onPickRepo = vi.fn();

const rows = () => [...document.querySelectorAll<HTMLButtonElement>(".start-row")];
const rowFor = (label: string) => rows().find((b) => b.textContent?.includes(label))!;
const nameInput = () => document.querySelector<HTMLInputElement>(".ui-modal-body input")!;
const footButton = (label: string) =>
  [...document.querySelectorAll<HTMLButtonElement>(".ui-modal-foot button")].find((b) => b.textContent?.includes(label))!;

async function click(el: Element): Promise<void> {
  await act(async () => {
    el.dispatchEvent(new MouseEvent("click", { bubbles: true, cancelable: true }));
  });
}

async function type(input: HTMLInputElement, value: string): Promise<void> {
  await act(async () => {
    Object.getOwnPropertyDescriptor(HTMLInputElement.prototype, "value")!.set!.call(input, value);
    input.dispatchEvent(new Event("input", { bubbles: true }));
  });
}

beforeEach(async () => {
  initRepo.mockReset();
  onPickRepo.mockReset();
  useReposStore.setState({ repos: [] });
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(
      <ToastProvider>
        <StartModal kinds={["claude"]} onClose={() => {}} onPickRepo={onPickRepo} />
      </ToastProvider>,
    );
  });
});

afterEach(() => {
  act(() => root?.unmount());
  root = null;
  host.remove();
});

describe("StartModal — 新しいフォルダで始める", () => {
  it("作ったフォルダをそのまま 作業を始める へ渡す", async () => {
    const created = { name: "new-project", branch: "main", unborn: true, path: "/home/dev/repos/new-project" };
    initRepo.mockImplementation(async () => {
      useReposStore.setState({ repos: [created] });
      return { ok: true, name: "new-project" };
    });

    await click(rowFor(t("start.newdir_title")));
    await type(nameInput(), "new-project");
    await click(footButton(t("start.create_and_continue")));

    expect(initRepo).toHaveBeenCalledWith("new-project", expect.any(Function));
    expect(onPickRepo).toHaveBeenCalledWith(created);
  });

  it("作成に失敗したらステージに留まり、起動へは進まない", async () => {
    initRepo.mockResolvedValue({ ok: false, name: "" });

    await click(rowFor(t("start.newdir_title")));
    await type(nameInput(), "new-project");
    await click(footButton(t("start.create_and_continue")));

    expect(initRepo).toHaveBeenCalled();
    expect(onPickRepo).not.toHaveBeenCalled();
    expect(nameInput()).toBeTruthy(); // まだ入力欄が見えている＝名前を直せる
  });

  it("既にある名前では作成ボタンが押せない", async () => {
    useReposStore.setState({ repos: [{ name: "docs" }] });
    await click(rowFor(t("start.newdir_title")));
    await type(nameInput(), "docs");
    expect(footButton(t("start.create_and_continue")).disabled).toBe(true);
    await click(footButton(t("start.create_and_continue")));
    expect(initRepo).not.toHaveBeenCalled();
  });
});
