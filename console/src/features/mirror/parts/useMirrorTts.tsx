import { useEffect, useRef, useState } from "react";
import type { RefObject } from "react";
import { createPortal } from "react-dom";
import { Icon } from "../../../ui/Icon.tsx";
import { t as tr } from "../../../lib/i18n/index.ts";
import { displayName } from "../../../lib/sessionview.ts";
import type { Session } from "../../../types/session.ts";
import type { Settings } from "../../../lib/settings.ts";
import { useTtsStore } from "../../../core/store/tts.ts";
import {
  sessionVoiceOpts,
  announce,
  onTtsStop,
  startTts,
  stopTtsForReplacement,
  ttsOptsFromSettings,
  workVoiceOpts,
  type TtsController,
} from "../../chat/tts.ts";
import { pendingSpeech } from "../../chat/ttsText.ts";
import { askAssistant } from "../../chat/api.ts";
import {
  readTurn,
  collectBlocks,
  finalAnswerStart,
  blockIndexAt,
  turnSpokenText,
  claimTurnReader,
  isTurnReader,
  type TurnReadHandle,
} from "../turnTts.ts";
import { confirmedWorkEnd, latestWorkPromptIndex, textOfParts } from "../mirrorParts.ts";
import type { Group, Question, Turn, TurnTtsWiring } from "../transcript/types.ts";

/**
 * ミラーの読み上げ一式（カラオケ朗読・自動読み上げ・作業過程の小声読み・確認の告知・
 * 「ここから朗読」ピル）。MirrorView から**そのまま**移したもので、判断は 1 つも変えていない。
 *
 * 呼び出し側に残るのは「いつ呼ぶか」が MirrorView の文脈に属する 3 つだけ:
 *   - `resetForSession()` — セッション持ち替えの layout effect の中から（順序に意味がある）
 *   - `resetForTranscript()` — ポーリングが `reset` を受け取った枝から
 *   - `syncAutoRead()` — 転写が動いたときの effect から（deps は呼び出し側が持つ）
 */
