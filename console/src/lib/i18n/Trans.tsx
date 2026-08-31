// <Trans> — JSX マークアップ混在の翻訳（docs/log/28-i18n.md §6.3）。カタログ値に番号スロット
// `<0>…</0>`（対タグ）や `<1/>`（自己終了）を書き、components[n] の React 要素へ差し込む。t() が
// 先に {vars} を補間するので、動的テキストはプレースホルダで、装飾/改行だけをスロットで渡す。
//
//   カタログ: "session.recreate_body": "今の会話は<0>アーカイブに退避</0>し<1/>あとで復帰できます。"
//   使用    : <Trans k="session.recreate_body" components={[<strong />, <br />]} />
//
// 入れ子スロットは非対応（実利用が浅いネストのみ＝設計どおり最小実装）。未知スロット番号は
// 素通し（テキストのみ表示）。
import { Fragment, cloneElement, type ReactElement, type ReactNode } from "react";
import { useT, type MsgKey } from "./index.ts";

// `<0>…</0>`（グループ1=番号, グループ2=中身）または `<0/>`（グループ3=番号）にマッチ。
const SLOT_RE = /<(\d+)>([\s\S]*?)<\/\1>|<(\d+)\/>/g;

function render(tpl: string, components: ReactElement[]): ReactNode[] {
  const out: ReactNode[] = [];
  let last = 0;
  let key = 0;
  let m: RegExpExecArray | null;
  SLOT_RE.lastIndex = 0;
  while ((m = SLOT_RE.exec(tpl))) {
    if (m.index > last) out.push(<Fragment key={key++}>{tpl.slice(last, m.index)}</Fragment>);
    const idx = Number(m[1] ?? m[3]);
    const inner = m[2]; // undefined for self-closing
    const comp = components[idx];
    if (comp) {
      out.push(inner ? cloneElement(comp, { key: key++ }, inner) : cloneElement(comp, { key: key++ }));
    } else if (inner) {
      // スロット未提供でも中身のテキストは失わない。
      out.push(<Fragment key={key++}>{inner}</Fragment>);
    }
    last = SLOT_RE.lastIndex;
  }
  if (last < tpl.length) out.push(<Fragment key={key++}>{tpl.slice(last)}</Fragment>);
  return out;
}

export function Trans({
  k,
  vars,
  components = [],
}: {
  k: MsgKey;
  vars?: Record<string, string | number>;
  components?: ReactElement[];
}): ReactElement {
  const tr = useT(); // ロケール変更で再レンダー
  return <>{render(tr(k, vars), components)}</>;
}
