import { useSyncExternalStore } from "react";
import { api, apiJSON } from "../core/api/client.ts";

// Display settings (theme / fonts / file-viewer options / icon set). Persisted in
// localStorage for instant load + offline, AND mirrored to the server per-user
// (GET/PUT /api/env/ui-prefs) so they follow the user across browsers/devices.
// Shared across React (useSettings) and non-React code (term.js via getSettings +
// subscribe). Terminal and viewer fonts are independent — they default to different
// families so they're visibly distinct out of the box.

const KEY = "af-display-settings";

export const CODE_FONTS = [
  "Source Code Pro",
  "JetBrains Mono",
  "Fira Code",
  "IBM Plex Mono",
  "システム等幅",
];

// Chat fonts: unlike the code viewer, the chat reads as prose, so proportional
// families are offered first ("システム" = system sans, "セリフ" = serif), with the
// monospace code fonts still available for anyone who prefers them.
export const CHAT_FONTS = ["システム", "セリフ", "Source Code Pro", "JetBrains Mono", "Fira Code", "IBM Plex Mono"];

// File-icon sets (brand SVGs under assets/fileicons/<id>/). value = asset subdir.
export const ICON_SETS = [
  { id: "vscode", label: "VS Code Icons（カラー）" },
  { id: "material", label: "Material（カラー）" },
  { id: "devicon", label: "Devicon（カラー）" },
  { id: "seti", label: "Seti（単色・タイプ別着色）" },
];

// Base UI theme.
export const THEMES = [
  { id: "dark", label: "ダーク" },
  { id: "light", label: "ライト" },
];

// Surface (top bar / left pane) background choices. Each color has a per-theme tint
// so it always contrasts with the theme's text color. "default" = theme default.
export const SURFACE_COLORS = [
  { id: "default", label: "デフォルト", dark: null, light: null, accent: null },
  { id: "slate", label: "スレート", dark: "#1b2733", light: "#e2e8f0", accent: "#6b8fc4" },
  { id: "blue", label: "ブルー", dark: "#16263f", light: "#dbe7fb", accent: "#3b82f6" },
  { id: "green", label: "グリーン", dark: "#15291f", light: "#dcefe0", accent: "#2fb872" },
  { id: "purple", label: "パープル", dark: "#241a33", light: "#ece0fb", accent: "#a875f5" },
  { id: "warm", label: "ウォーム", dark: "#2a1f17", light: "#f6e8da", accent: "#e0964a" },
];

// The four themeable surfaces (settings key + labels). Shared by DisplayTab and the
// TopBar 外観 popover so "which surfaces are colorable" is defined once — `short` for
// the compact popover rows, `long` for the settings-tab rows.
export const SURFACE_TARGETS: { key: "topbarColor" | "leftpaneColor" | "viewerColor" | "chatColor"; short: string; long: string }[] = [
  { key: "topbarColor", short: "上部バー", long: "上部バーの背景" },
  { key: "leftpaneColor", short: "左ペイン", long: "左ペインの背景" },
  { key: "viewerColor", short: "ビュアー", long: "ファイルビュアーの背景" },
  { key: "chatColor", short: "チャット", long: "チャットの背景" },
];

// Resolve a surface color id to its value for the active theme (null = no override).
export function surfaceValue(id: string, theme: string): string | null {
  const c = SURFACE_COLORS.find((x) => x.id === id);
  if (!c) return null;
  return theme === "light" ? c.light : c.dark;
}

// The vivid accent that matches a surface color's family (theme-independent), used to
// tint the chat highlights so they follow the chosen palette instead of a fixed blue.
export function surfaceAccent(id: string): string | null {
  const c = SURFACE_COLORS.find((x) => x.id === id);
  return (c && c.accent) || null;
}

