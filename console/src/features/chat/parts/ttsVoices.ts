import { getSettings } from "../../../lib/settings.ts";
import { getLocale, t } from "../../../lib/i18n/index.ts";
import { emotionOf } from "../ttsText.ts";
import { speakersCatalog, type Speaker, type SpeakerStyle } from "../ttsSpeakers.ts";
import { type TtsOptions } from "./ttsOptions.ts";

// --- セッションごとの声（docs/log/24） ----------------------------------------------
// 複数セッションの並行運用時に「どのセッションの回答か」を声で判別できるようにする。
// セッション名のハッシュで話者プールから決定的に選ぶ（同じセッション名は常に同じ声）。
// プールは「エンジン実カタログ（ttsSpeakers.ts）×ユーザーのキャラクター設定
// （settings.ttsVoicePool）」で決まる（activeVoicePool）。カタログ未取得（エンジン停止中
// 等）は下の静的一覧にフォールバック — これが既定で有効なキャラの定義でもある。感情
// スタイル（あまあま/ツンツン等）を持つキャラは variant も持たせておく（感情読み分けが
// 使う。カタログがあればスタイル名から導出）。Polly は JP 3 声で同様に。
export interface VoiceProfile {
  name: string; // エンジンのキャラ名（settings.ttsVoicePool のキー）
  base: string; // ノーマルの speaker 番号
  happy?: string; // 明るい系スタイル（あまあま等）
  angry?: string; // とがった系スタイル（ツンツン等）
}
const SESSION_VOICES: VoiceProfile[] = [
  { name: "ずんだもん", base: "3", happy: "1", angry: "7" },
  { name: "四国めたん", base: "2", happy: "0", angry: "6" },
  { name: "春日部つむぎ", base: "8" },
  { name: "雨晴はう", base: "10" },
  { name: "波音リツ", base: "9" },
  { name: "冥鳴ひまり", base: "14" },
  { name: "九州そら", base: "16", happy: "15", angry: "18" },
  { name: "もち子さん", base: "20" },
  { name: "玄野武宏", base: "11", happy: "39", angry: "40" }, // 男声
  { name: "白上虎太郎", base: "12", happy: "32", angry: "34" },
  { name: "青山龍星", base: "13" }, // 低い男声
  { name: "WhiteCUL", base: "23", happy: "24" },
  { name: "ナースロボ＿タイプＴ", base: "47", happy: "48" },
  { name: "櫻歌ミコ", base: "43" },
];
const SESSION_POLLY_VOICES = ["Takumi", "Kazuha", "Tomoko"]; // Polly の JP ニューラルは現状この 3 声

// 既定で有効なキャラ（ttsVoicePool に use 未設定のときの既定値）。
const DEFAULT_VOICE_NAMES = new Set(SESSION_VOICES.map((p) => p.name));
export function isDefaultVoice(name: string): boolean {
  return DEFAULT_VOICE_NAMES.has(name);
}

// --- キャラクター設定（ユーザーごとの使用キャラ・基準スタイル・速度, docs/log/24） --------
// エンジンのスタイル名から感情 variant を導出するためのキーワード（部分一致）。
const HAPPY_STYLES = ["あまあま", "わーい", "喜び", "たのしい", "楽々", "元気", "うきうき"];
const ANGRY_STYLES = ["ツンツン", "おこ", "ツンギレ", "不機嫌", "怒り"];

// profileOf はカタログの 1 キャラから既定プロファイルを組む。base はノーマル系スタイル
// （名前が「ノーマル」/「ふつう」。無ければ先頭）、happy/angry はスタイル名から導出。
function profileOf(sp: Speaker): VoiceProfile {
  const byName = (words: string[]) => sp.styles.find((st) => words.some((w) => st.name.includes(w)))?.id;
  const normal = sp.styles.find((st) => st.name === "ノーマル" || st.name === "ふつう")?.id ?? sp.styles[0].id;
  return { name: sp.name, base: normal, happy: byName(HAPPY_STYLES), angry: byName(ANGRY_STYLES) };
}

