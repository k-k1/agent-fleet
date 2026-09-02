// ツールチェーンタブの Java 行。押さえるのは「選んだのに何も起きない」を作らないこと:
//   ① 未インストールの major を選んだときだけ導入ボタンを出す（導入済みなら出さない）
//   ② ボタンは POST /env/jdk-install を叩き、完了までポーリングしてから一覧を取り直す
//   ③ 選択肢の見た目で「未インストール」が分かる（選んでから気づくのでは遅い）
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

const toolchains = (installed: string[], java: string) => ({
  node: "system",
  java,
  go: "system",
  timezone: "Asia/Tokyo",
  java_available: ["8", "17", "21"],
  java_installed: installed,
  node_options: ["system", "22"],
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

const javaBtn = () => document.querySelector<HTMLButtonElement>(".env-java-pick button");

describe("EnvTab の Java 行", () => {
  it("導入済みの major を選んでいるならボタンを出さない", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(["21"], "21") : {}),
    );
    await mount();
    expect(javaBtn()).toBeNull();
  });

  it("未インストールの major では導入ボタンと注記を出す", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(["21"], "17") : {}),
    );
    await mount();
    expect(javaBtn()).not.toBeNull();
    // 選択肢自体にも「未インストール」が読み取れること。
    const opts = Array.from(document.querySelectorAll<HTMLOptionElement>(".env-java-pick option"));
    const absent = opts.find((o) => o.value === "17")!;
    const present = opts.find((o) => o.value === "21")!;
    expect(absent.textContent).not.toBe(present.textContent);
    expect(present.textContent).toBe("Temurin 21");
  });

  it("押すと導入を開始し、完了後にツールチェーンを取り直してボタンが消える", async () => {
    let installed = ["21"];
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(installed, "17") : {}),
    );
    // 既に入っていた等で即 done が返る経路。ポーリング自体は kiro と同じ usePolling。
    apiJSON.mockImplementation(async () => {
      installed = ["17", "21"];
      return { state: "done", major: "17", java_installed: installed };
    });
    await mount();

    await act(async () => {
      javaBtn()!.click();
    });
    expect(apiJSON).toHaveBeenCalledWith("api/env/jdk-install", "POST", { major: "17" });
    await act(async () => {
      await Promise.resolve();
    });
    // 取り直した結果 17 が導入済みになり、ボタンは消える。
    expect(javaBtn()).toBeNull();
  });

  it("進行中はボタンを押せない（二重ダウンロードを人の手で起こさせない）", async () => {
    api.mockImplementation((path: string) =>
      Promise.resolve(path === "api/env/toolchains" ? toolchains(["21"], "17") : { state: "installing" }),
    );
    apiJSON.mockResolvedValue({ state: "installing", major: "17" });
    await mount();

    await act(async () => {
      javaBtn()!.click();
    });
    const btn = javaBtn()!;
    expect(btn.disabled).toBe(true);
    expect(btn.textContent).toBe("インストール中…");
    // セレクトも触らせない（別の major へ切り替えると進行中の対象が読めなくなる）。
    expect(document.querySelector<HTMLSelectElement>(".env-java-pick select")!.disabled).toBe(true);
  });
});
