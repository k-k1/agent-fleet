import { getSettings } from "../../../lib/settings.ts";
import { t } from "../../../lib/i18n/index.ts";
import { useTtsStore } from "../../../core/store/tts.ts";
import { plainifyStreaming, firstChunkCut, splitLongSentence, startsBlock, startsTame } from "../ttsText.ts";
import { effectiveDict } from "../ttsDict.ts";
import { type TtsController, type TtsEndReason } from "../ttsControl.ts";
import { type TtsOptions, ttsOptsFromSettings, localizedReadings } from "./ttsOptions.ts";
import { emotionOpts, voiceCharName } from "./ttsVoices.ts";
import { BLOCK_BEAT, CLAUSE_GAP, MAX_INFLIGHT, MIN_CHUNK, SENTENCE_END, SENTENCE_GAP, SENT_BEAT, TAME_BEAT, audioCtx, connectOutput, heardProvider, outputVolume, synthToBuffer } from "./ttsAudio.ts";


// --- グローバル停止の伝播 --------------------------------------------------------
// stop には 2 種類ある。(1) 明示的な停止（TopBar・ターンフッターの停止ボタン等）＝「静かに
// して」の意思なので、待機中のアナウンスキューと各ミラーペインの自動読み上げキューもまとめて
// 捨てる。(2) 新しい再生開始に伴う置き換え（プリエンプト、グローバル 1 本再生の維持）＝停止
// ではないので何も捨てない。stop(reason) で理由を明示し、非同期・再入可能な再生処理でも
// 一時的なグローバルフラグに依存しない。
export function preemptActive(): void {
  const st = useTtsStore.getState();
  if (!st.active) return;
  st.active.stop("replaced");
}
// onTtsStop は明示停止の購読（ミラーの自動読み上げキュー破棄用）。解除関数を返す。
const stopSubs = new Set<() => void>();
export function onTtsStop(fn: () => void): () => void {
  stopSubs.add(fn);
  return () => {
    stopSubs.delete(fn);
  };
}
export function notifyStopped(): void {
  announceQueue.length = 0;
  stopSubs.forEach((f) => f());
}

