import { describe, expect, it } from "vitest";
import { expandThinking, getSettings, isDeviceLocalSetting, migrateAiAssistPrefs, normalizeAgentLaunchDefaults, normalizeClaudeCustomModels, type Settings } from "./settings.ts";

// 純ロジックだが jsdom プロジェクト（.dom.test.tsx）に置く: settings.ts は API クライアント
// 経由で読み込み時に localStorage を触るため、node 環境では import 自体が落ちる。
//
// 「思考を展開して表示」（設定 > エージェント > 各カード > 動作設定）は kind スコープで、
// 未設定は必ずオフ＝従来どおり畳んで出す。サーバー ui-prefs は別バージョンの Console も
// 書くので、boolean 以外が入っていてもオフに倒れることまで固定する。
const withThinking = (map: unknown): Settings =>
  ({ ...getSettings(), expandThinking: map } as Settings);

describe("expandThinking", () => {
  it("defaults to off for every kind", () => {
    const s = withThinking({});
    expect(expandThinking(s, "opencode")).toBe(false);
    expect(expandThinking(s, "codex")).toBe(false);
    expect(expandThinking(s, "claude")).toBe(false);
  });

  it("applies per kind, independently", () => {
    const s = withThinking({ opencode: true, codex: false });
    expect(expandThinking(s, "opencode")).toBe(true);
    expect(expandThinking(s, "codex")).toBe(false);
    // 設定を持たない kind（cursor / kiro も思考を出す）は巻き添えにしない。
    expect(expandThinking(s, "cursor")).toBe(false);
  });

  it("falls back to off for a missing kind or a broken stored value", () => {
    const s = withThinking({ opencode: "yes", codex: 1 });
    expect(expandThinking(s, "opencode")).toBe(false);
    expect(expandThinking(s, "codex")).toBe(false);
    expect(expandThinking(s, undefined)).toBe(false);
    expect(expandThinking(withThinking(undefined), "opencode")).toBe(false);
  });
});

describe("normalizeClaudeCustomModels", () => {
  it("trims ids and drops aliases, duplicates, and broken values", () => {
    expect(normalizeClaudeCustomModels([
      " claude-opus-4-8 ", "CLAUDE-OPUS-4-8", "claude-opus-4-7", "claude-opus-4-6[1m]", "opus", "bad model", 42, "",
    ])).toEqual(["claude-opus-4-8", "claude-opus-4-7", "claude-opus-4-6[1m]"]);
  });

  it("falls back to an empty catalog for a broken stored value", () => {
    expect(normalizeClaudeCustomModels("claude-opus-4-8")).toEqual([]);
  });
});

describe("device-local settings", () => {
  it("keeps the pane layout profile on this device", () => {
    expect(isDeviceLocalSetting("paneLayout")).toBe(true);
  });

  it("continues syncing personal content preferences", () => {
    expect(isDeviceLocalSetting("viewerFont")).toBe(false);
    expect(isDeviceLocalSetting("locale")).toBe(false);
  });

  // 面ごとのテーマ/背景は「この端末でどう見せるか」なので端末ローカル。共有セッション
  // (docs/log/59)も同じ扱い — 追加時にここから漏らすと、別端末の見え方を勝手に上書きする。
  it("keeps every per-region look on this device", () => {
    for (const key of ["mirrorTheme", "sharedTheme", "assistantTheme", "chatColor", "sharedColor"] as const) {
      expect(isDeviceLocalSetting(key)).toBe(true);
    }
  });
});

// 権限確認の既定（docs/log/76）。**欠落は「スキップする」**でなければならない — 既存の
// prefs を読んだ端末で全セッションが承認待ちになるのは「既定は現状のまま」の破り方の
// 中で一番目立たない。明示 false のときだけ承認あり。
describe("normalizeAgentLaunchDefaults / skipPermissions", () => {
  it("defaults to skipping approvals when the key is absent or broken", () => {
    const rows = normalizeAgentLaunchDefaults({ claude: { model: "opus" }, cursor: { skipPermissions: "no" } });
    expect(rows.claude.skipPermissions).toBe(true);
    expect(rows.cursor.skipPermissions).toBe(true);
    // 一度も設定されていない kind も同じ（DEFAULT_AGENT_LAUNCH の行）。
    expect(rows.kiro.skipPermissions).toBe(true);
  });

  it("keeps an explicit opt-in to approvals, per kind", () => {
    const rows = normalizeAgentLaunchDefaults({ claude: { skipPermissions: false }, kiro: { skipPermissions: true } });
    expect(rows.claude.skipPermissions).toBe(false);
    expect(rows.kiro.skipPermissions).toBe(true);
    // 他 kind に漏れない。
    expect(rows.cursor.skipPermissions).toBe(true);
  });
});

// --- AI 補助生成の分離（docs/log/84）の移行 -----------------------------------
//
// 原則は「アップグレードで挙動を変えない」。ただし aiProseModels だけは意図的に
// 引き継がない（旧キーは名前に反して文章生成の既定まで置き換えていた）。
// load()（localStorage）と hydrateUIPrefs()（サーバ prefs）が同じ関数を通ることは
// settingsSync 側ではなくここで固定する — 規則が 2 箇所に写経されていたのが元の事故。
describe("migrateAiAssistPrefs", () => {
  it("splits the old title toggle into session / chat / branch", () => {
    const o: Record<string, unknown> = { autoTitleSuggest: false };
    migrateAiAssistPrefs(o);
    expect(o.assistantTitleSuggest).toBe(false);
    expect(o.branchSuggestEnabled).toBe(false);
  });

  it("never overwrites a key the user already set", () => {
    const o: Record<string, unknown> = { autoTitleSuggest: false, branchSuggestEnabled: true };
    migrateAiAssistPrefs(o);
    expect(o.branchSuggestEnabled).toBe(true);
  });

  it("carries the single agent order over to the assist side", () => {
    const o: Record<string, unknown> = { assistantAgentOrder: ["codex", "claude"] };
    migrateAiAssistPrefs(o);
    expect((o.aiAssistOrder as string[])[0]).toBe("codex");
    // チャット側はそのまま（分離しても片方だけ動くことはない）
    expect((o.assistantAgentOrder as string[])[0]).toBe("codex");
  });

  it("inherits the legacy utility models for SHORT only, not prose", () => {
    const o: Record<string, unknown> = { assistantUtilityModels: { claude: "haiku" } };
    migrateAiAssistPrefs(o);
    expect(o.aiShortModels).toEqual({ claude: "haiku" });
    // 文章生成（ファイル編集の提案・計画更新）は用途に合った推奨へ戻す。
    expect("aiProseModels" in o).toBe(false);
  });

  it("gives read-aloud its own language key, seeded from the borrowed one", () => {
    const o: Record<string, unknown> = { outputLanguage: "en" };
    migrateAiAssistPrefs(o);
    expect(o.ttsLang).toBe("en");
  });

  it("leaves a pre-split blob alone when nothing legacy is present", () => {
    const o: Record<string, unknown> = {};
    migrateAiAssistPrefs(o);
    expect(o).toEqual({});
  });
});
