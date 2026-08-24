// 設定のサーバ同期（ui-prefs）で「消える」経路を塞いだことの回帰テスト。
//
// 実際に起きた事故: ワークスペース再起動直後（GET が 502）に、localStorage が空の面
// （ログアウト直後・別ブラウザ・別オリジンの dev Console）から返信を 1 通送っただけで、
// 学習チップの保存が **既定値一式の PUT** になり、サーバの学習済み候補・ピン・利用実績が
// まとめて消えた。サーバ優先の hydrate がそれを全端末へ伝播させ、翌日には全員が初期状態。
//
// 固定する約束は 3 つ:
//   1. サーバの現在値を一度も読めていない間は保存しない（読めた時点でまとめて送る）
//   2. 空のサーバ値で、非空のローカルの累積データを潰さない（むしろ書き戻して復元する）
//   3. ふつうの設定のクロスデバイス同期は今までどおり（守りすぎて同期が壊れていない）
import { describe, it, expect, beforeEach, afterEach, vi } from "vitest";

const apiMock = vi.fn();
const apiJSONMock = vi.fn();
vi.mock("../core/api/client.ts", () => ({
  api: (...a: unknown[]) => apiMock(...a),
  apiJSON: (...a: unknown[]) => apiJSONMock(...a),
}));

const SETTINGS_KEY = "af-display-settings";
const learned = { "ok": { text: "OK", count: 9, at: 1 }, "commit して": { text: "commit して", count: 4, at: 2 } };

// settings.ts は import 時に localStorage を読む単一状態なので、毎回モジュールごと作り直す。
async function freshSettings(local: Record<string, unknown> = {}) {
  localStorage.setItem(SETTINGS_KEY, JSON.stringify(local));
  vi.resetModules();
  return await import("./settings.ts");
}

beforeEach(() => {
  vi.useFakeTimers();
  apiMock.mockReset();
  apiJSONMock.mockReset().mockResolvedValue({});
  localStorage.clear();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("ui-prefs: 読めるまで保存しない", () => {
  it("keeps quiet while the server copy is unreadable, then sends once it lands", async () => {
    // ワークスペース起動中の CP はこの形（api() は投げずに {error} を返す）。
    apiMock.mockResolvedValueOnce({ error: { code: "http_502", message: "workspace agent unreachable" } });
    const s = await freshSettings({}); // localStorage も空＝手元は DEFAULTS

    expect(await s.hydrateUIPrefs()).toBe(false);
    expect(s.uiPrefsLoaded()).toBe(false);

    s.setSetting("iconSet", "seti");
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).not.toHaveBeenCalled(); // ← 既定値でサーバを潰さない

    // 復帰後: サーバの実データを読めたら、保留していた変更を 1 回だけ送る。
    apiMock.mockResolvedValueOnce({ quickReplies: learned, chatSize: 16 });
    expect(await s.hydrateUIPrefs()).toBe(true);
    expect(s.uiPrefsLoaded()).toBe(true);
    expect(s.getSettings().quickReplies).toEqual(learned); // サーバの学習が届く
    expect(s.getSettings().chatSize).toBe(16);
    expect(s.getSettings().iconSet).toBe("seti"); // 保留していたローカル変更も残る

    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).toHaveBeenCalledTimes(1);
    const [path, method, body] = apiJSONMock.mock.calls[0] as [string, string, Record<string, unknown>];
    expect([path, method]).toEqual(["api/env/ui-prefs", "PUT"]);
    expect(body.quickReplies).toEqual(learned);
    expect(body.iconSet).toBe("seti");
  });

  it("keeps saving normally once the server copy has been read", async () => {
    apiMock.mockResolvedValueOnce({});
    const s = await freshSettings({});
    expect(await s.hydrateUIPrefs()).toBe(true); // 空の {} は「読めた」— 新規ユーザーの正常系

    s.setSetting("iconSet", "material");
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).toHaveBeenCalledTimes(1);
  });
});

describe("ui-prefs: 空で累積データを潰さない", () => {
  it("never lets an emptied server copy wipe learned replies, and pushes them back", async () => {
    const s = await freshSettings({ quickReplies: learned, quickRepliesPinned: ["OK"], iconSet: "seti" });
    // 事故後のサーバ（既定値一式）。ふつうのキーは採り、累積データは採らない。
    apiMock.mockResolvedValueOnce({ quickReplies: {}, quickRepliesPinned: [], iconSet: "vscode" });

    expect(await s.hydrateUIPrefs()).toBe(true);
    expect(s.getSettings().quickReplies).toEqual(learned);
    expect(s.getSettings().quickRepliesPinned).toEqual(["OK"]);
    expect(s.getSettings().iconSet).toBe("vscode"); // 累積でないキーは従来どおりサーバ優先

    // 自己修復: 手元が持っている学習をサーバへ書き戻す。
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).toHaveBeenCalledTimes(1);
    const body = apiJSONMock.mock.calls[0][2] as Record<string, unknown>;
    expect(body.quickReplies).toEqual(learned);
    expect(body.quickRepliesPinned).toEqual(["OK"]);
  });

  it("still adopts a populated server copy of the same keys", async () => {
    const s = await freshSettings({ quickReplies: { ok: { text: "OK", count: 1, at: 1 } } });
    apiMock.mockResolvedValueOnce({ quickReplies: learned });

    expect(await s.hydrateUIPrefs()).toBe(true);
    expect(s.getSettings().quickReplies).toEqual(learned); // 別端末で増えた分は届く
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).not.toHaveBeenCalled(); // 採れたなら書き戻す理由は無い
  });

});