// startTts は 1 つの読み上げセッションを開始する。アプリ全体で再生は 1 本に集約するため、
// 既存の再生中セッションがあれば止めてから始め、グローバルストアに自分を active として登録する。
// source は TopBar の「読み上げ中・〇〇」表示に使うラベル。onEnd は自然終了("done")／停止
// ("explicit" / "replaced")のいずれでも 1 回だけ呼ばれる（アナウンスキューの直列制御に使う）。
export function startTts(
  opts: TtsOptions,
  source = "",
  onEnd?: (reason: TtsEndReason) => void,
  sessionName = "", // 発生元セッション名（左ペインの再生中アイコン用。非セッションは ""）
  // onPiece(spoken): その文が実際に鳴り始める瞬間に、読み補正前の表示テキストを通知する
  // （ライブ配信カラオケ用・docs/log/19）。未指定なら一切コストは掛からない。
  onPiece?: (spoken: string) => void,
  purpose: "reading" | "session-notification" | "usage-notification" | "manual" = "reading",
): TtsController {
  preemptActive(); // 直前の再生を停止（グローバル 1 本・キューは温存）
  // 読み仮名辞書（ユーザー＋テナント共通の合成・ユーザー優先）はターン開始時に一度だけ
  // 組む（opts の provider/voice とは独立したテキスト処理なので opts には載せない）。
  const userDict = effectiveDict();
  const codeOpts = { abbrev: getSettings().ttsAbbrevCode, dict: userDict }; // インラインコードの省略読み
  const ctx = audioCtx();
  let buf = ""; // 未確定バッファ（文の途中）
  let pending = ""; // MIN_CHUNK 未満で持ち越し中の短い断片
  let pendingPre = 0; // 持ち越し断片の前拍（秒。ブロック頭=BLOCK_BEAT / 溜め=TAME_BEAT / 無し=0）
  let inFence = false; // ```code``` の内側か（読み飛ばす）
  let seq = 0; // 投入した文の連番
  let inflight = 0;
  const jobs: { seq: number; text: string }[] = [];
  const buffers = new Map<number, AudioBuffer | null>(); // seq → 復号済み（null=失敗/スキップ）
  const gaps = new Map<number, number>(); // seq → そのチャンクの後に挟む間（秒）
  const preGaps = new Map<number, number>(); // seq → そのチャンクの前に足す前拍（ブロック頭）
  const displays = new Map<number, string>(); // seq(文頭片) → 読み補正前の表示テキスト（onPiece 用）
  const pieceTimers = new Set<number>(); // onPiece の発火予約（stop で解除）
  let playCursor = 0; // 次に鳴らす seq
  // 実際に合成したプロバイダ（X-TTS-Provider）。設定が auto のとき、行き先を決めるのは CP なので
  // 最初の 1 文が返るまでは分からない。分かった時点で TopBar の声表示を名乗り直す。
  let heard = "";
  const noteHeard = (ab: AudioBuffer) => {
    const h = heardProvider(ab);
    if (!h || h === heard) return;
    heard = h;
    const st = useTtsStore.getState();
    if (st.active === controller) st.setActive(controller, source, voiceCharName(opts, heard), sessionName, purpose);
  };
  const srcs = new Set<AudioBufferSourceNode>(); // 再生中＋先行スケジュール済みのノード
  let nextStartAt = 0; // 次のバッファを開始する AudioContext 時刻
  let stopped = false;
  let startedAudio = false; // 最初の文を submit したら true（＝読み上げ開始）
  let flushed = false; // ストリーム完了（これ以上文は来ない）
  let ended = false; // onEnd を 1 回だけ呼ぶためのガード
  const acs = new Set<AbortController>();

  const finish = (reason: TtsEndReason) => {
    if (ended) return;
    ended = true;
    // 自分がまだ active なら登録を外す（自然終了でも外す — 残すと「準備中の再生あり」と
    // 区別できず、待機系のポンプ（announce / ミラー自動読み上げ）が永久に待ってしまう）。
    const st = useTtsStore.getState();
    if (st.active === controller) {
      st.setActive(null, "");
      st.setSpeaking(false);
      st.setPreparing(false);
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
        enqueuePiece(buf.slice(0, cut), /*hard*/ true, CLAUSE_GAP);
        buf = buf.slice(cut);
      }
    }
    for (;;) {
      const m = buf.match(SENTENCE_END);
      if (!m) break;
      const end = m.index! + 1;
      const piece = buf.slice(0, end);
      buf = buf.slice(end);
      const nl = /\n/.test(m[0]);
      // 改行で確定 → 一拍（SENTENCE_GAP）、文中の句点 → 短い一拍（SENT_BEAT）。
      enqueuePiece(piece, /*hard*/ nl || /[。．！？!?]/.test(m[0]), nl ? SENTENCE_GAP : SENT_BEAT);
    }
    if (force) {
      // 末尾の未確定分 + 持ち越しをすべて読み上げる。fence 状態は引き回す。
      const tailPre = !pending ? preBeatOf(buf) : 0; // 持ち越しが無ければ末尾片の頭で判定
      const spokenTail = plainifyStreaming(buf, { get: () => inFence, set: (v) => (inFence = v) }, codeOpts);
      buf = "";
      const combined = (pending + spokenTail).trim();
      const pre = pending ? pendingPre : tailPre;
      pending = "";
      pendingPre = 0;
      if (combined) submit(combined, SENTENCE_GAP, pre);
    }
  };

  // チャンク開始片の頭で判定する前拍（秒）。ブロック頭（リスト・見出し・引用）は BLOCK_BEAT、
  // 溜め（――・……等）は TAME_BEAT、どちらでもなければ 0（前拍無し）。
  const preBeatOf = (s: string): number => (startsBlock(s) ? BLOCK_BEAT : startsTame(s) ? TAME_BEAT : 0);

  const enqueuePiece = (piece: string, hard: boolean, gap = SENTENCE_GAP) => {
    const pre = pending ? pendingPre : preBeatOf(piece);
    const spoken = plainifyStreaming(
      piece,
      {
        get: () => inFence,
        set: (v) => (inFence = v),
      },
      codeOpts,
    );
    if (!spoken.trim()) {
      // 読み上げの無い改行だけの断片（段落の切れ目）: 直前チャンクの後の間を行間へ格上げする
      // （「…た。\n」は句点で先に SENT_BEAT が付いているため、段落末だけここで一拍になる）。
      // 直前チャンクが既に再生スケジュール済み（gaps 消費済み）なら間に合わないので触らない。
      if (/\n/.test(piece) && gaps.has(seq - 1)) gaps.set(seq - 1, Math.max(gaps.get(seq - 1)!, SENTENCE_GAP));
      return;
    }
    const combined = pending + spoken;
    if (!hard && combined.trim().length < MIN_CHUNK) {
      pending = combined; // まだ短い → 次とまとめる
      pendingPre = pre;
      return;
    }
    pending = "";
    pendingPre = 0;
    submit(combined, gap, pre);
  };

  const submit = (text: string, gap = SENTENCE_GAP, pre = 0) => {
    let t = text.trim();
    if (!t) return;
    const display = t; // カラオケ用の表示テキスト（読み補正・分割の前・onPiece で通知）
    // 読みの整形: ユーザー/テナント辞書 → 組み込み読み補正 → 助詞の小休止（enkana は CP 側で後段）。
    t = localizedReadings(t, userDict, getSettings().ttsParticlePause);
    if (!t) return;
    // 長い 1 文は合成用に分割（1 回の合成が重いと先読みが息切れして無音になる）。途中の片は
    // 読点相当に詰め、本来の間は最後の片の後だけ。前拍（ブロック頭）は先頭の片だけ。
    const pieces = splitLongSentence(t);
    for (let i = 0; i < pieces.length; i++) {
      gaps.set(seq, i === pieces.length - 1 ? gap : CLAUSE_GAP);
      if (pre && i === 0) preGaps.set(seq, pre); // リスト・見出し等の頭/溜め → 読む前に一拍
      if (onPiece && i === 0) displays.set(seq, display); // 文頭片が鳴る瞬間にこの文を通知
      jobs.push({ seq: seq++, text: pieces[i] });
    }
    if (!startedAudio) useTtsStore.getState().setPreparing(true); // 最初の音まで「生成中」
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
      noteHeard(ab); // 実際の合成先が分かったので TopBar の声表示を直す（auto のフォールバック）
      const src = ctx.createBufferSource();
      src.buffer = ab;
      connectOutput(ctx, src, outputVolume(opts, heard), opts.paneId);
      src.onended = () => {
        srcs.delete(src);
        notify();
      };
      srcs.add(src);
      let at = Math.max(ctx.currentTime, nextStartAt);
      if (sq > 0) at += pre; // ブロック頭の前拍（先頭チャンクは開始を遅らせない）
      src.start(at);
      // ライブ配信カラオケ: この文が実際に鳴り始める時刻（先行予約ぶん先）に onPiece を発火。
      // バッファは先読みでまとめて予約されるため、start 時ではなく再生開始時刻に合わせる。
      if (onPiece) {
        const d = displays.get(sq);
        displays.delete(sq);
        if (d) {
          const delayMs = Math.max(0, (at - ctx.currentTime) * 1000);
          const tm = window.setTimeout(() => {
            pieceTimers.delete(tm);
            if (!stopped) onPiece(d);
          }, delayMs);
          pieceTimers.add(tm);
        }
      }
      useTtsStore.getState().setPreparing(false); // 最初の音がスケジュールされた → 生成中を解除
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
    stop(reason = "explicit") {
      // Natural completion leaves callers holding a harmless stale controller. A later
      // lifecycle cleanup must not turn that old handle into a new global stop event.
      if (stopped || ended) return;
      stopped = true;
      jobs.length = 0;
      pieceTimers.forEach((t) => clearTimeout(t)); // 予約済みの onPiece 発火を取り消す
      pieceTimers.clear();
      acs.forEach((a) => a.abort());
      acs.clear();
      srcs.forEach((s) => {
        try {
          s.stop(); // 再生中も予約済み（未開始）もまとめて破棄
        } catch {}
      });
      srcs.clear();
      finish(reason); // ストアの後片づけ（active/speaking 解除）は finish が行う
      if (reason === "explicit") notifyStopped(); // ユーザー停止だけ待機キューも捨てる
    },
  };
  useTtsStore.getState().setActive(controller, source, voiceCharName(opts), sessionName, purpose);
  return controller;
}

