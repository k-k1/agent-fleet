// features/chat/tts — エージェント回答の音声読み上げ（docs/24 + ADR0013）。
//
// ストリーミング中の delta を受け取り、句点で「確定した文」だけを切り出して CP の
// /api/tts/synthesize（VOICEVOX/ずんだもん）へ逐次投げる。合成は in-flight を絞り
// （backpressure）、再生は到着順ではなく文の連番順に固定する（AudioContext のチェーン）。
// stop() で in-flight fetch を abort・再生停止・キュー破棄。
//
// Markdown/コードブロック/URL は読み上げ用にプレーン化して除く（plainify）。

import { rel } from "../../core/api/client.ts";
import { plainifyStreaming } from "./ttsText.ts";

export interface TtsOptions {
  provider: string; // "auto" | "voicevox" | "polly"
  voice: string; // VOICEVOX speaker 番号
  speed: number; // speedScale
}

// 同時に合成を投げる上限。長文で数十並列にしてエンジン/CP を溢れさせない。
const MAX_INFLIGHT = 2;
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

export function startTts(opts: TtsOptions): TtsController {
  const ctx = audioCtx();
  let buf = ""; // 未確定バッファ（文の途中）
  let pending = ""; // MIN_CHUNK 未満で持ち越し中の短い断片
  let inFence = false; // ```code``` の内側か（読み飛ばす）
  let seq = 0; // 投入した文の連番
  let inflight = 0;
  const jobs: { seq: number; text: string }[] = [];
  const buffers = new Map<number, AudioBuffer | null>(); // seq → 復号済み（null=失敗/スキップ）
  let playCursor = 0; // 次に鳴らす seq
  let playing = false;
  let cur: AudioBufferSourceNode | null = null;
  let stopped = false;
  const acs = new Set<AbortController>();

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
    if (force) {
      // 末尾の未確定分 + 持ち越しをすべて読み上げる。fence 状態は引き回す。
      const spokenTail = plainifyStreaming(buf, { get: () => inFence, set: (v) => (inFence = v) });
      buf = "";
      const combined = (pending + spokenTail).trim();
      pending = "";
      if (combined) submit(combined);
    }
  };

  const enqueuePiece = (piece: string, hard: boolean) => {
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
    submit(combined);
  };

  const submit = (text: string) => {
    const t = text.trim();
    if (!t) return;
    jobs.push({ seq: seq++, text: t });
    pump();
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
        });
    }
  };

  const synth = async (text: string): Promise<AudioBuffer | null> => {
    if (!ctx) return null;
    const ac = new AbortController();
    acs.add(ac);
    try {
      const res = await fetch(rel("api/tts/synthesize"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({ text, provider: opts.provider, voice: opts.voice, speed: opts.speed }),
        signal: ac.signal,
      });
      if (!res.ok) return null;
      const arr = await res.arrayBuffer();
      return await ctx.decodeAudioData(arr);
    } catch {
      return null; // abort / ネットワーク / 復号失敗 → その文はスキップ（キューは止めない）
    } finally {
      acs.delete(ac);
    }
  };

  // 連番順に再生。次の seq がまだ来ていなければ待つ（合成が前後しても順序は保つ）。
  const tryPlay = () => {
    if (stopped || playing || !ctx) return;
    if (!buffers.has(playCursor)) return; // まだ合成中
    const ab = buffers.get(playCursor)!;
    buffers.delete(playCursor);
    playCursor++;
    if (!ab) {
      tryPlay(); // 失敗文はスキップして次へ
      return;
    }
    playing = true;
    const src = ctx.createBufferSource();
    src.buffer = ab;
    src.connect(ctx.destination);
    src.onended = () => {
      playing = false;
      cur = null;
      tryPlay();
    };
    cur = src;
    src.start();
  };

  return {
    push(delta: string) {
      if (stopped) return;
      buf += delta;
      drain(false);
    },
    flush() {
      if (stopped) return;
      drain(true);
    },
    stop() {
      stopped = true;
      jobs.length = 0;
      acs.forEach((a) => a.abort());
      acs.clear();
      if (cur) {
        try {
          cur.stop();
        } catch {}
        cur = null;
      }
      playing = false;
    },
  };
}
