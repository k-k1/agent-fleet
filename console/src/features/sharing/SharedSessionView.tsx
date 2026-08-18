import { useEffect, useLayoutEffect, useRef, useState } from "react";
import type { CSSProperties, ReactNode } from "react";
import { api, apiJSON, errText } from "../../core/api/client.ts";
import { Icon } from "../../ui/Icon.tsx";
import { Button } from "../../ui/Button.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { agentOf } from "../../agents/registry.ts";
import { chatFontStack, effectiveTheme, expandThinking, surfaceAccent, surfaceBg, useSettings } from "../../lib/settings.ts";
import { HandoffBody, HandoffCard, type Proposal } from "../mirror/HandoffProposal.tsx";
import { TranscriptView } from "../mirror/transcript/TranscriptView.tsx";
import type { TranscriptCaps } from "../mirror/transcript/capabilities.ts";
import type { Turn } from "../mirror/transcript/types.ts";
import { coalesceUserActions, groupTurns, mergeTurns } from "../mirror/transcript/model.ts";
import { ownerLabel, useSharedSessionsStore } from "./store.ts";
import "./sharing.css";

// SharedSessionView — the RECIPIENT's read of a session somebody else owns (docs/59).
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
// (docs/59 §3, control-plane/session_share.go sharedTranscriptDTO).

// Page size, in transcript LINES (claude) / turns (store-backed agents) — the same
// window the mirror asks for. It used to be 60 for a faster first paint, but 60 claude
// jsonl lines is often a fraction of ONE exchange (a single answer spans a thinking
// line, every tool call and the reply), so the opening screen could start mid-answer
// with the prompt that caused it out of frame, and 以前の会話を読み込む had to be
// pressed over and over. The first-paint cost that motivated 60 was the per-request
// inventory sync, which is now throttled per owner (docs/59 §3).
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
  const seen = useRef(""); // 直近に受け取った提案の中身(同じなら state を触らない)
  const cursor = useRef(cached?.cursor ?? 0);
  const firstLine = useRef(cached?.firstLine ?? 0);
  const bodyRef = useRef<HTMLDivElement>(null);
  const atBottom = useRef(true);
  const loadingOlderRef = useRef(false);
  // Set while prepending older history, to keep the reader's position put (below).
  const anchor = useRef<number | null>(null);

  const path = `api/shared-sessions/${encodeURIComponent(sharedSessionId)}`;

  useEffect(() => {
    const entry = transcriptCache.get(sharedSessionId);
    setTurns(entry?.turns ?? []);
    setLoaded(!!entry);
    setHasMore(entry?.hasMore ?? false);
    setError("");
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
        const incoming: SharedTurn[] = Array.isArray(d.messages) ? d.messages : [];
        // reset: 所有者側の転写が縮んだ/差し替わった(圧縮など) — 置き換える。それ以外は
        // idx を鍵にした冪等マージ。同じターンの再送(伸びている最中の assistant ターン)や
        // ページ境界の重なりを、そのまま積み増さないため(mergeTurns の注記を参照)。
        if (d.reset) setTurns(incoming);
        else if (incoming.length) setTurns((old) => mergeTurns(old, incoming));
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

  // Restore the reading position after a prepend, and otherwise follow the tail.
  useLayoutEffect(() => {
    const el = bodyRef.current;
    if (!el) return;
    if (anchor.current !== null) {
      el.scrollTop = el.scrollHeight - anchor.current;
      anchor.current = null;
      return;
    }
    if (atBottom.current) el.scrollTop = el.scrollHeight;
    // handoffs も追従の対象。カードは転写とは別のポーリングで後から届くので、turns だけを
    // 見ていると末尾にいたまま高さだけが増え、その分だけ着地が上にずれる(実測 +263px)。
  }, [turns, handoffs]);

  const onScroll = () => {
    const el = bodyRef.current;
    if (!el) return;
    const stuck = el.scrollHeight - el.scrollTop - el.clientHeight <= NEAR_BOTTOM_PX;
    atBottom.current = stuck;
    setShowJump((s) => (s === !stuck ? s : !stuck));
  };

  // 「最新へ」: 末尾へ飛んで自動追従を再開する。
  const jumpToBottom = () => {
    const el = bodyRef.current;
    if (!el) return;
    el.scrollTop = el.scrollHeight;
    atBottom.current = true;
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
  };

  // 表示設定の「共有セッション」テーマ／背景(docs/59)。ミラー(.mirrorview)と同じ仕組み:
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
      <div className="shared-view-body" ref={bodyRef} onScroll={onScroll} tabIndex={-1}>
        <div className="mirror-scroll">
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
          ) : groups.length === 0 && handoffs.length === 0 ? (
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
