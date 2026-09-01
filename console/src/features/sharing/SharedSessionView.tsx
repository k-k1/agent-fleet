import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { CSSProperties, ReactNode } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { agentOf } from "../../agents/registry.ts";
import { chatFontStack, effectiveTheme, expandThinking, surfaceAccent, surfaceBg, useSettings } from "../../lib/settings.ts";
import { HandoffBody, HandoffCard, type Proposal } from "../mirror/HandoffProposal.tsx";
import { applyMark, captureMark, loadMark, saveMark, type ScrollMark } from "../mirror/scrollMark.ts";
import { TranscriptView } from "../mirror/transcript/TranscriptView.tsx";
import { PlanBlock, QuestionBlock } from "../mirror/transcript/blocks.tsx";
import { MarkdownView } from "../viewer/MarkdownView.tsx";
import type { TranscriptCaps } from "../mirror/transcript/capabilities.ts";
import type { Question, Turn } from "../mirror/transcript/types.ts";
import { coalesceUserActions, groupTurns, mergeTurns } from "../mirror/transcript/model.ts";
import { patchAnswers } from "../mirror/interactionAnswers.ts";
import { ownerLabel, useSharedSessionsStore } from "./store.ts";
import { HandoffInboxModal } from "./HandoffInboxModal.tsx";
import { startHandoffPolling, useHandoffStore } from "./handoffStore.ts";
import { useTenantStore } from "../../core/store/tenant.ts";
import { useMarksController } from "../mirror/transcript/useMarks.ts";
import { MarkStrip } from "../mirror/transcript/MarkStrip.tsx";
import "./sharing.css";

// SharedSessionView — the RECIPIENT's read of a session somebody else owns (docs/log/59).
//
// It renders through the very same pipeline and blocks as the mirror
// (features/mirror/transcript), so a shared conversation reads exactly like the owner's:
// tool runs folded, plans and reasoning as their own cards, compaction summaries
// collapsed. What differs is only the TranscriptCaps handed in — a recipient has no
// Workspace of their own to open files, diffs, panes or pasted images in, so those
// affordances are simply not rendered (see transcript/capabilities.ts).
//
// The transcript arrives through the control-plane's allowlist DTO, which strips cwd /
// path / filePath and every structured coordinate before it ever reaches the browser
// (docs/log/59 §3, control-plane/session_share.go sharedTranscriptDTO).

// Page size, in transcript LINES (claude) / turns (store-backed agents) — the same
// window the mirror asks for. It used to be 60 for a faster first paint, but 60 claude
// jsonl lines is often a fraction of ONE exchange (a single answer spans a thinking
// line, every tool call and the reply), so the opening screen could start mid-answer
// with the prompt that caused it out of frame, and 以前の会話を読み込む had to be
// pressed over and over. The first-paint cost that motivated 60 was the per-request
// inventory sync, which is now throttled per owner (docs/log/59 §3).
const WINDOW = 400;
// Poll cadence, matching the mirror's. The server allows 120 reads/min per
// recipient+session, so even the working cadence stays well inside the limit.
const POLL_WORKING = 1200;
const POLL_IDLE = 3000;
// The owner's Workspace is stopped: nothing can change until they start it, so back off.
const POLL_STOPPED = 5000;
// 引き継ぎ提案の取得間隔。転写より粗くてよい(提案は所有者の操作でしか変わらない)。
// 転写ポーリングと同じ 120回/分のバケツを共有するので、ここを詰めると転写の方が絞られる。
const POLL_HANDOFF = 5000;
const NEAR_BOTTOM_PX = 80;
// 読者自身の操作(開閉)が起こす高さの変化を追わない窓。ミラーと同じ値。
const INTERACT_HOLD_MS = 600;
// スクロールが止まったとみなして表示位置を控えるまで。
const MARK_SETTLE_MS = 150;

interface SharedTurn extends Turn {
  status?: string;
}

// Last-known transcript per shared session, kept at module level so re-opening a pane
// paints immediately instead of starting from an empty view while the first fetch flies.
// Same reasoning as the mirror's echoStore: this component unmounts on a pane/tab switch,
// and re-fetching from scratch every time is exactly what made the view feel slow.
interface CacheEntry {
  turns: SharedTurn[];
  cursor: number;
  firstLine: number;
  hasMore: boolean;
}
const transcriptCache = new Map<string, CacheEntry>();