// TtsTab のキャラリスト 1 行分。styles は基準スタイルの選択肢（カタログ未取得時はノーマルのみ）。
export interface VoiceCharRow {
  name: string;
  styles: SpeakerStyle[];
  profile: VoiceProfile;
}

// voiceCharacters は設定 UI・プール解決の元になるキャラ一覧。エンジン実カタログがあれば
// それを（新キャラ・新スタイルも自動で載る）、無ければ静的フォールバックを返す。
export function voiceCharacters(): VoiceCharRow[] {
  const cat = speakersCatalog();
  if (cat && cat.length) return cat.map((sp) => ({ name: sp.name, styles: sp.styles, profile: profileOf(sp) }));
  return SESSION_VOICES.map((p) => ({ name: p.name, styles: [{ id: p.base, name: "ノーマル" }], profile: p }));
}

// activeVoicePool は「いま使うキャラのプール」= voiceCharacters にユーザーのキャラクター
// 設定（use/style/speed）を適用した結果。セッション声の割り当てと朗読ビューの声一覧の
// 共通の源。voice は基準スタイルの speaker 番号、speed はキャラ別速度（undefined =
// グローバル設定に従う）。
export interface ActiveVoice {
  name: string;
  voice: string;
  speed?: number;
  profile: VoiceProfile;
}
export function activeVoicePool(): ActiveVoice[] {
  const pool = getSettings().ttsVoicePool || {};
  const out: ActiveVoice[] = [];
  for (const c of voiceCharacters()) {
    const conf = pool[c.name];
    if (!(conf?.use ?? DEFAULT_VOICE_NAMES.has(c.name))) continue;
    // 保存済みスタイルがカタログに無い（エンジン更新等）ときはノーマルへ。
    const style = conf?.style && c.styles.some((st) => st.id === conf.style) ? conf.style : c.profile.base;
    out.push({ name: c.name, voice: style, speed: conf?.speed || undefined, profile: c.profile });
  }
  return out;
}


// speaker 番号 → キャラ名（スタイル違いも同じキャラに束ねる）。TopBar の「読み上げ中・
// 〇〇（キャラ名）」表示用。セッション別の声や感情スタイルで「いま誰が喋っているか」を
// 見て確かめられるようにする。
const VV_CHAR_NAMES: Record<string, string> = {
  "3": "ずんだもん", "1": "ずんだもん", "7": "ずんだもん", "5": "ずんだもん", "22": "ずんだもん", "38": "ずんだもん",
  "2": "四国めたん", "0": "四国めたん", "6": "四国めたん", "4": "四国めたん",
  "8": "春日部つむぎ", "10": "雨晴はう", "9": "波音リツ", "14": "冥鳴ひまり",
  "16": "九州そら", "15": "九州そら", "18": "九州そら", "17": "九州そら", "19": "九州そら",
  "20": "もち子さん",
  "11": "玄野武宏", "39": "玄野武宏", "40": "玄野武宏", "41": "玄野武宏",
  "12": "白上虎太郎", "32": "白上虎太郎", "33": "白上虎太郎", "34": "白上虎太郎", "35": "白上虎太郎",
  "13": "青山龍星",
  "23": "WhiteCUL", "24": "WhiteCUL", "25": "WhiteCUL", "26": "WhiteCUL",
  "47": "ナースロボT", "48": "ナースロボT", "49": "ナースロボT", "50": "ナースロボT",
  "43": "櫻歌ミコ", "44": "櫻歌ミコ", "45": "櫻歌ミコ",
};