// Linear blend between two #rrggbb colors (t in 0..1 toward `to`).
function mixHex(from: string, to: string, t: number): string {
  const p = (h: string) => [1, 3, 5].map((i) => parseInt(h.slice(i, i + 2), 16));
  const a = p(from);
  const b = p(to);
  const c = a.map((v, i) => Math.round(v + (b[i] - v) * t));
  return "#" + c.map((v) => v.toString(16).padStart(2, "0")).join("");
}

// Default row highlight per theme — mirrors --active-bg / --hover-bg in styles.css
// (:root dark, [data-theme="light"]). Used when no left-pane surface is chosen, so
// the highlight matches the prior fixed behavior.
const THEME_ROW_DEFAULTS = {
  dark: { active: "#20303a", hover: "#1b2730" },
  light: { active: "#d7e6fb", hover: "#eaf1fb" },
};

// shadeForSurface derives a row highlight from a left-pane surface color so the
// active/hover stays in the surface's color family: darken toward black in light
// mode, lighten toward white in dark mode.
function shadeForSurface(hex: string, theme: string, kind: string): string {
  const t =
    kind === "active" ? (theme === "light" ? 0.12 : 0.16) : theme === "light" ? 0.06 : 0.08;
  return mixHex(hex, theme === "light" ? "#000000" : "#ffffff", t);
}

// 読み上げのキャラクター 1 体分の設定（ttsVoicePool の値）。すべて省略可＝既定挙動。
export interface TtsCharConf {
  use?: boolean;
  style?: string;
  speed?: number;
}