// 実際に起きた事故: 別アカウントで作った作業グループが、同じブラウザ/ワークスペースで
// 別アカウントに切り替えた後もメモリに残り続け、上の「空で潰さない」自己修復ロジックが
// 誤って新アカウントの ui-prefs.json へ書き戻してしまった。identity/tenant の切替では、
// この自己修復を先に迂回する必要がある。
describe("ui-prefs: 持ち主が切り替わったら前の持ち主の累積データを漏らさない", () => {
  it("clears every accumulated key locally instead of showing the previous owner's data", async () => {
    const s = await freshSettings({ workingSets: [{ id: "g1", name: "旧アカウントのグループ", repos: [], convs: [], sessions: [], schedules: [] }] });
    apiMock.mockResolvedValueOnce({}); // 新しい持ち主のサーバー値はまだ空（本来の姿）
    void s.resyncAccumulatedForIdentitySwitch();
    // hydrate の応答を待つ前に、ローカルはもう空になっている（前の持ち主のグループを
    // 一瞬たりとも新しい持ち主の画面に出さない）。
    expect(s.getSettings().workingSets).toEqual([]);
  });

  it("does not restore the previous owner's data via the empty-server self-heal path", async () => {
    const s = await freshSettings({ workingSets: [{ id: "g1", name: "旧アカウントのグループ", repos: [], convs: [], sessions: [], schedules: [] }] });
    apiMock.mockResolvedValueOnce({}); // 新しい持ち主は本当に空
    await s.resyncAccumulatedForIdentitySwitch();
    expect(s.getSettings().workingSets).toEqual([]);
    await vi.advanceTimersByTimeAsync(1_000);
    // 復元(restore)が誤って発火していれば、ここで旧アカウントのグループが書き戻される。
    expect(apiJSONMock).not.toHaveBeenCalled();
  });

  it("adopts the new owner's own accumulated data, not the previous owner's", async () => {
    const s = await freshSettings({ workingSets: [{ id: "g1", name: "旧アカウントのグループ", repos: [], convs: [], sessions: [], schedules: [] }] });
    apiMock.mockResolvedValueOnce({ workingSets: [{ id: "g2", name: "新アカウントのグループ", repos: [], convs: [], sessions: [], schedules: [] }] });
    await s.resyncAccumulatedForIdentitySwitch();
    expect(s.getSettings().workingSets).toEqual([{ id: "g2", name: "新アカウントのグループ", repos: [], convs: [], sessions: [], schedules: [] }]);
  });

  it("discards a pending debounced save instead of sending the previous owner's value to the new owner", async () => {
    const s = await freshSettings({});
    apiMock.mockResolvedValueOnce({});
    await s.hydrateUIPrefs();
    s.setSetting("workingSets", [{ id: "g1", name: "旧アカウントのグループ", repos: [], convs: [], sessions: [], schedules: [] }]); // 600ms 後に保存予定
    apiMock.mockResolvedValueOnce({});
    await s.resyncAccumulatedForIdentitySwitch(); // 保留中の保存はここで捨てられるはず
    await vi.advanceTimersByTimeAsync(1_000);
    expect(apiJSONMock).not.toHaveBeenCalled();
  });

  it("covers every ACCUMULATED key generically (no per-key wiring needed for new ones)", async () => {
    const s = await freshSettings({
      quickReplies: learned,
      quickRepliesPinned: ["OK"],
      quickRepliesHidden: ["NG"],
      keybindings: { save: "mod+s" },
      hiddenModels: { claude: ["opus"] },
      claudeCustomModels: ["claude-custom"],
      workingSets: [{ id: "g1", name: "旧", repos: [], convs: [], sessions: [], schedules: [] }],
      ttsVoicePool: { zundamon: { use: true } },
      ttsUserDict: "旧アカウントの辞書",
      ssmHostUsage: { host1: 3 },
      ssmHostColors: { host1: "#fff" },
      expandThinking: { claude: true },
    });
    apiMock.mockResolvedValueOnce({});
    await s.resyncAccumulatedForIdentitySwitch();
    const after = s.getSettings();
    for (const key of [
      "quickReplies", "quickRepliesPinned", "quickRepliesHidden", "keybindings",
      "claudeCustomModels", "workingSets", "ttsVoicePool", "ttsUserDict",
      "ssmHostUsage", "ssmHostColors", "expandThinking",
    ] as const) {
      expect(s.isEmptyPref((after as any)[key])).toBe(true);
    }
    // hiddenModels の既定値は {claude:["fable"]}（空ではなく初期おすすめ）— 「空」ではなく
    // 「初期値」であることを確認する。
    expect(after.hiddenModels).toEqual({ claude: ["fable"] });
  });
});

describe("ui-prefs: ACCUMULATED の分類", () => {
  it("classifies which keys are protected and what counts as empty", async () => {
    const s = await freshSettings({});
    expect(s.isAccumulatedSetting("quickReplies")).toBe(true);
    expect(s.isAccumulatedSetting("keybindings")).toBe(true);
    expect(s.isAccumulatedSetting("workingSets")).toBe(true);
    // トグルや色は「空」になりようがない＝守る対象ではない（消えても選び直せる）。
    expect(s.isAccumulatedSetting("quickRepliesEnabled")).toBe(false);
    expect(s.isAccumulatedSetting("chatSize")).toBe(false);
    // 0 / false は「消えた」ではなく選ばれた値。
    expect([s.isEmptyPref({}), s.isEmptyPref([]), s.isEmptyPref(""), s.isEmptyPref(null)]).toEqual([true, true, true, true]);
    expect([s.isEmptyPref(0), s.isEmptyPref(false), s.isEmptyPref({ a: 1 }), s.isEmptyPref(["a"])]).toEqual([false, false, false, false]);
  });
});