// --- アナウンス直列キュー（docs/log/24 Tier1: バックグラウンドのセッション通知など） ------------
// 短い告知を「1 本ずつ・割り込まず」読み上げる。何か再生中（チャット読み上げ等）なら終わるのを
// 待ってから。溜まりすぎ（>4 件）は古いものから捨てる（席を外した間の洪水を防ぐ）。
const announceQueue: { text: string; source: string; voice?: Partial<TtsOptions>; sessionName?: string; purpose?: "reading" | "session-notification" | "usage-notification" | "manual" }[] = [];
let announcing = false;

// voice はセッションごとの声（sessionVoiceOpts）等の上書き。未指定は設定の話者。
// sessionName は発生元セッション名（左ペインの再生中アイコン用。非セッション告知は省略）。
export function announce(text: string, source = "", voice?: Partial<TtsOptions>, sessionName = "", purpose: "reading" | "session-notification" | "usage-notification" | "manual" = "reading"): void {
  const t = text.trim();
  if (!t) return;
  announceQueue.push({ text: t, source, voice, sessionName, purpose });
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
      if (reason === "explicit") announceQueue.length = 0; // 全体停止でキューも破棄
      else pumpAnnounce();
    },
    next.sessionName ?? "",
    undefined,
    next.purpose ?? "reading",
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
// 長い朗読（ファイル・長文ターン）が終わるまでセッション通知を待たせない（docs/log/24）。
// pumpAnnounce は何か再生中（active あり）は動かないので、再生中の取り出しはここだけ
// ＝二重再生にはならない。
export function takeAnnounce():
  | { text: string; source: string; voice?: Partial<TtsOptions>; sessionName?: string; purpose?: "reading" | "session-notification" | "usage-notification" | "manual" }
  | undefined {
  return announceQueue.shift();
}

// speakText は与えたテキストをその場で読み上げる（FileView の選択範囲など、非ストリーム用途）。
// 設定（話者/速度/プロバイダ）は React 外から getSettings() で取得。voice は声の上書き
// （アシスタントの声 assistantVoiceOpts 等）。空文字は無視。
export function speakText(text: string, source = "", voice?: Partial<TtsOptions>): void {
  const t = text.trim();
  if (!t) return;
  const c = startTts({ ...ttsOptsFromSettings(), ...voice }, source, undefined, "", undefined, "manual");
  c.push(t);
  c.flush();
}

// previewVoice は設定のキャラリストの試聴。短い定型文をその場で読む（グローバル 1 本再生に
// 乗るので TopBar 停止と統合。同一文言×同一条件は合成キャッシュで 2 回目以降は即再生）。
export function previewVoice(name: string, voice: string, speed?: number): void {
  const opts = { ...ttsOptsFromSettings(), provider: "auto", voice };
  if (speed) opts.speed = speed;
  const c = startTts(opts, t("tts.preview_label", { name }));
  // 試聴文はキャラ（日本語話者）が読む音声サンプルなので、UI ロケールに依らず日本語のまま。
  c.push("こんにちは。この声で読み上げます。");
  c.flush();
}