export interface Settings {
  termFont: string;
  termSize: number;
  viewerFont: string;
  viewerSize: number;
  chatFont: string;
  chatSize: number;
  lineNumbers: boolean;
  wrap: boolean;
  tabSize: number;
  minimap: boolean;
  iconSet: string;
  theme: string;
  topbarColor: string;
  leftpaneColor: string;
  viewerColor: string;
  chatColor: string;
  mirrorSend: string;
  // Default claude model for new sessions (launch dialog + repo 起動). Always a concrete
  // tier alias (opus/sonnet/haiku) — the alias tracks the newest model in that tier, but
  // the tier itself is pinned, so cost/behavior never shift under you between releases.
  defaultModel: string;
  // Global ON/OFF for the auto session-title-suggestion feature (DisplayTab セッション).
  // Default true so existing users get it without an explicit opt-in.
  autoTitleSuggest: boolean;
  // Forced output language for assistant chat: "auto" = follow the input language
  // (default), "ja" / "en" = always reply in that language (even for foreign-language
  // content). The Agent reads this key from ui-prefs and injects a language rule into the
  // chat system prompt (translate assistant is exempt). See docs/19.
  outputLanguage: string;
  // Per-SSM-host terminal color: host id → color id (see lib/termcolor SSM_HOST_COLORS).
  // Applied to a session's terminal background when it's created (sent as its color).
  ssmHostColors: Record<string, string>;
  // 音声読み上げ（TTS, docs/24 + ADR0013）。エージェント回答を VOICEVOX（ずんだもん）で
  // 読み上げる。CP-native な /api/tts/synthesize を句点区切りで逐次呼ぶ（features/chat/tts.ts）。
  ttsEnabled: boolean;
  // プロバイダ選択（Phase 2 で auto ルーティング実装）。auto = 日本語×engine ready なら
  // ずんだもん、engine 不在（起動中/無効）は Polly JP へ自動フォールバック、非日本語は Polly。
  // 最終決定は CP（engine の ready を知る単一の真実源）。明示 polly なら常に Polly。
  ttsProvider: string; // "auto" | "voicevox" | "polly"
  ttsVoiceVoicevox: string; // VOICEVOX の speaker 番号（"3"=ずんだもん・ノーマル）
  ttsVoicePolly: string; // Polly の VoiceId（"Takumi" 等）。auto のフォールバック時も使う
  ttsSpeed: number; // 0.5〜2.0（speedScale）
  // バックグラウンドのセッションが回答/質問を返したら音声で知らせる（docs/24 Tier1）。チャットの
  // 自動読み上げ(ttsEnabled)とは別軸。名前前置きの短い告知を直列キューで読む。タブが見えている
  // 間のみ（セッション監視は document.hidden で止まるため）。
  ttsSessionNotify: boolean;
  // 英単語をカタカナ英語に変換してから VOICEVOX に読ませる（docs/24, CP の enkana 前処理）。
  // ずんだもんの声のまま英語を "それっぽく"（日本語アクセントで）読む。CMU 発音辞書ベースの
  // 音写なので、定着した和製カタカナ（コーヒー等）ではなく音写（カフィー等）になる。
  ttsEnglishKana: boolean;
  // ユーザー読み仮名辞書（docs/24）。1 行 "表記=読み"。読み上げ直前に読み上げテキストへ
  // リテラル置換で適用（英語/日本語/記号どれでも。enkana の ON/OFF に依らず効く）。表記は
  // 長いものから当てる。空 = 無効。features/chat/ttsText.ts の parse/applyUserDict。
  ttsUserDict: string;
  // 合成キャッシュの上限（合計再生秒数）。同一文言＋同一合成条件の音声をメモリに保持して
  // 再読み上げを即時化する（features/chat/tts.ts）。PCM で約 0.1MB/秒。0 = キャッシュなし。
  ttsCacheSec: number;
  // ミラー（チャット）: アクティブなペインのセッションに新しい回答が届いたら自動でカラオケ
  // 朗読する（features/mirror/turnTts.ts）。見ていないセッションの短い告知 ttsSessionNotify
  // とは相補（こちらは見ている画面の本文を読む）。
  ttsAutoReadMirror: boolean;
  // 自動読み上げをアクティブなペインだけでなく「開いている全ペイン」で行う（ttsAutoReadMirror
  // のサブオプション）。各ペインの新着回答は 1 本の再生に直列で並ぶ。同じセッションを複数
  // ペインで開いていても読むのは 1 ペインだけ（features/mirror/turnTts.ts の担当登録）。
  // セッションごとの声（ttsVoicePerSession）と併用すると、どのペインの回答かを声で判別できる。
  ttsAutoReadAllPanes: boolean;
  // セッションごとに声を変える（features/chat/tts.ts の sessionVoiceOpts）。セッション名の
  // ハッシュで話者プール（VOICEVOX 標準キャラ / Polly 3 声）から決定的に割り当て、ミラーの
  // 読み上げとセッション音声通知に適用（どのセッションの回答かを声で判別できる）。
  // チャットタブ・朗読ビューは選択した話者のまま。
  ttsVoicePerSession: boolean;
  // キャラクター設定（features/chat/tts.ts の voiceCharacters/activeVoicePool）。キーは
  // エンジンのキャラ名。use=セッション声プール・朗読ビューの選択肢に入れるか（未設定は
  // 既定プール = tts.ts の SESSION_VOICES に載っているキャラのみ true）、style=基準スタイル
  // （speaker 番号。未設定はノーマル）、speed=キャラ別速度（未設定はグローバル ttsSpeed）。
  // 一覧は VOICEVOX エンジンの実カタログ（GET /api/tts/speakers）から出す。
  ttsVoicePool: Record<string, TtsCharConf>;
  // ミラー（チャット）: アクティブなペインのセッションが確認待ち（AskUserQuestion／
  // プラン承認／許可要求）になったら、質問文と選択肢を読み上げる。選択肢は表示ラベルで
  // なく説明文（ツールチップの中身）を優先して読む（features/chat/ttsText.ts の pendingSpeech）。
  ttsReadPending: boolean;
  // ミラーの自動読み上げで、長い回答（目安 500 字超）はアシスタント（headless CLI）に
  // 2 文へ要約させてそれを読む（features/mirror/MirrorView.tsx）。フル本文はターンの
  // 読み上げボタンでいつでも聞ける。生成失敗・タイムアウトは全文読みにフォールバック。
  ttsSummaryRead: boolean;
  // 文の内容で感情スタイルを切り替える（features/chat/tts.ts の emotionOpts）。エラー・
  // 失敗系の文はツンツン系、成功・完了系はあまあま系スタイルで読む。スタイル variant を
  // 持つ話者（ずんだもん・四国めたん・九州そら）のときだけ効く。
  ttsEmotion: boolean;
  // インラインコード（`…`）を省略して読む（features/chat/ttsText.ts の abbrevCode）。
  // ハッシュ等は頭 2 文字＋フィラー語（なんとか 等）、camelCase/パスは頭一語＋フィラー
  // （3 語以上は＋末尾一語）。短い語・空白入り・日本語入り・読み仮名辞書に掛かる表記はそのまま。
  // バッククォート無しの裸ハッシュ（f437e17 等の小文字 16 進・UUID）も同じ扱い（isBareHash）。
  ttsAbbrevCode: boolean;
  // 助詞（を・は・で・に・と）の直後に漢字が続くとき、読点を挿入して小さな間で読む
  // （features/chat/ttsText.ts の pauseParticles。句点の一拍より短い「息継ぎ」相当）。
  ttsParticlePause: boolean;
  // 朗読ビュー（docs/24）を縦書きで表示するか（既定 false=横書き）。ReaderView のトグルに追随。
  readerVertical: boolean;
  // 朗読ビューの声。"" = 設定の話者のまま / "vv:<speaker>" = VOICEVOX のキャラ /
  // "polly:<VoiceId>" = Polly。ReaderView ヘッダーの選択に追随（features/chat/tts.ts の
  // voiceChoiceOpts が TtsOptions の上書きへ解決する）。
  readerVoice: string;
}

