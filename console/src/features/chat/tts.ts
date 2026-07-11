// features/chat/tts — エージェント回答の音声読み上げ（docs/24 + ADR0013）。
//
// ストリーミング中の delta を受け取り、句点で「確定した文」だけを切り出して CP の
// /api/tts/synthesize（VOICEVOX/ずんだもん）へ逐次投げる。合成は in-flight を絞り
// （backpressure）、再生は到着順ではなく文の連番順に固定。準備できたバッファは
// AudioContext の時計で「前の終了時刻 + SENTENCE_GAP」に先行予約し、文間の隙間を
// 設計値に固定する（onended 駆動の start() ではイベントループ分のジッタが毎回入る）。
// stop() で in-flight fetch を abort・再生（予約済み含む）停止・キュー破棄。
//
// Markdown/コードブロック/URL は読み上げ用にプレーン化して除く（plainify）。

import { rel } from "../../core/api/client.ts";
import { getSettings } from "../../lib/settings.ts";
import { useTtsStore } from "../../core/store/tts.ts";
import { plainify, plainifyStreaming, firstChunkCut, applyUserDict, emotionOf, startsBlock } from "./ttsText.ts";
import { effectiveDict } from "./ttsDict.ts";
import { makeAudioLru } from "./ttsCache.ts";
import { speakersCatalog, type Speaker, type SpeakerStyle } from "./ttsSpeakers.ts";

export interface TtsOptions {
  provider: string; // "auto" | "voicevox" | "polly"
  voice: string; // VOICEVOX speaker 番号
  speed: number; // speedScale
  enkana?: boolean; // 英語をカタカナ英語に前処理して読ませる（CP の enkana。voicevox 時のみ効く）
  pollyVoice?: string; // Polly の VoiceId（auto のフォールバック先でも使う）
  lang?: string; // 言語ヒント（設定 outputLanguage を再利用）: "auto" | "ja" | "en"
}

// settings から TtsOptions を組む共通処理（announce / speakText / startNarration / ChatView）。
export function ttsOptsFromSettings(s = getSettings()): TtsOptions {
  return {
    provider: s.ttsProvider,
    voice: s.ttsVoiceVoicevox,
    speed: s.ttsSpeed,
    enkana: s.ttsEnglishKana,
    pollyVoice: s.ttsVoicePolly,
    lang: s.outputLanguage,
  };
}

// --- セッションごとの声（docs/24） ----------------------------------------------
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

// --- キャラクター設定（ユーザーごとの使用キャラ・基準スタイル・速度, docs/24） --------
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

