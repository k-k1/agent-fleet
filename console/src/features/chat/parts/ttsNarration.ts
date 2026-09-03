import { getSettings } from "../../../lib/settings.ts";
import { useTtsStore } from "../../../core/store/tts.ts";
import { plainify, splitLongSentence } from "../ttsText.ts";
import { effectiveDict } from "../ttsDict.ts";
import { type TtsController, type TtsEndReason, type TtsStopReason } from "../ttsControl.ts";
import { type TtsOptions, ttsOptsFromSettings, localizedReadings } from "./ttsOptions.ts";
import { emotionOpts, voiceCharName } from "./ttsVoices.ts";
import { MAX_INFLIGHT, audioCtx, connectOutput, heardProvider, outputVolume, synthToBuffer } from "./ttsAudio.ts";
import { notifyStopped, preemptActive, takeAnnounce } from "./ttsPlay.ts";

// --- 朗読モード（docs/log/24）: ファイル本文を冒頭から順次読み上げ＋カラオケ追従 --------------
// units（各ブロックのプレーンテキスト）を上から順に合成・再生し、再生を開始した unit の index を
// onUnit で通知する（呼び手＝FileView がその要素をハイライト＋スクロールする）。startTts と同じ
// 合成・順次再生・グローバル 1 本再生の仕組みを流用しつつ、一時停止/再開（AudioContext の
// suspend/resume）と unit 単位の進捗通知を持つ点が異なる。

export interface NarrationHandle {
  pause(): void;
  resume(): void;
  stop(reason?: TtsStopReason): void;
  isPaused(): boolean;
  // 声の即時切替（朗読ビューのセレクト）。いま鳴っている文はそのまま、次の文から新しい声。
  setVoice(voice?: Partial<TtsOptions>): void;
}