// The pinned fallback model. Used as the seeded global default and as resolveModel's
// terminal fallback, so a new session always launches on a concrete tier — never claude's
// own release-varying default. Change here to move everyone's baseline tier.
export const DEFAULT_MODEL = "sonnet";

const DEFAULTS: Settings = {
  termFont: "Source Code Pro",
  termSize: 13,
  viewerFont: "JetBrains Mono",
  viewerSize: 13,
  chatFont: "システム",
  chatSize: 14,
  lineNumbers: true,
  wrap: false,
  tabSize: 4,
  minimap: true,
  iconSet: "vscode",
  theme: "dark",
  topbarColor: "default",
  leftpaneColor: "default",
  viewerColor: "default",
  chatColor: "default",
  // Markdown mirror composer: "mod-enter" = Ctrl/⌘+Enter submits, Enter inserts a
  // newline (phone-friendly default); "enter" = Enter submits, Shift+Enter newline.
  mirrorSend: "mod-enter",
  defaultModel: DEFAULT_MODEL, // concrete tier (avoids claude's release-varying own pick)
  autoTitleSuggest: true,
  outputLanguage: "auto",
  ssmHostColors: {},
  // 音声読み上げの初期値＝おすすめ設定。設定タブの「リセット」ボタンが戻す値（TTS_RESET）と
  // 同じで、新規ユーザー（と未設定の既存ユーザー）はこの状態から始まる。読み上げ本体・音声通知
  // だけは OFF スタート、ほかは ON にしたとき快適な既定（セッションごとに声を変える／はやめ／
  // 自動読み上げ・全ペイン・確認質問・カタカナ読み ON／キャッシュ 15 分）。TTS_RESET はこの
  // DEFAULTS から TTS 関連キーだけ抜き出して定義しているので、両者は常に一致する。
  ttsEnabled: false,
  ttsProvider: "auto",
  ttsVoiceVoicevox: "3", // ノーマル
  ttsVoicePolly: "Takumi",
  ttsSpeed: 1.25, // はやめ
  ttsSessionNotify: false,
  ttsEnglishKana: true,
  ttsUserDict: "",
  ttsCacheSec: 900, // 15分
  ttsAutoReadMirror: true,
  ttsAutoReadAllPanes: true,
  ttsVoicePerSession: true,
  // {} = 標準 14 キャラ（tts.ts の SESSION_VOICES）がセッション割り当て対象。静的な既定には
  // 「エンジンの全キャラ」を焼き込めない（全一覧は実行時にしか分からない）ため、標準 14 人を
  // 既定チェックとする。それを超える追加キャラまで全部チェックするのは明示リセット時のみ。
  ttsVoicePool: {},
  ttsEmotion: false,
  ttsReadPending: true,
  ttsSummaryRead: false,
  ttsAbbrevCode: true,
  ttsParticlePause: true,
  readerVertical: false,
  readerVoice: "",
};

