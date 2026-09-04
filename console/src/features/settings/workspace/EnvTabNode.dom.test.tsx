// ツールチェーンタブの Node.js 行。Java 行と同じことを押さえる——node には同じ穴が
// 開いたままだった（docs/decisions/0068）: nodeOptions は固定リストなので常に未導入の版を
// 提示しうるのに、選んでも導入は起きず、resolvedToolchains() は空を返し、セッションは
// 古い node のまま。エラーも警告も無く、Stop → Start するまで選択が効かなかった。
//   ① 未インストールの版を選んだときだけ導入ボタンを出す
//   ② ボタンは POST /env/node-install を叩き、完了後に一覧を取り直す
//   ③ 選択肢の見た目で「未インストール」が分かる（選んでから気づくのでは遅い）
//   ④ "system"（イメージの素の node）は導入対象ではない
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const apiJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: (...args: unknown[]) => apiJSON(...args),
  getTenant: () => "default",
  errText: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
  raw: () => Promise.resolve(new Response("")),
}));
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: unknown) => unknown) => sel({ state: "running", start: () => {} }),
  wsStartBusy: () => false,
}));
vi.mock("../../sessions/store.ts", () => ({
  useSessionsStore: (sel: (s: unknown) => unknown) => sel({ sessions: [] }),
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => async () => true }));
vi.mock("../hostUpdate.ts", () => ({ useHostUpdate: () => null }));

import { EnvTab } from "./EnvTab.tsx";

const toolchains = (installed: string[], node: string) => ({
  node,
  java: "",
  go: "system",
  timezone: "Asia/Tokyo",
  java_available: [],
  java_installed: [],
  node_options: ["system", "20", "22", "24"],
  node_installed: installed,
  go_options: ["system"],
  tz_options: ["Asia/Tokyo"],
});

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<EnvTab />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

beforeEach(() => {
  api.mockReset();
  apiJSON.mockReset();
  localStorage.clear();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

// ⚠️ 行ごとの識別クラスで引くこと。レイアウトの共有クラス（.env-tool-pick）で引くと
// Java 行と Node 行の両方に当たり、querySelector が先頭を返して別の行を検査する
// ——実際に node 行を足したとき、java のクラスを流用して Java のテストがそれで落ちた。
const nodeBtn = () => document.querySelector<HTMLButtonElement>(".env-node-pick button");

describe("EnvTab の Node.js 行", () => {
  it("導入済みの版を選んでいるならボタンを出さない", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(["22"], "22") : {}),
    );
    await mount();
    expect(nodeBtn()).toBeNull();
  });

  it("system（イメージの素の node）では導入ボタンを出さない", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains([], "system") : {}),
    );
    await mount();
    expect(nodeBtn()).toBeNull();
  });

  it("未インストールの版では導入ボタンを出し、選択肢からも未導入と分かる", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(["22"], "24") : {}),
    );
    await mount();
    expect(nodeBtn()).not.toBeNull();
    const opts = Array.from(document.querySelectorAll<HTMLOptionElement>(".env-node-pick option"));
    const absent = opts.find((o) => o.value === "24")!;
    const present = opts.find((o) => o.value === "22")!;
    expect(present.textContent).toBe("v22");
    expect(absent.textContent).not.toBe("v24");
  });

  it("押すと導入を開始し、完了後に取り直してボタンが消える", async () => {
    let installed = ["22"];
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(installed, "24") : {}),
    );
    apiJSON.mockImplementation(async () => {
      installed = ["22", "24"];
      return { state: "done", major: "24", node_installed: installed };
    });
    await mount();

    await act(async () => {
      nodeBtn()!.click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/env/node-install", "POST", { major: "24" });
    await act(async () => {
      await Promise.resolve();
    });
    expect(nodeBtn()).toBeNull();
  });

  it("進行中はボタンもセレクトも触らせない（二重ダウンロード防止）", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(["22"], "24") : { state: "installing" }),
    );
    apiJSON.mockResolvedValue({ state: "installing", major: "24" });
    await mount();

    await act(async () => {
      nodeBtn()!.click();
    });
    const btn = nodeBtn()!;
    expect(btn.disabled).toBe(true);
    expect(btn.textContent).toBe("インストール中…");
    expect(document.querySelector<HTMLSelectElement>(".env-node-pick select")!.disabled).toBe(true);
  });
});
