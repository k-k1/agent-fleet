// 動的 kind のピッカーが「選べるモデルが 1 つも無い」ことを黙らないこと。
//
// 実害: カタログが空で返ると、ピッカーは 既定 だけを出して理由を何も言わない。実機で
// 「モデルが選べない」と報告された形がこれで、調査側も「Console の回帰」と誤診した
// （200 の中身が {"models":[]} でも同じ絵になる）。
//
// ⚠️ 注記は**原因を断定しない**。空の理由は「未ログイン」「認証はあるがプロバイダへ
// 到達できない」「プランが既定のみ（Copilot Free は Auto だけ＝空が正常）」「設定で全部
// 除外した」があり、Console はどれか分からない。**Copilot Free の画面に出ても嘘に
// ならないこと**が文言の判定基準。
//
// ⚠️ 取得中に出してはいけない。useModelOptions は解決するまで 既定 だけを返すので、
// settled を見ないと開いた直後に必ず一瞬出て消える。
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";

let respond: (body: unknown) => void = () => {};
const api = vi.fn(
  (_path: string) =>
    new Promise((resolve) => {
      respond = resolve;
    }),
);
vi.mock("../core/api/client.ts", async (orig) => {
  const real = (await orig()) as Record<string, unknown>;
  return { ...real, api: (path: string) => api(path) };
});

const { ModelPicker } = await import("./ModelPicker.tsx");
const { t } = await import("../lib/i18n/index.ts");

let root: Root | null = null;
let host: HTMLDivElement;

const hints = () => [...host.querySelectorAll(".ui-field-hint")].map((n) => n.textContent || "");
const optionValues = () => [...host.querySelectorAll("option")].map((o) => (o as HTMLOptionElement).value);

async function mount(kind: string) {
  await act(async () => {
    root!.render(<ModelPicker kind={kind} model="" onChange={() => {}} />);
  });
}

async function settle(body: unknown) {
  await act(async () => {
    respond(body);
    await Promise.resolve();
    await Promise.resolve();
  });
}

beforeEach(() => {
  api.mockClear();
  host = document.createElement("div");
  document.body.appendChild(host);
  root = createRoot(host);
});

afterEach(async () => {
  await act(async () => root?.unmount());
  host.remove();
  root = null;
});

describe("動的モデルピッカーの「既定のみ」注記", () => {
  it("取得中は出さない（既定だけの状態は読み込み中と同じ）", async () => {
    await mount("cursor");
    expect(optionValues()).toEqual([""]); // まだ 既定 だけ
    expect(hints().join()).not.toContain(t("ui.model_default_only"));
  });

  it("空カタログで決着したら出す", async () => {
    await mount("kiro");
    await settle({ models: [] });
    expect(optionValues()).toEqual([""]);
    expect(hints().join()).toContain(t("ui.model_default_only"));
  });

  it("モデルが来たら出さない", async () => {
    await mount("agy");
    await settle({ models: [{ id: "sonnet-x", label: "Sonnet X" }] });
    expect(optionValues()).toContain("sonnet-x");
    expect(hints().join()).not.toContain(t("ui.model_default_only"));
  });

  // 両カタログの**実物**を読む。t() は現在の表示言語しか返さないので、片方だけ
  // 断定形に書き換えられても気付けない。
  it("文言は ja / en とも原因を断定しない（Copilot Free の空にも出るため）", async () => {
    const ja = (await import("../lib/i18n/locales/ja/common.ts")).common;
    const en = (await import("../lib/i18n/locales/en/common.ts")).common;
    for (const cat of [ja, en] as Record<string, string>[]) {
      const s = cat["ui.model_default_only"];
      expect(s).toBeTruthy();
      // 断定形へ書き換えると、プランが既定のみのアカウント（空が正常）へ誤報になる。
      expect(s).not.toMatch(/ログインしていません|未ログイン|接続されていません|not signed in|sign in|not connected|failed/i);
    }
  });
});
