import { useEffect, useRef, useState } from "react";
import { applyMark, captureMark, saveMark, scrollTopForTurn, loadMark, type ScrollMark } from "../scrollMark.ts";
import type { Group } from "../transcript/types.ts";

// The user counts as "stuck to the bottom" (auto-follow on) while within this many px of
// the end. Above it, following stops and the jump-to-latest button appears. Narrower than
// before by request, so follow drops more readily on scroll-up — note this sits close to
// the typing indicator / stop-button row's ~40–60px height swing between polls, so that
// swing can occasionally nudge us out of "at bottom" on its own.
const NEAR_BOTTOM_PX = 80;

// After an interaction inside the transcript, hold off the bottom re-pin for this long, so
// content the READER grew (expanding a 作業過程 disclosure, switching code wrapping) keeps
// their position instead of snapping past it. Only needs to outlive the reflow the click
// causes — everything else that grows the transcript is content, and is followed.
const INTERACT_HOLD_MS = 600;

// 「返信を頭から」の頭出しで、返信ブロックの上端に残す余白（px）。0 だと切り出しに見える。
const REPLY_TOP_PAD = 8;

/**
 * 転写のスクロール位置ぜんぶ（末尾追従・完了時の頭出し・位置復元・浮くピル・古い履歴を
 * 継ぎ足したときの視点固定）。MirrorView からそのまま移送したもので、判断は 1 つも
 * 変えていない。
 *
 * 呼び出し側に残るのは「いつ」だけ:
 *   - `applyFollow()`  転写が動いたときの layout effect（deps は MirrorView が持つ）
 *   - `resetForSession()` / `saveMarkFor()` セッション持ち替えの layout effect と、その後始末
 *   - `armFollow()`    送信＝「会話へ連れて行け」の意思表示
 *   - `capturePrependHeight()` / `applyPrependAdjust()` 古い履歴の継ぎ足し
 */
