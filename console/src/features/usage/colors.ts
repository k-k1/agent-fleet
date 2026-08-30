// features/usage/colors — 系列 → 色スロットの割り当て（docs/log/46 P4）。
//
// 規約は3つだけ。守るとグラフが色覚特性を含めて読める状態のまま保たれる。
//
//  1. **色は「順位」ではなく「実体」に付く。** フィルタで系列が減っても生き残った
//     系列の色は変わらない（"Acme は青" を学んだ読み手を裏切らない）。列挙型の軸
//     （feature / kind / trigger / origin / measured / model_src / verb）は固定表で、
//     データに一切依存しない。
//  2. **スロット順に積む。** 積み上げ棒で触れ合うのは「隣のスロット同士」だけになる
//     ので、パレットの隣接ペアさえ検証しておけば実際の隣接も安全（dataviz スキルの
//     adjacent 検証はまさにこの前提。tokens.css --viz-* のコメント参照）。
//  3. **9本目の色は作らない。** 8スロットを超えた分は必ずグレーの「その他」へ畳む。
//     生成した9色目は CVD 下で既存色と区別できず、検証が崩れる。
//
// model / origin_conv は値が無限に増えうるので固定表を作れない。**キー文字列のハッシュ**
// から優先スロットを決める（同じモデル名は常に同じ色＝実体に付く）。衝突時だけ空き
// スロットへ回す。上位8件を超えた分は「その他」へ畳む — 畳む対象の選択だけは量に依存
// するが、これは top-N + その他 という一般的な畳み方で、色の実体固定とは両立する。

/** 使用量ビューが塗る1系列。slot 0 = 「その他」（グレー・実体には割り当てない）。 */
export interface SeriesPaint {
  key: string;
  slot: number;
  color: string;
  /** 畳まれた（その他に入った）系列か。凡例と表では畳まれた内訳も名前で出す。 */
  folded: boolean;
}

/** カテゴリカルの上限。dataviz の「8スロット・9色目を作らない」に合わせる。 */
export const MAX_SLOTS = 8;

export const OTHER_KEY = "__other__";

/**
 * feature の固定スロット表（docs/log/46 §1-a の列挙）。
 *
 * enum は13個（ADR0029 の凍結12個＋docs/log/44 Phase 4 の `suggest.edit`）あり、スロットは
 * 8つしかない。**9色目を作らない**（規約3）ので、溢れる5つは常にグレーの「その他」へ
 * 入る — これは取りこぼしではなく選択で、色を持つ8つは「単独で桁が立ちうるもの」を
 * 採ってある:
 *
 * - `assistant.ask`（単発アドバイザリ・非永続）は `assistant.chat` の陰に隠れる量。
 * - `title.chat` / `suggest.chat` は会話1本あたり数回で、`title.session` /
 *   `suggest.session`（セッション毎に自動発火）より1桁小さい。
 * - `branch.suggest` / `suggest.edit` は手動起動のみ。
 *
 * **畳んだ分を見えなくしない**のがこの表の条件で、UI 側で3つ担保している: 畳みが1つでも
 * あれば凡例に「その他」を必ず出す（系列が1本でも出す）／ツールチップに畳まれた実キーを
 * 並べる／「その他」クリックで畳まれたキー全部の絞り込みを掛ける（同一軸 OR）。
 * 内訳リストと表ビューは畳まずに実キーのまま出す。
 */
const FEATURE_SLOT: Record<string, number> = {
  session: 1,
  "assistant.chat": 2,
  "assistant.autoturn": 3,
  compact: 4,
  "title.session": 5,
  "suggest.session": 6,
  "assistant.bridge": 7,
  // unknown は「タグ付け忘れ＝見えない消費」の信号なので、必ず色を持たせて目立たせる。
  unknown: 8,
};