export function startNarration(
  units: string[],
  source: string,
  // onUnit(i) = i 番目の unit の再生を開始。onUnit(null, reason) = 終了（done=自然終了 /
  // explicit=明示停止、replaced=他の再生への置き換え。ミラーの自動読み上げキューが
  // 継続可否の判断に使う）。
  onUnit: (i: number | null, endReason?: TtsEndReason) => void,
  // 声の上書き（セッションごとの声 sessionVoiceOpts 等）。未指定は設定の話者。
  voice?: Partial<TtsOptions>,
  // unit ごとの前拍（秒）。リスト項目・段落頭など「新しいブロックの最初の文」に BLOCK_BEAT を
  // 渡すと、読む前に一拍おく（先頭 unit の前拍は開始遅延になるだけなので無視する）。
  preGaps?: number[],
  // 発生元セッション名（左ペインの再生中アイコン用。非セッションの朗読は ""）。
  sessionName = "",
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
    if (t) t = localizedReadings(t, userDict, s.ttsParticlePause); // 辞書 → 読み補正 → 助詞の小休止
    return t;
  });

  const buffers = new Map<number, AudioBuffer | null>(); // index → 復号済み（null=空/失敗）
  const acs = new Set<AbortController>();
  let synthAt = 0; // 次に合成を仕掛ける index
  let cursor = 0; // 次に再生する index
  let epoch = 0; // 声の世代（setVoice で進む。古い声の合成結果を無効化する）
  // 実際に合成したプロバイダ（X-TTS-Provider）。auto の行き先は CP が決めるので、最初の
  // ユニットが返るまで分からない。分かった時点で TopBar の声表示を名乗り直す。
  let heard = "";
  const noteHeard = (ab: AudioBuffer) => {
    const h = heardProvider(ab);
    if (!h || h === heard) return;
    heard = h;
    const st = useTtsStore.getState();
    if (st.active === adapter) st.setActive(adapter, source, voiceCharName(opts, heard), sessionName);
  };
  let inflight = 0;
  let playing = false;
  let cur: AudioBufferSourceNode | null = null;
  let paused = false;
  let stopped = false;
  let ended = false;

  const finish = (reason: TtsEndReason) => {
    if (ended) return;
    ended = true;
    onUnit(null, reason);
    const st = useTtsStore.getState();
    if (st.active === adapter) {
      st.setActive(null, "");
      st.setSpeaking(false);
      st.setPreparing(false);
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
    heard = ""; // 声が変われば行き先も変わりうる。次のユニットの応答で名乗り直す
    const st = useTtsStore.getState();
    if (st.active === adapter) st.setActive(adapter, source, voiceCharName(opts), sessionName); // TopBar の声表示を更新
    pump();
    tryPlay(); // 再生が追いついて待っていた場合に備える
  };

  // 告知の差し挟み（docs/log/24）: 長い朗読の途中でもセッション通知・確認の告知を待たせすぎない。
  // ユニット境界で announce キューから 1 件取り出し、次のユニットの前にその場で読む。再生中は
  // TopBar のラベル/声を告知側に差し替え、終わったら朗読のものへ戻す。停止・一時停止は朗読と
  // 一体（同じ ctx・同じ adapter）。
  const playInterlude = (a: { text: string; source: string; voice?: Partial<TtsOptions>; sessionName?: string; purpose?: "reading" | "session-notification" | "usage-notification" | "manual" }) => {
    let t = plainify(a.text, codeOpts).trim();
    if (t) t = localizedReadings(t, userDict, getSettings().ttsParticlePause);
    if (!t) {
      tryPlay();
      return;
    }
    const aopts = { ...ttsOptsFromSettings(getSettings()), ...a.voice };
    let aheard = ""; // 告知側の実際の合成先（朗読本体の heard とは別に持つ）
    const label = (on: boolean) => {
      const st = useTtsStore.getState();
      if (st.active !== adapter) return;
      if (on) st.setActive(adapter, a.source, voiceCharName(aopts, aheard), a.sessionName ?? "", a.purpose ?? "reading");
      else st.setActive(adapter, source, voiceCharName(opts, heard), sessionName); // 朗読側の名乗りへ戻す
    };
    // 長い告知（要約など）も合成用に分割して順に鳴らす（1 回の合成が重いと無音の待ちになる）。
    const pieces = splitLongSentence(t);
    let pi = 0;
    playing = true; // 合成待ちの間も次ユニットの再生開始と maybeDone を抑える
    const playNext = () => {
      if (stopped) return;
      if (pi >= pieces.length) {
        playing = false;
        label(false);
        tryPlay();
        maybeDone();
        return;
      }
      const piece = pieces[pi++];
      const ac = new AbortController();
      acs.add(ac);
      void synthToBuffer(ctx!, piece, emotionOpts(piece, aopts), ac.signal).then((ab) => {
        acs.delete(ac);
        if (stopped) {
          playing = false;
          return;
        }
        if (!ab) {
          playNext(); // 失敗した片は飛ばして次へ
          return;
        }
        aheard = heardProvider(ab) || aheard;
        label(true);
        const src = ctx!.createBufferSource();
        src.buffer = ab;
        connectOutput(ctx!, src, outputVolume(aopts, aheard), aopts.paneId);
        src.onended = () => {
          cur = null;
          playNext();
        };
        cur = src;
        src.start(ctx!.currentTime + (pi === 1 ? 0.15 : 0)); // 朗読との切れ目に小さな間（先頭の片のみ）
        useTtsStore.getState().setSpeaking(true);
        useTtsStore.getState().setPreparing(false);
      });
    };
    playNext();
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
      useTtsStore.getState().setPreparing(false); // 音が出はじめた → 生成中を解除
      noteHeard(ab); // 実際の合成先が分かったので TopBar の声表示を直す（auto のフォールバック）
      const src = ctx.createBufferSource();
      src.buffer = ab;
      connectOutput(ctx, src, outputVolume(opts, heard), opts.paneId);
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

  const adapter: TtsController = { push() {}, flush() {}, stop: (reason) => stop(reason) };

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
  const stop = (reason: TtsStopReason = "explicit") => {
    if (stopped || ended) return;
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
    finish(reason);
    if (reason === "explicit") notifyStopped(); // ユーザー停止だけ待機キューも捨てる
  };

  useTtsStore.getState().setActive(adapter, source, voiceCharName(opts), sessionName);
  // 初回キックは microtask に回す。呼び手（FileView）が返り値の handle と自分の state を
  // 確定してから onUnit が走るようにするため（空/エンジン無しで finish が同期発火すると、
  // beginNarration のセットアップ前に onUnit(null) が来て状態が不整合になるのを防ぐ）。
  queueMicrotask(() => {
    if (stopped) return;
    if (!ctx || texts.every((t) => !t)) {
      finish("done"); // エンジン無し（AudioContext 不可）or 読む中身が無い
      return;
    }
    useTtsStore.getState().setPreparing(true); // 最初の音が鳴るまで「生成中」（TopBar のぐるぐる）
    pump();
    tryPlay();
  });
  return { pause, resume, stop, isPaused: () => paused, setVoice };
}
