// features/mirror/turnTts — ミラーのターン本文をカラオケ朗読する（docs/log/24）。
//
// MarkdownView が innerHTML で描画した DOM からブロック（p / h1-h6 / li / blockquote 内の
// 段落）を文書順に集め、textContent を文分割して startNarration（features/chat/tts.ts）へ
// 渡す。音声の単位＝文、ハイライトの単位＝ブロック。pre（コード）・table・mermaid は
// 読まない。ソース（Markdown 文字列）側で分割しないのは、marked のトークンとレンダ結果の
// 対応維持が脆いため — textContent なら記法は既に落ちており、リンクは表示テキストだけが残る。
// ターンは完結してから届く（ポーリング）ので、抽出は読み上げ開始時の 1 回で安定する。

import {
  startNarration,
  BLOCK_BEAT,
  SENT_BEAT,
  type TtsEndReason,
  type TtsOptions,
  type TtsStopReason,
} from "../chat/tts.ts";
import { splitSentences, splitLongSentence, abbrevCode, type CodeReadOpts } from "../chat/ttsText.ts";
import { effectiveDict } from "../chat/ttsDict.ts";
import { getSettings } from "../../lib/settings.ts";

// 読み上げ対象のリーフブロック。ul/ol は li 単位（入れ子リストは別ブロック）、blockquote は
// 中の段落へ降りる。pre / table / hr / mermaid（div）などはスキップ。
const LEAF = new Set(["P", "H1", "H2", "H3", "H4", "H5", "H6"]);

// collectBlocks はターン本文（.mirror-turn-body）から読み上げ・ハイライト単位のブロック要素を
// 文書順で返す。テキストパート（本文直下の .markdown ＝ MarkdownView のルート）だけが対象で、
// ツール表示・thinking（details 内の .markdown）・plan・question は拾わない。
export function collectBlocks(body: HTMLElement): HTMLElement[] {
  const out: HTMLElement[] = [];
  body.querySelectorAll<HTMLElement>(":scope > .markdown").forEach((md) => walk(md, out));
  return out;
}

function walk(container: HTMLElement, out: HTMLElement[]): void {
  for (const el of Array.from(container.children) as HTMLElement[]) {
    const tag = el.tagName;
    if (LEAF.has(tag)) out.push(el);
    else if (tag === "UL" || tag === "OL") walkList(el, out);
    else if (tag === "BLOCKQUOTE") walk(el, out);
  }
}

function walkList(list: HTMLElement, out: HTMLElement[]): void {
  for (const li of Array.from(list.children) as HTMLElement[]) {
    if (li.tagName !== "LI") continue;
    out.push(li);
    for (const sub of Array.from(li.children) as HTMLElement[]) {
      if (sub.tagName === "UL" || sub.tagName === "OL") walkList(sub, out);
    }
  }
}

// finalAnswerStart は「最終回答の本文」が始まるブロック index を返す（index は collectBlocks と
// 同じブロック順）。ツールが無ければ 0。ミラー自動読み上げが、最終回答より前の作業ナレーションを
// 飛ばして最終回答だけ読むために使う（chat の分離と同趣・docs/log/19）。
// 本文パートは body 直下の .markdown、ツール実行は mt-tool* クラスの直下要素として並ぶ。飛ばすのは
// 「最初の最終回答本文が現れる前」の作業ツールだけ。最終回答の後ろに来るツール（メモ書き込み等の
// 後始末）以降は最終回答の一部なので飛ばさない — 完了ターンでは workSplit が作業過程を disclosure へ
// 畳むので、直下に残るツールは後始末だけ。ここで飛ばすと続きの一言しか読まない不具合になる。
export function finalAnswerStart(body: HTMLElement): number {
  let count = 0; // ここまでに数えた読み上げブロック数
  let boundary = 0; // 最終回答本文が始まるブロック数
  let sawAnswer = false; // 最終回答の本文ブロックを既に見たか
  for (const el of Array.from(body.children) as HTMLElement[]) {
    if (el.classList.contains("markdown")) {
      const blocks: HTMLElement[] = [];
      walk(el, blocks);
      if (blocks.length) sawAnswer = true;
      count += blocks.length;
    } else if (!sawAnswer && Array.from(el.classList).some((c) => c.startsWith("mt-tool"))) {
      boundary = count; // 最終回答本文が現れる前のツール＝作業過程なので飛ばす
    }
  }
  return boundary;
}

// blockText はブロック自身の読み上げテキスト。li は入れ子リスト（別ブロックとして読む）と
// コードブロック・表・mermaid（div）を除いた自前のテキストだけを返す。インライン要素は
// 再帰で降り、<code>（バッククォート由来）は省略読み（abbrevCode）を当てる — レンダ済み
// DOM にはバッククォートが残っていないため、ここが plainify 相当の唯一の判定点。
const EXCLUDE = new Set(["UL", "OL", "PRE", "TABLE", "DIV"]);
function blockText(el: HTMLElement, code?: CodeReadOpts): string {
  let t = "";
  el.childNodes.forEach((n) => {
    if (n.nodeType === Node.ELEMENT_NODE) {
      const e = n as HTMLElement;
      if (EXCLUDE.has(e.tagName)) return;
      if (e.tagName === "CODE") {
        const s = e.textContent ?? "";
        t += code?.abbrev ? abbrevCode(s, code.dict) : s;
        return;
      }
      t += blockText(e, code);
      return;
    }
    t += n.textContent ?? "";
  });
  return t;
}

