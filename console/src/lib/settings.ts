import { useSyncExternalStore } from "react";
import { api, apiJSON } from "../core/api/client.ts";
import { setLocale } from "./i18n/index.ts";
import type { MsgKey } from "./i18n/index.ts";

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
  "システム等幅", // i18n-exempt: fontStack 突合用の生フォント値（表示は font.* で翻訳）
];

// Chat fonts: unlike the code viewer, the chat reads as prose, so proportional
// families are offered first ("システム" = system sans, "セリフ" = serif), with the
// monospace code fonts still available for anyone who prefers them.
// i18n-exempt: fontStack 突合用の生フォント値（表示は font.* で翻訳・docs/28 §2.4）
export const CHAT_FONTS = ["システム", "セリフ", "Source Code Pro", "JetBrains Mono", "Fira Code", "IBM Plex Mono"];

// Reader (朗読ビュー) fonts: the reader is Japanese prose, so it offers the two
// families that matter for reading — 明朝 (serif, the default) and ゴシック (sans) —
// each resolved with CJK fallbacks in readerFontStack.
// i18n-exempt: fontStack 突合用の生フォント値（表示は font.* で翻訳・docs/28 §2.4）
export const READER_FONTS = ["明朝", "ゴシック"];

// File-icon sets (brand SVGs under assets/fileicons/<id>/). value = asset subdir.
export const ICON_SETS: { id: string; labelKey: MsgKey }[] = [
  { id: "vscode", labelKey: "iconset.vscode" },
  { id: "material", labelKey: "iconset.material" },
  { id: "devicon", labelKey: "iconset.devicon" },
  { id: "seti", labelKey: "iconset.seti" },
];

// Base UI theme. label は i18n キー（DisplayTab / TopBar が t() で解決）。
export const THEMES: { id: string; labelKey: MsgKey }[] = [
  { id: "dark", labelKey: "theme.dark" },
  { id: "light", labelKey: "theme.light" },
];

// UI 表示言語（docs/28 / ADR 0016）。ラベルは各言語の自称なので翻訳しない（どの言語で見ても
// 母語名で並ぶ）。id は i18n カタログ／SUPPORTED_LOCALES と一致させる。
export const LOCALES = [
  { id: "ja", label: "日本語" }, // i18n-exempt: 言語の自称（どの UI 言語でも母語名で表示）
  { id: "en", label: "English" },
];

// Per-region theme choices (session mirror / assistant chat). "inherit" = follow the app
// theme above; "dark"/"light" give that region its own base theme, independent of the rest
// of the Console — applied by scoping data-theme onto the region container (.mirrorview /
// .chatview; see tokens.css scoped token blocks + effectiveTheme).
export const REGION_THEMES: { id: string; labelKey: MsgKey }[] = [
  { id: "inherit", labelKey: "region_theme.inherit" },
  { id: "dark", labelKey: "theme.dark" },
  { id: "light", labelKey: "theme.light" },
];

// Resolve a per-region theme preference against the app theme, to a concrete "light"/"dark".
export function effectiveTheme(pref: string, base: string): "light" | "dark" {
  if (pref === "dark" || pref === "light") return pref;
  return base === "light" ? "light" : "dark";
}

// Surface (top bar / left pane) background choices. Each color has a per-theme tint
// so it always contrasts with the theme's text color. "default" = theme default.
export const SURFACE_COLORS: { id: string; labelKey: MsgKey; dark: string | null; light: string | null; accent: string | null }[] = [
  { id: "default", labelKey: "surface_color.default", dark: null, light: null, accent: null },
  { id: "slate", labelKey: "surface_color.slate", dark: "#1b2733", light: "#e2e8f0", accent: "#6b8fc4" },
  { id: "blue", labelKey: "surface_color.blue", dark: "#16263f", light: "#dbe7fb", accent: "#3b82f6" },
  { id: "green", labelKey: "surface_color.green", dark: "#15291f", light: "#dcefe0", accent: "#2fb872" },
  { id: "purple", labelKey: "surface_color.purple", dark: "#241a33", light: "#ece0fb", accent: "#a875f5" },
  { id: "warm", labelKey: "surface_color.warm", dark: "#2a1f17", light: "#f6e8da", accent: "#e0964a" },
];

