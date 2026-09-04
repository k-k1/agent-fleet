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
// api は経路ごとに返し分けたいので、差し替え可能な 1 本に集約する（既定は従来どおり空配列）。
const apiGet = vi.fn(async (_path: string): Promise<unknown> => []);
vi.mock("../../core/api/client.ts", () => ({
  api: (path: string) => apiGet(path),
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

async function mount(): Promise<void> {
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
}

function unmount(): void {
  act(() => root?.unmount());
  root = null;
  host.remove();
}

beforeEach(async () => {
  initRepo.mockReset();
  onPickRepo.mockReset();
  apiGet.mockReset();
  apiGet.mockImplementation(async () => []);
  useReposStore.setState({ repos: [] });
  await mount();
});

afterEach(() => {
  if (root) unmount();
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

// SSM ホストのカード副題。芯は **アカウント id の出どころ**:
// これは**ホストではなくプロファイルの属性**（control-plane の ssmProfileDTO が持ち、
// ssmHostDTO は出さない）。以前ここは `h.accountId` を読んでおり、ワイヤに存在しないので
// **常に undefined ＝この副題が一度も描かれていなかった**（optional なので tsc も鳴らない）。
// 🔴 **このテストは修正前のコードで赤くなる**——それがこの 2 本を足す理由。
// 下の 2 本は**読みの 2 箇所（カード副題とドロップダウン）に 1 本ずつ**当たっている。
describe("StartModal — SSM ホストのカード副題", () => {
  const profile = { id: "p1", label: "prod", accountId: "123456789012" };
  const host1 = {
    id: "h1",
    alias: "mng@g3prod-mon01",
    profileId: "p1",
    region: "",
    instanceId: "i-0abc123",
    documentName: "",
  };
  const ssmApi = (hosts: unknown[]) => async (path: string) => {
    if (path === "api/ssm/profiles") return [profile];
    if (path === "api/ssm/hosts") return hosts;
    return [];
  };

  // profiles はマウント時に取りに行くので、モックを差し替えてから mount し直す。
  async function remountWith(hosts: unknown[]): Promise<void> {
    unmount();
    apiGet.mockImplementation(ssmApi(hosts));
    await mount();
    await click(rowFor(t("start.ssm_title")));
    await act(async () => {}); // hosts の取得を流す
  }

  it("カード副題にプロファイルのアカウント id が出る", async () => {
    await remountWith([host1]);

    const sub = document.querySelector(".ssm-card-sub")!;
    expect(sub).toBeTruthy();
    // 期待は「ラベル · アカウント <id> · インスタンス id」。
    expect(sub.textContent).toContain(t("start.ssm_acct", { id: "123456789012" }));
    // 併せて、他の 2 要素を巻き添えで落としていないことも見る（副題は 3 つの連結）。
    expect(sub.textContent).toContain("prod");
    expect(sub.textContent).toContain("i-0abc123");
  });

  it("ホストが多くドロップダウンになる場合も、選択肢にアカウント id が出る", async () => {
    // SSM_CARD_ALL_MAX = 8。9 件にするとカードは上位だけになり、ドロップダウンが出る。
    const many = Array.from({ length: 9 }, (_, i) => ({ ...host1, id: `h${i}`, instanceId: `i-${i}` }));
    await remountWith(many);

    const opts = [...document.querySelectorAll("option")].map((o) => o.textContent || "");
    const withAcct = opts.filter((tx) => tx.includes(t("start.ssm_acct", { id: "123456789012" })));
    expect(withAcct.length).toBe(9);
  });
});