// VOICEVOX ずんだもんのスタイル（speaker 番号 → ラベル）。設定 UI の話者選択に使う。
export const VOICEVOX_ZUNDAMON: [string, string][] = [
  ["3", "ノーマル"],
  ["1", "あまあま"],
  ["7", "ツンツン"],
  ["5", "セクシー"],
  ["22", "ささやき"],
  ["38", "ヒソヒソ"],
];

// TTS プロバイダ（docs/24 Phase 2）。auto の使い分けは CP が決める。
export const TTS_PROVIDERS: [string, string][] = [
  ["auto", "自動"],
  ["voicevox", "ずんだもん"],
  ["polly", "Polly"],
];

// Polly の日本語ニューラル話者（VoiceId → ラベル）。
export const TTS_POLLY_VOICES: [string, string][] = [
  ["Takumi", "Takumi（男性）"],
  ["Kazuha", "Kazuha（女性）"],
  ["Tomoko", "Tomoko（女性）"],
];

// 合成キャッシュの上限（合計再生秒数 → ラベル）。メモリ消費は PCM で約 0.1MB/秒。
export const TTS_CACHE_SIZES: [number, string][] = [
  [0, "なし"],
  [300, "5分（約30MB）"],
  [900, "15分（約90MB）"],
  [1800, "30分（約180MB）"],
];

// 音声読み上げ設定の「初期状態」（設定タブのリセットボタンが書き戻す値）。DEFAULTS の TTS 関連
// キーだけを抜き出したもので、新規ユーザーの初期値と常に一致する（DEFAULTS を単一の真実源に
// することでドリフトを防ぐ）。キャラクターの「全てチェック」はカタログ依存なので TtsTab 側で
// ttsVoicePool を組み立てて足す（ここには含めない）。読み仮名辞書（ttsUserDict）はユーザーが
// 打ち込んだ内容なのでリセットでは消さない（含めない）。
const TTS_RESET_KEYS = [
  "ttsEnabled",
  "ttsSessionNotify",
  "ttsProvider",
  "ttsVoiceVoicevox",
  "ttsVoicePolly",
  "ttsVoicePerSession",
  "ttsEmotion",
  "ttsSpeed",
  "ttsAutoReadMirror",
  "ttsAutoReadAllPanes",
  "ttsSummaryRead",
  "ttsReadPending",
  "ttsAbbrevCode",
  "ttsParticlePause",
  "ttsEnglishKana",
  "ttsCacheSec",
] as const;
export const TTS_RESET: Partial<Settings> = Object.fromEntries(
  TTS_RESET_KEYS.map((k) => [k, DEFAULTS[k]]),
) as Partial<Settings>;

// 読み上げ速度（speedScale）。
export const TTS_SPEEDS: [number, string][] = [
  [0.75, "ゆっくり"],
  [1.0, "標準"],
  [1.25, "はやめ"],
  [1.5, "はやい"],
];

// Assistant-chat output-language choices, shared by the settings UI. "auto" leaves the
// language to the user's input; "ja"/"en" force the reply language.
export const OUTPUT_LANGUAGES: [string, string][] = [
  ["auto", "入力に合わせる"],
  ["ja", "日本語"],
  ["en", "English"],
];