// voiceCharName は再生に使う声のキャラ名ラベル。明示 polly は VoiceId をそのまま。
// エンジン実カタログがあればそこから引く（新キャラ・新スタイルも正しく出る）。
//
// heard（実際に鳴らしたプロバイダ = heardProvider の値）を渡すと auto のフォールバックまで
// 追う。設定が auto でも CP が Polly に落としていれば Polly の声名を返す — これを見ずに
// 設定だけで名乗ると、VOICEVOX を立てていないデプロイで「Polly が喋っているのに TopBar は
// ずんだもん」になる。空文字（未合成・旧 CP）は従来どおり設定ベースのベストエフォート。
export function voiceCharName(opts: TtsOptions, heard = ""): string {
  // 明示 polly と同じ扱い。既定の VoiceId は CP の pollyVoiceFor と揃える（en=Joanna / 他=Takumi）。
  if (heard === "polly" && opts.provider !== "polly") return opts.pollyVoice || (opts.lang === "en" ? "Joanna" : "Takumi");
  if (opts.provider === "polly") return opts.pollyVoice || "Polly";
  const cat = speakersCatalog();
  if (cat) {
    for (const sp of cat) if (sp.styles.some((st) => st.id === opts.voice)) return sp.name;
  }
  return VV_CHAR_NAMES[opts.voice] || "";
}

// sessionVoiceOpts はセッション名から声の上書き（voice / pollyVoice）を返す。設定 OFF や
// セッション名なしは undefined（= 選択中の話者のまま）。startTts / startNarration の
// opts にスプレッドして使う。
// voicePoolOpts はキー文字列のハッシュで有効キャラのプール（activeVoicePool）から声を
// 決定的に選ぶ（同じキーは常に同じ声）。セッション（sessionVoiceOpts）とアシスタント・
// チャット（assistantVoiceOpts）の共通処理。
function voicePoolOpts(key: string): Partial<TtsOptions> | undefined {
  const pool = activeVoicePool();
  if (!pool.length) return undefined; // 全キャラ OFF → 選択中の話者のまま
  let h = 0;
  for (const c of key) h = (h * 31 + c.codePointAt(0)!) >>> 0;
  // 上位ビットを折り込んでから剰余を取る。素の h % N は下位ビットしか見ず、31 ≡ -1 (mod 8)
  // なので実質「文字コードの交代和」になり、似た形式の名前（共通プレフィックス＋数字等）で
  // 偏る（例: 末尾が 1 と 9、0 と 8 の違いだと必ず同じ声）。折り畳みで実質一様にする。
  h = (h ^ (h >>> 16)) >>> 0;
  const v = pool[h % pool.length];
  const o: Partial<TtsOptions> = {
    voice: v.voice,
    pollyVoice: SESSION_POLLY_VOICES[h % SESSION_POLLY_VOICES.length],
  };
  if (v.speed) o.speed = v.speed; // キャラ別速度（未設定キーを作らない — spread で上書きするため）
  return o;
}

export function sessionVoiceOpts(session: string): Partial<TtsOptions> | undefined {
  if (!session || !getSettings().ttsVoicePerSession) return undefined;
  return voicePoolOpts(session);
}

// assistantVoiceOpts はアシスタント・チャットの声。アシスタントに明示の声（assistant.voice、
// 作成/編集で指定）があれば最優先。無ければ「セッションごとに声を変える」ON のときに
// アシスタント ID のハッシュでプールから割り当て（同じアシスタントは常に同じ声）。
// どちらも無ければ undefined（設定の話者）。
export function assistantVoiceOpts(assistantId?: string, explicit?: string): Partial<TtsOptions> | undefined {
  if (explicit) return voiceChoiceOpts(explicit);
  if (!assistantId || !getSettings().ttsVoicePerSession) return undefined;
  return voicePoolOpts("assistant:" + assistantId);
}

