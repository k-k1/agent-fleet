// 設定の書き出し / 取り込みタブ（docs/log/79）。画面側で守るべきは 3 点だけを jsdom で押さえる:
//   ① ワークスペース停止中は「エージェントへの指示」を選ばせない（Agent が持つ層なので
//      読み書きできない。選べてしまうと「入ったつもり」になる）
//   ② 読み込んだファイルに入っているカテゴリだけがチェックリストに並ぶ
//   ③ 取り込みは **プロファイル → ホスト** の順で作り、既にある物は作り直さない
//      （逆順だと参照先が無く 400、作り直すと既存環境を書き換えてしまう）
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
const rawJSON = vi.fn();
vi.mock("../../../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: () => Promise.resolve({}),
  raw: () => Promise.resolve(new Response("")),
  rawJSON: (...args: unknown[]) => rawJSON(...args),
  errText: (e: { message?: string }) => e?.message || "",
  errDetail: (e: { message?: string }) => e?.message || "",
  isTransientErr: () => false,
  getTenant: () => "t1",
}));
let running = true;
vi.mock("../../../core/store/workspace.ts", () => ({
  useWorkspaceStore: (sel: (s: unknown) => unknown) => sel({ state: running ? "running" : "stopped" }),
  wsStartBusy: () => false,
}));
vi.mock("../../../ui/ToastProvider.tsx", () => ({ useToast: () => () => {} }));
vi.mock("../../../ui/ConfirmProvider.tsx", () => ({ useConfirm: () => () => Promise.resolve(true) }));

import { BackupTab } from "./BackupTab.tsx";
import { BUNDLE_KIND, BUNDLE_VERSION } from "../../../lib/settingsBundle.ts";

const bundle = {
  kind: BUNDLE_KIND,
  version: BUNDLE_VERSION,
  exportedAt: "2026-08-26T00:00:00.000Z",
  sections: {
    prefs: { termSize: 15 },
    ssm: {
      profiles: [
        { label: "prod", startUrl: "https://c.awsapps.com/start", ssoRegion: "ap-northeast-1", accountId: "", roleName: "", region: "" },
        { label: "kept", startUrl: "https://c.awsapps.com/start", ssoRegion: "ap-northeast-1", accountId: "", roleName: "", region: "" },
      ],
      hosts: [{ alias: "mng@web-01", profile: "prod", instanceId: "i-1", documentName: "", region: "" }],
    },
  },
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<BackupTab />);
  });
  await act(async () => {
    await Promise.resolve();
  });
}

async function pickFile(text: string) {
  const input = document.querySelector<HTMLInputElement>('input[type="file"]')!;
  const file = new File([text], "af-settings.json", { type: "application/json" });
  Object.defineProperty(input, "files", { value: [file], configurable: true });
  await act(async () => {
    input.dispatchEvent(new Event("change", { bubbles: true }));
  });
  await act(async () => {
    await Promise.resolve();
  });
}

const picks = () => Array.from(document.querySelectorAll<HTMLInputElement>(".backup-preview input[type=checkbox]"));

beforeEach(() => {
  running = true;
  api.mockReset();
  rawJSON.mockReset();
  // 既存: プロファイル "kept" だけ登録済み、ホストは無し。
  api.mockImplementation((path: string) => {
    if (path === "api/ssm/profiles") return Promise.resolve([{ id: "p-kept", label: "kept" }]);
    if (path === "api/ssm/hosts") return Promise.resolve([]);
    if (path === "api/user-notes") return Promise.resolve({ text: "hi", enabled: true, targets: [] });
    return Promise.resolve({});
  });
  rawJSON.mockImplementation((path: string) =>
    Promise.resolve(new Response(JSON.stringify({ id: "new-" + path }), { status: 201 })),
  );
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("BackupTab", () => {
  it("ワークスペース停止中は指示のカテゴリを選ばせない", async () => {
    running = false;
    await mount();
    const boxes = Array.from(document.querySelectorAll<HTMLInputElement>(".ds-group .backup-picks input[type=checkbox]"));
    expect(boxes).toHaveLength(3);
    expect(boxes[2].disabled).toBe(true); // エージェントへの指示
    expect(boxes[2].checked).toBe(false);
  });

  it("ファイルに入っているカテゴリだけを取り込みの選択肢に出す", async () => {
    await mount();
    await pickFile(JSON.stringify(bundle));
    // prefs と ssm はあるが instructions は入っていないので 2 行だけ。
    expect(picks()).toHaveLength(2);
    // 書き出し日時は表示用に整形して出す（生の ISO 文字列をそのまま見せない）。
    const head = document.querySelector(".backup-preview")?.textContent ?? "";
    expect(head).toContain("2026");
    expect(head).not.toContain("2026-08-26T00:00:00.000Z");
  });

  it("プロファイルを作ってからホストを作り、既にある物は作り直さない", async () => {
    await mount();
    await pickFile(JSON.stringify(bundle));
    const apply = Array.from(document.querySelectorAll<HTMLButtonElement>(".backup-preview button")).find(
      (b) => b.className.includes("primary"),
    )!;
    await act(async () => {
      apply.click();
    });
    await act(async () => {
      await Promise.resolve();
    });
    const calls = rawJSON.mock.calls.map((c) => [c[0], c[1], c[2]] as [string, string, any]);
    // "kept" は既存なので作らない。作るのは prod 1 件 → その後にホスト。
    expect(calls.map((c) => c[0])).toEqual(["api/ssm/profiles", "api/ssm/hosts"]);
    expect(calls[0][2].label).toBe("prod");
    // ホストは新しく採番された id を参照する（バンドルは表示名しか運ばない）。
    expect(calls[1][2].profileId).toBe("new-api/ssm/profiles");
    expect(document.querySelector(".backup-result")?.textContent).toBeTruthy();
  });

  it("設定の書き出しファイルでなければ取り込みへ進まない", async () => {
    await mount();
    await pickFile(JSON.stringify({ kind: "something-else" }));
    expect(document.querySelector(".backup-preview")).toBeNull();
  });
});