// Mirror composer submit-key options, shared by the settings UI.
export const MIRROR_SEND_MODES = [
  { id: "mod-enter", label: "Ctrl+Enter で送信" },
  { id: "enter", label: "Enter で送信" },
];

// Claude model choices, shared by the launch dialog and the default-model setting. Only
// concrete tiers — the "既定"/"" (defer to claude's release-varying own default) option was
// dropped on purpose so model selection stays deterministic. Each alias still tracks the
// newest model within its tier.
export const CLAUDE_MODELS: [string, string][] = [
  ["fable", "Fable"],
  ["opus", "Opus"],
  ["sonnet", "Sonnet"],
  ["haiku", "Haiku"],
];

// Build a CSS font-family stack for a chosen family, with CJK + generic fallbacks.
export function fontStack(name: string): string {
  if (!name || name === "システム等幅") {
    return 'ui-monospace, SFMono-Regular, Menlo, Consolas, "DejaVu Sans Mono", "Noto Sans Mono CJK JP", monospace';
  }
  return `"${name}", "Noto Sans Mono CJK JP", ui-monospace, Menlo, Consolas, monospace`;
}

// Chat font stack — proportional by default. "システム"/"セリフ" map to sans/serif
// system stacks (with CJK fallbacks); any other name is a code font (monospace).
export function chatFontStack(name: string): string {
  if (!name || name === "システム") {
    return 'system-ui, -apple-system, "Hiragino Kaku Gothic ProN", "Noto Sans CJK JP", sans-serif';
  }
  if (name === "セリフ") {
    return 'Georgia, "Times New Roman", "Hiragino Mincho ProN", "Noto Serif CJK JP", serif';
  }
  return fontStack(name);
}

function load(): Settings {
  try {
    return { ...DEFAULTS, ...JSON.parse(localStorage.getItem(KEY) || "{}") };
  } catch {
    return { ...DEFAULTS };
  }
}

let state = load();
const subs = new Set<() => void>();

// applyTheme writes the base theme + region color overrides onto <html>, so the
// whole CSS-variable palette switches. Called at load (before paint) and on change.
export function applyTheme(s: Settings): void {
  if (typeof document === "undefined") return;
  const root = document.documentElement;
  const theme = s.theme === "light" ? "light" : "dark";
  root.dataset.theme = theme;
  const setVar = (name: string, val: string | null) =>
    val ? root.style.setProperty(name, val) : root.style.removeProperty(name);
  setVar("--topbar-bg", surfaceValue(s.topbarColor, theme));
  // Admin modal accent follows the chosen top-bar surface (buttons/tabs/bars retint via
  // .admin-surface). Null (no color chosen) → falls back to the CSS :root default blue.
  setVar("--topbar-accent", surfaceAccent(s.topbarColor));
  const lp = surfaceValue(s.leftpaneColor, theme);
  setVar("--leftpane-bg", lp);
  // Make the left-pane row highlight follow the chosen surface color (sessions /
  // repos / files active + hover). The .leftpane rule rebinds --active-bg /
  // --hover-bg to these. When no surface is chosen, fall back to the theme default
  // (read live so it tracks dark/light) so behavior is unchanged.
  if (lp) {
    setVar("--lp-active-bg", shadeForSurface(lp, theme, "active"));
    setVar("--lp-hover-bg", shadeForSurface(lp, theme, "hover"));
  } else {
    const d = THEME_ROW_DEFAULTS[theme];
    setVar("--lp-active-bg", d.active);
    setVar("--lp-hover-bg", d.hover);
  }
  // Left-pane accent = the chosen surface's matching accent, so the rail's focus
  // bar (active session / repo) and the worktree spine follow the palette instead
  // of the fixed app blue. Null (no surface chosen) → CSS falls back to --accent.
  setVar("--lp-accent", surfaceAccent(s.leftpaneColor));
  // File viewer background, derived from the chosen surface: lighter than the
  // surfaces in light theme (toward white), darker in dark theme (toward black).
  // Unset => theme --bg.
  const vw = surfaceValue(s.viewerColor, theme);
  setVar("--viewer-bg", vw ? (theme === "light" ? mixHex(vw, "#ffffff", 0.45) : mixHex(vw, "#000000", 0.34)) : null);
  // Viewer surface accent — the terminal pane shares --viewer-bg, so the チャット/ターミナル
  // toggle rebinds --sel-accent to this. Null (no viewer color) → CSS falls back to topbar.
  setVar("--viewer-accent", surfaceAccent(s.viewerColor));
  // Chat surface (the Markdown mirror) is independent of the file viewer: its own
  // background, derived the same way. Unset => falls back to the viewer surface (then
  // the theme --bg) in CSS, preserving the prior "chat sits on the viewer" behavior.
  const cw = surfaceValue(s.chatColor, theme);
  setVar("--chat-bg", cw ? (theme === "light" ? mixHex(cw, "#ffffff", 0.45) : mixHex(cw, "#000000", 0.34)) : null);
  // Chat highlights (question options, "あなた" block, gauge…) follow the chat's own
  // surface accent first, then the viewer / left pane / top bar; null falls back to
  // the CSS default (--accent, the app blue).
  setVar(
    "--chat-accent",
    surfaceAccent(s.chatColor) ||
      surfaceAccent(s.viewerColor) ||
      surfaceAccent(s.leftpaneColor) ||
      surfaceAccent(s.topbarColor),
  );
}
applyTheme(state);

