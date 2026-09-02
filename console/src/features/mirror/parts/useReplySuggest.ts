import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent as RKeyboardEvent, RefObject } from "react";
import { apiJSON } from "../../../core/api/client.ts";
import { t as tr } from "../../../lib/i18n/index.ts";
import { coarsePointer } from "../../../lib/device.ts";
import { useDragScroll } from "../../../lib/dragScroll.ts";
import { setSetting } from "../../../lib/settings.ts";
import type { Settings } from "../../../lib/settings.ts";
import {
  rankQuickReplies,
  forgetQuickReply,
  hideQuickReply,
  unhideQuickReply,
  pinQuickReply,
  unpinQuickReply,
  isQuickReplyPinned,
  quickReplyKey,
} from "../../../lib/quickReplies.ts";
import {
  stepSuggestCycle,
  suggestFilterDraft,
  cycledSuggestion,
  type SuggestCycle,
} from "../../../lib/suggestCycle.ts";
import { useChipMenu } from "../SuggestChipMenu.tsx";
import type { SuggestChip } from "./SuggestRow.tsx";

const q = encodeURIComponent;

/**
 * 返信サジェスト（lib/quickReplies ＋ v2 の LLM 候補）。チップ列そのもの・そのキーボード
 * リング・学習の増減（消す/ピン留め）・✨の on-demand 取得をまとめて持つ。
 *
 * 呼び出しは `lastReplyText` が確定したあと（候補の文脈がそれなので）。MirrorView から
 * そのまま移送したもので、判断は 1 つも変えていない。
 */