/** kind の積み順（＝スロット順）。
 *
 * kind の色は tokens.css --kind-* が唯一の正で、使用量ビューのために塗り替えない
 * （agent-display-naming の1ソース規約）。ただし --kind-* は識別子であってグラフ用
 * パレットとして検証されたものではなく、7色を素の並びで積むと隣接ペアが CVD 検証に
 * 落ちる（agy 青 ↔ kiro 紫 が protan ΔE 2.0、copilot / opencode はどちらもほぼ無彩色）。
 *
 * そこで**色は変えず並びだけ**を全順列から選んだ。この順で積むと隣接ペアは
 * dark: CVD ΔE 13.0 / 通常視 19.8、light: CVD ΔE 17.3 / 通常視 19.4 で、どちらのテーマも
 * 隣接ゲートを通る。彩度・明度の帯（灰2色）は色そのものの性質なので通らないままで、
 * その分は凡例のラベル・ツールチップ・表ビュー（色だけに頼らせない）で担保する。 */
export const KIND_STACK_ORDER = ["cursor", "agy", "claude", "copilot", "codex", "kiro", "opencode"];

/** 小さな列挙軸の固定順（スロットは 1 起点で順に割り当てる）。 */
const ENUM_ORDER: Record<string, string[]> = {
  trigger: ["user", "auto", "manual", "schedule", "operator", "bridge", "recovery"],
  origin: ["user", "operator", "schedule", "handoff", "unknown"],
  model_src: ["reported", "requested", "default_unknown"],
  measured: ["exact", "partial", "none"],
  verb: ["", "translate", "summarize"],
};

/** キー文字列 → 安定ハッシュ（FNV-1a 32bit）。同じ名前は常に同じ優先スロット。 */
export function hashKey(s: string): number {
  let h = 0x811c9dc5;
  for (let i = 0; i < s.length; i++) {
    h ^= s.charCodeAt(i);
    h = Math.imul(h, 0x01000193);
  }
  return h >>> 0;
}

/** スロット番号 → CSS 色（0 = その他）。 */
export const slotColor = (slot: number): string => (slot <= 0 ? "var(--viz-other)" : `var(--viz-${slot})`);

const kindColor = (kind: string): string =>
  KIND_STACK_ORDER.includes(kind) ? `var(--kind-${kind})` : "var(--viz-other)";

/**
 * paintSeries — 軸 dim の系列キー群に色を割り当て、**描画順（スロット順）** で返す。
 *
 * keysByMagnitude は「量の多い順」で渡す（畳む対象を決めるためだけに使う。色は順位に
 * 依存しない）。戻り値は常に slot 昇順で、その他（slot 0）は最後。
 */
export function paintSeries(dim: string, keysByMagnitude: string[]): SeriesPaint[] {
  const out: SeriesPaint[] = [];
  const push = (key: string, slot: number, color: string) =>
    out.push({ key, slot, color, folded: slot <= 0 });

  if (dim === "kind") {
    for (const key of keysByMagnitude) {
      const i = KIND_STACK_ORDER.indexOf(key);
      push(key, i < 0 ? 0 : i + 1, kindColor(key));
    }
  } else if (dim === "feature") {
    for (const key of keysByMagnitude) {
      const slot = FEATURE_SLOT[key] ?? 0;
      push(key, slot, slotColor(slot));
    }
  } else if (ENUM_ORDER[dim]) {
    const order = ENUM_ORDER[dim];
    for (const key of keysByMagnitude) {
      const i = order.indexOf(key);
      const slot = i < 0 || i >= MAX_SLOTS ? 0 : i + 1;
      push(key, slot, slotColor(slot));
    }
  } else {
    // 無限に増えうる軸（model / origin_conv）: ハッシュ優先スロット + 衝突は空きへ。
    const taken = new Set<number>();
    keysByMagnitude.forEach((key, idx) => {
      if (idx >= MAX_SLOTS) {
        push(key, 0, slotColor(0));
        return;
      }
      let slot = (hashKey(key) % MAX_SLOTS) + 1;
      if (taken.has(slot)) {
        for (let s = 1; s <= MAX_SLOTS; s++) {
          if (!taken.has(s)) {
            slot = s;
            break;
          }
        }
      }
      taken.add(slot);
      push(key, slot, slotColor(slot));
    });
  }

  // スロット順に描く（規約2）。同スロット（その他）内はキー名で安定化。
  return out.sort((a, b) => {
    const as = a.slot <= 0 ? Infinity : a.slot;
    const bs = b.slot <= 0 ? Infinity : b.slot;
    return as - bs || (a.key < b.key ? -1 : a.key > b.key ? 1 : 0);
  });
}
