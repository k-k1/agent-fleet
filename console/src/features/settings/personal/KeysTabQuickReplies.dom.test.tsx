// キー設定の「学習済みの候補」ブロック。学習は送信のたび黙って増えるので、掃除の導線が
// 「すべて消去」しかないと、一度きりの言い回しを片付けるたびに常用の候補まで巻き添えになる。
// ここで押さえるのは:
//   ① 1回だけの候補があるときにその件数付きのボタンが出る（無ければ出ない）
//   ② 押すと 1 回の候補だけが消え、常用とピンは残る
//   ③ 隠しリストは増やさない（＝また送れば学習し直す。「二度と出すな」ではない）
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

import { KeysTab } from "./KeysTab.tsx";
import { getSettings, setSetting } from "../../../lib/settings.ts";

let root: Root | null = null;
let host: HTMLDivElement | null = null;

async function mount() {
  host = document.createElement("div");
  document.body.append(host);
  root = createRoot(host);
  await act(async () => {
    root!.render(<KeysTab />);
  });
}

// 表示言語に依らないようクラスで引く（件数だけラベルから読む）。
const btn = (cls: string) => document.querySelector<HTMLButtonElement>(".qr-learned ." + cls);

beforeEach(() => {
  api.mockReset().mockResolvedValue({});
  apiJSON.mockReset().mockResolvedValue({});
  localStorage.clear();
  setSetting("quickRepliesHidden", []);
  setSetting("quickRepliesPinned", []);
  setSetting("quickReplies", {});
});

afterEach(() => {
  act(() => root?.unmount());
  host?.remove();
  root = null;
  host = null;
});

describe("KeysTab の学習済みクイック返信", () => {
  it("1回だけの候補を件数付きで一括削除し、常用・ピン・隠しには手を出さない", async () => {
    setSetting("quickReplies", {
      ok: { text: "OK", count: 7, at: 30 },
      "後で": { text: "後で", count: 1, at: 20 },
      "あとで見る": { text: "あとで見る", count: 1, at: 10 },
      "ピン留めの一言": { text: "ピン留めの一言", count: 1, at: 5 },
    });
    setSetting("quickRepliesPinned", ["ピン留めの一言"]);
    setSetting("quickRepliesHidden", ["やめて"]);
    await mount();

    const clear = btn("qr-clear-once")!;
    expect(clear).toBeTruthy();
    expect(clear.textContent).toContain("2"); // ピン留めの1回は数えない
    await act(async () => {
      clear.click();
    });

    expect(Object.keys(getSettings().quickReplies)).toEqual(["ok", "ピン留めの一言"]);
    expect(getSettings().quickRepliesPinned).toEqual(["ピン留めの一言"]);
    expect(getSettings().quickRepliesHidden).toEqual(["やめて"]); // 隠しは増やさない
    expect(btn("qr-clear-once")).toBeNull(); // 対象が無くなればボタンも消える
  });

  it("1回だけの候補が無ければボタンを出さない（「すべて消去」は出したまま）", async () => {
    setSetting("quickReplies", { ok: { text: "OK", count: 3, at: 30 } });
    await mount();
    expect(btn("qr-clear-once")).toBeNull();
    expect(btn("qr-clear-all")).toBeTruthy();
  });
});
