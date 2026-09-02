import { useEffect, useRef, useState } from "react";
import type { KeyboardEvent as RKeyboardEvent, RefObject } from "react";
import { sessionSkills } from "../../../core/api/client.ts";
import type { SessionSkill } from "../../../core/api/client.ts";
import { t as tr } from "../../../lib/i18n/index.ts";
import { coarsePointer } from "../../../lib/device.ts";
import { useDismiss } from "../../../lib/useDismiss.ts";
import type { AgentDescriptor } from "../../../agents/registry.ts";
import {
  applySkillToDraft,
  exactSkills,
  filterSkills,
  hasTriggerHead,
  pickerTokenAt,
  slashTokenAt,
  type SlashToken,
} from "../skillPicker.ts";

/**
 * スキルピッカー（docs/log/50 / ADR0034、v2 クロスエージェント＋§8 クロススキル注入）:
 * セッションで呼べるスキル/コマンドの補完リスト。ネイティブ起動（invoke — "/name" や
 * codex "$name"）に加え、他規約の SKILL.md（foreign — path/origin 付き）は「path を
 * 読んで指示に従え」プロンプトとして差し込む — ただの指示文なので kind/ドライバ不問。
 * 開き方は 2 系統 — 先頭トリガ文字のタイプ（キーボード派。skillTrigger="" の kind は
 * ボタンのみ）と専用ボタン（マウス/タップ派）。選択はフォーカスを textarea に残す
 * sel-index 方式（CommandPalette と同型）。managed 発火未検証の kind（opencode）は
 * ネイティブ項目だけ slashSkillsManaged=false で落とす — foreign はゲート対象外。
 *
 * MirrorView からそのまま移送したもので、判断は 1 つも変えていない。composerLocked を
 * 見るので、呼び出しは composerLocked が決まったあと（＝コンポーサーの下ごしらえの後）。
 */
