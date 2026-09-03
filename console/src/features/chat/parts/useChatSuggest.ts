import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent, RefObject } from "react";
import { errText } from "../../../core/api/client.ts";
import { useDragScroll } from "../../../lib/dragScroll.ts";
import { t } from "../../../lib/i18n/index.ts";
import { coarsePointer } from "../../../lib/device.ts";
import { setSetting, type Settings } from "../../../lib/settings.ts";
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
import { suggestFilterDraft, cycledSuggestion, type SuggestCycle } from "../../../lib/suggestCycle.ts";
import { useChipMenu } from "../../mirror/SuggestChipMenu.tsx";
import { chatSuggestReplies } from "../api.ts";
import { splitPastedImages } from "../../../lib/pastedImages.ts";
import type { Conversation } from "../../../types/chat.ts";

// useChatSuggest owns the reply-suggestion strip above the composer: the candidate list
// (✨ = on-demand LLM candidates merged ahead of lib/quickReplies' learned ones), the
// pin/forget menu, and the keyboard focus ring the chips live in. ChatView keeps the
// composer state itself and hands in what the suggestions have to act on.
export function useChatSuggest({
  conv,
  conversationId,
  input,
  settings,
  modSend,
  inputRef,
  isStreaming,
  setInput,
  setHistIdx,
  send,
  toast,
}: {
  conv: Conversation | null;
  conversationId: string | null;
  input: string;
  settings: Settings;
  modSend: boolean;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  /** 送信中/再接続中は差し込みも即送信もしない（呼び出し時に評価する）。 */
  isStreaming: () => boolean;
  setInput: (v: string) => void;
  setHistIdx: (v: number | null) => void;
  send: (override?: string) => void;
  toast: (msg: string) => void;
}) {
  // 返信サジェスト v2: ✨ボタンで取得した LLM 候補（Layer A のチップ列にマージ）と取得中フラグ。
  const [llmSuggestions, setLlmSuggestions] = useState<string[]>([]);
  const [suggesting, setSuggesting] = useState(false);
  // 入力途中の Tab 補完サイクル（lib/suggestCycle）。null = サイクル中でない。
  const [cycle, setCycle] = useState<SuggestCycle | null>(null);
  const suggestRef = useRef<HTMLDivElement>(null); // チップ行（Tab でここへフォーカスを移す）
  // 1行に収めた候補列をマウスのドラッグ/縦ホイールで左右スクロール（スワイプは既定動作）。
  // チップ行はストリーミング中に消えて戻るので、返り値のコールバック ref で付け替える
  // （ref オブジェクト任せだと戻ってきた要素にリスナーが付かない — dragScroll.ts の注記）。
  const attachSuggestRow = useDragScroll(suggestRef);
  // チップの右クリック / 長タップ / Menu キーで開くメニュー（ピン留め・削除）。MirrorView と共有。
  const chipMenu = useChipMenu();

  // 返信サジェスト（lib/quickReplies）。直近アシスタント発話を B-1 の文脈にし、頻度学習と統合。
  let chatLastReply = "";
  const msgs = conv?.messages ?? [];
  for (let i = msgs.length - 1; i >= 0; i--) {
    if (msgs[i].role === "assistant") {
      chatLastReply = splitPastedImages(msgs[i].content).text.trim();
      break;
    }
  }
  // Tab 補完サイクル中は、絞り込みキーを「ユーザーが打った文字」に凍結する（入力欄は補完で
  // 候補そのものに変わっているので、そのまま渡すとチップ列が痩せてサイクルが崩れる）。
  const suggestDraft = suggestFilterDraft(cycle, input);
  const cycledText = cycledSuggestion(cycle, input); // いま入力欄に入っている候補（強調用）
  const learned = settings.quickRepliesEnabled
    ? rankQuickReplies(settings.quickReplies || {}, {
        draft: suggestDraft,
        lastReply: chatLastReply,
        locale: settings.locale,
        hidden: settings.quickRepliesHidden || [],
        pinned: settings.quickRepliesPinned || [],
        limit: 20, // チップ行は横スクロールなので、画面幅に収まらない分は流して見せる（ピンは別枠）
      })
    : [];
  // v2 の LLM 候補を先頭に、Layer A の学習候補を後ろにマージ（重複は畳む）。llm フラグで見た目を分ける。
  // 重複判定は学習キーと同じ畳み方（大小・空白に加えて全角半角）で行う。
  const llmSet = new Set(llmSuggestions.map((s) => quickReplyKey(s)));
  const suggestChips: { text: string; llm: boolean }[] = [
    ...llmSuggestions.map((text) => ({ text, llm: true })),
    ...learned.filter((s) => !llmSet.has(quickReplyKey(s))).map((text) => ({ text, llm: false })),
  ];
  // 会話が進む（新しい回答が来る）と古い LLM 候補は文脈遅れ。直近回答と会話切替で捨てる。
  useEffect(() => {
    setLlmSuggestions([]);
  }, [conversationId, chatLastReply]);
  // Tab 補完でたどっている候補が、1行スクロールのチップ行からはみ出していたら見える位置へ。
  useEffect(() => {
    if (!cycledText) return;
    const el = suggestRef.current?.querySelector<HTMLElement>(".chat-suggest-chip.cycling");
    // scrollIntoView は Chrome 150 で Promise を返す — 暗黙 return にすると effect の
    // クリーンアップ扱いで落ちるので、必ずブロック本体で捨てる（effect-implicit-return）。
    if (el) {
      el.scrollIntoView({ block: "nearest", inline: "nearest" });
    }
  }, [cycledText]);
  // サジェストのチップ: 通常クリックはコンポーサーへ差し込み、⌥/Alt で即送信（MirrorView と同挙動）。
  const applySuggestion = (text: string, immediate: boolean) => {
    if (isStreaming()) return;
    if (immediate) {
      void send(text);
      return;
    }
    setInput(text);
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
  // メニューの「この候補を消す」: 学習を消し、隠しリストへ積み、ピンも外す（消すだけではシード/
  // 再学習で戻る）。LLM 候補（✨）は学習物ではないので、その場の候補列から外すだけ。
  const forgetSuggestion = (text: string, llm: boolean) => {
    if (llm) {
      setLlmSuggestions((prev) => prev.filter((s) => s !== text));
      return;
    }
    setSetting("quickReplies", forgetQuickReply(settings.quickReplies || {}, text));
    setSetting("quickRepliesHidden", hideQuickReply(settings.quickRepliesHidden || [], text));
    setSetting("quickRepliesPinned", unpinQuickReply(settings.quickRepliesPinned || [], text));
  };
  // メニューの「常に表示（ピン留め）」/「ピン留めを解除」。MirrorView と同じ扱い（ピンは隠しより
  // 強い意思表示なので、ピンするときは隠しを解除する）。
  const togglePin = (text: string) => {
    const pinned = settings.quickRepliesPinned || [];
    if (isQuickReplyPinned(pinned, text)) {
      setSetting("quickRepliesPinned", unpinQuickReply(pinned, text));
      return;
    }
    setSetting("quickRepliesPinned", pinQuickReply(pinned, text));
    setSetting("quickRepliesHidden", unhideQuickReply(settings.quickRepliesHidden || [], text));
  };
  // v2: ✨ボタン — 会話ログを一発ヘッドレス LLM に渡し、文脈に沿った返信候補をチップ列にマージ
  // （chat_suggest_reply.go）。会話が確定していない（下書き）ときは押せない。
  const fetchLlmSuggestions = async () => {
    if (!conversationId || suggesting) return;
    setSuggesting(true);
    try {
      const j = await chatSuggestReplies(conversationId);
      // apiJSON はサーバエラーを {error} で解決する — 失敗を「候補なし」トーストに化けさせない。
      if (j?.error) {
        toast(errText(j.error) || t("chat.suggest_failed"));
        return;
      }
      const list = Array.isArray(j?.suggestions) ? j.suggestions.filter((x): x is string => typeof x === "string") : [];
      setLlmSuggestions(list);
      if (!list.length) toast(t("chat.suggest_none"));
    } catch {
      toast(t("chat.suggest_failed"));
    } finally {
      setSuggesting(false);
    }
  };

  // 返信サジェストのフォーカスリング = ✨ボタン＋候補チップ（DOM 順）。MirrorView と同挙動。
  const suggestRing = (): HTMLButtonElement[] =>
    Array.from(suggestRef.current?.querySelectorAll<HTMLButtonElement>("button") ?? []);

  // チップ行は1行スクロールなので、キー移動のフォーカス先が隠れないよう横だけ最小限追従させる
  // （focus 既定のスクロールは縦にも効いて本文が飛ぶため preventScroll で殺す）。
  const focusRingItem = (el: HTMLButtonElement) => {
    el.focus({ preventScroll: true });
    el.scrollIntoView({ block: "nearest", inline: "nearest" });
  };

  // リング内の移動。Tab/Shift+Tab は「候補＋入力欄」を一巡（端まで来たら入力欄へ戻る）。
  // ←/→ は候補内だけで循環。Escape で入力欄へ。処理したら true。
  const onSuggestNav = (e: KeyboardEvent<HTMLButtonElement>): boolean => {
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
  // 送信キー設定に合わせる: modSend なら mod+Enter=送信・素の Enter=差し込み、enter モードなら逆。
  const onSuggestKeyDown = (e: KeyboardEvent<HTMLButtonElement>, text: string, llm: boolean) => {
    if (onSuggestNav(e)) return;
    if (chipMenu.onKeyDown(e, text, llm)) return; // Menu キー / Shift+F10 → ピン留め・削除メニュー
    if (e.key !== "Enter" || e.nativeEvent.isComposing) return;
    const mod = e.ctrlKey || e.metaKey;
    e.preventDefault(); // ボタン既定の click（＝差し込み）と二重発火させない
    applySuggestion(text, modSend ? mod : !mod);
  };

  return {
    suggestRef,
    attachSuggestRow,
    chipMenu,
    suggesting,
    suggestChips,
    cycle,
    setCycle,
    cycledText,
    applySuggestion,
    forgetSuggestion,
    togglePin,
    fetchLlmSuggestions,
    suggestRing,
    focusRingItem,
    onSuggestNav,
    onSuggestKeyDown,
  };
}
