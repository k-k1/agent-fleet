// transcript/useMarks — マーカーの取得・追加・削除（docs/69 / ADR 0050）。
//
// アンカーの決め方そのものは marks.ts（純粋・React も I/O も無し）。ここはミラーと共有
// ビューが共通で使う配線で、差はエンドポイントと「自分は誰か」だけ。

import { useCallback, useEffect, useRef, useState } from "react";
import { api, apiJSON } from "../../../core/api/client.ts";
import { markRootKey, type NewMark, type TranscriptMark } from "./marks.ts";

/** マーカーを描画層へ渡す配線。無い能力の操作要素は描かない（capabilities.ts の規約）。 */
export interface TranscriptMarksWiring {
  /** root キー → その root に付いている印。参照が変わることで再適用が走る。 */
  byRoot: Map<string, TranscriptMark[]>;
  /** 一覧帯が使う全件（作成日時の新しい順）。 */
  all: TranscriptMark[];
  /** 印を足せるか。false なら選択ピルを出さない（RO の共有先）。 */
  canEdit: boolean;
  add: (m: NewMark) => void;
  remove: (id: string) => void;
  /** この印を消せるか。所有者は誰の印でも、共有先は自分の印だけ。 */
  canRemove: (m: TranscriptMark) => boolean;
  /** 作成者の表示名。"" = 所有者。自分のものは「あなた」。 */
  authorLabel: (author: string | undefined) => string;
  /**
   * 作成者ごとの色スロット（0 = 所有者）。⚠️ 色そのものは利用者が意味づけに選ぶ軸なので、
   * 作成者はそこへ載せず下線で示す（ADR 0050 決定 5）。
   */
  authorSlot: (author: string | undefined) => number;
  /** id 引き（印をクリックしたときのカード表示用）。 */
  find: (id: string) => TranscriptMark | undefined;
}

/** 作成者に割り当てる下線色の数（CSS の --mark-author-N と対応）。 */
export const MARK_AUTHOR_SLOTS = 6;

/**
 * 再取得の下限間隔。印は「もう一方の誰か」が付けたものが遅れて見えるだけの補助情報で、
 * 転写と同じ毎秒で取り直す価値は無い（所有者 Workspace への往復だけが増える）。
 */
const MARKS_REFRESH_MS = 15000;

// authorSlotsOf は作成者 → スロット番号。所有者（""）は必ず 0 で、残りは login id の
// 昇順に 1 から割り当てる — 到着順にすると、ポーリングのたびに色が入れ替わる。
function authorSlotsOf(list: TranscriptMark[]): Map<string, number> {
  const others = [...new Set(list.map((m) => m.author || "").filter(Boolean))].sort();
  const map = new Map<string, number>([["", 0]]);
  others.forEach((a, i) => map.set(a, 1 + (i % (MARK_AUTHOR_SLOTS - 1))));
  return map;
}

function byRootOf(list: TranscriptMark[]): Map<string, TranscriptMark[]> {
  const map = new Map<string, TranscriptMark[]>();
  for (const m of list) {
    const key = markRootKey(m.turn, m.part);
    const at = map.get(key);
    if (at) at.push(m);
    else map.set(key, [m]);
  }
  return map;
}

export interface MarksControllerOptions {
  /** `api/sessions/<name>/marks` か `api/shared-sessions/<id>/marks`。空 = 機能ごと無効。 */
  path: string;
  /** 印を足せるか（所有者は常に true、共有先は RW のときだけ）。 */
  canEdit: boolean;
  /** 所有者として見ているか（誰の印でも消せる）。 */
  isOwner: boolean;
  /** 自分の login id。所有者は ""。 */
  viewerId: string;
  /** 所有者の表示名（共有ビューが「相手の名前」を出すために渡す）。 */
  ownerLabel: string;
  /** 「あなた」の訳語。i18n をこのモジュールへ引き込まないための注入。 */
  youLabel: string;
  /** 取得を止める条件（所有者 Workspace 停止中など）。 */
  paused?: boolean;
}

/**
 * マーカーの取得・追加・削除。追加と削除は楽観更新してから送り、失敗したら元へ戻す
 * （印は補助機能なので、通信の都合で会話の読みを止めない）。
 *
 * ⚠️ 新しいポーリングは作らない。転写のポーリングから `reload()` を呼び、実際の再取得は
 * MARKS_REFRESH_MS で間引く。
 */