export function useSkillPicker({
  session,
  agent,
  managed,
  draft,
  setDraft,
  setHistIdx,
  inputRef,
  composerLocked,
}: {
  session: string;
  agent: AgentDescriptor;
  managed: boolean;
  draft: string;
  setDraft: (v: string) => void;
  setHistIdx: (v: number | null) => void;
  inputRef: RefObject<HTMLTextAreaElement | null>;
  composerLocked: boolean;
}) {
  const canSkills = agent.caps.slashSkills;
  const skillTrigger = agent.skillTrigger; // "" = ボタンのみ（タイプでは開かない）
  const [skills, setSkills] = useState<SessionSkill[] | null>(null); // null = 未取得
  const [slashTok, setSlashTok] = useState<SlashToken | null>(null); // 入力中の先頭 /トークン
  const [skillBtnOpen, setSkillBtnOpen] = useState(false); // ボタン起点で開いた（全件表示）
  const [skillSel, setSkillSel] = useState(0);
  const skillDismissRef = useRef<string | null>(null); // Esc/外クリックで閉じた時点の token（変わるまで再表示しない）
  const skillPopRef = useRef<HTMLDivElement>(null);
  const skillBtnRef = useRef<HTMLButtonElement>(null);
  const skillSelRef = useRef<HTMLButtonElement>(null);

  // slashOpen: 先頭トリガのトークンが生きていて、かつ直前に閉じられていない。
  // skillListVisible: 実際にリストを描く条件 — タイプ起点は該当ゼロなら出さない
  // （素の /plan 等の手打ちを覆い隠さない）。ボタン起点は空でも「無い」ことを見せる。
  // 開く条件はトリガのタイプ（bare トークンでは開かない）、絞り込みはどちらの起点でも
  // 同じトークンで効かせる — ボタンで開いてからタイプしても候補が絞れる。
  // skillArgs（受動表示）: コマンドを打ち終えて引数を書いている間。引数ヒントを見ながら書け
  // るようにリストは出したままにするが、確定した 1 件だけに絞り、キーボードは横取りしない
  // （Enter は送信のまま — ここで Enter を奪うと引数入力中に送信できなくなる）。
  const slashOpen = canSkills && !composerLocked && slashTok !== null && !slashTok.bare && skillDismissRef.current !== slashTok.token;
  const skillArgs = slashOpen && !!slashTok?.args;
  const skillsOpen = canSkills && !composerLocked && (skillBtnOpen || slashOpen);
  const skillQuery = slashTok?.token ?? "";
  const skillItems = (skillArgs ? exactSkills(skills ?? [], skillQuery) : skills ? filterSkills(skills, skillQuery) : [])
    // managed 発火未検証 kind はネイティブ項目だけ落とす（foreign=注入はただのプロンプト）。
    .filter((s) => !!s.path || !managed || agent.caps.slashSkillsManaged);
  // 受動表示は「一致した 1 件があるときだけ」— 読み込み中や不一致で "/" 始まりの文章を書いて
  // いる間にポップが出入りしないように、ボタン起点/タイプ起点の緩い条件は使わない。
  const skillListVisible = skillsOpen && (skillArgs ? skillItems.length > 0 : skillBtnOpen || skills === null || skillItems.length > 0);
  // キーボード（↑↓移動・Enter/Tab 確定）を横取りするのは能動表示のときだけ。
  const skillNavActive = skillListVisible && !skillArgs;
  // ネイティブは invoke をそのまま、foreign は「path を読んで指示に従え」プロンプトに組む
  // （末尾空白 — 続けて引数を打てる）。
  const skillInsertText = (s: SessionSkill): string =>
    s.invoke || tr("mirror.skills_use_foreign", { path: s.path ?? "" }) + " ";

  // 開いた時に取得（セッション替えでリセット）。都度取得 — セッション途中で SKILL.md を
  // 作らせる使い方が普通にあるので、開くたびに新鮮なリストを引く（走査は安い）。
  useEffect(() => setSkills(null), [session]);
  useEffect(() => {
    if (!skillsOpen || !session) return;
    let live = true;
    sessionSkills(session)
      .then((d) => live && setSkills(d.skills || []))
      .catch(() => live && setSkills((s) => s ?? [])); // 失敗時: 既取得はそのまま、未取得は空扱い
    return () => {
      live = false;
    };
  }, [skillsOpen, session]);

  // draft が手元の token とずれたら（送信でクリア・履歴呼び出し等の setDraft 直書き）閉じる。
  // 先頭は全角エイリアス（／・＄ — JP IME）も許すので startsWith でなく hasTriggerHead
  // （bare トークンはそもそもトリガを持たないので、この確認は非 bare のときだけ）。
  useEffect(() => {
    if (!slashTok) return;
    if ((!slashTok.bare && !hasTriggerHead(draft, skillTrigger)) || !draft.slice(0, slashTok.end).endsWith(slashTok.token))
      setSlashTok(null);
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [draft]);

  // 絞り込みが変わったら選択を先頭へ戻し、選択が動いたら見える位置へ追従。
  useEffect(() => setSkillSel(0), [slashTok?.token, skillBtnOpen]);
  // ★ ブロック本体で書くこと（式のまま返さない）: Chrome 150 以降 scrollIntoView() は
  // スクロール完了の Promise を返すので、暗黙 return するとその Promise が effect の
  // クリーンアップとして保存され、次回実行時に React が関数として呼んで TypeError →
  // 未捕捉のまま root ごとアンマウント＝画面真っ黒になる（候補件数が変わるたびに再実行
  // される effect なので、絞り込みが 1→0 件に変わった瞬間に踏む）。
  useEffect(() => {
    skillSelRef.current?.scrollIntoView({ block: "nearest" });
  }, [skillSel, skillItems.length]);

  // 差し込み: 入力中のトークン（無ければ下書き全体の頭）を起動文字列（invoke —
  // "/name " や "$name "）に置換し、既存の本文は引数として残す。タッチ端末はフォーカス
  // しない（GBoard が画面を覆う — applySuggestion と同じ規約）。送信はしない —
  // 引数を足してからユーザーが送る。
  const pickSkill = (invoke: string) => {
    const el = inputRef.current;
    const caret = el ? (el.selectionStart ?? draft.length) : draft.length;
    const { next, caret: nc } = applySkillToDraft(draft, caret, invoke, skillTrigger, skillBtnOpen);
    setDraft(next);
    setHistIdx(null);
    setSkillBtnOpen(false);
    skillDismissRef.current = null;
    // invoke 直後のキャレットは末尾空白の右＝引数位置なので args トークンになる → リストは
    // 受動表示のまま残り、選んだスキルの引数ヒントを見ながら引数を書ける。
    setSlashTok(slashTokenAt(next, nc, skillTrigger));
    if (coarsePointer()) {
      inputRef.current?.blur();
      return;
    }
    requestAnimationFrame(() => {
      const el2 = inputRef.current;
      if (el2) {
        el2.focus();
        el2.setSelectionRange(nc, nc);
      }
    });
  };

  // 閉じる（Esc・外クリック・ボタン再押下）。タイプ起点は「いまの token のままなら
  // 再表示しない」印を残す — 消して打ち直したら（token が変われば）また開く。
  const closeSkillPicker = () => {
    setSkillBtnOpen(false);
    skillDismissRef.current = slashTok?.token ?? null;
  };
  // 外クリックで閉じる。textarea 内クリック（キャレット移動）は対象外 — onSelect が
  // token を追い直してリストが生きるべき操作なので、refs に inputRef も含める。
  useDismiss([skillPopRef, skillBtnRef, inputRef], skillListVisible, closeSkillPicker);

  // ボタン起点で開く / 開いていれば閉じる（「/」ボタン）。
  const toggleFromButton = () => {
    if (skillListVisible) {
      closeSkillPicker();
      return;
    }
    skillDismissRef.current = null;
    setSkillBtnOpen(true);
    // 既に書いてある先頭トークンを即クエリにする（開いた瞬間から絞り込まれた
    // 状態で出す）。2 語目以降にキャレットがあれば null＝全件のまま。
    const el = inputRef.current;
    setSlashTok(pickerTokenAt(draft, el?.selectionStart ?? draft.length, skillTrigger, true));
  };

  // スキルピッカーのトリガ追跡: 先頭トリガ文字の 1 トークン内にキャレットが
  // ある間だけ token が立つ。トークンが死んだら Esc 抑止も解除（打ち直しで再表示）。
  // ボタンで開いている間はトリガ無しの先頭トークンも拾う（＝そのまま絞り込める）。
  const trackTyping = (value: string, caret: number) => {
    if (!canSkills) return;
    const tok = pickerTokenAt(value, caret, skillTrigger, skillBtnOpen);
    if (!tok) skillDismissRef.current = null;
    setSlashTok(tok);
  };
  /** キャレット移動（クリック・矢印）でも token の生死を追い直す。 */
  const trackCaret = (value: string, caret: number) => {
    if (canSkills) setSlashTok(pickerTokenAt(value, caret, skillTrigger, skillBtnOpen));
  };

  // スキルピッカーが開いている間は ↑/↓（選択移動）・Enter/Tab（確定）・Esc（閉じる）を
  // ここで横取りする — 履歴呼び出し（↑/↓）・チップ Tab・送信 Enter より先。IME の
  // 変換中は触らない。Ctrl/⌘+Enter と Shift+Enter は素通し（そのまま送信/改行できる逃げ道）。
  // 受動表示（引数入力中 = skillArgs）は横取りしない — 引数ヒントを見せているだけなので、
  // Enter は送信・↑/↓ はキャレット移動のまま。閉じる Esc だけは受け付ける。
  // 横取りしたら true を返す（呼び出し側はそこで打ち切る）。
  const handleKeyDown = (e: RKeyboardEvent): boolean => {
    if (!skillListVisible || e.nativeEvent.isComposing) return false;
    if (skillNavActive && (e.key === "ArrowDown" || e.key === "ArrowUp") && skillItems.length) {
      e.preventDefault();
      const n = skillItems.length;
      setSkillSel((s) => (s + (e.key === "ArrowDown" ? 1 : n - 1)) % n);
      return true;
    }
    if (skillNavActive && ((e.key === "Enter" && !e.ctrlKey && !e.metaKey && !e.shiftKey) || e.key === "Tab") && skillItems[skillSel]) {
      e.preventDefault();
      pickSkill(skillInsertText(skillItems[skillSel]));
      return true;
    }
    if (e.key === "Escape") {
      e.preventDefault();
      closeSkillPicker();
      return true;
    }
    return false;
  };

  return {
    canSkills,
    trigger: skillTrigger,
    skills,
    items: skillItems,
    sel: skillSel,
    setSel: setSkillSel,
    query: skillQuery,
    listVisible: skillListVisible,
    passive: skillArgs,
    popRef: skillPopRef,
    btnRef: skillBtnRef,
    selRef: skillSelRef,
    pick: (s: SessionSkill) => pickSkill(skillInsertText(s)),
    toggleFromButton,
    trackTyping,
    trackCaret,
    handleKeyDown,
  };
}