// workVoiceOpts は確定済みの作業過程を小声で読むための上書き。現在の VOICEVOX 話者と
// 同じキャラに対象スタイルがあればそれを使い、無ければ音量だけを下げる。Polly への
// フォールバックでも volume はクライアント再生に効くため、通常声との区別は維持される。
export function workVoiceOpts(
  base?: Partial<TtsOptions>,
  mode = getSettings().ttsWorkRead,
): Partial<TtsOptions> | undefined {
  if (mode === "off") return undefined;
  // ヒソヒソ/ささやきは話者スタイル自体の演技に加えて、出力ゲインも明確に下げる。
  // 下げ幅はユーザーがスライダーで調整する（ttsWorkVolume）。スタイルによって素の音圧が
  // 高い場合でも、最終回答より小さく聞こえる値にする。
  const volume = Math.max(0, Math.min(1, getSettings().ttsWorkVolume));
  const voice = base?.voice || getSettings().ttsVoiceVoicevox;
  const wanted = mode === "hushed" ? ["ヒソヒソ"] : ["ささやき", "囁き"];
  const cat = speakersCatalog();
  if (cat) {
    const speaker = cat.find((sp) => sp.styles.some((st) => st.id === voice));
    const style = speaker?.styles.find((st) => wanted.some((w) => st.name.includes(w)));
    if (style) return { voice: style.id, volume };
  }
  // カタログ取得前でも、同梱設定で番号が確定しているずんだもんはスタイルを使える。
  if (VV_CHAR_NAMES[voice] === "ずんだもん") return { voice: mode === "hushed" ? "38" : "22", volume };
  return { volume };
}

// --- 朗読ビューの声選択（docs/log/24） -----------------------------------------------
// ReaderView ヘッダーの「声」セレクト用。"" = 設定の話者のまま。"vv:<speaker>" は VOICEVOX
// のキャラ（provider は auto に上げる — エンジン不在時は Polly が代読し、復帰したら選んだ
// キャラに戻る）。"polly:<VoiceId>" は明示 Polly。一覧はキャラクター設定で有効にした
// キャラ（activeVoicePool。基準スタイル・キャラ別速度も反映）。
export function readerVoiceChoices(): [string, string][] {
  return [
    ["", t("tts.voice_default")],
    ...activeVoicePool().map((v): [string, string] => ["vv:" + v.voice, v.name]),
    ...SESSION_POLLY_VOICES.map((v): [string, string] => ["polly:" + v, t("tts.voice_polly", { voice: v })]),
  ];
}

// voiceChoiceOpts は readerVoiceChoices の値を TtsOptions の上書きへ解決する（"" や不明値は
// undefined = 設定のまま）。キャラ別速度が設定されていればそれも載せる。
export function voiceChoiceOpts(v: string): Partial<TtsOptions> | undefined {
  if (v.startsWith("vv:")) {
    const id = v.slice(3);
    const o: Partial<TtsOptions> = { provider: "auto", voice: id };
    const pv = activeVoicePool().find((p) => p.voice === id);
    if (pv?.speed) o.speed = pv.speed;
    return o;
  }
  if (v.startsWith("polly:")) return { provider: "polly", pollyVoice: v.slice(6) };
  return undefined;
}

// --- 感情スタイルの読み分け（docs/log/24） -------------------------------------------
// 文にエラー・失敗系の語があればツンツン系、成功・完了系ならあまあま系のスタイルで読む
// （emotionOf の判定。文単位＝合成 1 回単位で切り替え）。感情 variant を持つ話者
// （エンジン実カタログのスタイル名から導出。カタログ未取得時は SESSION_VOICES の
// happy/angry）のときだけ効き、Polly やスタイル無しの話者はそのまま。ノーマル以外を
// 基準スタイルに選んでいる場合も触らない（好みを尊重 — voice がノーマルの speaker 番号に
// 一致するときだけ変える）。
function emotionProfile(voice: string): VoiceProfile | undefined {
  for (const c of voiceCharacters()) {
    if (c.profile.base === voice && (c.profile.happy || c.profile.angry)) return c.profile;
  }
  return undefined;
}

export function emotionOpts(text: string, base: TtsOptions): TtsOptions {
  if (getLocale() !== "ja") return base; // 感情スタイルは日本語の語彙判定なので ja 限定（docs/log/28 §2.4）
  if (!getSettings().ttsEmotion) return base;
  const prof = emotionProfile(base.voice);
  if (!prof) return base;
  const e = emotionOf(text);
  if (e === "happy" && prof.happy) return { ...base, voice: prof.happy };
  if (e === "angry" && prof.angry) return { ...base, voice: prof.angry };
  return base;
}