export function getSettings(): Settings {
  return state;
}

// Debounced mirror of the full settings object to the per-user server store. Best
// effort: if the workspace is stopped / agent unreachable, localStorage still holds it.
let saveTimer: ReturnType<typeof setTimeout> | null = null;
function scheduleServerSave(): void {
  if (saveTimer) clearTimeout(saveTimer);
  saveTimer = setTimeout(() => {
    apiJSON("api/env/ui-prefs", "PUT", state).catch(() => {});
  }, 600);
}

// hydrateUIPrefs pulls the server-stored prefs (if any) and merges the known keys
// over the local state, so a fresh browser inherits the user's settings. Called once
// at boot after the tenant is resolved (state.jsx). Server wins over localStorage.
export async function hydrateUIPrefs(): Promise<void> {
  let srv: any;
  try {
    srv = await api("api/env/ui-prefs");
  } catch {
    return;
  }
  if (!srv || typeof srv !== "object" || srv.error) return;
  let changed = false;
  const merged: Settings = { ...state };
  for (const k of Object.keys(DEFAULTS)) {
    if (k in srv && srv[k] !== (merged as any)[k]) {
      (merged as any)[k] = srv[k];
      changed = true;
    }
  }
  if (!changed) return;
  state = merged;
  try {
    localStorage.setItem(KEY, JSON.stringify(state));
  } catch {}
  applyTheme(state);
  subs.forEach((fn) => fn());
}

export function setSetting(key: keyof Settings, value: Settings[keyof Settings]): void {
  setSettings({ [key]: value } as Partial<Settings>);
}

// setSettings — 複数キーを 1 回で更新する（1 レンダー・1 サーバー保存にまとめる）。
// リセットのように多数のキーをまとめて書き換える用途で使う（setSetting を 17 回呼ぶと
// その回数だけ再レンダーとデバウンス保存が走るため）。
export function setSettings(patch: Partial<Settings>): void {
  state = { ...state, ...patch };
  try {
    localStorage.setItem(KEY, JSON.stringify(state));
  } catch {}
  applyTheme(state);
  scheduleServerSave();
  subs.forEach((fn) => fn());
}

export function subscribe(fn: () => void): () => void {
  subs.add(fn);
  return () => {
    subs.delete(fn);
  };
}

export function useSettings(): Settings {
  return useSyncExternalStore(subscribe, getSettings, getSettings);
}