// The four themeable surfaces (settings key + labels). Shared by DisplayTab and the
// TopBar 外観 popover so "which surfaces are colorable" is defined once — `short` for
// the compact popover rows, `long` for the settings-tab rows.
export const SURFACE_TARGETS: { key: "topbarColor" | "leftpaneColor" | "viewerColor" | "chatColor" | "assistantColor"; shortKey: MsgKey; longKey: MsgKey }[] = [
  { key: "topbarColor", shortKey: "surface.topbar.short", longKey: "surface.topbar.long" },
  { key: "leftpaneColor", shortKey: "surface.leftpane.short", longKey: "surface.leftpane.long" },
  { key: "viewerColor", shortKey: "surface.viewer.short", longKey: "surface.viewer.long" },
  // chatColor drives the session mirror's (.mirrorview) --chat-bg / --chat-accent; labelled
  // セッション so it isn't confused with the assistant chat. Key kept as chatColor for
  // backward-compat with persisted prefs.
  { key: "chatColor", shortKey: "surface.session.short", longKey: "surface.session.long" },
  // assistantColor is the same surface mechanism for the assistant chat (.chatview).
  { key: "assistantColor", shortKey: "surface.assistant.short", longKey: "surface.assistant.long" },
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

// Derive a region's surface background from a chosen surface color, for the given
// (region-effective) theme: lighten toward white in light, darken toward black in dark.
// null (default color) => no override, so the region falls back to the theme --bg in CSS.
// Used for the file viewer (applyTheme) and, per region theme, the mirror / assistant chat.
export function surfaceBg(id: string, theme: string): string | null {
  const c = surfaceValue(id, theme);
  if (!c) return null;
  return theme === "light" ? mixHex(c, "#ffffff", 0.45) : mixHex(c, "#000000", 0.34);
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

export interface AgentLaunchDefault {
  model: string;
  effort: string;
  startMode: "normal" | "plan";
}

export type AgentLaunchDefaults = Record<string, AgentLaunchDefault>;

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
  // UI 表示言語（docs/28 / ADR 0016）。"ja" | "en"。theme と違い端末ローカルにせずサーバ同期し、
  // 言語は人単位で全端末に追従させる。既定はブラウザ言語判定→日本語フォールバック（detectLocale）。
  locale: string;
  // Per-region base theme, independent of `theme`: "inherit" (default, follow the app),
  // "dark", or "light". Applied by scoping data-theme onto the region container.
  // mirrorTheme → .mirrorview (session mirror); assistantTheme → .chatview (assistant chat).
  mirrorTheme: string;
  assistantTheme: string;
  topbarColor: string;
  leftpaneColor: string;
  viewerColor: string;
  // Surface color for the session mirror (chatColor) and the assistant chat (assistantColor).
  chatColor: string;
  assistantColor: string;
  mirrorSend: string;
  // Default claude model for new sessions (launch dialog + repo 起動). Always a concrete
  // tier alias (opus/sonnet/haiku) — the alias tracks the newest model in that tier, but
  // the tier itself is pinned, so cost/behavior never shift under you between releases.
  defaultModel: string;
  // Per-agent launch defaults. defaultModel remains as a migration mirror for older
  // Console/server prefs; new code reads this map for all three agent kinds.
  agentLaunchDefaults: AgentLaunchDefaults;
  // Global ON/OFF for the auto session-title-suggestion feature (DisplayTab セッション).
  // Default true so existing users get it without an explicit opt-in.
  autoTitleSuggest: boolean;
  // Forced output language for assistant chat: "auto" = follow the input language
  // (default), "ja" / "en" = always reply in that language (even for foreign-language
  // content). The Agent reads this key from ui-prefs and injects a language rule into the
  // chat system prompt (translate assistant is exempt). See docs/19.
  outputLanguage: string;
  // Assistant-chat backend: "auto" = the first connected of claude → codex → opencode
  // (the Agent's preferredHeadlessAgent), or a fixed kind. Applies to builtin
  // assistants' NEW conversations and one-shot calls (title/branch suggestions); the
  // Agent reads this key from ui-prefs live. User-defined assistants keep their own
  // per-assistant agent choice.
  assistantAgent: string;
  // Auto turn on session reports (docs/30): when a session an af_write assistant
  // launched/steered reports back, the assistant runs one turn automatically to
  // process it. Default ON; the backend caps unattended turns at 10 per conversation
  // (reset by a user message) regardless of this switch.
  assistantAutoTurn: boolean;
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
  // ずんだもん（VOICEVOX）の出力音量倍率（0〜1）。ずんだもんは他キャラより素の音圧が高いため、
  // 少し下げて他の声・通知音と揃える。ずんだもんの声（VV_CHAR_NAMES がずんだもんの speaker）で
  // 再生するときだけ、再生時の出力ゲインに掛ける（合成条件ではないのでキャッシュは無効化しない）。
  ttsZundamonVolume: number;
  // Console が背景タブ／最小化（document.hidden）、または別ウィンドウへフォーカスが移った間の
  // 再生方法。mute=無音、quiet=マスター音量を下げる（既定 35%・ttsBackgroundVolume で調整）、
  // normal=通常音量。
  ttsBackgroundPlayback: "mute" | "quiet" | "normal";
  // バックグラウンド再生（ttsBackgroundPlayback="quiet"）時のマスター音量倍率（0〜1）。スライダーで
  // 調整。Console に戻ると通常音量へ滑らかに戻る（ttsControl.ts の ttsMasterGain）。
  ttsBackgroundVolume: number;
  // ペインに属する読み上げを、現在の横方向の列位置に合わせてステレオ配置する。
  // 左右端でも完全には振り切らず、聞きやすさのため最大 ±70% に留める。
  ttsStereoByPane: boolean;
  // バックグラウンドのセッションが回答/質問を返したら音声で知らせる（docs/24 Tier1）。チャットの
  // 自動読み上げ(ttsEnabled)とは別軸。名前前置きの短い告知を直列キューで読む。タブが見えている
  // 間のみ（セッション監視は document.hidden で止まるため）。
  ttsSessionNotify: boolean;
  // サブスク利用制限（5時間 / 週次）の窓が、制限に当たっていた状態からリセットされたら通知する
  // （app/usageResetNotify.ts）。WsBar の使用状況チップが持つ resetsAt を使い、ブラウザ通知＋
  // （音声読み上げ ON 時は）短い音声で「利用を再開できます」と知らせる。制限に当たっていない
  // 通常のリセットでは鳴らさない（スパム防止）。Console のタブが開いている間に確実に検知し、
  // 閉じている間のリセットは次に開いたとき 1 度だけ通知する。
  usageResetNotify: boolean;
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
  // 確定した作業過程（最終回答より前のナレーション）を小声で読む。off=読まない、
  // whisper=ささやき、hushed=ヒソヒソ。対応スタイルが無い話者/Polly は同じ声の音量を下げる。
  ttsWorkRead: string; // "off" | "whisper" | "hushed"
  // 作業過程を小声で読むとき（ttsWorkRead≠off）の出力音量倍率（0〜1）。スライダーで調整。
  // スタイル（ささやき／ヒソヒソ）とは別に、最終回答より小さく聞こえる出力ゲインを与える。
  ttsWorkVolume: number;
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
  // 朗読ビューの本文フォント（READER_FONTS の値。"明朝"=セリフ既定 / "ゴシック"=サンセリフ）。
  // ReaderView が --reader-font（readerFontStack で解決）として本文へ渡す。
  readerFont: string;
  // 朗読ビューの本文文字サイズ（px）。ReaderView が --reader-size として本文へ渡す。
  readerSize: number;
  // キーボードショートカットのユーザー再割当（docs/29 + ADR-0017）。キー＝コマンド id、または
  // アプリ予約キーの合成 id（app.leader / app.palette / app.cheatsheet）。値＝上書きするコード
  // （chords.ts の正規形文字列。""＝明示的に無効化）。未登録キーは既定のまま。直接アクセラレータと
  // 予約キーのみ再割当可（リーダー配下のシーケンス p r 等は構造なので固定）。features/keys/bindings.ts
  // の effectiveCommands / boundChord が解決する。クロスデバイス同期（DEVICE_LOCAL 外）。
  keybindings: Record<string, string>;
  // 端末入力優先（docs/29）。ON のとき、端末（xterm）にフォーカスがある間はアプリのグローバル
  // ショートカットを抑止して端末へ素通しする（Ctrl 系を端末に渡す）。唯一 Leader（既定 Ctrl/⌘+K・
  // 再割当可）だけは残し、そこから which-key／パレットで全コマンドに到達できる。既定 OFF（capture 優先）。
  terminalPriority: boolean;
}

// The pinned fallback model. Used as the seeded global default and as resolveModel's
// terminal fallback, so a new session always launches on a concrete tier — never claude's
// own release-varying default. Change here to move everyone's baseline tier.
export const DEFAULT_MODEL = "sonnet";

const DEFAULT_AGENT_LAUNCH: AgentLaunchDefaults = {
  claude: { model: DEFAULT_MODEL, effort: "", startMode: "normal" },
  codex: { model: "", effort: "", startMode: "normal" },
  agy: { model: "", effort: "", startMode: "normal" },
  opencode: { model: "", effort: "", startMode: "normal" },
};

const DEFAULTS: Settings = {
  termFont: "Source Code Pro",
  termSize: 13,
  viewerFont: "JetBrains Mono",
  viewerSize: 13,
  chatFont: "システム", // i18n-exempt: fontStack 突合用の生フォント値
  chatSize: 14,
  lineNumbers: true,
  wrap: false,
  tabSize: 4,
  minimap: true,
  iconSet: "vscode",
  theme: "dark",
  locale: detectLocale(),
  mirrorTheme: "inherit",
  assistantTheme: "inherit",
  topbarColor: "default",
  leftpaneColor: "default",
  viewerColor: "default",
  chatColor: "default",
  assistantColor: "default",
  // Markdown mirror composer: "mod-enter" = Ctrl/⌘+Enter submits, Enter inserts a
  // newline (phone-friendly default); "enter" = Enter submits, Shift+Enter newline.
  mirrorSend: "mod-enter",
  defaultModel: DEFAULT_MODEL, // concrete tier (avoids claude's release-varying own pick)
  agentLaunchDefaults: DEFAULT_AGENT_LAUNCH,
  autoTitleSuggest: true,
  outputLanguage: "auto",
  assistantAgent: "auto",
  assistantAutoTurn: true,
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
  ttsZundamonVolume: 0.85, // ずんだもんは少し下げて他の声と揃える
  ttsBackgroundPlayback: "quiet",
  ttsBackgroundVolume: 0.35, // 背景時 35%（従来の固定値 HIDDEN_TTS_GAIN と同値）
  ttsStereoByPane: true,
  ttsSessionNotify: false,
  usageResetNotify: true,
  ttsEnglishKana: true,
  ttsUserDict: "",
  ttsCacheSec: 900, // 15分
  ttsAutoReadMirror: true,
  ttsAutoReadAllPanes: true,
  ttsWorkRead: "off",
  ttsWorkVolume: 0.5, // 作業過程の小声（従来の ささやき 0.58 / ヒソヒソ 0.3 の中間）
  ttsVoicePerSession: true,
  // {} = 標準 14 キャラ（tts.ts の SESSION_VOICES）がセッション割り当て対象。新規ユーザーも
  // リセットもこの状態から始まる（キャラは標準 14 人スタートで統一）。エンジンに追加キャラが
  // いても、使いたければキャラクター一覧で個別にチェックする運用。
  ttsVoicePool: {},
  ttsEmotion: false,
  ttsReadPending: true,
  ttsSummaryRead: false,
  ttsAbbrevCode: true,
  ttsParticlePause: true,
  readerVertical: false,
  readerVoice: "",
  readerFont: "明朝", // i18n-exempt: fontStack 突合用の生フォント値
  readerSize: 17,
  keybindings: {},
  terminalPriority: false,
};

// VOICEVOX ずんだもんのスタイル（speaker 番号 → ラベル）。設定 UI の話者選択に使う。
// i18n-exempt-start: VOICEVOX スタイル名は固有名詞として未翻訳（docs/28 §6.4）
export const VOICEVOX_ZUNDAMON: [string, string][] = [
  ["3", "ノーマル"],
  ["1", "あまあま"],
  ["7", "ツンツン"],
  ["5", "セクシー"],
  ["22", "ささやき"],
  ["38", "ヒソヒソ"],
];
// i18n-exempt-end

// TTS プロバイダ（docs/24 Phase 2）。auto の使い分けは CP が決める。ラベルは i18n キー。
export const TTS_PROVIDERS: [string, MsgKey][] = [
  ["auto", "tts.provider_auto"],
  ["voicevox", "tts.provider_voicevox"],
  ["polly", "tts.provider_polly"],
];

// Polly の日本語ニューラル話者（VoiceId → i18n キー）。
export const TTS_POLLY_VOICES: [string, MsgKey][] = [
  ["Takumi", "tts.polly_takumi"],
  ["Kazuha", "tts.polly_kazuha"],
  ["Tomoko", "tts.polly_tomoko"],
];

// 合成キャッシュの上限（合計再生秒数 → i18n キー）。メモリ消費は PCM で約 0.1MB/秒。
export const TTS_CACHE_SIZES: [number, MsgKey][] = [
  [0, "tts.cache_none"],
  [300, "tts.cache_5m"],
  [900, "tts.cache_15m"],
  [1800, "tts.cache_30m"],
];

// 音声読み上げ設定の「初期状態」（設定タブのリセットボタンが書き戻す値）。DEFAULTS の TTS 関連
// キーだけを抜き出したもので、新規ユーザーの初期値と常に一致する（DEFAULTS を単一の真実源に
// することでドリフトを防ぐ）。ttsVoicePool は {}（= tts.ts の標準 14 キャラがチェック済み）で、
// リセットも新規ユーザーもキャラは標準 14 人からのスタートで揃う。読み仮名辞書（ttsUserDict）は
// ユーザーが打ち込んだ内容なのでリセットでは消さない（含めない）。
const TTS_RESET_KEYS = [
  "ttsEnabled",
  "ttsSessionNotify",
  "ttsProvider",
  "ttsVoiceVoicevox",
  "ttsVoicePolly",
  "ttsVoicePerSession",
  "ttsVoicePool",
  "ttsEmotion",
  "ttsSpeed",
  "ttsZundamonVolume",
  "ttsBackgroundPlayback",
  "ttsBackgroundVolume",
  "ttsStereoByPane",
  "ttsAutoReadMirror",
  "ttsAutoReadAllPanes",
  "ttsWorkRead",
  "ttsWorkVolume",
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

// 読み上げ速度（speedScale）。ラベルは i18n キー。
export const TTS_SPEEDS: [number, MsgKey][] = [
  [0.75, "tts.speed_slow"],
  [1.0, "tts.speed_normal"],
  [1.25, "tts.speed_fast"],
  [1.5, "tts.speed_faster"],
];

export const TTS_WORK_READ_MODES: [string, MsgKey][] = [
  ["off", "tts.work_off"],
  ["whisper", "tts.work_whisper"],
  ["hushed", "tts.work_hushed"],
];

export const TTS_BACKGROUND_PLAYBACK_MODES: [string, MsgKey][] = [
  ["mute", "tts.bg_mute"],
  ["quiet", "tts.bg_quiet"],
  ["normal", "tts.bg_normal"],
];

// Assistant-chat output-language choices, shared by the settings UI. "auto" leaves the
// language to the user's input; "ja"/"en" force the reply language.
export const OUTPUT_LANGUAGES: [string, MsgKey][] = [
  ["auto", "out_lang.auto"],
  ["ja", "out_lang.ja"],
  ["en", "out_lang.en"],
];

// Assistant-chat backend choices (AgentsTab). "auto" picks the first CONNECTED of
// claude → codex → opencode; a fixed choice falls back to auto when that CLI isn't
// connected (opencode is always usable — its free tier needs no login).
export const ASSISTANT_AGENTS: [string, MsgKey][] = [
  ["auto", "asst_agent.auto"],
  ["claude", "asst_agent.claude"],
  ["codex", "asst_agent.codex"],
  ["opencode", "asst_agent.opencode"],
];

// Mirror composer submit-key options, shared by the settings UI.
export const MIRROR_SEND_MODES: { id: string; labelKey: MsgKey }[] = [
  { id: "mod-enter", labelKey: "mirror_send.mod_enter" },
  { id: "enter", labelKey: "mirror_send.enter" },
];

// Claude model choices, shared by the launch dialog and the default-model setting. Only
// concrete tiers — the "既定"/"" (defer to claude's release-varying own default) option was
// dropped on purpose so model selection stays deterministic. Each alias still tracks the
// newest model within its tier. Mirrored in Go as claude.Models()
// (workspace/agent/internal/agents/claude/models.go, served by /agents/claude/models
// for the MCP list_models) — keep the two lists in sync.
export const CLAUDE_MODELS: [string, string][] = [
  ["fable", "Fable"],
  ["opus", "Opus"],
  ["sonnet", "Sonnet"],
  ["haiku", "Haiku"],
];

// Build a CSS font-family stack for a chosen family, with CJK + generic fallbacks.
export function fontStack(name: string): string {
  if (!name || name === "システム等幅") { // i18n-exempt: fontStack 突合用の生フォント値
    return 'ui-monospace, SFMono-Regular, Menlo, Consolas, "DejaVu Sans Mono", "Noto Sans Mono CJK JP", monospace';
  }
  return `"${name}", "Noto Sans Mono CJK JP", ui-monospace, Menlo, Consolas, monospace`;
}

// Chat font stack — proportional by default. "システム"/"セリフ" map to sans/serif
// system stacks (with CJK fallbacks); any other name is a code font (monospace).
export function chatFontStack(name: string): string {
  if (!name || name === "システム") { // i18n-exempt: fontStack 突合用の生フォント値
    return 'system-ui, -apple-system, "Hiragino Kaku Gothic ProN", "Noto Sans CJK JP", sans-serif';
  }
  if (name === "セリフ") { // i18n-exempt: fontStack 突合用の生フォント値
    return 'Georgia, "Times New Roman", "Hiragino Mincho ProN", "Noto Serif CJK JP", serif';
  }
  return fontStack(name);
}

// Reader font stack — Japanese prose. "ゴシック" = sans (gothic); anything else
// (default "明朝") = serif (mincho). Both list CJK families first with generic
// fallbacks so they render correctly where the OS lacks Hiragino/Yu.
export function readerFontStack(name: string): string {
  if (name === "ゴシック") { // i18n-exempt: fontStack 突合用の生フォント値
    return '"Hiragino Kaku Gothic ProN", "Yu Gothic", "Noto Sans JP", "Noto Sans CJK JP", system-ui, sans-serif';
  }
  return '"Hiragino Mincho ProN", "Yu Mincho", "Noto Serif JP", "Noto Serif CJK JP", "Noto Serif", serif';
}

function load(): Settings {
  try {
    const saved = JSON.parse(localStorage.getItem(KEY) || "{}");
    // 旧ON/OFF設定を3択へ移行する。明示済みの新設定があればそちらを優先する。
    if (!("ttsBackgroundPlayback" in saved) && typeof saved.ttsQuietWhenHidden === "boolean") {
      saved.ttsBackgroundPlayback = saved.ttsQuietWhenHidden ? "quiet" : "normal";
    }
    delete saved.ttsQuietWhenHidden;
    const legacyClaudeModel = typeof saved.defaultModel === "string" ? saved.defaultModel : DEFAULT_MODEL;
    const rows = saved.agentLaunchDefaults && typeof saved.agentLaunchDefaults === "object"
      ? saved.agentLaunchDefaults
      : {};
    return {
      ...DEFAULTS,
      ...saved,
      agentLaunchDefaults: normalizeAgentLaunchDefaults(rows, legacyClaudeModel),
    };
  } catch {
    return { ...DEFAULTS };
  }
}

function normalizeAgentLaunchDefaults(rows: unknown, legacyClaudeModel = DEFAULT_MODEL): AgentLaunchDefaults {
  const src = rows && typeof rows === "object" ? rows as Record<string, Partial<AgentLaunchDefault>> : {};
  const out: AgentLaunchDefaults = {};
  for (const kind of ["claude", "codex", "agy", "opencode"]) {
    const base = DEFAULT_AGENT_LAUNCH[kind];
    const row = src[kind] || {};
    out[kind] = {
      model: typeof row.model === "string" ? row.model : kind === "claude" ? legacyClaudeModel : base.model,
      effort: typeof row.effort === "string" ? row.effort : base.effort,
      startMode: row.startMode === "plan" ? "plan" : "normal",
    };
  }
  return out;
}

export function agentLaunchDefault(s: Settings, kind: string): AgentLaunchDefault {
  return s.agentLaunchDefaults[kind] || { model: "", effort: "", startMode: "normal" };
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
  setVar("--viewer-bg", surfaceBg(s.viewerColor, theme));
  // Viewer surface accent — the terminal pane shares --viewer-bg, so the ターミナル/チャット
  // toggle rebinds --sel-accent to this. Null (no viewer color) → CSS falls back to topbar.
  setVar("--viewer-accent", surfaceAccent(s.viewerColor));
  // NOTE: the session mirror's --chat-bg/--chat-accent and the assistant chat's surface are
  // NOT set here anymore — each region owns them per its own theme (mirrorTheme/assistantTheme)
  // as inline vars on .mirrorview / .chatview, since those regions can differ from the app
  // theme. See MirrorView.tsx / ChatView.tsx (surfaceBg + surfaceAccent + effectiveTheme).
}

// detectLocale — 保存済み locale が無いときの初期 UI 言語。ブラウザ言語から解決し、未対応/不明は
// 日本語へフォールバック（既存ユーザーの挙動を変えない）。DEFAULTS 評価時に一度だけ走る。
function detectLocale(): string {
  try {
    if ((navigator.language || "").toLowerCase().startsWith("en")) return "en";
  } catch {
    /* navigator 不在 */
  }
  return "ja";
}

// applyLocale — i18n ランタイムへ現ロケールを push し、<html lang> を実行時更新する。applyTheme と
// 同じ 3 箇所（初回描画前・変更時・サーバ hydrate 時）で呼ぶ。
export function applyLocale(s: Settings): void {
  setLocale(s.locale);
  if (typeof document !== "undefined") document.documentElement.lang = s.locale;
}
applyTheme(state);
applyLocale(state);

export function getSettings(): Settings {
  return state;
}

// Debounced mirror of the full settings object to the per-user server store. Best
// effort: if the workspace is stopped / agent unreachable, localStorage still holds it.
let saveTimer: ReturnType<typeof setTimeout> | null = null;
function scheduleServerSave(): void {
  if (saveTimer) clearTimeout(saveTimer);
  saveTimer = setTimeout(() => {
    // 端末ローカルキーはサーバへ送らない（この端末の外へ出さない）。
    apiJSON("api/env/ui-prefs", "PUT", serverPrefs(state)).catch(() => {});
  }, 600);
}

// 端末ローカル設定 — localStorage にだけ持ち、サーバへは送らず・サーバからも復元しない。
// 「この端末で音を鳴らすか」「この画面をどう見せるか」は使う場所（オフィス/自宅、明るさ、
// ヘッドホン有無）で変わる環境依存の設定なので、ユーザ単位で全端末に追従させると邪魔になる。
// 声・速度・辞書・フォント等の“好み”は従来どおりクロスデバイス同期のまま（DEVICE_LOCAL 外）。
// これにより「サーバ優先＋保存デバウンス取りこぼしで OFF が復活」する経路もこれらのキーでは消える。
const DEVICE_LOCAL = new Set<keyof Settings>([
  "ttsEnabled", // 音声読み上げ ON/OFF
  "ttsSessionNotify", // 音声通知 ON/OFF
  "usageResetNotify", // 制限リセット通知 ON/OFF（この端末で鳴らすか）
  "theme", // ダーク/ライト
  "mirrorTheme", // セッションミラーのテーマ（端末ごとの見せ方）
  "assistantTheme", // アシスタントチャットのテーマ（端末ごとの見せ方）
  "topbarColor", // 外観の配色（サーフェス色）
  "leftpaneColor",
  "viewerColor",
  "chatColor",
  "assistantColor",
]);

// serverPrefs は端末ローカルキーを除いた、サーバへ保存してよい設定だけの浅いコピー。
function serverPrefs(s: Settings): Partial<Settings> {
  const out: Partial<Settings> = {};
  for (const k of Object.keys(s) as (keyof Settings)[]) {
    if (!DEVICE_LOCAL.has(k)) (out as any)[k] = s[k];
  }
  return out;
}

// hydrateUIPrefs pulls the server-stored prefs (if any) and merges the known keys
// over the local state, so a fresh browser inherits the user's settings. Called once
// at boot after the tenant is resolved (state.jsx). Server wins over localStorage —
// EXCEPT DEVICE_LOCAL keys, which stay whatever this browser's localStorage holds.
export async function hydrateUIPrefs(): Promise<void> {
  let srv: any;
  try {
    srv = await api("api/env/ui-prefs");
  } catch {
    return;
  }
  if (!srv || typeof srv !== "object" || srv.error) return;
  // サーバーに旧booleanだけが残るユーザーも、新しい3択へ一度だけ移行する。
  if (!("ttsBackgroundPlayback" in srv) && typeof srv.ttsQuietWhenHidden === "boolean") {
    srv.ttsBackgroundPlayback = srv.ttsQuietWhenHidden ? "quiet" : "normal";
  }
  let changed = false;
  const merged: Settings = { ...state };
  for (const k of Object.keys(DEFAULTS)) {
    if (DEVICE_LOCAL.has(k as keyof Settings)) continue; // 端末ローカルは復元しない
    if (k in srv && srv[k] !== (merged as any)[k]) {
      (merged as any)[k] = srv[k];
      changed = true;
    }
  }
  const serverRows = srv.agentLaunchDefaults;
  const legacyClaudeModel = typeof srv.defaultModel === "string" ? srv.defaultModel : merged.defaultModel;
  // A server written by an older Console has only defaultModel. Do not let the
  // already-normalized local map mask that server-side value during migration.
  // Once the new map exists on the server it is authoritative for every agent.
  const rows = serverRows && typeof serverRows === "object"
    ? serverRows
    : {
        ...merged.agentLaunchDefaults,
        claude: { ...merged.agentLaunchDefaults.claude, model: legacyClaudeModel },
      };
  const normalized = normalizeAgentLaunchDefaults(rows, legacyClaudeModel);
  if (JSON.stringify(normalized) !== JSON.stringify(merged.agentLaunchDefaults)) {
    merged.agentLaunchDefaults = normalized;
    changed = true;
  }
  if (!changed) return;
  state = merged;
  try {
    localStorage.setItem(KEY, JSON.stringify(state));
  } catch {}
  applyTheme(state);
  applyLocale(state);
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
  applyLocale(state);
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
