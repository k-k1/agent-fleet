import { rel } from "../../../core/api/client.ts";
import { getSettings, subscribe as subscribeSettings } from "../../../lib/settings.ts";
import { useLayoutStore } from "../../../layout/store.ts";
import { makeAudioLru } from "../ttsCache.ts";
import { ttsIsBackground, ttsMasterGain, ttsPanePan } from "../ttsControl.ts";
import { type TtsOptions } from "./ttsOptions.ts";
import { voiceCharName } from "./ttsVoices.ts";

// 同時に合成を投げる上限。長文で数十並列にしてエンジン/CP を溢れさせない。
export const MAX_INFLIGHT = 2;
// チャンク間に挟む「間」（秒）。素材側の前後無音は CP が短縮している（audio_query の
// pre/postPhonemeLength 上書き）ため、実際の間隔はほぼこの値＋残り無音（~0.07s）になる。
// 3 段構え: 改行（段落・行の切れ目）の後は一拍 SENTENCE_GAP、文中の句点（。！？）の後は
// より短い一拍 SENT_BEAT、読点などでの文中早出しの後は詰める CLAUSE_GAP。
export const SENTENCE_GAP = 0.3;
export const CLAUSE_GAP = 0.08;
// 文中の句点（。！？）の後の短い一拍。ストリーミング（startTts）はチャンク後の間として、
// 朗読（startNarration）では同一ブロック/行内の文境界の前拍として呼び手（turnTts /
// readerText 経由）が使う。改行・ブロック頭（SENTENCE_GAP / BLOCK_BEAT = 0.3）より短い。
export const SENT_BEAT = 0.15;
// リスト項目・見出し・引用など「新しいブロックの頭」の前に足す一拍（前拍）。マーカー記号は
// 読まないので、構造の切れ目を間で表す。ストリーミング（startTts）は通常の間に加算、
// 朗読（startNarration）は preGaps として呼び手（turnTts / ReaderView）が渡す。
export const BLOCK_BEAT = 0.3;
// 行頭の溜め（――・……等・startsTame）の前拍。「一拍おいてから話す」演出なので通常の
// ブロック頭（BLOCK_BEAT）より長く、はっきり間が空いたと感じる長さにする（実機報告）。
export const TAME_BEAT = 0.6;
// これ未満の断片は次の文とまとめてから読む（細切れ再生を避ける）。改行/文末では強制フラッシュ。
export const MIN_CHUNK = 6;

// 文末・区切りとみなす文字。ここまでを 1 チャンクとして確定する。
export const SENTENCE_END = /[。．！？!?\n]/;

// 単一チャットターンの読み上げを司るコントローラ。send() 開始時に start し、onDelta で
// push、onDone で flush、stop() で中断する。
// --- 合成キャッシュ ------------------------------------------------------------
// 同一文言＋同一合成条件の復号済み AudioBuffer をメモリ内 LRU で持ち、再読み上げ
// （同じ回答の読み上げボタン再押下、定型 announce、朗読のやり直し等）を合成・
// ネットワークなしで即再生する。AudioBuffer は再生ごとに AudioBufferSourceNode を
// 作り直すので使い回して安全。上限は合計再生秒数（設定 ttsCacheSec、0=無効）で管理する
// （VOICEVOX 24kHz mono float32 の PCM で約 0.1MB/秒）。リロードで消える（永続化しない）。

const synthCache = makeAudioLru<AudioBuffer>(() => getSettings().ttsCacheSec);

// バッファ → それを実際に合成したプロバイダ（レスポンスの X-TTS-Provider）。auto の行き先を
// 決めるのは CP 側（エンジンの到達性・言語・管理トグル）なので、設定だけを見て「ずんだもんで
// 鳴っている」と名乗ると嘘になる — VOICEVOX を立てていないデプロイでは日本語も Polly に落ちる。
// WeakMap にしてあるので LRU から落ちたバッファの分は一緒に回収される。
const bufProvider = new WeakMap<AudioBuffer, string>();

// heardProvider は「そのバッファを実際に鳴らしたプロバイダ」。未知（旧 CP・キャッシュ外）は ""。
export function heardProvider(ab: AudioBuffer | null | undefined): string {
  return (ab && bufProvider.get(ab)) || "";
}

// キーは合成条件＋テキスト。区切りはテキストに現れない NUL。provider は設定値
// （auto 含む）で持つため、auto のルーティング先が変わった直後は旧エンジンの声が
// 再生されうる（エビクトで解消する程度の割り切り）。
function synthCacheKey(text: string, opts: TtsOptions): string {
  return [opts.provider, opts.voice, opts.speed, opts.enkana ? 1 : 0, opts.pollyVoice ?? "", opts.lang ?? "", opts.particlePause ? 1 : 0, text].join(
    "\u0000",
  );
}

