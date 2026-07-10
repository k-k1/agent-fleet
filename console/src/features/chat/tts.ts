// features/chat/tts — エージェント回答の音声読み上げ（docs/24 + ADR0013）。
//
// ストリーミング中の delta を受け取り、句点で「確定した文」だけを切り出して CP の
// /api/tts/synthesize（VOICEVOX/ずんだもん）へ逐次投げる。合成は in-flight を絞り
// （backpressure）、再生は到着順ではなく文の連番順に固定する（AudioContext のチェーン）。
// stop() で in-flight fetch を abort・再生停止・キュー破棄。
//
// Markdown/コードブロック/URL は読み上げ用にプレーン化して除く（plainify）。

import { rel } from "../../core/api/client.ts";
import { getSettings } from "../../lib/settings.ts";
import { useTtsStore } from "../../core/store/tts.ts";
import { plainifyStreaming, firstChunkCut, parseUserDict, applyUserDict } from "./ttsText.ts";

export interface TtsOptions {
  provider: string; // "auto" | "voicevox" | "polly"
  voice: string; // VOICEVOX speaker 番号
  speed: number; // speedScale
  enkana?: boolean; // 英語をカタカナ英語に前処理して読ませる（CP の enkana）
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
  let playCursor = 0; // 次に鳴らす seq
  let playing = false;
  let cur: AudioBufferSourceNode | null = null;
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
    const active = startedAudio && (playing || jobs.length > 0 || inflight > 0 || !flushed);
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
    // startedAudio 後は何もしない（以降は句点粒度）。
    if (!force && !startedAudio) {
      const cut = firstChunkCut(buf);
      if (cut > 0) {
        enqueuePiece(buf.slice(0, cut), /*hard*/ true);
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
    let t = text.trim();
    if (!t) return;
    // ユーザー辞書を適用（enkana は CP 側でこの後。katakana はそのまま通るので競合しない）。
    if (userDict.length) t = applyUserDict(t, userDict).trim();
    if (!t) return;
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
      const res = await fetch(rel("api/tts/synthesize"), {
        method: "POST",
        headers: { "Content-Type": "application/json" },
        body: JSON.stringify({
          text,
          provider: opts.provider,
          voice: opts.voice,
          speed: opts.speed,
          enkana: opts.enkana ?? false,
        }),
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
      notify();
    };
    cur = src;
    src.start();
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
      if (cur) {
        try {
          cur.stop();
        } catch {}
        cur = null;
      }
      playing = false;
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
  const s = getSettings();
  const c = startTts(
    { provider: s.ttsProvider, voice: s.ttsVoiceVoicevox, speed: s.ttsSpeed, enkana: s.ttsEnglishKana },
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
  const s = getSettings();
  const c = startTts({ provider: s.ttsProvider, voice: s.ttsVoiceVoicevox, speed: s.ttsSpeed, enkana: s.ttsEnglishKana }, source);
  c.push(t);
  c.flush();
}