export function useReplySuggest({
  session,
  settings,
  draft,
  setDraft,
  setHistIdx,
  inputRef,
  composerLocked,
  modSend,
  lastReplyText,
  send,
  toast,
  wsDown,
}: {
  session: string;
  settings: Settings;
  draft: string;
  setDraft: (v: string) => void;
  setHistIdx: (v: number | null) => void;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  composerLocked: boolean;
  /** Ctrl/⌘+Enter で送信する設定か（チップ上の Enter の役割をこれに合わせる）。 */
  modSend: boolean;
  lastReplyText: string;
  send: (override?: string) => Promise<void>;
  toast: (m: string) => void;
  /** Workspace が停止していれば true を返し、トーストも出す（副作用つき）。 */
  wsDown: () => boolean;
}) {
  // 返信サジェスト v2: ✨ボタンで取得した LLM 文脈候補（Layer A のチップ列にマージ）と取得中フラグ。
  const [llmSuggestions, setLlmSuggestions] = useState<string[]>([]);
  const [suggesting, setSuggesting] = useState(false);
  // 入力途中の Tab 補完サイクル（lib/suggestCycle）。null = サイクル中でない。
  const [cycle, setCycle] = useState<SuggestCycle | null>(null);
  const suggestRef = useRef<HTMLDivElement>(null); // チップ行（Tab でここへフォーカスを移す）
  // 1行に収めた候補列をマウスのドラッグ/縦ホイールで左右スクロール（スワイプは既定動作）。
  // 返り値をチップ行の ref に渡す — この行は条件付きレンダーで出入りするので、ref オブジェクト
  // 任せだと戻ってきた要素にリスナーが付かない（dragScroll.ts の注記）。
  const attachSuggestRow = useDragScroll(suggestRef);
  // チップの右クリック / 長タップ / Menu キーで開くメニュー（ピン留め・削除）。
  const chipMenu = useChipMenu();


  // 返信サジェストのチップ: 通常クリックはコンポーサーへ差し込み（編集してから Enter）、
  // ⌥/Alt 併用で即送信。差し込み時はキャレットを末尾に置いてフォーカスする。
  const applySuggestion = (text: string, immediate: boolean) => {
    if (composerLocked) return;
    if (immediate) {
      void send(text);
      return;
    }
    setDraft(text);
    setHistIdx(null);
    // スマホ: チップ差し込みで textarea にフォーカスすると GBoard が開いて画面を覆う。タッチ端末では
    // フォーカスしない（キーボードを出さない）— ユーザーは送信 or タップして編集を選べる。
    if (coarsePointer()) {
      inputRef.current?.blur(); // 既に開いていたキーボードも畳む
      return;
    }
    requestAnimationFrame(() => {
      const el = inputRef.current;
      if (el) {
        el.focus();
        el.setSelectionRange(el.value.length, el.value.length);
      }
    });
  };

  // メニューの「この候補を消す」: 学習を消し、かつ隠しリストへ積む（消すだけではシード/再学習で
  // 戻ってくる）。ピン留めしていたなら当然そのピンも外す。LLM 候補（✨）は学習物ではないので、
  // その場の候補列から外すだけでよい。
  const forgetSuggestion = (text: string, llm: boolean) => {
    if (llm) {
      setLlmSuggestions((prev) => prev.filter((s) => s !== text));
      return;
    }
    setSetting("quickReplies", forgetQuickReply(settings.quickReplies || {}, text));
    setSetting("quickRepliesHidden", hideQuickReply(settings.quickRepliesHidden || [], text));
    setSetting("quickRepliesPinned", unpinQuickReply(settings.quickRepliesPinned || [], text));
  };

  // メニューの「常に表示（ピン留め）」/「ピン留めを解除」。ピンは隠しより強い意思表示なので、
  // ピンするときは隠しも外す（以前に消した文をピンし直せる）。✨の候補もそのままピンできる
  // ——「この一文はこれから常用する」と決めた時点で、学習を待つ理由がない。
  const togglePin = (text: string) => {
    const pinned = settings.quickRepliesPinned || [];
    if (isQuickReplyPinned(pinned, text)) {
      setSetting("quickRepliesPinned", unpinQuickReply(pinned, text));
      return;
    }
    setSetting("quickRepliesPinned", pinQuickReply(pinned, text));
    setSetting("quickRepliesHidden", unhideQuickReply(settings.quickRepliesHidden || [], text));
  };

  // v2: ✨ボタン — 直近の会話ログを一発ヘッドレス LLM に渡し、文脈に沿った返信候補を取得して
  // チップ列にマージする（session_suggest_reply.go）。押した時だけトークンを使う on-demand。
  const fetchLlmSuggestions = async () => {
    if (!session || suggesting || wsDown()) return;
    setSuggesting(true);
    try {
      const j = await apiJSON(`api/sessions/${q(session)}/suggest-replies`, "POST", {});
      const list = Array.isArray(j?.suggestions) ? (j.suggestions as unknown[]).filter((x): x is string => typeof x === "string") : [];
      // LLM が同文を重複して返すことがある — チップの React key は本文由来なので畳んでおく。
      setLlmSuggestions([...new Set(list)]);
      // 候補ゼロ = バックエンド不在（claude/codex/opencode いずれも無い）か会話が浅い。無反応だと
      // 壊れて見えるので一言知らせる（Layer A のチップはそのまま残る）。
      if (!list.length) toast(tr("mirror.suggest_none"));
    } catch {
      toast(tr("mirror.suggest_failed")); // 生成失敗（機能OFF含む）— 学習チップはそのまま
    } finally {
      setSuggesting(false);
    }
  };

  // 返信サジェストのフォーカスリング = ✨ボタン＋候補チップ（DOM 順）。✨も候補の一員として
  // 巡回に含める（Enter はボタン既定の click ＝ LLM 候補取得がそのまま走る）。
  const suggestRing = (): HTMLButtonElement[] =>
    Array.from(suggestRef.current?.querySelectorAll<HTMLButtonElement>("button") ?? []);

  // チップ行は1行スクロール（はみ出した候補は画面外）。キー移動のフォーカス先が隠れないよう
  // 横だけ最小限スクロールして追従させる。focus 既定のスクロールは縦にも効いて本文が飛ぶので
  // preventScroll で殺し、inline/block:nearest の scrollIntoView で必要分だけ動かす。
  const focusRingItem = (el: HTMLButtonElement) => {
    el.focus({ preventScroll: true });
    el.scrollIntoView({ block: "nearest", inline: "nearest" });
  };

  // リング内の移動。Tab/Shift+Tab は「候補＋入力欄」を一巡（端まで来たら入力欄へ戻る＝
  // 入力欄→候補1→候補2→入力欄…のループ）。←/→ は候補内だけで循環。Escape で入力欄へ。
  // 処理したら true を返し、呼び出し側はそこで打ち切る。
  const onSuggestNav = (e: RKeyboardEvent<HTMLButtonElement>): boolean => {
    if (e.nativeEvent.isComposing) return false;
    if (e.key === "Escape") {
      e.preventDefault();
      inputRef.current?.focus();
      return true;
    }
    const ring = suggestRing();
    const i = ring.indexOf(e.currentTarget);
    if (i < 0 || !ring.length) return false;
    if (e.key === "Tab") {
      e.preventDefault();
      const next = e.shiftKey ? i - 1 : i + 1;
      if (next < 0 || next >= ring.length) inputRef.current?.focus(); // 端 → 入力欄へ戻る
      else focusRingItem(ring[next]);
      return true;
    }
    if (e.key === "ArrowRight" || e.key === "ArrowLeft") {
      e.preventDefault();
      const d = e.key === "ArrowRight" ? 1 : -1;
      focusRingItem(ring[(i + d + ring.length) % ring.length]); // ←/→ は候補内で循環
      return true;
    }
    return false;
  };

  // チップ上のキー操作。移動系は onSuggestNav に委ね、Enter/Ctrl(⌘)+Enter の役割はコンポーサーの
  // 送信キー設定に合わせる: modSend（Ctrl+Enter で送信）なら mod+Enter=送信・素の Enter=差し込み、
  // enter モード（Enter で送信）なら逆。
  const onSuggestKeyDown = (e: RKeyboardEvent<HTMLButtonElement>, text: string, llm: boolean) => {
    if (onSuggestNav(e)) return;
    if (chipMenu.onKeyDown(e, text, llm)) return; // Menu キー / Shift+F10 → ピン留め・削除メニュー
    if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
    const mod = e.ctrlKey || e.metaKey;
    e.preventDefault(); // ボタン既定の click（＝差し込み）と二重発火させない
    applySuggestion(text, modSend ? mod : !mod);
  };

  // 入力欄での Tab（チップ行への入場／補完サイクル）。処理したら true を返す。
  const handleKeyDown = (e: RKeyboardEvent): boolean => {
    // 入力欄が空なら Tab で返信サジェストへ入る（＝入力欄→候補1→候補2→入力欄…のループ）。
    // 素の Tab は最初の「候補チップ」から始める（先頭の✨は飛ばす／Shift+Tab で戻れる）。
    // Shift+Tab は逆回りなのでリング末尾から入る。テキストがあるときは従来どおりの Tab。
    if (e.key === "Tab" && !e.nativeEvent.isComposing && draft === "") {
      const ring = suggestRing();
      const target = e.shiftKey
        ? ring[ring.length - 1]
        : suggestRef.current?.querySelector<HTMLButtonElement>(".mirror-suggest-chip");
      if (target) {
        e.preventDefault();
        focusRingItem(target);
        return true;
      }
    }
    // 入力途中の Tab は候補の補完サイクル（シェル流）。打った文字に前方一致する候補＝チップ行に
    // 見えているものを順に入力欄へ入れ、一周したら自分が打った文字へ戻る。Shift+Tab は逆回り。
    // 補完できる候補が無ければ何もせず、従来どおりの Tab（フォーカス移動）に落とす。
    if (e.key === "Tab" && !e.nativeEvent.isComposing && draft !== "" && !composerLocked) {
      const next = stepSuggestCycle(cycle, draft, suggestChips.map((c) => c.text), e.shiftKey);
      if (next) {
        e.preventDefault();
        setCycle(next);
        setDraft(next.text);
        setHistIdx(null);
        // 値の差し替えでキャレットが動く（先頭に残る）ブラウザがあるので末尾に置き直す。
        requestAnimationFrame(() => {
          const el = inputRef.current;
          if (el) el.setSelectionRange(el.value.length, el.value.length);
        });
        return true;
      }
    }
    return false;
  };

  // 返信サジェスト（lib/quickReplies）。直近回答の最終テキストを B-1 ヒューリスティックの
  // 文脈に、頻度学習（settings.quickReplies）と合わせて候補化する。
  // Tab 補完サイクル中は、絞り込みキーを「ユーザーが打った文字」に凍結する（入力欄は補完で
  // 候補そのものに変わっているので、そのまま渡すとチップ列が1件に痩せてサイクルが崩れる）。
  const suggestDraft = suggestFilterDraft(cycle, draft);
  const cycledText = cycledSuggestion(cycle, draft); // いま入力欄に入っている候補（強調用）
  const learned = settings.quickRepliesEnabled
    ? rankQuickReplies(settings.quickReplies || {}, {
        draft: suggestDraft,
        lastReply: lastReplyText,
        locale: settings.locale,
        hidden: settings.quickRepliesHidden || [],
        pinned: settings.quickRepliesPinned || [],
        limit: 20, // チップ行は横スクロールなので、画面幅に収まらない分は流して見せる（ピンは別枠）
      })
    : [];
  // v2 の LLM 候補を先頭に、Layer A の学習候補を後ろにマージ（重複は畳む）。llm フラグで見た目を分ける。
  // 重複判定は学習キーと同じ畳み方（大小・空白に加えて全角半角）で行う。
  const llmSet = new Set(llmSuggestions.map((s) => quickReplyKey(s)));
  const suggestChips: SuggestChip[] = [
    ...llmSuggestions.map((text) => ({ text, llm: true })),
    ...learned.filter((s) => !llmSet.has(quickReplyKey(s))).map((text) => ({ text, llm: false })),
  ];
  // Tab 補完でたどっている候補が、1行スクロールのチップ行からはみ出していたら見える位置へ。
  // 入力欄のフォーカスは動かさないので scrollIntoView だけ（横方向の最小限）。
  useEffect(() => {
    if (!cycledText) return;
    const el = suggestRef.current?.querySelector<HTMLElement>(".mirror-suggest-chip.cycling");
    // scrollIntoView は Chrome 150 で Promise を返す — 暗黙 return にすると effect の
    // クリーンアップ扱いで落ちるので、必ずブロック本体で捨てる（effect-implicit-return）。
    if (el) {
      el.scrollIntoView({ block: "nearest", inline: "nearest" });
    }
  }, [cycledText]);
  // 会話が進む（新しい回答が来る）と古い LLM 候補は文脈遅れになるので、直近回答の変化とセッション
  // 切替で捨てる。lastReplyText 確定後に置くことで依存の TDZ を避ける。
  useEffect(() => {
    setLlmSuggestions([]);
  }, [session, lastReplyText]);

  return {
    chips: suggestChips,
    cycledText,
    suggesting,
    rowRef: attachSuggestRow,
    chipMenu,
    applySuggestion,
    forgetSuggestion,
    togglePin,
    fetchLlmSuggestions,
    onNav: onSuggestNav,
    onChipKeyDown: onSuggestKeyDown,
    handleKeyDown,
  };
}