export function useMirrorTts({
  session,
  sessionMeta,
  paneId,
  active,
  readOnly,
  settings,
  bodyRef,
  statusRef,
  loaded,
  pending,
  pendingPlan,
  pendingPerm,
}: {
  session: string;
  sessionMeta?: Session | null;
  paneId: string;
  active?: boolean;
  readOnly: boolean;
  settings: Settings;
  bodyRef: RefObject<HTMLDivElement | null>;
  statusRef: RefObject<string>;
  loaded: boolean;
  pending: Question[] | null;
  pendingPlan: string | null;
  pendingPerm: string | null;
}) {
  // --- カラオケ朗読（turnTts, docs/log/24） -----------------------------------------
  // 読み上げ中のターン（transcript の idx）と一時停止状態。onEnd（自然終了・TopBar 停止・
  // 他の再生開始）で自分の分だけ片づける。
  const [ttsReading, setTtsReading] = useState<{ idx: number; paused: boolean } | null>(null);
  const ttsHandleRef = useRef<TurnReadHandle | null>(null);
  // 選択位置から読み上げるピル（ReaderView の「ここから朗読」と同パターン）。
  const [ttsPill, setTtsPill] = useState<{ x: number; y: number; idx: number; body: HTMLElement; block: number } | null>(
    null,
  );
  // 自動読み上げ（P2）: 基準 idx（これ以前の履歴は読まない）／読むべきグループ idx のキュー／
  // グループごとの読み上げ済みブロック数（グループは追記で育つので、増えた分だけ読む）。
  const ttsAutoSeenRef = useRef<number | null>(null);
  // seen 基準（上記）が属するセッション。基準は裸の jsonl 行番号なので、セッションが変わると
  // 意味を失う。ペイン D&D の swap は同一インスタンスのまま session prop だけ差し替える
  // （＋ドロップ先を active 化する）ため、前セッションの turns が残ったまま自動読み上げ effect が
  // 走り、その行番号で seen を作ってしまう→新セッションの本文が「新着」に見えて最後の最終回答を
  // 勝手に読み上げる。session 一致を確認するまで基準を取り直しに留めるためのガード。
  const ttsAutoSessionRef = useRef(session);
  const ttsAutoQueueRef = useRef<number[]>([]);
  const ttsAutoDoneRef = useRef(new Map<number, number>());
  // 確定済み作業過程の小声読み。part index で既読を持ち、最後の tool/question/plan までに
  // 確定した text だけを読む。最終回答（idle）到着時はキューごと破棄して通常朗読へ譲る。
  const ttsWorkRef = useRef<TtsController | null>(null);
  const ttsWorkQueueRef = useRef<string[]>([]);
  const ttsWorkDoneRef = useRef(new Map<number, number>());
  // 読み上げ担当の登録（turnTts.ts）。同じセッションを複数ペインで開いても読むのは先着の
  // 1 ペインだけ。readOnly（未アタッチ）ペインは読まないので登録しない。
  const ttsTokenRef = useRef(Symbol("ttsReader"));
  useEffect(() => {
    if (readOnly) return;
    return claimTurnReader(session, ttsTokenRef.current);
  }, [session, readOnly]);
  // 明示的な停止（TopBar・フッター等。プリエンプトは除く）は「静かにして」の意思なので、
  // 自分の自動読み上げキューも捨てる（全ペイン読みでは他ペイン発の停止もここに届く）。
  useEffect(
    () =>
      onTtsStop(() => {
        ttsAutoQueueRef.current.length = 0;
        ttsWorkQueueRef.current.length = 0;
      }),
    [],
  );
  const ttsStart = (idx: number, body: HTMLElement, fromBlock = 0) => {
    ttsHandleRef.current?.stop("replaced"); // 内部置換なので自動読み上げキューは温存
    const h = readTurn(
      body,
      sessionMeta ? displayName(sessionMeta) : tr("mirror.session_fallback"),
      fromBlock,
      (reason) => {
        ttsHandleRef.current = null;
        setTtsReading((cur) => (cur?.idx === idx ? null : cur));
        // ユーザーの明示停止だけキューを捨てる。他再生への置換はキューを温存し、置換先が
        // active に登録された後の状態を見るため microtask から再開判定する。
        if (reason === "explicit") ttsAutoQueueRef.current.length = 0;
        else queueMicrotask(() => ttsAutoPumpRef.current());
      },
      { ...(sessionVoiceOpts(session) ?? {}), paneId }, // セッション声＋発生元ペインのステレオ位置
      session, // 左ペインの再生中アイコン用
    );
    if (!h) return; // 読み上げられる本文が無い（ツールだけのターン等）
    ttsHandleRef.current = h;
    setTtsReading({ idx, paused: false });
  };
  // 長い回答の要約読み上げ（設定 ttsSummaryRead）。この文字数を超える新着分は、全文を
  // 読む代わりにアシスタント（headless CLI・ツールなし one-shot）へ 2 文要約させて読む。
  const TTS_SUMMARY_MIN = 500;
  // i18n-exempt-start: LLM プロンプト（表示でなくモデル挙動・docs/log/28 §4）
  const TTS_SUMMARY_PROMPT =
    "次のテキストはコーディングエージェントの回答です。音声で聞くための要約を、日本語で最大2文・120字以内で書いてください。" +
    "記号・コード・URL・箇条書きは使わず、プレーンな文章だけを返してください。要約以外の前置きや説明は書かないでください。\n\n---\n";
  // i18n-exempt-end
  const ttsSummaryBusyRef = useRef(false); // 要約の生成中（1 本ずつ。終わるまでキューは待つ）

  // 要約を生成してアナウンス（announce = 再生が空くのを待つ直列キュー・TopBar 停止と統合）で
  // 読む。カラオケ・ハイライトは付けない（要約文は画面に無いため）— フル本文はフッターの
  // 読み上げボタンでいつでもカラオケ再生できる。失敗・タイムアウトは全文読みへフォールバック。
  const ttsSummarize = async (gi: number, body: HTMLElement, fromBlock: number, text: string) => {
    const label = (sessionMeta ? displayName(sessionMeta) : tr("mirror.session_fallback")) + tr("mirror.tts.summary_suffix");
    try {
      const r = await Promise.race([
        askAssistant(TTS_SUMMARY_PROMPT + text.slice(0, 6000)),
        new Promise<never>((_, rej) => setTimeout(() => rej(new Error("timeout")), 30000)),
      ]);
      const reply = (r?.reply || "").trim();
      if (!r?.error && reply)
        announce(tr("mirror.tts.summary_prefix") + reply, label, { ...(sessionVoiceOpts(session) ?? {}), paneId }, session);
      else ttsStart(gi, body, fromBlock); // 要約が得られない → 全文読み
    } catch {
      ttsStart(gi, body, fromBlock); // ワークスペース停止・タイムアウト等 → 全文読み
    } finally {
      ttsSummaryBusyRef.current = false;
      ttsAutoPumpRef.current(); // 待たせていた後続へ（再生中なら speaking 解放で再開）
    }
  };

  // キューの先頭から「まだ読んでいないブロック」を読む。何か再生中（自分・チャット読み上げ・
  // アナウンス）なら待つ — 再開のトリガは onEnd と speaking の解放（下の subscribe）。
  const ttsAutoPump = () => {
    if (!settings.ttsEnabled || !settings.ttsAutoReadMirror) {
      ttsAutoQueueRef.current.length = 0;
      return;
    }
    // ポーリング途中の本文だけを見て最終回答か判定すると、ナレーションがツール表示より
    // 1 ポール先行した場合だけ作業過程を読み始めてしまう。作業完了まではキューに貯め、
    // status が working を抜けた時点の完成 DOM から最後のツール以降だけを読む。
    if (statusRef.current === "working") return;
    if (ttsSummaryBusyRef.current) return; // 要約の生成中 → 終わってから順に
    // 何か再生中/準備中なら待つ。speaking だけだと合成待ち（登録済みで最初の音がまだ）の
    // 再生へ割り込むため active も見る（全ペイン読みでは他ペインのポンプと直列になる要）。
    const st = useTtsStore.getState();
    if (ttsHandleRef.current || st.speaking || st.active) return;
    const q = ttsAutoQueueRef.current;
    while (q.length) {
      const gi = q.shift()!;
      const body = bodyRef.current?.querySelector<HTMLElement>(`[data-turn-idx="${gi}"] .mirror-turn-body`);
      if (!body) continue; // リセット等で消えたターン
      const done = ttsAutoDoneRef.current.get(gi) ?? 0;
      const total = collectBlocks(body).length;
      ttsAutoDoneRef.current.set(gi, total);
      if (total <= done) continue; // 増分なし（ツールだけの追記等）
      // 過程スキップ（chat の分離と同趣・docs/log/19）: 完成した本文からツール前ナレーションを
      // 飛ばし、最後のツール以降の本文（＝最終回答）だけを自動読み上げする。
      // 完了後の作業過程は disclosure 内へ移るため、DOM 直下を読む手動朗読も最終回答に揃う。
      const from = Math.max(done, finalAnswerStart(body));
      if (total <= from) continue; // 読むべき最終回答ブロックがまだ無い（過程だけの追記）
      if (settings.ttsSummaryRead) {
        const text = turnSpokenText(body, from);
        if (text.length > TTS_SUMMARY_MIN) {
          ttsSummaryBusyRef.current = true;
          void ttsSummarize(gi, body, from, text);
          return;
        }
      }
      ttsStart(gi, body, from);
      if (ttsHandleRef.current) return; // 読み始めた（読める文が無ければ次の候補へ）
    }
  };
  const ttsAutoPumpRef = useRef(ttsAutoPump);
  ttsAutoPumpRef.current = ttsAutoPump;
  const ttsWorkPump = () => {
    if (!settings.ttsEnabled || !settings.ttsAutoReadMirror || settings.ttsWorkRead === "off") {
      ttsWorkQueueRef.current.length = 0;
      return;
    }
    if (statusRef.current !== "working" || ttsWorkRef.current) return;
    const st = useTtsStore.getState();
    if (st.active || st.speaking) return; // 最終回答・告知など重要な再生へ割り込まない
    const text = ttsWorkQueueRef.current.shift();
    if (!text) return;
    const voice = { ...(sessionVoiceOpts(session) ?? {}), paneId };
    const c = startTts(
      { ...ttsOptsFromSettings(settings), ...voice, ...workVoiceOpts(voice, settings.ttsWorkRead) },
      (sessionMeta ? displayName(sessionMeta) : tr("mirror.session_fallback")) + tr("mirror.tts.work_suffix"),
      (reason) => {
        ttsWorkRef.current = null;
        if (reason === "explicit") ttsWorkQueueRef.current.length = 0;
        else queueMicrotask(() => ttsWorkPumpRef.current());
      },
      session,
    );
    ttsWorkRef.current = c;
    c.push(text);
    c.flush();
  };
  const ttsWorkPumpRef = useRef(ttsWorkPump);
  ttsWorkPumpRef.current = ttsWorkPump;
  // 他の再生が終わって音声が空いたら、待たせていた自動読み上げを再開する。zustand の
  // subscribe は setState 中に同期で呼ばれ、プリエンプト（旧再生 stop → 新再生の登録）の
  // 途中は active が一瞬 null になるため、microtask に逃がして置き換え完了後の状態で判定する。
  useEffect(() => {
    return useTtsStore.subscribe((st, prev) => {
      if (prev.speaking && !st.speaking)
        queueMicrotask(() => {
          ttsWorkPumpRef.current();
          ttsAutoPumpRef.current();
        });
    });
  }, []);

  // 確認・質問の読み上げ（設定 ttsReadPending）: 保留中の AskUserQuestion／プラン承認／
  // 許可要求が「新しく現れたら」内容を読む（アクティブなペインのみ。全ペイン読み
  // ttsAutoReadAllPanes では開いている全ペイン。ペインに無いセッションは
  // useSessionNotifications の短い告知が担当）。開いた時点で既に出ていた
  // 保留は基準として飲み込み、読まない（ペインを行き来するたびに再読しないため）。
  const ttsPendingInitRef = useRef(false);
  const ttsPendingSigRef = useRef("");
  useEffect(() => {
    if (!loaded) return;
    const sig = pending
      ? "q:" + JSON.stringify(pending)
      : pendingPlan
        ? "plan:" + pendingPlan.slice(0, 200)
        : pendingPerm
          ? "perm:" + pendingPerm
          : "";
    if (!ttsPendingInitRef.current) {
      ttsPendingInitRef.current = true;
      ttsPendingSigRef.current = sig;
      return;
    }
    if (sig === ttsPendingSigRef.current) return;
    ttsPendingSigRef.current = sig;
    if (!sig || readOnly) return;
    // 対象ペインは自動読み上げと同じ規則（アクティブのみ／全ペイン読みなら担当ペイン）。
    if (settings.ttsAutoReadAllPanes ? !isTurnReader(session, ttsTokenRef.current) : !active) return;
    if (!settings.ttsEnabled || !settings.ttsReadPending) return;
    const label = (sessionMeta ? displayName(sessionMeta) : tr("mirror.session_fallback")) + tr("mirror.tts.confirm_suffix");
    const text = pending
      ? pendingSpeech(pending)
      : pendingPlan
        ? tr("mirror.tts.plan_ready")
        : tr("mirror.tts.permission_wait") + (pendingPerm || "").slice(0, 100);
    announce(text, label, { ...(sessionVoiceOpts(session) ?? {}), paneId });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [loaded, pending, pendingPlan, pendingPerm]);
  const ttsWiring: TurnTtsWiring = {
    reading: ttsReading,
    start: ttsStart,
    pause: () => {
      ttsHandleRef.current?.pause();
      setTtsReading((c) => (c ? { ...c, paused: true } : c));
    },
    resume: () => {
      ttsHandleRef.current?.resume();
      setTtsReading((c) => (c ? { ...c, paused: false } : c));
    },
    stop: () => ttsHandleRef.current?.stop(), // 後始末は onEnd 側で
  };
  // セッション切替で停止（本文 DOM ごと入れ替わるため）。アンマウント（ターミナルへの
  // 切替・ペインを閉じる）では止めない — 再生はグローバル 1 本でビューに依存しないので
  // そのまま流し、操作は TopBar の停止で足りる。カラオケ・ハイライトは外れた DOM に付いた
  // まま破棄されるだけで無害（ミラーへ戻ったときのハイライト復元まではしない）。
  const ttsSessionRef = useRef(session);
  useEffect(() => {
    if (ttsSessionRef.current === session) return;
    ttsSessionRef.current = session;
    ttsHandleRef.current?.stop("replaced");
  }, [session]);
  // 本文内でテキスト選択が確定したら「ここから読み上げ」ピルを出す（assistant ターン内のみ）。
  const captureTtsSel = () => {
    const sel = window.getSelection();
    const root = bodyRef.current;
    if (!settings.ttsEnabled || !sel || sel.isCollapsed || sel.rangeCount === 0 || !root) {
      setTtsPill(null);
      return;
    }
    const range = sel.getRangeAt(0);
    const node = range.startContainer;
    const el = node.nodeType === Node.TEXT_NODE ? node.parentElement : (node as HTMLElement);
    // 完了後に畳んだ作業過程は自動・フッター朗読の対象外。展開中の選択から最終回答へ
    // 飛ぶピルを出すと誤解を招くので、disclosure 内の選択には操作を出さない。
    if (el?.closest(".mt-work")) {
      setTtsPill(null);
      return;
    }
    const turnEl = root.contains(node) ? el?.closest<HTMLElement>(".mirror-turn.assistant") : null;
    const turnBody = turnEl ? el?.closest<HTMLElement>(".mirror-turn-body") : null;
    const idx = turnEl?.dataset.turnIdx;
    if (!turnEl || !turnBody || idx === undefined) {
      setTtsPill(null);
      return;
    }
    const block = blockIndexAt(collectBlocks(turnBody), node);
    if (block < 0) {
      setTtsPill(null);
      return;
    }
    const rect = range.getBoundingClientRect();
    setTtsPill({ x: Math.round(rect.left), y: Math.round(rect.top - 34), idx: Number(idx), body: turnBody, block });
  };
  // タッチ選択（長押し＋ドラッグ）は mouseup を出さないので selectionchange でも更新する
  // （デバウンス・最新クロージャを ref 経由で。ReaderView と同じ）。
  const ttsCaptureRef = useRef(captureTtsSel);
  ttsCaptureRef.current = captureTtsSel;
  useEffect(() => {
    let t: ReturnType<typeof setTimeout> | null = null;
    const onSelChange = () => {
      if (t) clearTimeout(t);
      t = setTimeout(() => ttsCaptureRef.current(), 250);
    };
    document.addEventListener("selectionchange", onSelChange);
    return () => {
      document.removeEventListener("selectionchange", onSelChange);
      if (t) clearTimeout(t);
    };
  }, []);
  // 新しい回答の自動読み上げ（P2）: ポーリングで append された新規 assistant ターンを
  // 朗読キューへ（通常はアクティブなペインのみ、ttsAutoReadAllPanes なら開いている全ペイン。
  // ペイン間は 1 本の再生を待ち合って直列）。初回ロード（tail）とリセット（idx の巻き戻り）は
  // 基準 idx を取り直すだけで履歴は読まない。連続 assistant ターンは同じグループに折り畳まれて
  // 育つので、キューはグループ idx 単位（重複なし）に持ち、pump が増えたブロックだけ読む。
  // DOM は commit 後（この effect 実行時）に描画済み。
  const syncAutoRead = ({ turns, groups, status }: { turns: Turn[]; groups: Group[]; status: string }) => {
  // セッションが変わった直後は、まだ前セッションの turns が残ったまま（swap は同一インスタンスの
  // まま session prop だけ差し替え、ドロップ先を active 化する）この effect が active 変化で走る
  // ことがある。その turns の idx で seen を作ると新セッションの本文を誤読するので、session が
  // 揃うまでは基準を捨てて何も読まない（新セッションの turns が届いた回で改めて基準化する）。
  if (ttsAutoSessionRef.current !== session) {
    ttsAutoSessionRef.current = session;
    ttsAutoSeenRef.current = null;
    return;
  }
  let newest = -1;
  for (let i = turns.length - 1; i >= 0; i--) {
    const x = turns[i].idx;
    if (x !== undefined) {
      newest = x;
      break;
    }
  }
  if (newest < 0) return;
  const seen = ttsAutoSeenRef.current;
  ttsAutoSeenRef.current = newest; // 非対象ペインでも履歴を飲み込み、後から一括再読しない
  const canRead =
    !readOnly &&
    settings.ttsEnabled &&
    settings.ttsAutoReadMirror &&
    (settings.ttsAutoReadAllPanes ? isTurnReader(session, ttsTokenRef.current) : active);

  if (status === "working" && settings.ttsWorkRead !== "off") {
    // 現在のユーザープロンプト以後だけを見る。送信直後の pending echo も境界に含め、
    // 実ターンが履歴へ着地するまでの間に一つ前の作業過程へ巻き戻らないようにする。
    // まだ実行されていない queued prompt は現在の作業境界にはしない。
    const lastUser = latestWorkPromptIndex(groups);
    for (let i = lastUser + 1; i < groups.length; i++) {
      const g = groups[i];
      if (g.role !== "assistant" || g.sidechain || g.compact || g.idx === undefined) continue;
      const end = confirmedWorkEnd(g.parts);
      const done = ttsWorkDoneRef.current.get(g.idx) ?? 0;
      if (end <= done) continue;
      ttsWorkDoneRef.current.set(g.idx, end);
      const text = textOfParts(g.parts.slice(done, end));
      if (canRead && seen !== null && text) ttsWorkQueueRef.current.push(text);
    }
    while (ttsWorkQueueRef.current.length > 4) ttsWorkQueueRef.current.shift();
    if (canRead) ttsWorkPumpRef.current();
  } else {
    // idle = 最終回答が確定。残っている小声を置換停止し、通常の最終回答朗読へ譲る。
    stopTtsForReplacement(ttsWorkRef.current);
    ttsWorkRef.current = null;
    ttsWorkQueueRef.current.length = 0;
    ttsWorkDoneRef.current.clear();
  }
  if (!canRead) {
    stopTtsForReplacement(ttsWorkRef.current);
    ttsWorkRef.current = null;
    ttsWorkQueueRef.current.length = 0;
    return;
  }
  if (seen !== null && newest > seen) {
    const q = ttsAutoQueueRef.current;
    for (const t of turns) {
      if (t.idx === undefined || t.idx <= seen) continue;
      if (t.role !== "assistant" || t.sidechain || t.compact) continue;
      // このターンが属するグループ＝idx が t.idx 以下で最後のグループ
      let g: Group | null = null;
      for (const gg of groups) {
        if (gg.idx === undefined) continue;
        if (gg.idx <= t.idx) g = gg;
        else break;
      }
      if (!g || g.idx === undefined || g.role !== "assistant" || g.sidechain || g.compact) continue;
      if (!q.includes(g.idx)) q.push(g.idx);
    }
    while (q.length > 4) q.shift();
  }
  ttsAutoPumpRef.current();
  };

  // セッション持ち替えのリセット（MirrorView の layout effect から、そこでの順序のまま呼ぶ）。
  const resetForSession = () => {
    ttsAutoSeenRef.current = null; // 自動読み上げの基準も取り直す（履歴は読まない）
    ttsAutoQueueRef.current.length = 0;
    ttsAutoDoneRef.current.clear();
    stopTtsForReplacement(ttsWorkRef.current);
    ttsWorkRef.current = null;
    ttsWorkQueueRef.current.length = 0;
    ttsWorkDoneRef.current.clear();
    ttsPendingInitRef.current = false; // 確認読み上げの基準も取り直す
    ttsPendingSigRef.current = "";
  };

  // 転写そのものが差し替わった（サーバが reset を返した）ときのリセット。idx が振り直される
  // ので基準も捨てる。全体停止にはしない — 本文 DOM の入れ替えなので "replaced"。
  const resetForTranscript = () => {
    ttsHandleRef.current?.stop("replaced");
    ttsAutoSeenRef.current = null;
    ttsAutoQueueRef.current.length = 0;
    ttsAutoDoneRef.current.clear();
    stopTtsForReplacement(ttsWorkRef.current);
    ttsWorkRef.current = null;
    ttsWorkQueueRef.current.length = 0;
    ttsWorkDoneRef.current.clear();
  };

  // 選択位置から読み上げるピル。本文の外（document.body）へ出すのは、転写のスクロールに
  // 巻き込まれずビューポート座標で置くため。
  const pillPortal =
    ttsPill &&
    createPortal(
      <div className="sel-pill-group" style={{ left: ttsPill.x, top: Math.max(4, ttsPill.y) }}>
        <button
          type="button"
          className="sel-send-pill"
          onMouseDown={(e) => e.preventDefault()}
          onClick={() => {
            ttsStart(ttsPill.idx, ttsPill.body, ttsPill.block);
            setTtsPill(null);
            window.getSelection()?.removeAllRanges();
          }}
        >
          <Icon name="unmute" /> {tr("chat.read_from_here")}
        </button>
      </div>,
      document.body,
    );

  return { wiring: ttsWiring, captureSel: captureTtsSel, pillPortal, syncAutoRead, resetForSession, resetForTranscript };
}