// synthToBuffer は 1 文を CP の /api/tts/synthesize で合成し、AudioBuffer へ復号する。
// キャッシュにあれば即返す。失敗（abort / ネットワーク / 非 200 / 復号失敗）は null
// （呼び手は当該文をスキップ）。ストリーム読み上げ（startTts）と朗読（startNarration）
// の両方から使う共通処理。
export async function synthToBuffer(
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
        particlePause: opts.particlePause ?? false,
      }),
      signal,
    });
    if (!res.ok) return null;
    const arr = await res.arrayBuffer();
    const ab = await ctx.decodeAudioData(arr);
    // 実際に鳴らしたプロバイダ。auto の行き先を決めるのは CP（エンジンの到達性・言語・管理
    // トグル）なので、設定だけからは分からない。バッファに紐づけて憶えるのは、キャッシュに
    // 当たった再生でも同じ判定を引き継ぐため（WeakMap なので LRU から落ちれば一緒に消える）。
    const actual = res.headers.get("X-TTS-Provider");
    if (actual) bufProvider.set(ab, actual);
    synthCache.put(key, ab);
    return ab;
  } catch {
    return null;
  }
}

// AudioContext と最終出力の master gain は 1 つを使い回す。各再生の volume（作業過程の
// 小声等）を掛けた後、master で背景時の音量を全再生まとめて滑らかに変更する。
let sharedCtx: AudioContext | null = null;
let masterGain: GainNode | null = null;
let backgroundEventsWired = false;
function masterTarget(): number {
  const hidden = typeof document !== "undefined" && document.hidden;
  const focused = typeof document === "undefined" || document.hasFocus();
  return ttsMasterGain(getSettings().ttsBackgroundPlayback, ttsIsBackground(hidden, focused), getSettings().ttsBackgroundVolume);
}

// voiceLoudness は声ごとの出力音量倍率。ずんだもんは他キャラより素の音圧が高いため、設定
// ttsZundamonVolume で少し下げて他の声・通知音と揃える（実機フィードバック）。Polly や他キャラは 1。
// heard を見るのはラベルと同じ理由: auto が Polly に落ちているのにずんだもん向けの減衰を
// 掛けると、鳴っていない声の設定で音量が下がる。
function voiceLoudness(opts: TtsOptions, heard = ""): number {
  if (voiceCharName(opts, heard) === "ずんだもん") return Math.max(0, Math.min(1, getSettings().ttsZundamonVolume));
  return 1;
}

// outputVolume は連結する再生ゲイン = 呼び手が指定した volume（作業過程の小声等）× 声ごとの
// 音量倍率。3 つの connectOutput 呼び出し（ストリーム/朗読/告知）が共通で使う。
export function outputVolume(opts: TtsOptions, heard = ""): number {
  return (opts.volume ?? 1) * voiceLoudness(opts, heard);
}

function syncMasterGain(immediate = false): void {
  if (!sharedCtx || !masterGain) return;
  const gain = masterGain.gain;
  const now = sharedCtx.currentTime;
  const target = masterTarget();
  gain.cancelScheduledValues(now);
  if (immediate) {
    gain.setValueAtTime(target, now);
    return;
  }
  // 約150msで目標の95%へ到達。瞬時変更によるクリックノイズを避ける。
  gain.setValueAtTime(gain.value, now);
  gain.setTargetAtTime(target, now, 0.05);
}

export function audioCtx(): AudioContext | null {
  try {
    if (!sharedCtx) {
      sharedCtx = new (window.AudioContext || (window as any).webkitAudioContext)();
      masterGain = sharedCtx.createGain();
      masterGain.connect(sharedCtx.destination);
      syncMasterGain(true);
    }
    if (!backgroundEventsWired && typeof document !== "undefined" && typeof window !== "undefined") {
      backgroundEventsWired = true;
      document.addEventListener("visibilitychange", () => syncMasterGain());
      window.addEventListener("blur", () => syncMasterGain());
      window.addEventListener("focus", () => syncMasterGain());
    }
    if (sharedCtx.state === "suspended") void sharedCtx.resume();
    return sharedCtx;
  } catch {
    return null;
  }
}

export function connectOutput(ctx: AudioContext, src: AudioBufferSourceNode, volume = 1, paneId?: string): void {
  const destination: AudioNode = masterGain ?? ctx.destination;
  let output: AudioNode = src;
  if (volume < 0.999) {
    const gain = ctx.createGain();
    gain.gain.value = Math.max(0, Math.min(1, volume));
    output.connect(gain);
    output = gain;
  }
  const s = getSettings();
  const pan = ttsPanePan(s.ttsStereoByPane, useLayoutStore.getState().layout, paneId);
  if (Math.abs(pan) > 0.001 && typeof ctx.createStereoPanner === "function") {
    const panner = ctx.createStereoPanner();
    panner.pan.value = pan;
    output.connect(panner);
    output = panner;
  }
  output.connect(destination);
}

// 設定画面で切り替えた瞬間にも、再生中の音へ反映する。サーバー同期で設定が更新された場合も
// 同じ settings subscription を通るため、次の visibilitychange / focus を待たない。
subscribeSettings(() => syncMasterGain());