// blockIndexAt は選択開始ノードから「そこ（以降）で最初に読めるブロック」の index を返す。
// ノードがブロック内ならそのブロック、ブロック間（ツール表示等）に始まる選択なら後続の
// 最初のブロック。無ければ -1。
export function blockIndexAt(blocks: HTMLElement[], node: Node): number {
  const el = node.nodeType === Node.TEXT_NODE ? node.parentElement : (node as HTMLElement);
  if (!el) return -1;
  const within = blocks.findIndex((b) => b.contains(el));
  if (within >= 0) return within;
  return blocks.findIndex((b) => !!(node.compareDocumentPosition(b) & Node.DOCUMENT_POSITION_FOLLOWING));
}

// turnSpokenText は fromBlock 以降の読み上げ対象テキストを返す（要約読み上げの入力・
// 長さ判定用。省略読みや辞書は掛けない生テキスト。コード・表はブロック収集段階で除外済み）。
export function turnSpokenText(body: HTMLElement, fromBlock = 0): string {
  return collectBlocks(body)
    .slice(fromBlock)
    .map((b) => blockText(b).replace(/\s+/g, " ").trim())
    .filter(Boolean)
    .join("\n");
}

// --- 読み上げ担当の登録（全ペイン自動読み上げ, docs/log/24） ---------------------------
// 同じセッションを複数ペインで開いているとき、自動読み上げ・確認読み上げを担うのは最初に
// 登録したペインだけ（二重読み防止）。担当ペインが閉じたら次の登録ペインが自動で引き継ぐ。
// hasTurnReader は useSessionNotifications が「本文をそのまま朗読するセッション」へ短い告知を
// 重ねないための判定に使う。
const readers = new Map<string, symbol[]>();

// claimTurnReader はペイン（token）をセッションの読み上げ担当候補に登録し、解除関数を返す
// （useEffect のクリーンアップにそのまま渡せる）。
export function claimTurnReader(session: string, token: symbol): () => void {
  const arr = readers.get(session) ?? [];
  arr.push(token);
  readers.set(session, arr);
  return () => {
    const cur = readers.get(session);
    if (!cur) return;
    const i = cur.indexOf(token);
    if (i >= 0) cur.splice(i, 1);
    if (!cur.length) readers.delete(session);
  };
}

// isTurnReader は token がそのセッションの担当（先着）か。
export function isTurnReader(session: string, token: symbol): boolean {
  return (readers.get(session) ?? [])[0] === token;
}

// hasTurnReader はそのセッションを読み上げ可能なミラーペインが（どこかに）開いているか。
export function hasTurnReader(session: string): boolean {
  return (readers.get(session)?.length ?? 0) > 0;
}

export interface TurnReadHandle {
  pause(): void;
  resume(): void;
  stop(reason?: TtsStopReason): void;
}

const ACTIVE = "tts-active";

// readTurn は body の fromBlock 番目のブロック以降を朗読する。再生を開始した文が属する
// ブロックへカラオケ・ハイライト＋追従スクロール。onEnd は自然終了・明示停止・他再生への
// 置換のいずれでも理由付きで 1 回だけ呼ばれる。読み上げる
// 文が無ければ null を返し、onEnd は呼ばれない。
export function readTurn(
  body: HTMLElement,
  source: string,
  fromBlock: number,
  onEnd: (reason: TtsEndReason) => void,
  voice?: Partial<TtsOptions>, // セッションごとの声（sessionVoiceOpts）等の上書き
  sessionName = "", // 発生元セッション名（左ペインの再生中アイコン用）
): TurnReadHandle | null {
  const code: CodeReadOpts = { abbrev: getSettings().ttsAbbrevCode, dict: effectiveDict() };
  const blocks = collectBlocks(body);
  const texts: string[] = [];
  const blockOf: number[] = [];
  const sentHead: boolean[] = []; // 文の先頭の片か（false = 長文の合成分割の続き）
  blocks.forEach((b, bi) => {
    if (bi < fromBlock) return;
    for (const s of splitSentences(blockText(b, code))) {
      // 長い 1 文は合成用にさらに分割（合成の待ちで無音にならないように）。ハイライトは
      // ブロック単位のままなので見た目は変わらない。
      splitLongSentence(s).forEach((piece, j) => {
        texts.push(piece);
        blockOf.push(bi);
        sentHead.push(j === 0);
      });
    }
  });
  if (!texts.length) return null;

  let lit: HTMLElement | null = null;
  const light = (el: HTMLElement | null) => {
    if (lit === el) return;
    lit?.classList.remove(ACTIVE);
    lit = el;
    if (el) {
      el.classList.add(ACTIVE);
      el.scrollIntoView({ block: "nearest", behavior: "smooth" });
    }
  };
  // ブロック（段落・リスト項目・見出し）が変わる最初の文には前拍を置く（マーカー記号は
  // 読まないぶん、構造の切れ目を間で表す）。同一ブロック内の文境界（。区切り）には
  // より短い一拍（SENT_BEAT）。長文の合成分割の続き片は間を置かない（素材の残り無音のみ）。
  const preGaps = blockOf.map((b, i) => {
    if (i === 0 || !sentHead[i]) return 0;
    return b !== blockOf[i - 1] ? BLOCK_BEAT : SENT_BEAT;
  });
  const h = startNarration(
    texts,
    source,
    (i, endReason) => {
      if (i == null) {
        light(null);
        onEnd(endReason ?? "done");
      } else light(blocks[blockOf[i]]);
    },
    voice,
    preGaps,
    sessionName,
  );
  return { pause: h.pause, resume: h.resume, stop: h.stop };
}