export function useMirrorScroll() {
  // Show a "jump to latest ↓" affordance whenever the user has scrolled up off the bottom
  // (auto-follow is paused) so new/streaming content below is discoverable with one click.
  const [showJump, setShowJump] = useState(false);
  // 「返信を頭から」— 最新の回答ブロックの先頭が画面より上に流れていて、かつ末尾追従が切れて
  // いるときだけ出す（末尾では出さない: 押すべきボタンの上に被るため。syncReplyTop の注記）。
  const [showReplyTop, setShowReplyTop] = useState(false);
  // Backward paging: the pre-prepend scrollHeight, so we can pin the viewport across it.
  const prependAdjustRef = useRef<number | null>(null);
  const mirrorRef = useRef<HTMLDivElement>(null);
  const bodyRef = useRef<HTMLDivElement>(null);
  const scrollBoxRef = useRef<HTMLDivElement>(null); // inner content wrapper — its height tracks the transcript
  // Is auto-follow on (keep the end of the transcript in view)? This tracks the user's
  // INTENT, not raw geometry: it goes false when they actually move the viewport up, and
  // true again when they come back to the end (or send, or press 最新へ). Content growing
  // under a pinned viewport is not a reason to drop it — see onBodyScroll.
  const atBottomRef = useRef(true);
  // The scrollTop WE last wrote. onBodyScroll compares against it to tell "content grew
  // under our own pin" (not user intent) from "the user scrolled up" — see there.
  const selfTopRef = useRef(0);
  // Until this ms, a geometry change is attributed to the reader's own click, not to content.
  const interactUntilRef = useRef(0);
  // 位置復元（scrollMark）: このセッションに戻ってきたときに復元すべき位置。復元中（= restoring
  // が true）は、末尾ピンではなくこのアンカーを保つ。
  //
  // 時間で切らないのは末尾ピンと同じ理由 — 高さは何段にも分かれて遅れて確定し、その最後の一段が
  // いつ来るかは端末しだい。実測（4x スロットリング / 400 ターン）では、遅延レイアウトが 1 回の
  // 大きなコミットで片付き、ResizeObserver が鳴ったのは着地の 3.6 秒後だった。3 秒で切る設計だと
  // その 1 回を取りこぼし、目的地の 24〜729px 手前で固まる。抜けるのは「読者が触った」ときと
  // 「末尾追従に戻った」とき（送信・最新へ）だけにする。
  const restoreMarkRef = useRef<ScrollMark | null>(null);
  const restoringRef = useRef(false);
  // 「返信を頭から」の対象＝最新の回答ブロックの idx。レンダごとに書き、[] で作られる
  // ResizeObserver / onScroll のクロージャからも今の値が読めるようにする（ttsCaptureRef と同型）。
  const lastReplyIdxRef = useRef<number | undefined>(undefined);
  // The idx of the assistant block whose TOP we last brought to the viewport top. A fresh
  // reply is anchored there once (so the user reads it from its first line) and then left
  // alone as it streams; this remembers which reply we've already anchored.
  const anchoredIdxRef = useRef<number | undefined>(undefined);
  // The idx of the reply whose FINAL ANSWER top we've already brought to the viewport top.
  // On completion a following pane collapses the 作業過程 into a disclosure, so the reply's
  // top becomes that collapsed row; we then re-anchor once to the final answer's first line
  // (docs/log/24). Kept separate from anchoredIdxRef so the top-anchor and the answer-anchor each
  // fire exactly once per reply.
  const answerAnchoredRef = useRef<number | undefined>(undefined);
  // False until the first content settle for a session. On open we land at the bottom (as
  // before) and mark the reply already present as "seen", so only replies that arrive while
  // the user is watching get anchored to the top — history isn't retro-scrolled.
  const didInitRef = useRef(false);
  // Keep a bottom-stuck view pinned as geometry changes OUTSIDE the poll-driven follow
  // effect: the body's own box resizing (ToDo / 消費推移 / コンテキスト panels above it, the
  // composer auto-growing, a pane/window resize) AND — via the inner wrapper — the
  // transcript's content height changing as late content lays out (images, code
  // highlighting, math) or streams in. This is what makes opening a session settle at the
  // TRUE bottom instead of a stale pre-layout position, and keeps streaming glued to the
  // tail. atBottomRef is authoritative (the follow effect sets it synchronously right after
  // it scrolls), so a completion-anchored view that was scrolled up is left alone.
  useEffect(() => {
    const el = mirrorRef.current;
    if (!el) return;
    const syncHeight = () => el.style.setProperty("--mirror-todo-max-height", el.clientHeight * 0.2 + "px");
    syncHeight();
    if (typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(syncHeight);
    ro.observe(el);
    return () => ro.disconnect();
  }, []);

  // Re-pin whenever the geometry changes while follow is on: the body's own box resizing
  // (ToDo / 消費推移 / コンテキスト panels above it, the composer auto-growing, a pane/window
  // resize) AND — via the inner wrapper — the transcript's content height changing as late
  // content lays out or streams in.
  //
  // There is deliberately NO list of late-layout sources here and no time window. The
  // transcript's body is rendered by MarkdownView into innerHTML from a PASSIVE effect, so
  // at the moment the follow effect pins the bottom the turns are still empty: essentially
  // ALL of a transcript's height arrives late, in several steps (parse → highlight → math →
  // mermaid → image decode → web fonts). Enumerating those sources is what the previous
  // rounds of this fix tried; each new source (and each slow machine) reopened the bug. The
  // rule is simply "while following, keep the end in view".
  //
  // The one growth we must NOT chase is the one the user caused themselves — expanding a
  // 作業過程 disclosure while parked at the bottom must keep their position, not snap them
  // past what they just opened. That is decided by cause, not by timing: interactUntilRef
  // is armed by an interaction inside the transcript (see the handlers on .mirror-scroll).
  useEffect(() => {
    const el = bodyRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      scheduleReplyTopSync();
      // 位置復元中は、末尾ではなくアンカーを保つ。理由は末尾ピンと同じ（高さが遅れて入る）で、
      // 向き先だけが違う。atBottomRef が立っていたら誰かが末尾追従へ戻した合図（送信・最新へ）
      // なので、そちらを優先して復元を畳む。
      if (restoringRef.current) {
        const mark = restoreMarkRef.current;
        if (mark && !atBottomRef.current && applyMark(el, mark)) {
          selfTopRef.current = el.scrollTop;
          return;
        }
        endRestore();
      }
      if (!atBottomRef.current) return; // scrolled up, or parked at a completion anchor
      if (Date.now() < interactUntilRef.current) return; // the reader's own click grew it
      if (el.scrollHeight - el.scrollTop - el.clientHeight < 1) return;
      el.scrollTop = el.scrollHeight;
      selfTopRef.current = el.scrollTop;
    });
    ro.observe(el);
    if (scrollBoxRef.current) ro.observe(scrollBoxRef.current);
    return () => ro.disconnect();
  }, []);
  // Arm the "the reader caused this reflow" window. A pointer or keyboard interaction inside
  // the transcript can toggle a disclosure (作業過程 / thinking / tool run), switch code
  // wrapping, or open a plan comment box — all of which grow the content under a reader who
  // is sitting at the bottom. Both are captured on .mirror-scroll, so the pointer path and
  // the keyboard path (Enter/Space on a <summary>) arm it before the reflow lands. A fold
  // that WE change (foldWork on completion) is content, not interaction, and is followed.
  const noteInteraction = () => {
    interactUntilRef.current = Date.now() + INTERACT_HOLD_MS;
    endRestoreOnInput(); // 読者が触った ⇒ 位置の復元より、その手を優先する
  };

  // Follow state, from user INTENT rather than from raw geometry.
  //
  // The trap this replaces: a scroll EVENT is dispatched asynchronously, so by the time the
  // handler runs the content may have grown past the offset we ourselves just pinned.
  // Measuring "distance to the bottom" at that point reads our own pin as "the user scrolled
  // up", drops follow, and thereby disarms every re-pin path above — the view then stays
  // wherever the last late layout left it (measured: 754→1246px above the end when opening
  // a long transcript). Only an actual UPWARD move relative to the offset we last wrote is
  // the user. Comparing positions is not the old "suppress the next event" dance: nothing is
  // skipped, so the flag cannot drift out of sync with a scroll we never hear about.
  const onBodyScroll = () => {
    const el = bodyRef.current;
    if (!el) return;
    scheduleReplyTopSync();
    const movedUp = el.scrollTop < selfTopRef.current - 1;
    if (atBottomRef.current && !movedUp) {
      // Following, and the viewport did not move up — the gap (if any) is content that grew
      // after our pin, and the ResizeObserver above closes it. Stay armed.
      selfTopRef.current = el.scrollTop;
      setShowJump((s) => (s === false ? s : false));
      return;
    }
    // Either the user moved up (drop follow), or they are scrolling back down (re-arm once
    // they are within NEAR_BOTTOM_PX of the end).
    const stuck = el.scrollHeight - el.scrollTop - el.clientHeight < NEAR_BOTTOM_PX;
    atBottomRef.current = stuck;
    if (stuck) selfTopRef.current = el.scrollTop;
    setShowJump((s) => (s === !stuck ? s : !stuck));
  };

  // 位置復元をやめる（ユーザーが触った／末尾追従へ戻った）。以後は従来どおり atBottomRef だけが
  // 追従を決める。
  const endRestore = () => {
    restoringRef.current = false;
    restoreMarkRef.current = null;
  };

  // 復元を打ち切るのは「ユーザーが入力した」ときだけ — scrollTop が自分の書いた値からズレた
  // ことを根拠にしてはいけない。ブラウザ自身のスクロールアンカリング（上の内容が伸びた分だけ
  // scrollTop を勝手に足して見た目を保つ機構）が遅延レイアウトのたびに動かすので、それを
  // 「触られた」と読むと復元を途中でやめてしまう（実測: 目的地の 354px 手前で固まり、以後
  // 二度と直らなかった）。入力（ホイール・タッチ・キー・ポインタ）だけを退出条件にすれば、
  // アンカリングのズレは次の再適用で必ず上書きされる。
  //
  // 取りこぼすのはネイティブのスクロールバーをドラッグした場合（Chromium は要素へ
  // pointerdown を出さない）。復元が畳まれるまで引っぱり合いになるが、掴み直せば済む。
  const endRestoreOnInput = () => {
    if (restoringRef.current) endRestore();
  };

  // Jump-to-latest button: snap to the bottom and re-arm auto-follow.
  const jumpToBottom = () => {
    const el = bodyRef.current;
    if (!el) return;
    endRestore(); // 明示的に末尾を選んだ ⇒ 復元アンカーは捨てる
    el.scrollTop = el.scrollHeight;
    selfTopRef.current = el.scrollTop;
    atBottomRef.current = true;
    setShowJump(false);
    syncReplyTop();
  };

  // 「返信を頭から」— 最新の回答ブロックの上端を画面の一番上へ。長い回答の途中から 1 タップで
  // 頭出しするための導線（末尾に貼り付いている間は出さない — syncReplyTop の注記）。
  //
  // 対象はユーザー発言ではなく回答ブロックの先頭（＝畳まれた 作業過程 の行から）。完了時の
  // 自動アンカー（answerAnchoredRef、回答本文の 1 行目）より 1 段上を見せる位置で、「この
  // 返信は何をやったのか」から読み直せる。
  const jumpToReplyTop = () => {
    const el = bodyRef.current;
    const idx = lastReplyIdxRef.current;
    if (!el || idx === undefined) return;
    const top = scrollTopForTurn(el, idx, REPLY_TOP_PAD);
    if (top === null) return;
    endRestore();
    el.scrollTop = top;
    selfTopRef.current = el.scrollTop;
    // 末尾から離れた ⇒ 追従は切る（ここで切らないと、次の poll でまた末尾へ引き戻される）。
    atBottomRef.current = false;
    setShowJump(true);
    syncReplyTop();
  };

  // ピルの出し入れ（＝下の setState）は、必ず次のフレームへ逃がす。末尾ピンと同じフレームで
  // DOM を足し引きすると着地を壊す — 実測: ResizeObserver や follow の layout effect から
  // 直接呼んだ版は、末尾着地が 4 回に 1 回ほど 240px（＝画像 1 枚ぶんの遅延レイアウト）手前で
  // 止まり、そのまま直らなかった。mirror-scroll ハーネスの long シナリオが赤くなる。
  // 1 フレーム遅れて出ることに実害はないので、素直に逃がす。
  const replyTopSyncRef = useRef(false);
  const scheduleReplyTopSync = () => {
    if (replyTopSyncRef.current) return;
    replyTopSyncRef.current = true;
    requestAnimationFrame(() => {
      replyTopSyncRef.current = false;
      syncReplyTop();
    });
  };

  // 「返信を頭から」を出すべきか — 最新の回答ブロックの先頭が、ビューポート上端より上に
  // 流れているときだけ。すでに頭が見えているなら押しても何も起きないので出さない。
  const syncReplyTop = () => {
    const el = bodyRef.current;
    const idx = lastReplyIdxRef.current;
    const turn = el && idx !== undefined ? el.querySelector<HTMLElement>(`[data-turn-idx="${idx}"]`) : null;
    const on = !!(
      el &&
      turn &&
      // 末尾に貼り付いている間は出さない。末尾には押すべきものが並ぶ面（引き継ぎカードの
      // 起動ボタン、質問 / プラン / 許可の回答ボタン、コピー…）で、その上に浮くピルが被って
      // 押せなくなる。読んでいる途中＝追従が切れているときだけの導線にする。
      !atBottomRef.current &&
      turn.getBoundingClientRect().top < el.getBoundingClientRect().top - REPLY_TOP_PAD
    );
    setShowReplyTop((s) => (s === on ? s : on));
  };
  // Keep the conversation in view as it grows — but ONLY while the user is stuck to the
  // bottom (atBottomRef). If they've scrolled up to read, we never move them.
  //
  // Runs as a LAYOUT effect: it fires synchronously after the DOM mutates but BEFORE the
  // browser paints or dispatches scroll events. That matters at completion, when the work
  // trace folds into a disclosure and the content height suddenly shrinks — reading/
  // scrolling here first means we set a valid scrollTop before the browser would clamp it
  // and fire a stray scroll (which used to race this effect and mis-place the viewport).
  //
  // While a reply is still WORKING we follow the bottom so the streamed 作業過程 / answer stays
  // in view. We do NOT strand the user at the end of a long answer, though: the moment the
  // reply COMPLETES we re-anchor once to the FINAL ANSWER's first line at the viewport top
  // (tracked by its idx), so it reads from the start instead of the tail. That upward scroll
  // honestly flips atBottomRef→false via onBodyScroll, so afterwards the user is left alone.
  // ※ 元は useLayoutEffect の本体。呼ぶのは MirrorView（deps を持っているのはあちら）。
  const applyFollow = ({
    groups,
    loaded,
    busy,
    pending,
    pendingPlan,
    pendingPerm,
  }: {
    groups: Group[];
    loaded: boolean;
    busy: boolean;
    pending: unknown;
    pendingPlan: string | null;
    pendingPerm: string | null;
  }) => {
    scheduleReplyTopSync(); // 内容が変わるたび「返信を頭から」の要否を採り直す（末尾追従の有無に依らない）
    if (!atBottomRef.current) return;
    const el = bodyRef.current;
    if (!el) return;
    const toBottom = () => {
      el.scrollTop = el.scrollHeight;
      selfTopRef.current = el.scrollTop;
      atBottomRef.current = true; // authoritative now (don't wait for the async scroll event)
    };

    // Actionable prompts (question / plan / permission) render at the very bottom and need
    // a response — always surface them fully.
    if (pending || pendingPlan || pendingPerm) {
      toBottom();
      return;
    }

    // The reply to the latest user prompt is the first assistant block after the last user
    // turn. Its idx is stable for the whole reply (further streamed turns and any subagent
    // blocks append after it), so a change of idx marks a genuinely new reply.
    let u = -1;
    for (let i = groups.length - 1; i >= 0; i--) {
      if (groups[i].role === "user") { u = i; break; }
    }
    const reply = groups[u + 1];
    const replyIdx = reply && reply.role !== "user" ? reply.idx : undefined;

    // First settle for this session: land at the bottom (the familiar "open shows the
    // latest" position) and remember whatever reply is already there, so history isn't
    // retro-anchored — only replies that arrive while watching get the top treatment below.
    if (!didInitRef.current) {
      if (groups.length || loaded) {
        didInitRef.current = true;
        anchoredIdxRef.current = replyIdx;
        answerAnchoredRef.current = replyIdx; // a reply already present at open isn't re-anchored
        // …ただし、このセッションを「途中まで読んだ状態」で離れていたなら、そこへ戻す
        // （scrollMark）。末尾で離れていた（atBottom）ときは意図が「最新を見る」なので
        // 従来どおり末尾。アンカーのターンが tail ウィンドウに載っていなければ復元は
        // 諦めて末尾＝どのみち読み直せる位置に落とす。
        const mark = restoreMarkRef.current;
        if (mark && !mark.atBottom && applyMark(el, mark)) {
          selfTopRef.current = el.scrollTop;
          atBottomRef.current = false; // 末尾ではない ⇒ 追従は切れ、最新へ の導線が出る
          restoringRef.current = true; // 以後、遅れて入る高さのたびにこのアンカーへ置き直す
          setShowJump(true);
          scheduleReplyTopSync();
          return;
        }
        restoreMarkRef.current = null;
      }
      toBottom();
      return;
    }

    if (replyIdx !== undefined) {
      // Start tracking a newly-arrived reply, but do NOT anchor its top: while it streams we
      // follow the bottom (below) so the user watches progress. answerAnchoredRef resets so the
      // final-answer anchor fires once this reply completes.
      if (replyIdx !== anchoredIdxRef.current) {
        anchoredIdxRef.current = replyIdx;
        answerAnchoredRef.current = undefined; // this reply's final answer hasn't been anchored yet
      }
      // Still working, a background run (サブエージェント/Workflow) is appending, or we're
      // bridging the idle→reply gap (finalizing) — follow the bottom so the streamed tail
      // (and the typing indicator) stay in view.
      if (busy) {
        toBottom();
        return;
      }
      // Completed: a following pane collapses the 作業過程 into a disclosure (defaultWorkOpen=
      // !atBottom, and we've been at the bottom) so the reply's top becomes that collapsed row —
      // re-anchor once to the FINAL ANSWER's first line at the viewport top, so the user reads
      // it from the start rather than the tail we followed to. Only when work was actually
      // folded; a reply with no foldable work already sits with its answer at the top.
      if (answerAnchoredRef.current !== replyIdx) {
        const body = el.querySelector<HTMLElement>(`[data-turn-idx="${replyIdx}"] .mirror-turn-body`);
        const work = body?.querySelector<HTMLElement>(":scope > .mt-work");
        const answer = work?.nextElementSibling as HTMLElement | null;
        if (work && answer) {
          answerAnchoredRef.current = replyIdx;
          const top = el.scrollTop + (answer.getBoundingClientRect().top - el.getBoundingClientRect().top) - 12;
          el.scrollTop = Math.max(0, top);
          selfTopRef.current = el.scrollTop;
          atBottomRef.current = false; // parked at the answer top — leave the user here (and stop the RO re-pin)
        } else if (body && !work) {
          answerAnchoredRef.current = replyIdx; // nothing folded — top already is the answer
        }
      }
      return;
    }

    // No reply yet (the user's own just-sent prompt is the newest thing): keep it in view.
    toBottom();
  };

  // 送信・「最新へ」＝「会話へ連れて行け」の意思表示。末尾追従を張り直す。
  const armFollow = () => {
    atBottomRef.current = true;
    setShowJump(false);
  };

  // セッション持ち替え時のリセット（MirrorView の layout effect の中で、そこでの順序のまま）。
  const resetForSession = (session: string) => {
    atBottomRef.current = true; // a freshly opened session starts pinned to the bottom
    // The old scroller can be reused for another session (pane D&D / opening a row
    // in the current mirror). Clear its physical offset in the same pre-paint phase;
    // the first transcript layout effect then pins the new content to its end.
    if (bodyRef.current) { bodyRef.current.scrollTop = 0; selfTopRef.current = 0; }
    // 「読者自身が広げた」窓は前のセッションの話なので、持ち越さない。スマホの横スワイプで
    // セッションを持ち替えると、その指の pointerdown が transcript の上に降りて
    // noteInteraction を 600ms 武装する（.mirror-scroll の capture ハンドラ）。ミラーの高さは
    // ほぼ全部が遅れて入るので、窓が開いたままだと ResizeObserver の再ピンが握りつぶされ、
    // 着地位置が中途半端なところで止まりうる。
    //
    // ただし正直に言うと、これは塞いだ穴であって再現した不具合ではない: mirror-scroll の
    // swipe シナリオでは、この 1 行の有無にかかわらず末尾に着地した（fetch とレンダが毎回
    // 600ms より長くかかり、窓が閉じたあとの成長で再ピンが効いてしまう）。窓が実際に効く
    // 速さの端末では効く、という理屈のぶんだけの手当て。
    interactUntilRef.current = 0;
    // このセッションを最後に見ていた位置（あれば）。実際に戻すのは transcript が載ってから＝
    // 初回 settle で、そこまでは末尾ピンのまま待つ。
    restoreMarkRef.current = loadMark(session);
    restoringRef.current = false;
    setShowJump(false); // …so no jump-to-latest affordance until they scroll up
    setShowReplyTop(false); // 新しいセッションの回答が載るまで頭出しの対象が無い
    anchoredIdxRef.current = undefined; // no reply anchored yet in the new session
    answerAnchoredRef.current = undefined; // …nor its final answer
    didInitRef.current = false; // re-run the "land at bottom on open" settle for this session
  };

  /** 継ぎ足しの保留を捨てる（セッション持ち替えの頭で、他のカーソル類と一緒に）。 */
  const resetPrepend = () => {
    prependAdjustRef.current = null;
  };

  /** 離脱時に、いま見ていた位置を控える（cleanup が読む DOM は「出ていく側」のもの）。 */
  const saveMarkFor = (session: string) => saveMark(session, captureMark(bodyRef.current, atBottomRef.current));

  /** 古い履歴を継ぎ足す直前の高さを控える（継ぎ足したぶんだけ scrollTop を足して視点を保つ）。 */
  const capturePrependHeight = () => {
    const el = bodyRef.current;
    prependAdjustRef.current = el ? el.scrollHeight : null; // pin the viewport across the prepend
  };
  // After an older page is prepended, restore the viewport: scrollTop grows by exactly the
  // height added on top, so the user stays on the same content instead of jumping up.
  const applyPrependAdjust = () => {
    const el = bodyRef.current;
    if (el && prependAdjustRef.current != null) {
      el.scrollTop += el.scrollHeight - prependAdjustRef.current;
      selfTopRef.current = el.scrollTop;
      prependAdjustRef.current = null;
    }
  };

  return {
    mirrorRef,
    bodyRef,
    scrollBoxRef,
    atBottomRef,
    lastReplyIdxRef,
    showJump,
    showReplyTop,
    applyFollow,
    armFollow,
    resetForSession,
    resetPrepend,
    saveMarkFor,
    capturePrependHeight,
    applyPrependAdjust,
    noteInteraction,
    onBodyScroll,
    endRestoreOnInput,
    jumpToBottom,
    jumpToReplyTop,
  };
}
