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
import { plainify, plainifyStreaming, firstChunkCut, parseUserDict, applyUserDict } from "./ttsText.ts";

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

// 同時に合成を投げる上限。長文で数十並列にしてエンジン/CP を溢れさせない。
const MAX_INFLIGHT = 2;
// チャンク間に挟む「間」（秒）。素材側の前後無音は CP が短縮している（audio_query の
// pre/postPhonemeLength 上書き）ため、実際の間隔はほぼこの値＋残り無音（~0.07s）になる。
// 句点・改行で確定した文の後は一拍置き、読点などでの文中早出しの後は詰める。
const SENTENCE_GAP = 0.3;
const CLAUSE_GAP = 0.08;
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

// synthToBuffer は 1 文を CP の /api/tts/synthesize で合成し、AudioBuffer へ復号する。
// 失敗（abort / ネットワーク / 非 200 / 復号失敗）は null（呼び手は当該文をスキップ）。
// ストリーム読み上げ（startTts）と朗読（startNarration）の両方から使う共通処理。
async function synthToBuffer(
  ctx: AudioContext,
  text: string,
  opts: TtsOptions,
  signal: AbortSignal,
): Promise<AudioBuffer | null> {
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
    return await ctx.decodeAudioData(arr);
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

// startTts は 1 つの読み上げセッションを開始する。アプリ全体で再生は 1 本に集約するため、
// 既存の再生中セッションがあれば止めてから始め、グローバルストアに自分を active として登録する。
// source は TopBar の「読み上げ中・〇〇」表示に使うラベル。onEnd は自然終了("done")／停止
// ("stopped")のどちらでも 1 回だけ呼ばれる（アナウンスキューの直列制御に使う）。
export function startTts(opts: TtsOptions, source = "", onEnd?: (reason: "done" | "stopped") => void): TtsController {
  useTtsStore.getState().active?.stop(); // 直前の再生を停止（グローバル 1 本）
  // ユーザー読み仮名辞書はターン開始時に一度だけ読む（opts の provider/voice とは独立した
  // テキスト処理なので、全呼び出し元で opts に載せ替えず getSettings から取る）。
  const userDict = parseUserDict(getSettings().ttsUserDict);
  const ctx = audioCtx();
  let buf = ""; // 未確定バッファ（文の途中）
  let pending = ""; // MIN_CHUNK 未満で持ち越し中の短い断片
  let inFence = false; // ```code``` の内側か（読み飛ばす）
  let seq = 0; // 投入した文の連番
  let inflight = 0;
  const jobs: { seq: number; text: string }[] = [];
  const buffers = new Map<number, AudioBuffer | null>(); // seq → 復号済み（null=失敗/スキップ）
  const gaps = new Map<number, number>(); // seq → そのチャンクの後に挟む間（秒）
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
    for (;;) {
      const m = buf.match(SENTENCE_END);
      if (!m) break;
      const end = m.index! + 1;
      const piece = buf.slice(0, end);
      buf = buf.slice(end);
      enqueuePiece(piece, /*hard*/ /\n/.test(m[0]) || /[。！？!?]/.test(m[0]));
    }
    // 最初の発話だけ、句点が来る前に読点/長さで早出しして発話開始を早める。
    // startedAudio 後は何もしない（以降は句点粒度）。文中の切れ目なので後の間は詰める。
    if (!force && !startedAudio) {
      const cut = firstChunkCut(buf);
      if (cut > 0) {
        enqueuePiece(buf.slice(0, cut), /*hard*/ true, /*beat*/ false);
        buf = buf.slice(cut);
      }
    }
    if (force) {
      // 末尾の未確定分 + 持ち越しをすべて読み上げる。fence 状態は引き回す。
      const spokenTail = plainifyStreaming(buf, { get: () => inFence, set: (v) => (inFence = v) });
      buf = "";
      const combined = (pending + spokenTail).trim();
      pending = "";
      if (combined) submit(combined);
    }
  };

  const enqueuePiece = (piece: string, hard: boolean, beat = true) => {
    const spoken = plainifyStreaming(piece, {
      get: () => inFence,
      set: (v) => (inFence = v),
    });
    if (!spoken.trim()) return;
    const combined = pending + spoken;
    if (!hard && combined.trim().length < MIN_CHUNK) {
      pending = combined; // まだ短い → 次とまとめる
      return;
    }
    pending = "";
    submit(combined, beat);
  };

  const submit = (text: string, beat = true) => {
    let t = text.trim();
    if (!t) return;
    // ユーザー辞書を適用（enkana は CP 側でこの後。katakana はそのまま通るので競合しない）。
    if (userDict.length) t = applyUserDict(t, userDict).trim();
    if (!t) return;
    gaps.set(seq, beat ? SENTENCE_GAP : CLAUSE_GAP);
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
      return await synthToBuffer(ctx, text, opts, ac.signal);
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
      const at = Math.max(ctx.currentTime, nextStartAt);
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
      // 自分がまだ active なら speaking を落とす（別セッションに置き換わっている場合は触らない）。
      const st = useTtsStore.getState();
      if (st.active === controller) {
        st.setActive(null, "");
        st.setSpeaking(false);
      }
      finish("stopped");
    },
  };
  useTtsStore.getState().setActive(controller, source);
  return controller;
}