export function useMarksController(opts: MarksControllerOptions): TranscriptMarksWiring & { reload: () => void } {
  const { path, canEdit, isOwner, viewerId, ownerLabel, youLabel, paused } = opts;
  const [byRoot, setByRoot] = useState<Map<string, TranscriptMark[]>>(() => new Map());
  const [all, setAll] = useState<TranscriptMark[]>([]);
  const [slots, setSlots] = useState<Map<string, number>>(() => new Map());
  // 現在の一覧は ref で持つ。楽観更新→応答という往復の途中で別の追加/削除が挟まるので、
  // クロージャに焼き付いた配列から組み立てると、あとから来た応答が先の変更を巻き戻す。
  const listRef = useRef<TranscriptMark[]>([]);
  const pathRef = useRef(path);
  const lastFetch = useRef(0);

  const apply = useCallback((list: TranscriptMark[]) => {
    listRef.current = list;
    setByRoot(byRootOf(list));
    setSlots(authorSlotsOf(list));
    setAll([...list].sort((a, b) => (b.created_at || 0) - (a.created_at || 0)));
  }, []);

  // セッションを切り替えたら、前のセッションの印を一瞬でも出さない。
  if (pathRef.current !== path) {
    pathRef.current = path;
    lastFetch.current = 0; // 別のセッションへ移った直後は、間引きに引っかからせない
    if (listRef.current.length) apply([]);
  }

  // 転写のポーリング（毎秒）に相乗りして呼ばれるので、ここで間引く。⚠️ 2 本目の周期を
  // 増やさないための作りで、増やすと転写と印が別の時刻を見る（docs/68 決定 3 と同じ理由）。
  const reload = useCallback(() => {
    if (!path || paused) return;
    const now = Date.now();
    if (now - lastFetch.current < MARKS_REFRESH_MS) return;
    lastFetch.current = now;
    void api(path).then((d) => {
      if (pathRef.current !== path) return; // 切り替え後に届いた前のセッションの応答
      if (!d || d.error || !Array.isArray(d.marks)) return;
      apply(d.marks as TranscriptMark[]);
    });
  }, [path, paused, apply]);

  useEffect(() => {
    reload();
  }, [reload]);

  const add = useCallback(
    (m: NewMark) => {
      if (!path || !canEdit) return;
      // id は呼び出し側が採番する。Agent 側が create-only なので、再送しても印は増えない
      // （ADR 0050 決定 4 — 副作用の台帳を持ち出さずに冪等）。
      const id = "mk_" + newMarkHex();
      const optimistic: TranscriptMark = { ...m, id, author: viewerId || undefined, created_at: Date.now() };
      apply([...listRef.current, optimistic]);
      void apiJSON(path, "POST", { ...m, id }).then((d) => {
        if (pathRef.current !== path) return;
        const rest = listRef.current.filter((x) => x.id !== id);
        // 付かなかったものを残さない／保存された姿（created_at と CP が刻んだ author）で置き換える。
        apply(!d || d.error || !d.mark ? rest : [...rest, d.mark as TranscriptMark]);
      });
    },
    [path, canEdit, viewerId, apply],
  );

  const remove = useCallback(
    (id: string) => {
      if (!path) return;
      const gone = listRef.current.find((x) => x.id === id);
      apply(listRef.current.filter((x) => x.id !== id));
      void api(path + "?id=" + encodeURIComponent(id), { method: "DELETE" }).then((d) => {
        if (pathRef.current !== path) return;
        // 消せなかったものを消えたままにしない（他人の印＝403 がここに来る）。
        if (d && d.error && gone && !listRef.current.some((x) => x.id === id)) apply([...listRef.current, gone]);
      });
    },
    [path, apply],
  );

  const canRemove = useCallback(
    (m: TranscriptMark) => (isOwner ? true : !!viewerId && m.author === viewerId),
    [isOwner, viewerId],
  );

  const authorLabel = useCallback(
    (author: string | undefined) => {
      const who = author || "";
      if (who === viewerId) return youLabel; // 所有者から見た "" も、共有先から見た自分も
      return who || ownerLabel;
    },
    [viewerId, youLabel, ownerLabel],
  );

  const authorSlot = useCallback((author: string | undefined) => slots.get(author || "") ?? 0, [slots]);
  const find = useCallback((id: string) => listRef.current.find((m) => m.id === id), []);

  return { byRoot, all, canEdit, add, remove, canRemove, authorLabel, authorSlot, find, reload };
}

function newMarkHex(): string {
  const b = new Uint8Array(4);
  crypto.getRandomValues(b);
  return [...b].map((x) => x.toString(16).padStart(2, "0")).join("");
}
