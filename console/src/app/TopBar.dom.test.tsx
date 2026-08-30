// アカウントメニュー最下部の「バージョン帯」（docs/log/35 §35.6.1 / version_info.go）。
//
// 押さえるのは 3 つ:
//   ① イメージで配られるデプロイ（ECS）では、版だけでなく **どのイメージか** が出る。
//      CP と WS は同じ ImageTag から作られる約束だが片方だけ巻き戻せるので別行にし、
//      `:dev` は MUTABLE なので digest を添える — その並びがそのまま出ること。
//   ② それ以外のデプロイでは CP がキーごと落とすので、行が **消える**こと。空の
//      「イメージ」行を出すのは、何も指していない情報を出す嘘に近い。
//   ③ メニューを開くまで取りに行かないこと。全タブ・全ユーザーが払う起動時 fetch を
//      増やさないための遅延取得なので、閉じている間の呼び出しは 0 でなければならない。
import { describe, it, expect, afterEach, beforeEach, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

const api = vi.fn();
vi.mock("../core/api/client.ts", () => ({
  api: (...args: unknown[]) => api(...args),
  apiJSON: () => Promise.resolve({}),
  rawJSON: () => Promise.resolve(new Response("")),
  errText: (e: { message?: string }) => e?.message || "",
  rel: (p: string) => p,
  clearLocalState: () => {},
  getTenant: () => "",
  setTenant: () => {},
  getUser: () => "",
  setUser: () => {},
  isTransientErr: () => false,
}));
// 通知センターは別系統（自前で購読する）。ここでは版の行だけを見たいので器に落とす。
vi.mock("../features/notifications/NotificationCenter.tsx", () => ({
  NotificationCenter: () => null,
}));
// native の自己更新行は ECS には無い（CP がルートを登録しない = null）。
vi.mock("../features/settings/hostUpdate.ts", () => ({ useHostUpdate: () => null }));

import { TopBar } from "./TopBar.tsx";
import { resetDeploymentVersionCache } from "../features/settings/deploymentVersion.ts";

const ECS_PAYLOAD = {
  version: "9.9.9",
  runtime: "ecs-ec2",
  image: { repo: "af-control-plane", tag: "9.9.9", digest: "sha256:cafe123456789" },
  workspace_image: { repo: "af-workspace", tag: "9.9.9" },
};

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<TopBar toggleNav={() => {}} toggleLeft={() => {}} toggleLeftMode={() => {}} />);
  });
  await settle();
}

async function settle() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function openMenu() {
  const btn = host!.querySelector<HTMLButtonElement>(".acct-btn")!;
  await act(async () => {
    btn.click();
  });
  await settle();
}

const text = () => host?.textContent || "";
const versionRows = () => Array.from(host!.querySelectorAll(".acct-build")).map((n) => n.textContent || "");

beforeEach(() => {
  api.mockReset();
  resetDeploymentVersionCache();
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("TopBar version zone", () => {
  it("メニューを開くまで /api/version を取りに行かない", async () => {
    api.mockResolvedValue(ECS_PAYLOAD);
    await mount();
    expect(api).not.toHaveBeenCalled();

    await openMenu();
    expect(api).toHaveBeenCalledWith("api/version");
  });

  it("ECS では版・CP イメージ・WS イメージ・ビルドが並ぶ", async () => {
    api.mockResolvedValue(ECS_PAYLOAD);
    await mount();
    await openMenu();

    const rows = versionRows();
    expect(rows.some((r) => r.includes("9.9.9"))).toBe(true);
    // タグだけでは実体を特定できないので digest の頭が添う。
    expect(rows.some((r) => r.includes("af-control-plane:9.9.9") && r.includes("cafe123"))).toBe(true);
    expect(rows.some((r) => r.includes("af-workspace:9.9.9"))).toBe(true);
    // digest は 7 桁に切って出す（全長はツールチップ側）。
    expect(text()).not.toContain("cafe123456789");
    expect(host!.querySelector(".acct-build[title='sha256:cafe123456789']")).not.toBeNull();
  });

  it("イメージの無いデプロイではイメージ行が出ない", async () => {
    api.mockResolvedValue({ version: "9.9.9", runtime: "local" });
    await mount();
    await openMenu();

    expect(text()).not.toContain("af-workspace");
    expect(text()).not.toContain("af-control-plane");
    // ビルド刻印（FE バンドル）は元から常時出る行なので残る。
    expect(versionRows().length).toBeGreaterThan(0);
  });

  it("コピーは版とイメージとビルドを 1 ブロックで書き出す", async () => {
    api.mockResolvedValue(ECS_PAYLOAD);
    const writeText = vi.fn().mockResolvedValue(undefined);
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    await mount();
    await openMenu();

    const copy = host!.querySelector<HTMLButtonElement>(".acct-ver-copy")!;
    await act(async () => {
      copy.click();
    });
    await settle();

    const written = writeText.mock.calls[0][0] as string;
    expect(written).toContain("Agent Fleet 9.9.9 (ecs-ec2)");
    expect(written).toContain("control-plane: af-control-plane:9.9.9 (cafe123)");
    expect(written).toContain("workspace: af-workspace:9.9.9");
    expect(written).toContain("console: ");
  });
});