// previewVoice は設定のキャラリストの試聴。短い定型文をその場で読む（グローバル 1 本再生に
// 乗るので TopBar 停止と統合。同一文言×同一条件は合成キャッシュで 2 回目以降は即再生）。
export function previewVoice(name: string, voice: string, speed?: number): void {
  const opts = { ...ttsOptsFromSettings(), provider: "auto", voice };
  if (speed) opts.speed = speed;
  const c = startTts(opts, "試聴・" + name);
  c.push("こんにちは。この声で読み上げます。");
  c.flush();
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
// auto ルーティングで Polly に落ちた場合までは追わない（設定ベースのベストエフォート）。
export function voiceCharName(opts: TtsOptions): string {
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
export function sessionVoiceOpts(session: string): Partial<TtsOptions> | undefined {
  if (!session || !getSettings().ttsVoicePerSession) return undefined;
  const pool = activeVoicePool();
  if (!pool.length) return undefined; // 全キャラ OFF → 選択中の話者のまま
  let h = 0;
  for (const c of session) h = (h * 31 + c.codePointAt(0)!) >>> 0;
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

// --- 朗読ビューの声選択（docs/24） -----------------------------------------------
// ReaderView ヘッダーの「声」セレクト用。"" = 設定の話者のまま。"vv:<speaker>" は VOICEVOX
// のキャラ（provider は auto に上げる — エンジン不在時は Polly が代読し、復帰したら選んだ
// キャラに戻る）。"polly:<VoiceId>" は明示 Polly。一覧はキャラクター設定で有効にした
// キャラ（activeVoicePool。基準スタイル・キャラ別速度も反映）。
export function readerVoiceChoices(): [string, string][] {
  return [
    ["", "設定の話者"],
    ...activeVoicePool().map((v): [string, string] => ["vv:" + v.voice, v.name]),
    ...SESSION_POLLY_VOICES.map((v): [string, string] => ["polly:" + v, "Polly（" + v + "）"]),
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

// --- 感情スタイルの読み分け（docs/24） -------------------------------------------
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

function emotionOpts(text: string, base: TtsOptions): TtsOptions {
  if (!getSettings().ttsEmotion) return base;
  const prof = emotionProfile(base.voice);
  if (!prof) return base;
  const e = emotionOf(text);
  if (e === "happy" && prof.happy) return { ...base, voice: prof.happy };
  if (e === "angry" && prof.angry) return { ...base, voice: prof.angry };
  return base;
}

// 同時に合成を投げる上限。長文で数十並列にしてエンジン/CP を溢れさせない。
const MAX_INFLIGHT = 2;
// チャンク間に挟む「間」（秒）。素材側の前後無音は CP が短縮している（audio_query の
// pre/postPhonemeLength 上書き）ため、実際の間隔はほぼこの値＋残り無音（~0.07s）になる。
// 句点・改行で確定した文の後は一拍置き、読点などでの文中早出しの後は詰める。
const SENTENCE_GAP = 0.3;
const CLAUSE_GAP = 0.08;
// リスト項目・見出し・引用など「新しいブロックの頭」の前に足す一拍（前拍）。マーカー記号は
// 読まないので、構造の切れ目を間で表す。ストリーミング（startTts）は通常の間に加算、
// 朗読（startNarration）は preGaps として呼び手（turnTts / ReaderView）が渡す。
export const BLOCK_BEAT = 0.3;
// これ未満の断片は次の文とまとめてから読む（細切れ再生を避ける）。改行/文末では強制フラッシュ。
const MIN_CHUNK = 6;

// 文末・区切りとみなす文字。ここまでを 1 チャンクとして確定する。
const SENTENCE_END = /[。．！？!?\n]/;

// 単一チャットターンの読み上げを司るコントローラ。send() 開始時に start し、onDelta で
// push、onDone で flush、stop() で中断する。
export interface TtsController {
  push(delta: string): void;
  flush(): void;
  stop(): void;
}

// --- 合成キャッシュ ------------------------------------------------------------
// 同一文言＋同一合成条件の復号済み AudioBuffer をメモリ内 LRU で持ち、再読み上げ
// （同じ回答の読み上げボタン再押下、定型 announce、朗読のやり直し等）を合成・
// ネットワークなしで即再生する。AudioBuffer は再生ごとに AudioBufferSourceNode を
// 作り直すので使い回して安全。上限は合計再生秒数（設定 ttsCacheSec、0=無効）で管理する
// （VOICEVOX 24kHz mono float32 の PCM で約 0.1MB/秒）。リロードで消える（永続化しない）。

const synthCache = makeAudioLru<AudioBuffer>(() => getSettings().ttsCacheSec);

// キーは合成条件＋テキスト。区切りはテキストに現れない NUL。provider は設定値
// （auto 含む）で持つため、auto のルーティング先が変わった直後は旧エンジンの声が
// 再生されうる（エビクトで解消する程度の割り切り）。
function synthCacheKey(text: string, opts: TtsOptions): string {
  return [opts.provider, opts.voice, opts.speed, opts.enkana ? 1 : 0, opts.pollyVoice ?? "", opts.lang ?? "", text].join(
    "\u0000",
  );
}

// synthToBuffer は 1 文を CP の /api/tts/synthesize で合成し、AudioBuffer へ復号する。
// キャッシュにあれば即返す。失敗（abort / ネットワーク / 非 200 / 復号失敗）は null
// （呼び手は当該文をスキップ）。ストリーム読み上げ（startTts）と朗読（startNarration）
// の両方から使う共通処理。
async function synthToBuffer(
  ctx: AudioContext,
  text: string,
  opts: TtsOptions,
  signal: AbortSignal,
): Promise<AudioBuffer | null> {
  const key = synthCacheKey(text, opts);
  const hit = synthCache.get(key);
  if (hit) return hit;
  try {
    const res = await fetch(rel("api/tts/synthesize"), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({
        text,
        provider: opts.provider,
        voice: opts.voice,
        speed: opts.speed,
        enkana: opts.enkana ?? false,
        pollyVoice: opts.pollyVoice ?? "",
        lang: opts.lang ?? "",
      }),
      signal,
    });
    if (!res.ok) return null;
    const arr = await res.arrayBuffer();
    const ab = await ctx.decodeAudioData(arr);
    synthCache.put(key, ab);
    return ab;
  } catch {
    return null;
  }
}

// AudioContext は 1 つを使い回す（ユーザー操作＝送信起点で resume できる）。
let sharedCtx: AudioContext | null = null;
function audioCtx(): AudioContext | null {
  try {
    if (!sharedCtx) sharedCtx = new (window.AudioContext || (window as any).webkitAudioContext)();
    if (sharedCtx.state === "suspended") void sharedCtx.resume();
    return sharedCtx;
  } catch {
    return null;
  }
}

// --- グローバル停止の伝播 --------------------------------------------------------
// stop には 2 種類ある。(1) 明示的な停止（TopBar・ターンフッターの停止ボタン等）＝「静かに
// して」の意思なので、待機中のアナウンスキューと各ミラーペインの自動読み上げキューもまとめて
// 捨てる。(2) 新しい再生開始に伴う置き換え（プリエンプト、グローバル 1 本再生の維持）＝停止
// ではないので何も捨てない。preemptActive() 経由の stop だけを (2) として除外する。
let preempting = false;
function preemptActive(): void {
  const st = useTtsStore.getState();
  if (!st.active) return;
  preempting = true;
  try {
    st.active.stop();
  } finally {
    preempting = false;
  }
}
// onTtsStop は明示停止の購読（ミラーの自動読み上げキュー破棄用）。解除関数を返す。
const stopSubs = new Set<() => void>();
export function onTtsStop(fn: () => void): () => void {
  stopSubs.add(fn);
  return () => {
    stopSubs.delete(fn);
  };
}
function notifyStopped(): void {
  if (preempting) return;
  announceQueue.length = 0;
  stopSubs.forEach((f) => f());
}

// startTts は 1 つの読み上げセッションを開始する。アプリ全体で再生は 1 本に集約するため、
// 既存の再生中セッションがあれば止めてから始め、グローバルストアに自分を active として登録する。
// source は TopBar の「読み上げ中・〇〇」表示に使うラベル。onEnd は自然終了("done")／停止
// ("stopped")のどちらでも 1 回だけ呼ばれる（アナウンスキューの直列制御に使う）。
export function startTts(opts: TtsOptions, source = "", onEnd?: (reason: "done" | "stopped") => void): TtsController {
  preemptActive(); // 直前の再生を停止（グローバル 1 本・キューは温存）
  // 読み仮名辞書（ユーザー＋テナント共通の合成・ユーザー優先）はターン開始時に一度だけ
  // 組む（opts の provider/voice とは独立したテキスト処理なので opts には載せない）。
  const userDict = effectiveDict();
  const codeOpts = { abbrev: getSettings().ttsAbbrevCode, dict: userDict }; // インラインコードの省略読み
  const ctx = audioCtx();
  let buf = ""; // 未確定バッファ（文の途中）
  let pending = ""; // MIN_CHUNK 未満で持ち越し中の短い断片
  let pendingPre = false; // 持ち越し断片がブロック頭（リスト等）で始まっていたか
  let inFence = false; // ```code``` の内側か（読み飛ばす）
  let seq = 0; // 投入した文の連番
  let inflight = 0;
  const jobs: { seq: number; text: string }[] = [];
  const buffers = new Map<number, AudioBuffer | null>(); // seq → 復号済み（null=失敗/スキップ）
  const gaps = new Map<number, number>(); // seq → そのチャンクの後に挟む間（秒）
  const preGaps = new Map<number, number>(); // seq → そのチャンクの前に足す前拍（ブロック頭）
  let playCursor = 0; // 次に鳴らす seq
  const srcs = new Set<AudioBufferSourceNode>(); // 再生中＋先行スケジュール済みのノード
  let nextStartAt = 0; // 次のバッファを開始する AudioContext 時刻
  let stopped = false;
  let startedAudio = false; // 最初の文を submit したら true（＝読み上げ開始）
  let flushed = false; // ストリーム完了（これ以上文は来ない）
  let ended = false; // onEnd を 1 回だけ呼ぶためのガード
  const acs = new Set<AbortController>();

  const finish = (reason: "done" | "stopped") => {
    if (ended) return;
    ended = true;
    // 自分がまだ active なら登録を外す（自然終了でも外す — 残すと「準備中の再生あり」と
    // 区別できず、待機系のポンプ（announce / ミラー自動読み上げ）が永久に待ってしまう）。
    const st = useTtsStore.getState();
    if (st.active === controller) {
      st.setActive(null, "");
      st.setSpeaking(false);
    }
    onEnd?.(reason);
  };

  // 再生中か（合成待ち/再生中/ストリーム継続中）をストアへ通知。文間の一瞬の空きで
  // チラつかないよう、flush 前は startedAudio 以降ずっと true に保つ。flush 後に空になったら
  // 自然終了（done）を通知。
  const notify = () => {
    if (stopped) return;
    const active = startedAudio && (srcs.size > 0 || jobs.length > 0 || inflight > 0 || !flushed);
    useTtsStore.getState().setSpeaking(active);
    if (flushed && !active) finish("done");
  };

  // 確定バッファから完全な文を切り出し、プレーン化してジョブ投入する。
  const drain = (force: boolean) => {
    // 最初の発話だけ、最初の文の頭を読点/長さで短く早出しして再生開始を早める
    // （最初の文を丸ごと合成すると、その合成時間がそのまま開始待ちになる。
    // ストリーミングだけでなく speakText/announce の一括 push も同経路）。
    // 文中の切れ目なので後の間は詰める。startedAudio 後は句点粒度。
    if (!startedAudio) {
      const m = buf.match(SENTENCE_END);
      const head = m ? buf.slice(0, m.index! + 1) : buf;
      const cut = firstChunkCut(head);
      if (cut > 0) {
        enqueuePiece(buf.slice(0, cut), /*hard*/ true, /*beat*/ false);
        buf = buf.slice(cut);
      }
    }
    for (;;) {
      const m = buf.match(SENTENCE_END);
      if (!m) break;
      const end = m.index! + 1;
      const piece = buf.slice(0, end);
      buf = buf.slice(end);
      enqueuePiece(piece, /*hard*/ /\n/.test(m[0]) || /[。！？!?]/.test(m[0]));
    }
    if (force) {
      // 末尾の未確定分 + 持ち越しをすべて読み上げる。fence 状態は引き回す。
      const tailPre = !pending && startsBlock(buf); // 持ち越しが無ければ末尾片の頭で判定
      const spokenTail = plainifyStreaming(buf, { get: () => inFence, set: (v) => (inFence = v) }, codeOpts);
      buf = "";
      const combined = (pending + spokenTail).trim();
      const pre = pending ? pendingPre : tailPre;
      pending = "";
      pendingPre = false;
      if (combined) submit(combined, true, pre);
    }
  };

  const enqueuePiece = (piece: string, hard: boolean, beat = true) => {
    const pre = pending ? pendingPre : startsBlock(piece); // チャンク開始片の頭で判定
    const spoken = plainifyStreaming(
      piece,
      {
        get: () => inFence,
        set: (v) => (inFence = v),
      },
      codeOpts,
    );
    if (!spoken.trim()) return;
    const combined = pending + spoken;
    if (!hard && combined.trim().length < MIN_CHUNK) {
      pending = combined; // まだ短い → 次とまとめる
      pendingPre = pre;
      return;
    }
    pending = "";
    pendingPre = false;
    submit(combined, beat, pre);
  };

  const submit = (text: string, beat = true, pre = false) => {
    let t = text.trim();
    if (!t) return;
    // ユーザー辞書を適用（enkana は CP 側でこの後。katakana はそのまま通るので競合しない）。
    if (userDict.length) t = applyUserDict(t, userDict).trim();
    if (!t) return;
    gaps.set(seq, beat ? SENTENCE_GAP : CLAUSE_GAP);
    if (pre) preGaps.set(seq, BLOCK_BEAT); // リスト・見出し等の頭 → 読む前に一拍
    jobs.push({ seq: seq++, text: t });
    startedAudio = true;
    pump();
    notify();
  };

  // in-flight を上限まで満たしつつ合成を回す。
  const pump = () => {
    while (!stopped && inflight < MAX_INFLIGHT && jobs.length) {
      const job = jobs.shift()!;
      inflight++;
      synth(job.text)
        .then((ab) => buffers.set(job.seq, ab))
        .catch(() => buffers.set(job.seq, null))
        .finally(() => {
          inflight--;
          tryPlay();
          pump();
          notify();
        });
    }
  };

  const synth = async (text: string): Promise<AudioBuffer | null> => {
    if (!ctx) return null;
    const ac = new AbortController();
    acs.add(ac);
    try {
      return await synthToBuffer(ctx, text, emotionOpts(text, opts), ac.signal);
    } finally {
      acs.delete(ac);
    }
  };

  // 連番順に再生。次の seq がまだ来ていなければ待つ（合成が前後しても順序は保つ）。
  // onended を待ってから start() すると毎回イベントループ分の隙間が入るため、準備できた
  // バッファは「前の終了時刻 + SENTENCE_GAP」に AudioContext の時計で先行予約する。
  // 再生が追いついていた（予約時刻が過去）場合は即時開始。
  const tryPlay = () => {
    if (stopped || !ctx) return;
    while (buffers.has(playCursor)) {
      const sq = playCursor;
      const ab = buffers.get(sq)!;
      buffers.delete(sq);
      const gap = gaps.get(sq) ?? SENTENCE_GAP;
      gaps.delete(sq);
      const pre = preGaps.get(sq) ?? 0;
      preGaps.delete(sq);
      playCursor++;
      if (!ab) continue; // 失敗文はスキップして次へ
      const src = ctx.createBufferSource();
      src.buffer = ab;
      src.connect(ctx.destination);
      src.onended = () => {
        srcs.delete(src);
        notify();
      };
      srcs.add(src);
      let at = Math.max(ctx.currentTime, nextStartAt);
      if (sq > 0) at += pre; // ブロック頭の前拍（先頭チャンクは開始を遅らせない）
      src.start(at);
      nextStartAt = at + ab.duration + gap;
    }
  };

  const controller: TtsController = {
    push(delta: string) {
      if (stopped) return;
      buf += delta;
      drain(false);
    },
    flush() {
      if (stopped) return;
      flushed = true;
      drain(true);
      notify();
    },
    stop() {
      if (stopped) return;
      stopped = true;
      jobs.length = 0;
      acs.forEach((a) => a.abort());
      acs.clear();
      srcs.forEach((s) => {
        try {
          s.stop(); // 再生中も予約済み（未開始）もまとめて破棄
        } catch {}
      });
      srcs.clear();
      finish("stopped"); // ストアの後片づけ（active/speaking 解除）は finish が行う
      notifyStopped(); // 明示停止 → 待機中のキューも捨てる（プリエンプト時は no-op）
    },
  };
  useTtsStore.getState().setActive(controller, source, voiceCharName(opts));
  return controller;
}

// --- アナウンス直列キュー（docs/24 Tier1: バックグラウンドのセッション通知など） ------------
// 短い告知を「1 本ずつ・割り込まず」読み上げる。何か再生中（チャット読み上げ等）なら終わるのを
// 待ってから。溜まりすぎ（>4 件）は古いものから捨てる（席を外した間の洪水を防ぐ）。
const announceQueue: { text: string; source: string; voice?: Partial<TtsOptions> }[] = [];
let announcing = false;

// voice はセッションごとの声（sessionVoiceOpts）等の上書き。未指定は設定の話者。
export function announce(text: string, source = "", voice?: Partial<TtsOptions>): void {
  const t = text.trim();
  if (!t) return;
  announceQueue.push({ text: t, source, voice });
  while (announceQueue.length > 4) announceQueue.shift();
  pumpAnnounce();
}

function pumpAnnounce(): void {
  if (announcing) return;
  // 何か再生中/準備中（登録済みで最初の音がまだ）→ 終了後に再開（下の subscribe）。
  // speaking だけ見ると合成待ちの再生に割り込んでしまうので active も見る。
  const st = useTtsStore.getState();
  if (st.speaking || st.active) return;
  const next = announceQueue.shift();
  if (!next) return;
  announcing = true;
  const c = startTts(
    { ...ttsOptsFromSettings(), ...next.voice },
    next.source,
    (reason) => {
      announcing = false;
      if (reason === "stopped") announceQueue.length = 0; // 全体停止でキューも破棄
      else pumpAnnounce();
    },
  );
  c.push(next.text);
  c.flush();
}

// 再生（チャット読み上げ等）が外部で終わったら、待たせていた告知を再開する。zustand の
// subscribe は setState 中に同期で呼ばれ、プリエンプト（旧再生 stop → 新再生の登録）の途中は
// active が一瞬 null になるため、microtask に逃がして置き換え完了後の状態で判定する。
useTtsStore.subscribe((st, prev) => {
  if (prev.speaking && !st.speaking) queueMicrotask(pumpAnnounce);
});

// takeAnnounce は朗読（startNarration）がユニット境界で告知を差し挟むための取り出し口。
// 長い朗読（ファイル・長文ターン）が終わるまでセッション通知を待たせない（docs/24）。
// pumpAnnounce は何か再生中（active あり）は動かないので、再生中の取り出しはここだけ
// ＝二重再生にはならない。
function takeAnnounce(): { text: string; source: string; voice?: Partial<TtsOptions> } | undefined {
  return announceQueue.shift();
}

// speakText は与えたテキストをその場で読み上げる（FileView の選択範囲など、非ストリーム用途）。
// 設定（話者/速度/プロバイダ）は React 外から getSettings() で取得。空文字は無視。
export function speakText(text: string, source = ""): void {
  const t = text.trim();
  if (!t) return;
  const c = startTts(ttsOptsFromSettings(), source);
  c.push(t);
  c.flush();
}

// --- 朗読モード（docs/24）: ファイル本文を冒頭から順次読み上げ＋カラオケ追従 --------------
// units（各ブロックのプレーンテキスト）を上から順に合成・再生し、再生を開始した unit の index を
// onUnit で通知する（呼び手＝FileView がその要素をハイライト＋スクロールする）。startTts と同じ
// 合成・順次再生・グローバル 1 本再生の仕組みを流用しつつ、一時停止/再開（AudioContext の
// suspend/resume）と unit 単位の進捗通知を持つ点が異なる。

export interface NarrationHandle {
  pause(): void;
  resume(): void;
  stop(): void;
  isPaused(): boolean;
  // 声の即時切替（朗読ビューのセレクト）。いま鳴っている文はそのまま、次の文から新しい声。
  setVoice(voice?: Partial<TtsOptions>): void;
}

export function startNarration(
  units: string[],
  source: string,
  // onUnit(i) = i 番目の unit の再生を開始。onUnit(null, reason) = 終了（done=自然終了 /
  // stopped=停止・他の再生への置き換え。ミラーの自動読み上げキューが継続可否の判断に使う）。
  onUnit: (i: number | null, endReason?: "done" | "stopped") => void,
  // 声の上書き（セッションごとの声 sessionVoiceOpts 等）。未指定は設定の話者。
  voice?: Partial<TtsOptions>,
  // unit ごとの前拍（秒）。リスト項目・段落頭など「新しいブロックの最初の文」に BLOCK_BEAT を
  // 渡すと、読む前に一拍おく（先頭 unit の前拍は開始遅延になるだけなので無視する）。
  preGaps?: number[],
): NarrationHandle {
  preemptActive(); // グローバル 1 本（既存の再生を止める・キューは温存）
  const ctx = audioCtx();
  const s = getSettings();
  let opts = { ...ttsOptsFromSettings(s), ...voice }; // let: setVoice で差し替わる
  const userDict = effectiveDict(); // ユーザー＋テナント共通辞書（ユーザー優先）

  // 各 unit を読み上げ用にクリーン化（Markdown 記法/URL 除去 + コード片の省略読み +
  // ユーザー辞書）。空になった unit（コード等）は "" のまま残し、原 index を保つ
  // （再生・ハイライトを飛ばす）。
  const codeOpts = { abbrev: s.ttsAbbrevCode, dict: userDict };
  const texts = units.map((u) => {
    let t = plainify(u, codeOpts).trim();
    if (t && userDict.length) t = applyUserDict(t, userDict).trim();
    return t;
  });

  const buffers = new Map<number, AudioBuffer | null>(); // index → 復号済み（null=空/失敗）
  const acs = new Set<AbortController>();
  let synthAt = 0; // 次に合成を仕掛ける index
  let cursor = 0; // 次に再生する index
  let epoch = 0; // 声の世代（setVoice で進む。古い声の合成結果を無効化する）
  let inflight = 0;
  let playing = false;
  let cur: AudioBufferSourceNode | null = null;
  let paused = false;
  let stopped = false;
  let ended = false;

  const finish = (reason: "done" | "stopped") => {
    if (ended) return;
    ended = true;
    onUnit(null, reason);
    const st = useTtsStore.getState();
    if (st.active === adapter) {
      st.setActive(null, "");
      st.setSpeaking(false);
    }
  };

  const maybeDone = () => {
    if (!stopped && !playing && inflight === 0 && cursor >= texts.length) finish("done");
  };

  // in-flight 上限まで先読み合成。空 unit は即 null。結果はエポック一致時だけ採用
  // （setVoice 後に届いた古い声の合成を鳴らさない）。
  const pump = () => {
    while (!stopped && inflight < MAX_INFLIGHT && synthAt < texts.length) {
      const i = synthAt++;
      const text = texts[i];
      if (!text) {
        buffers.set(i, null);
        continue;
      }
      inflight++;
      const ac = new AbortController();
      acs.add(ac);
      const ep = epoch;
      synthToBuffer(ctx!, text, emotionOpts(text, opts), ac.signal)
        .then((ab) => {
          if (ep === epoch) buffers.set(i, ab);
        })
        .catch(() => {
          if (ep === epoch) buffers.set(i, null);
        })
        .finally(() => {
          acs.delete(ac);
          inflight--;
          pump();
          tryPlay();
        });
    }
  };

  // 声の即時切替（NarrationHandle.setVoice）。いま鳴っている文は触らず、次の文から新しい
  // 声にする: 未再生の先読み分（buffers は再生済みを消しながら進むので残りは全部 cursor
  // 以降）を捨て、次に鳴る文（cursor）から合成をやり直す。in-flight は abort しつつ、
  // 応答順の競合はエポックで確実に無効化する。
  const setVoice = (voice2?: Partial<TtsOptions>) => {
    if (stopped) return;
    opts = { ...ttsOptsFromSettings(getSettings()), ...voice2 };
    epoch++;
    acs.forEach((a) => a.abort());
    acs.clear();
    buffers.clear();
    synthAt = cursor;
    const st = useTtsStore.getState();
    if (st.active === adapter) st.setActive(adapter, source, voiceCharName(opts)); // TopBar の声表示を更新
    pump();
    tryPlay(); // 再生が追いついて待っていた場合に備える
  };

  // 告知の差し挟み（docs/24）: 長い朗読の途中でもセッション通知・確認の告知を待たせすぎない。
  // ユニット境界で announce キューから 1 件取り出し、次のユニットの前にその場で読む。再生中は
  // TopBar のラベル/声を告知側に差し替え、終わったら朗読のものへ戻す。停止・一時停止は朗読と
  // 一体（同じ ctx・同じ adapter）。
  const playInterlude = (a: { text: string; source: string; voice?: Partial<TtsOptions> }) => {
    let t = plainify(a.text, codeOpts).trim();
    if (t && userDict.length) t = applyUserDict(t, userDict).trim();
    if (!t) {
      tryPlay();
      return;
    }
    const aopts = { ...ttsOptsFromSettings(getSettings()), ...a.voice };
    const label = (on: boolean) => {
      const st = useTtsStore.getState();
      if (st.active !== adapter) return;
      if (on) st.setActive(adapter, a.source, voiceCharName(aopts));
      else st.setActive(adapter, source, voiceCharName(opts));
    };
    playing = true; // 合成待ちの間も次ユニットの再生開始と maybeDone を抑える
    const ac = new AbortController();
    acs.add(ac);
    void synthToBuffer(ctx!, t, emotionOpts(t, aopts), ac.signal).then((ab) => {
      acs.delete(ac);
      playing = false;
      if (stopped) return;
      if (!ab) {
        tryPlay();
        return;
      }
      playing = true;
      label(true);
      const src = ctx!.createBufferSource();
      src.buffer = ab;
      src.connect(ctx!.destination);
      src.onended = () => {
        playing = false;
        cur = null;
        label(false);
        if (stopped) return;
        tryPlay();
        maybeDone();
      };
      cur = src;
      src.start(ctx!.currentTime + 0.15); // 朗読との切れ目に小さな間
      useTtsStore.getState().setSpeaking(true);
    });
  };

  // 連番順に再生。空/失敗 unit は飛ばし、次に再生開始した unit を onUnit で通知。
  const tryPlay = () => {
    if (stopped || paused || playing || !ctx) return;
    if (cursor < texts.length) {
      const a = takeAnnounce(); // 朗読の続きがあるときだけ差し挟む（最後は通常の直列へ）
      if (a) {
        playInterlude(a);
        return;
      }
    }
    while (cursor < texts.length && buffers.has(cursor)) {
      const ab = buffers.get(cursor)!;
      buffers.delete(cursor);
      const idx = cursor;
      cursor++;
      if (!ab) continue; // 空/失敗 → ハイライトせず次へ
      playing = true;
      onUnit(idx);
      useTtsStore.getState().setSpeaking(true);
      const src = ctx.createBufferSource();
      src.buffer = ab;
      src.connect(ctx.destination);
      src.onended = () => {
        playing = false;
        cur = null;
        if (stopped) return;
        tryPlay();
        maybeDone();
      };
      cur = src;
      // ブロック頭の前拍。ハイライト（onUnit）は先に出るが、間が「次はここ」の予告に
      // なるのでカラオケとしてはむしろ自然。
      const pre = idx > 0 ? (preGaps?.[idx] ?? 0) : 0;
      src.start(ctx.currentTime + pre);
      return;
    }
    maybeDone();
  };

  const adapter: TtsController = { push() {}, flush() {}, stop: () => stop() };

  const pause = () => {
    if (stopped || paused) return;
    paused = true;
    if (ctx && ctx.state === "running") void ctx.suspend(); // 現在の音を含めて停止
  };
  const resume = () => {
    if (stopped || !paused) return;
    paused = false;
    if (ctx) void ctx.resume();
    tryPlay(); // 一時停止が「文の切れ目」だった場合に備え、再生を促す
  };
  const stop = () => {
    if (stopped) return;
    stopped = true;
    acs.forEach((a) => a.abort());
    acs.clear();
    if (cur) {
      try {
        cur.stop();
      } catch {}
      cur = null;
    }
    playing = false;
    if (ctx && ctx.state === "suspended") void ctx.resume(); // 次の再生のため戻しておく
    finish("stopped");
    notifyStopped(); // 明示停止 → 待機中のキューも捨てる（プリエンプト時は no-op）
  };

  useTtsStore.getState().setActive(adapter, source, voiceCharName(opts));
  // 初回キックは microtask に回す。呼び手（FileView）が返り値の handle と自分の state を
  // 確定してから onUnit が走るようにするため（空/エンジン無しで finish が同期発火すると、
  // beginNarration のセットアップ前に onUnit(null) が来て状態が不整合になるのを防ぐ）。
  queueMicrotask(() => {
    if (stopped) return;
    if (!ctx || texts.every((t) => !t)) {
      finish("done"); // エンジン無し（AudioContext 不可）or 読む中身が無い
      return;
    }
    pump();
    tryPlay();
  });
  return { pause, resume, stop, isPaused: () => paused, setVoice };
}