export function SharedSessionView({ sharedSessionId, headerActions }: { sharedSessionId: string; headerActions?: ReactNode }) {
  const tr = useT();
  const settings = useSettings();
  const meta = useSharedSessionsStore((s) => s.sessions.find((x) => x.id === sharedSessionId));
  // 自分の login id。マーカーの「あなた」判定と、自分の印だけ消せる判定に使う。
  const myEmail = useTenantStore((s) => s.whoami?.email || "");
  const refreshList = useSharedSessionsStore((s) => s.refresh);
  const cached = transcriptCache.get(sharedSessionId);
  const [turns, setTurns] = useState<SharedTurn[]>(cached?.turns ?? []);
  const [loaded, setLoaded] = useState(!!cached);
  const [hasMore, setHasMore] = useState(cached?.hasMore ?? false);
  const [loadingOlder, setLoadingOlder] = useState(false);
  const [working, setWorking] = useState(false);
  const [error, setError] = useState("");
  const [draft, setDraft] = useState("");
  const [sending, setSending] = useState(false);
  // 上へ読み返している間(自動追従が外れている間)だけ「最新へ」を出す。ミラーと同じ
  // 見た目・同じ文言で、共有側にだけ無いと「下に何か来ているのか」が分からない。
  const [showJump, setShowJump] = useState(false);
  // 共有元が提案した引き継ぎ(propose_session_handoff)。転写に残るのはツール行と定型文
  // だけで、本文は所有者側の別ストアにある — 出さないと「引き継いだ」ことしか分からず、
  // 何を引き継いだのかが共有先には見えない。
  const [handoffs, setHandoffs] = useState<Proposal[]>([]);
  // メンバーから自分宛に届いた引き継ぎ（docs/log/77）。通知の行き先はこの面なので、受け取る口が
  // ここに無いと「引き継ぎが届きました」を押した人が**どこにも辿り着けない**（唯一の口が
  // レール見出しのアイコンだった）。⚠️ セレクタは id/名前だけを返す — offer 自体を返すと
  // 15 秒ごとの取り直しで毎回別オブジェクトになり、読んでいる面が丸ごと再描画される。
  const offerId = useHandoffStore((st) => st.received.find((o) => o.sessionId === sharedSessionId)?.id ?? "");
  const offerFrom = useHandoffStore((st) => st.received.find((o) => o.sessionId === sharedSessionId)?.ownerUserKey ?? "");
  const [offerOpen, setOfferOpen] = useState(false);
  // 在庫はこの面でも自分で持つ（レールが無い切り離しタブでも帯が出るように）。ポーリングは
  // 参照カウントで 1 本に束ねてある。
  useEffect(() => startHandoffPolling(), []);
  // いま出ているモーダル(AskUserQuestion / ExitPlanMode)。所有者向けの応答と同じく転写とは
  // **別枠**で届く — Agent は開いているあいだ、その質問/プランを messages から外して
  // カーソルも手前で止める(hidePendingInteraction)ので、ここを描かないと共有先は
  // 「質問が出ているあいだだけ何も見えない」ことになる。読むだけの面なので答える口は
  // 出さない(押せない要素を出さない — transcript/capabilities.ts)。
  const [pendingQuestions, setPendingQuestions] = useState<Question[] | null>(null);
  const [pendingText, setPendingText] = useState("");
  const [pendingPlan, setPendingPlan] = useState<string | null>(null);
  const seen = useRef(""); // 直近に受け取った提案の中身(同じなら state を触らない)
  const cursor = useRef(cached?.cursor ?? 0);
  const firstLine = useRef(cached?.firstLine ?? 0);
  const bodyRef = useRef<HTMLDivElement>(null);
  // 中身の高さと等しい内側のラッパ。ResizeObserver はこちらも見る(スクロール容器自体は
  // ペインの寸法なので、転写が伸びても鳴らない)。ミラーの .mirror-scroll と同じ役目。
  const scrollBoxRef = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  // 自分が最後に書いた scrollTop。onScroll はこれと比べて「自分のピンの下で中身が伸びた」と
  // 「読者が上へ動かした」を見分ける(ミラーの selfTopRef と同じ — 生の距離で判定すると、
  // 自分で留めた直後に伸びた分を「読者が上へ行った」と誤読して追従が切れる)。
  const selfTop = useRef(0);
  // この時刻までの高さの変化は「読者が自分で広げた(作業過程の開閉など)」ものとして追わない。
  const interactUntil = useRef(0);
  // 位置復元(scrollMark): 戻ってきたときに復元する位置と、復元中かどうか。
  const restoreMark = useRef<ScrollMark | null>(null);
  const restoring = useRef(false);
  // このセッションの初回着地(末尾 or 復元)を済ませたか。
  const didInit = useRef(false);
  // スクロールが止まるたびに控える「いま見ている位置」。離脱時に DOM から採り直せない
  // (下の cleanup の注記)ので、生きているうちに控えておく。
  const pendingMark = useRef<ScrollMark | null>(null);
  const markTimer = useRef(0);
  const loadingOlderRef = useRef(false);
  // Set while prepending older history, to keep the reader's position put (below).
  const anchor = useRef<number | null>(null);

  const path = `api/shared-sessions/${encodeURIComponent(sharedSessionId)}`;
  // 位置の記憶はミラーと同じモジュール内 Map を使う。所有者側はセッション名、こちらは
  // catalog id なので、鍵が混ざらないよう接頭辞を付ける。
  const markKey = `shared:${sharedSessionId}`;
  // 会話へ引いたマーカー（docs/log/69 / ADR 0050）。読むのは RO でもでき、引けるのは RW だけ。
  // 消せるのは自分の印だけ（判定は Agent 側、CP が login id を刻む）。所有者 Workspace が
  // 停止中は転写と同じく取りに行かない。
  const marks = useMarksController({
    path: `${path}/marks`,
    canEdit: meta?.permission === "rw",
    isOwner: false,
    viewerId: myEmail,
    ownerLabel: meta ? ownerLabel(meta) : "",
    youLabel: tr("chat.you"),
    paused: !!meta && meta.workspaceState !== "running",
  });
  // 引き継ぎ提案のポーリング effect から呼ぶ（新しい周期を作らない）。実際の往復は
  // useMarksController 側で間引かれる。
  const marksReloadRef = useRef(marks.reload);
  marksReloadRef.current = marks.reload;

  useEffect(() => {
    const entry = transcriptCache.get(sharedSessionId);
    setTurns(entry?.turns ?? []);
    setLoaded(!!entry);
    setHasMore(entry?.hasMore ?? false);
    setError("");
    // 保留はキャッシュしない(別セッションのモーダルが一瞬残るのを避ける) — 最初の poll で入る。
    setPendingQuestions(null);
    setPendingText("");
    setPendingPlan(null);
    cursor.current = entry?.cursor ?? 0;
    firstLine.current = entry?.firstLine ?? 0;
    atBottom.current = true;
    setShowJump(false); // 別セッションを開いた直後は末尾にいる
    // Kick a list refresh so the header meta fills in if the store is still cold — but
    // never await it. Blocking the first transcript fetch behind a full
    // GET /api/shared-sessions (which probes every owner's Workspace state in turn) was
    // the single biggest reason a shared session took so long to show anything.
    void refreshList();

    let live = true;
    let timer = 0;
    const tick = async () => {
      if (!live) return;
      // Read the owner's Workspace state from the store rather than fetching it: the
      // global 5s poll (store.ts startSharedSessionsPolling) already keeps it current.
      const current = useSharedSessionsStore.getState().sessions.find((x) => x.id === sharedSessionId);
      if (current && current.workspaceState !== "running") {
        setError(tr("share.owner_stopped"));
        timer = window.setTimeout(tick, POLL_STOPPED);
        return;
      }
      // A missing entry is NOT treated as "no access": the store may simply not have
      // loaded yet, and the server is the authority — it answers 404 if the share is gone.
      const first = cursor.current === 0;
      const url = first ? `${path}/messages?since=0&tail=1&limit=${WINDOW}` : `${path}/messages?since=${cursor.current}`;
      const d = await api(url).catch(() => ({ error: { message: tr("share.load_failed") } }));
      if (!live) return;
      if (d?.error) {
        setError(errText(d.error));
      } else {
        setError("");
        setLoaded(true);
        if (typeof d.cursor === "number") cursor.current = d.cursor;
        if (typeof d.firstLine === "number") firstLine.current = d.firstLine;
        if (typeof d.hasMore === "boolean") setHasMore(d.hasMore);
        setWorking(d.status === "working");
        // 保留は毎回サーバが今の状態を返す(増分ポーリングでも窓に依らない)ので、
        // 「入っていない = もう出ていない」。前回の値を残すと、決着したモーダルが
        // 共有先にだけ出しっぱなしになる。
        setPendingQuestions(Array.isArray(d.pendingQuestions) && d.pendingQuestions.length ? (d.pendingQuestions as Question[]) : null);
        setPendingText(typeof d.pendingText === "string" ? d.pendingText : "");
        setPendingPlan(typeof d.pendingPlan === "string" && d.pendingPlan ? d.pendingPlan : null);
        const incoming: SharedTurn[] = Array.isArray(d.messages) ? d.messages : [];
        // reset: 所有者側の転写が縮んだ/差し替わった(圧縮など) — 置き換える。それ以外は
        // idx を鍵にした冪等マージ。同じターンの再送(伸びている最中の assistant ターン)や
        // ページ境界の重なりを、そのまま積み増さないため(mergeTurns の注記を参照)。
        // 質問/プランの tool_use は「訊いた時点」で転写に書かれ、回答は別の行として
        // あとから来る。窓がその2行をまたぐと持っているターンは未回答のままなので、
        // qid を鍵にした全転写マップで後追いに貼る(ミラーと同じ patchAnswers)。
        // 新しいターンが1つも無い poll でも貼る — 貼るべきなのはまさにその場合。
        const answers = d.answers && typeof d.answers === "object" ? (d.answers as Record<string, { text: string; declined?: boolean }>) : null;
        if (d.reset) setTurns(patchAnswers(incoming, answers));
        else setTurns((old) => patchAnswers(incoming.length ? mergeTurns(old, incoming) : old, answers));
      }
      timer = window.setTimeout(tick, d?.status === "working" ? POLL_WORKING : POLL_IDLE);
    };
    void tick();
    return () => {
      live = false;
      window.clearTimeout(timer);
    };
  }, [sharedSessionId, refreshList, tr, path]);

  // 引き継ぎ提案は転写とは別ストアなので、別ポーリングで取る(ミラーの useHandoffProposals と
  // 同じ形)。転写に相乗りさせると CP が所有者 Agent へ毎回2往復することになり、共有履歴の
  // 読み出しコストが倍になる。所有者 Workspace が停止中は転写と同じく取りに行かない。
  useEffect(() => {
    let live = true;
    seen.current = "";
    setHandoffs([]); // 別セッションの提案が一瞬残らないように(ペインはセッションを差し替える)
    const load = async () => {
      const current = useSharedSessionsStore.getState().sessions.find((x) => x.id === sharedSessionId);
      if (current && current.workspaceState !== "running") return;
      marksReloadRef.current();
      const d = await api(`${path}/handoff-proposals`).catch(() => null);
      if (!live || !d || d.error) return; // 一時的な失敗では今出ているカードを消さない
      const next = JSON.stringify(Array.isArray(d.proposals) ? d.proposals : []);
      // 中身が変わっていないなら state を触らない。毎回新しい配列を入れると、5秒ごとに
      // 転写ごと再描画し、末尾追従のレイアウト効果(下の useLayoutEffect)も空振りで回る。
      if (next === seen.current) return;
      seen.current = next;
      setHandoffs(JSON.parse(next) as Proposal[]);
    };
    void load();
    const timer = window.setInterval(() => void load(), POLL_HANDOFF);
    return () => {
      live = false;
      window.clearInterval(timer);
    };
  }, [sharedSessionId, path]);

  // 表示位置の記憶(scrollMark・ミラーと同じ仕組みをそのまま使う)。
  //
  // 初めて開いたときは末尾へ、いちど読んだ会話へ戻ったときは最後に見ていた位置へ。位置は px
  // ではなくターン([data-turn-idx])を基準に持つ — 高さは遅れて確定するうえ、再訪では tail を
  // 取り直すので、同じ px が同じ内容を指す保証が無い。タブが生きている間だけ覚える(リロードで
  // 消える = 次は末尾)のもミラーと同じ。
  useEffect(() => {
    restoreMark.current = loadMark(markKey);
    restoring.current = false;
    didInit.current = false;
    interactUntil.current = 0;
    selfTop.current = 0;
    pendingMark.current = null;
    return () => {
      window.clearTimeout(markTimer.current);
      // 同じペインで別の共有セッションへ持ち替えただけなら、まだ出ていく側の DOM が載って
      // いるので測り直せる。**ペインを閉じた/自分のセッションへ切り替えた場合はこの面ごと
      // unmount され、この後片付けが走る時点で ref は外れている**(測っても rect は全部 0)。
      // ミラーは持ち替えで unmount しないので気づけない差 — そちら側だけを真似ると、
      // 「読みかけで離れて戻る」という一番よくある道で位置が消える。
      saveMark(markKey, captureMark(bodyRef.current, atBottom.current) ?? pendingMark.current);
    };
  }, [markKey]);

  // Keep the module cache in step so the next mount paints from it.
  useEffect(() => {
    if (loaded) {
      transcriptCache.set(sharedSessionId, { turns, cursor: cursor.current, firstLine: firstLine.current, hasMore });
    }
  }, [sharedSessionId, turns, hasMore, loaded]);

  // Older history, one page at a time. The server already supports `before=` (it proxies
  // the query through to the Agent) and the DTO already passes firstLine/hasMore — this
  // just uses them, which is what lets the first fetch stay small.
  const loadOlder = async () => {
    const el = bodyRef.current;
    // 進行中の判定は ref で持つ。state の loadingOlder はボタンの disabled が効くまでに
    // 1 レンダー遅れるので、素早い2連打が同じ `before=` で二重に取りに行き、同じページを
    // 2回積む(「押すと同じ履歴が何度も出てくる」)。
    if (!el || loadingOlderRef.current || firstLine.current <= 0) return;
    loadingOlderRef.current = true;
    setLoadingOlder(true);
    const keep = el.scrollHeight - el.scrollTop; // distance from the bottom, held across the prepend
    const d = await api(`${path}/messages?before=${firstLine.current}&limit=${WINDOW}`).catch(() => null);
    if (d && !d.error) {
      const incoming: SharedTurn[] = Array.isArray(d.messages) ? d.messages : [];
      if (typeof d.firstLine === "number") firstLine.current = d.firstLine;
      setHasMore(!!d.hasMore);
      if (incoming.length) {
        anchor.current = keep;
        // 古いページを先頭に置いて、いま持っている分をマージし直す(idx 昇順)。
        setTurns((old) => mergeTurns(incoming, old));
      }
    }
    loadingOlderRef.current = false;
    setLoadingOlder(false);
  };

  const toBottom = (el: HTMLDivElement) => {
    el.scrollTop = el.scrollHeight;
    selfTop.current = el.scrollTop;
  };

  // スクロールが落ち着いたら位置を控える(離脱時に測り直せないため)。ターンを上から順に
  // 当たるので、毎イベントではなく止まってから1回だけ。
  const rememberMark = () => {
    window.clearTimeout(markTimer.current);
    markTimer.current = window.setTimeout(() => {
      pendingMark.current = captureMark(bodyRef.current, atBottom.current);
    }, MARK_SETTLE_MS);
  };

  // Restore the reading position after a prepend, land on open, and otherwise follow the tail.
  useLayoutEffect(() => {
    const el = bodyRef.current;
    if (!el) return;
    if (anchor.current !== null) {
      el.scrollTop = el.scrollHeight - anchor.current;
      selfTop.current = el.scrollTop;
      anchor.current = null;
      return;
    }
    // このセッションでの初回着地。末尾で離れていた(または初めて開いた)なら末尾、途中まで
    // 読んで離れていたならその位置へ。アンカーのターンが tail ウィンドウに載っていなければ
    // 復元は諦めて末尾へ落とす(どのみち読み直せる位置)。
    if (!didInit.current) {
      if (!turns.length && !loaded) return; // まだ何も無い — 次の更新で着地する
      didInit.current = true;
      const mark = restoreMark.current;
      if (mark && !mark.atBottom && applyMark(el, mark)) {
        selfTop.current = el.scrollTop;
        atBottom.current = false; // 末尾ではない ⇒ 追従は切れ、「最新へ」が出る
        restoring.current = true; // 以後、遅れて入る高さのたびにこのアンカーへ置き直す
        setShowJump(true);
        rememberMark(); // 触らずにまた離れても、この位置を覚えたままにする
        return;
      }
      restoreMark.current = null;
      toBottom(el);
      rememberMark();
      return;
    }
    // handoffs も追従の対象。カードは転写とは別のポーリングで後から届くので、turns だけを
    // 見ていると末尾にいたまま高さだけが増え、その分だけ着地が上にずれる(実測 +263px)。
    if (atBottom.current) toBottom(el);
    // 保留カードも追従の対象。転写の外に足される高さなので、turns だけを見ていると
    // 質問が出た瞬間にその分だけ着地が上へずれる(handoffs と同じ理由)。
  }, [turns, handoffs, loaded, pendingQuestions, pendingPlan, pendingText]);

  // 転写の高さはほぼ全部が遅れて確定する(markdown の流し込み → ハイライト → 画像 decode →
  // web フォント)。発生源を数え上げるのではなく「追従中は末尾を、復元中はアンカーを保つ」で
  // 受ける — ミラーと同じ規則。これが無いと、開いた直後に末尾から数百〜数千 px 手前で
  // 止まったままになる(実測 gap 2096px)。
  useEffect(() => {
    const el = bodyRef.current;
    if (!el || typeof ResizeObserver === "undefined") return;
    const ro = new ResizeObserver(() => {
      if (restoring.current) {
        const mark = restoreMark.current;
        if (mark && !atBottom.current && applyMark(el, mark)) {
          selfTop.current = el.scrollTop;
          return;
        }
        endRestore(); // 末尾追従へ戻った(「最新へ」)か、アンカーが消えた
      }
      if (!atBottom.current) return;
      if (Date.now() < interactUntil.current) return; // 読者自身が広げた分は追わない
      if (el.scrollHeight - el.scrollTop - el.clientHeight < 1) return;
      toBottom(el);
    });
    ro.observe(el);
    if (scrollBoxRef.current) ro.observe(scrollBoxRef.current);
    return () => ro.disconnect();
  }, []);

  // 追従するかは「読者の意図」で決める。生の距離で判定すると、自分で末尾へ留めた直後に伸びた
  // 分を「読者が上へ行った」と読んでしまい、以後の再ピンが全部止まる(ミラーで実測済み)。
  const onScroll = () => {
    const el = bodyRef.current;
    if (!el) return;
    rememberMark();
    const movedUp = el.scrollTop < selfTop.current - 1;
    if (atBottom.current && !movedUp) {
      selfTop.current = el.scrollTop;
      setShowJump((s) => (s === false ? s : false));
      return;
    }
    const stuck = el.scrollHeight - el.scrollTop - el.clientHeight <= NEAR_BOTTOM_PX;
    atBottom.current = stuck;
    if (stuck) selfTop.current = el.scrollTop;
    setShowJump((s) => (s === !stuck ? s : !stuck));
  };

  const endRestore = () => {
    restoring.current = false;
    restoreMark.current = null;
  };

  // 復元を打ち切るのは読者が入力したときだけ。「scrollTop が自分の書いた値とズレた」を根拠に
  // してはいけない — ブラウザのスクロールアンカリングが遅延レイアウトのたびに動かすので、
  // それを「触られた」と読むと目的地の手前で固着する(ミラーで実測)。
  const endRestoreOnInput = () => {
    if (restoring.current) endRestore();
  };

  // 読者自身が起こす reflow(作業過程・思考・ツール実行の開閉)の窓を張る。ポインタとキーの
  // 両方を capture で拾ってから reflow が来る。
  const noteInteraction = () => {
    interactUntil.current = Date.now() + INTERACT_HOLD_MS;
    endRestoreOnInput();
  };

  // 「最新へ」: 末尾へ飛んで自動追従を再開する。
  const jumpToBottom = () => {
    const el = bodyRef.current;
    if (!el) return;
    endRestore();
    atBottom.current = true;
    toBottom(el);
    setShowJump(false);
  };

  const propose = async () => {
    const prompt = draft.trim();
    if (!prompt || sending || !meta) return;
    setSending(true);
    const d = await apiJSON(`api/shared-sessions/${encodeURIComponent(meta.id)}/proposals`, "POST", {
      action: "turn",
      payload: { op: "start", prompt },
    }).catch(() => ({ error: { message: tr("share.proposal_failed") } }));
    setSending(false);
    if (d?.error) setError(errText(d.error));
    else {
      setDraft("");
      setError(tr("share.proposal_sent"));
    }
  };

  const groups = groupTurns(coalesceUserActions(turns));


  // A recipient can read, and nothing else. Every capability the mirror fills in is
  // deliberately absent here — there is no local file to open, no diff pane, no pasted
  // image to fetch from someone else's Workspace, no fork, and no agent of theirs to
  // re-authenticate. The blocks drop those affordances instead of showing dead controls,
  // and fall back to self-contained renderings (tool edits and plans expand in place).
  const caps: TranscriptCaps = {
    agentName: agentOf(meta?.kind).assistantName,
    // 発言者は読み手ではなく共有元。「あなた」のままだと、他人の会話を読んでいるのに
    // 自分が書いたように見える。名乗りは共有元のログイン ID(メールアドレス) — user_key は
    // sanitizeUser を通した正規化キーで、誰のことか読み手に伝わらない。
    userName: meta && ownerLabel(meta),
    expandThinking: expandThinking(settings, meta?.kind),
    // 唯一の例外。読むだけの面でも印は「会話の一部」として要り、RW なら自分でも引ける
    // （マーカーはエージェントを動かさないので、提案→承認には載せない — ADR 0050 決定 4）。
    marks,
  };

  // 表示設定の「共有セッション」テーマ／背景(docs/log/59)。ミラー(.mirrorview)と同じ仕組み:
  // data-theme がこの面だけの基本トークンを切り替え、--chat-bg / --chat-accent はこの面の
  // 実効テーマから導く(アプリ側の色をそのまま持ち込むと、反転した面で浮く)。他人の会話を
  // 読んでいる面を自分のミラーと違う色にできることが、この設定の目的。
  const sharedEff = effectiveTheme(settings.sharedTheme, settings.theme);
  const sharedBg = surfaceBg(settings.sharedColor, sharedEff);
  const sharedAccent = surfaceAccent(settings.sharedColor);
  return (
    <div
      className="shared-view"
      data-theme={settings.sharedTheme !== "inherit" ? settings.sharedTheme : undefined}
      style={{
        // 文字サイズ・フォントは色と違って面ごとに分けない — 読みやすさの好みは読み手のもので、
        // 自分のミラーだけ大きくても意味がない。表示設定「セッションミラー」の値をそのまま渡す
        // (MirrorView と同じ契約)。渡さないと本文 .mirror-turn .markdown が CSS 側の
        // フォールバック 13.5px / system-ui で固まり、設定を変えてもここだけ動かなかった。
        "--chat-font": chatFontStack(settings.chatFont),
        "--chat-size": settings.chatSize + "px",
        ...(sharedBg ? { "--chat-bg": sharedBg } : {}),
        ...(sharedAccent ? { "--chat-accent": sharedAccent } : {}),
      } as CSSProperties}
    >
      <header className="shared-view-head">
        <div className="shared-view-info">
          <div>
            <Icon name="broadcast" /> <strong>{meta?.title || meta?.label || meta?.name || tr("share.shared_sessions")}</strong>
          </div>
          {meta && (
            <small>
              {ownerLabel(meta)} · {tr(meta.permission === "rw" ? "share.permission_rw" : "share.permission_ro")} ·{" "}
              {meta.state}
            </small>
          )}
        </div>
        {headerActions && <span className="view-head-actions">{headerActions}</span>}
      </header>
      {/* 自分宛の引き継ぎ（docs/log/77）。本文と一緒にスクロールさせない —— 転写は末尾に
          追従するので、中に置くと「届いているのに画面外」になる。押すと受信箱がこの 1 件に
          絞って開き、受諾も辞退もそこで完結する。 */}
      {offerId && (
        <div className="shared-view-handoff">
          <Icon name="git-branch" />
          <span className="shared-view-handoff-text">
            <strong>{tr("handoff.banner_title")}</strong>
            {offerFrom && <small>{tr("handoff.from", { who: offerFrom })}</small>}
          </span>
          <Button variant="primary" onClick={() => setOfferOpen(true)}>
            <Icon name="run" /> {tr("handoff.banner_open")}
          </Button>
        </div>
      )}
      {offerOpen && <HandoffInboxModal offerId={offerId} onClose={() => setOfferOpen(false)} />}
      {/* マーカーの一覧（docs/log/69 §69.7）。ミラーと同じ位置＝ヘッド直下の帯に置く。 */}
      <MarkStrip marks={marks} storageKey={`shared:${sharedSessionId}`} />
      <div
        className="shared-view-body"
        // 縦へ送って読む面 — 横へはみ出しても横スワイプを殺さない（app/swipeGuard.ts）。
        data-swipe-y=""
        ref={bodyRef}
        onScroll={onScroll}
        // 位置復元の打ち切り条件。ホイールとタッチはここで拾う — 下の pointerdown/keydown は
        // ホイールでは出ない。
        onWheelCapture={endRestoreOnInput}
        onTouchStartCapture={endRestoreOnInput}
        tabIndex={-1}
      >
        {/* 高さが転写と等しい内側のラッパ。ResizeObserver がこれを見て、遅れて入る高さのたびに
            末尾(または復元中のアンカー)へ置き直す。開閉の操作はここで捕まえて「読者が広げた
            reflow」として観測側へ伝える。 */}
        <div
          className="mirror-scroll"
          ref={scrollBoxRef}
          onPointerDownCapture={noteInteraction}
          onKeyDownCapture={noteInteraction}
        >
          {error && <div className="shared-view-notice">{error}</div>}
          {loaded && hasMore && (
            <div className="mirror-loadmore">
              <button type="button" className="ghost mirror-loadmore-btn" disabled={loadingOlder} onClick={() => void loadOlder()}>
                {loadingOlder ? (
                  <>
                    <Icon name="loading" spin /> {tr("chat.ph_loading")}
                  </>
                ) : (
                  <>
                    <Icon name="chevron-up" /> {tr("mirror.load_earlier")}
                  </>
                )}
              </button>
            </div>
          )}
          {!loaded ? (
            !error && (
              <div className="mirror-empty muted mirror-loading">
                <Icon name="loading" spin /> {tr("chat.ph_loading")}
              </div>
            )
          ) : groups.length === 0 && handoffs.length === 0 && !pendingQuestions && !pendingPlan ? (
            // handoffs.length === 0: 転写が空なら提案カードだけが出すものになる(空表示は
            // TranscriptView ごと飛ばしてしまうので、ミラーと同じ条件にする)。
            <div className="mirror-empty muted">{tr("mirror.no_history")}</div>
          ) : (
            <TranscriptView
              groups={groups}
              caps={caps}
              working={working}
              autoCollapseWork={atBottom.current}
              // 読むだけの面なので、編集・破棄・起動は出さない(押せない要素を出さない)。
              // 置き場所はミラーと同じ「提案された時点」— 末尾に固定すると、以後の会話が
              // ずっとカードの裏に隠れる(handoffPlacement の注記)。
              inlineCards={handoffs.map((h) => ({
                at: h.created_at,
                node: (
                  <HandoffCard key={"handoff-" + h.id} launched={!!h.launched_at} intro={tr("share.handoff_intro")}>
                    <HandoffBody title={h.title} prompt={h.prompt} />
                  </HandoffCard>
                ),
              }))}
            />
          )}
          {/* いま出ているモーダル。転写からは外されて別枠で届くので(上の pendingQuestions
              の注記)、ここで描かないと共有先はモーダルが開いているあいだ何も見えない。
              決着すると Agent がその行をカーソルごと出し直し、決定済みのカードとして
              転写側に現れる — 入れ替わりであって二重にはならない。 */}
          {pendingPlan && (
            <div className="mirror-turn assistant">
              <div className="mirror-turn-head">
                <span className="mt-who">{caps.agentName}</span>
                <span className="mt-model muted">{tr("mirror.plan_pending")}</span>
              </div>
              <div className="mirror-turn-body">
                {/* 承認・却下は所有者の判断。pending を渡すとその2つのボタンが出るので
                    渡さない(押せない要素を出さない) — 本文はその場で開ける。 */}
                <PlanBlock plan={pendingPlan} />
              </div>
            </div>
          )}
          {pendingQuestions && (
            <div className="mirror-turn assistant">
              <div className="mirror-turn-head">
                <span className="mt-who">{caps.agentName}</span>
                <span className="mt-model muted">{tr("mirror.questioning")}</span>
              </div>
              <div className="mirror-turn-body">
                {pendingText && <MarkdownView source={pendingText} />}
                {/* 転写に残った質問と同じ不活性カード。選択肢も preview もそのまま読めて、
                    答える口だけが無い(答えるのは所有者)。 */}
                <QuestionBlock questions={pendingQuestions} />
              </div>
            </div>
          )}
          {showJump && (
            // ミラーと同じ sticky ピル(mirror.css)。高さ0の帯なのでスクロール量を増やさない。
            // .mirror-jump-row は飾りではなく位置そのもの: これが無いとボタンは高さ0の帯の中に
            // in-flow で置かれ、左端に寄り、箱は帯より下へはみ出す(実測 左1px / 下端を 13px
            // はみ出し / スクロール量 +24px)。row(absolute bottom:0 + center)を挟むと
            // ミラーと同じ「中央・下端から 11px・スクロール量そのまま」になる。
            <div className="mirror-jump-wrap">
              <div className="mirror-jump-row">
                <button
                  type="button"
                  className="mirror-jump"
                  onClick={jumpToBottom}
                  title={tr("mirror.jump_latest")}
                  aria-label={tr("mirror.jump_latest")}
                >
                  <Icon name="arrow-down" /> {tr("mirror.jump_latest")}
                </button>
              </div>
            </div>
          )}
        </div>
      </div>
      {meta?.permission === "rw" && meta.workspaceState === "running" && (
        <div className="shared-propose">
          <textarea value={draft} onChange={(e) => setDraft(e.target.value)} placeholder={tr("share.proposal_placeholder")} />
          <Button variant="primary" icon="send" disabled={!draft.trim() || sending} onClick={() => void propose()}>
            {tr("share.propose")}
          </Button>
          <small>{tr("share.owner_approval_note")}</small>
        </div>
      )}
    </div>
  );
}