// --- アナウンス直列キュー（docs/24 Tier1: バックグラウンドのセッション通知など） ------------
// 短い告知を「1 本ずつ・割り込まず」読み上げる。何か再生中（チャット読み上げ等）なら終わるのを
// 待ってから。溜まりすぎ（>4 件）は古いものから捨てる（席を外した間の洪水を防ぐ）。
const announceQueue: { text: string; source: string }[] = [];
let announcing = false;

export function announce(text: string, source = ""): void {
  const t = text.trim();
  if (!t) return;
  announceQueue.push({ text: t, source });
  while (announceQueue.length > 4) announceQueue.shift();
  pumpAnnounce();
}

function pumpAnnounce(): void {
  if (announcing) return;
  if (useTtsStore.getState().speaking) return; // 何か再生中 → 終了後に再開（下の subscribe）
  const next = announceQueue.shift();
  if (!next) return;
  announcing = true;
  const c = startTts(
    ttsOptsFromSettings(),
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

// 再生（チャット読み上げ等）が外部で終わったら、待たせていた告知を再開する。
useTtsStore.subscribe((st, prev) => {
  if (prev.speaking && !st.speaking) pumpAnnounce();
});

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
}

export function startNarration(units: string[], source: string, onUnit: (i: number | null) => void): NarrationHandle {
  useTtsStore.getState().active?.stop(); // グローバル 1 本（既存の再生を止める）
  const ctx = audioCtx();
  const s = getSettings();
  const opts = ttsOptsFromSettings(s);
  const userDict = parseUserDict(s.ttsUserDict);

  // 各 unit を読み上げ用にクリーン化（Markdown 記法/URL 除去 + ユーザー辞書）。空になった
  // unit（コード等）は "" のまま残し、原 index を保つ（再生・ハイライトを飛ばす）。
  const texts = units.map((u) => {
    let t = plainify(u).trim();
    if (t && userDict.length) t = applyUserDict(t, userDict).trim();
    return t;
  });

  const buffers = new Map<number, AudioBuffer | null>(); // index → 復号済み（null=空/失敗）
  const acs = new Set<AbortController>();
  let synthAt = 0; // 次に合成を仕掛ける index
  let cursor = 0; // 次に再生する index
  let inflight = 0;
  let playing = false;
  let cur: AudioBufferSourceNode | null = null;
  let paused = false;
  let stopped = false;
  let ended = false;

  const finish = (reason: "done" | "stopped") => {
    if (ended) return;
    ended = true;
    onUnit(null);
    const st = useTtsStore.getState();
    if (st.active === adapter) {
      st.setActive(null, "");
      st.setSpeaking(false);
    }
    void reason;
  };

  const maybeDone = () => {
    if (!stopped && !playing && inflight === 0 && cursor >= texts.length) finish("done");
  };

  // in-flight 上限まで先読み合成。空 unit は即 null。
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
      synthToBuffer(ctx!, text, opts, ac.signal)
        .then((ab) => buffers.set(i, ab))
        .catch(() => buffers.set(i, null))
        .finally(() => {
          acs.delete(ac);
          inflight--;
          pump();
          tryPlay();
        });
    }
  };

  // 連番順に再生。空/失敗 unit は飛ばし、次に再生開始した unit を onUnit で通知。
  const tryPlay = () => {
    if (stopped || paused || playing || !ctx) return;
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
      src.start();
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
  };

  useTtsStore.getState().setActive(adapter, source);
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
  return { pause, resume, stop, isPaused: () => paused };
}
