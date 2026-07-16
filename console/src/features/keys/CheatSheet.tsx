// Shortcut cheat-sheet — a searchable reference of every keyboard shortcut, opened
// with "?" (or leader → ?). Generated from the registry, so it can never drift from
// what actually works. Read-only; run a command from the palette (Ctrl/⌘+P) instead.
import { useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Kbd } from "../../ui/Kbd.tsx";
import { GROUPS, ALL_COMMANDS } from "./commands.ts";
import { useKeysStore } from "./store.ts";
import { useEscLayer } from "../../lib/escLayer.ts";
import { useBackClose } from "../../lib/backClose.ts";

// The leader + seq keys rendered as a chip sequence, e.g. ⌘K → p → r.
function LeaderKeys({ seq }: { seq: string }) {
  return (
    <span className="cheat-keys">
      <Kbd chord="mod+k" />
      {seq.split(" ").map((k, i) => (
        <Kbd key={i} chord={k} />
      ))}
    </span>
  );
}

interface Row {
  keys: ReactNode;
  title: string;
}
interface Sec {
  title: string;
  rows: Row[];
}

// Static "basics" — the dispatcher-level chords that aren't registry commands.
const BASICS: Row[] = [
  { keys: <Kbd chord="mod+k" />, title: "コマンドメニュー（which-key）" },
  { keys: <Kbd chord="mod+p" />, title: "コマンドパレット" },
  { keys: <Kbd chord="shift+/" />, title: "このショートカット一覧" },
  {
    keys: (
      <span className="cheat-keys">
        <Kbd chord="f6" />
        <Kbd chord="shift+f6" />
      </span>
    ),
    title: "領域を移動（レール / メイン / バー）",
  },
  { keys: <Kbd chord="escape" />, title: "閉じる / 戻る" },
];

export function CheatSheet() {
  const open = useKeysStore((s) => s.cheatOpen);
  const close = () => useKeysStore.getState().closeCheat();
  useEscLayer(open ? close : undefined, open);
  useBackClose(open ? close : undefined, open);
  const [q, setQ] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (!open) return;
    setQ("");
    const id = requestAnimationFrame(() => inputRef.current?.focus());
    return () => cancelAnimationFrame(id);
  }, [open]);

  const sections = useMemo<Sec[]>(() => {
    const secs: Sec[] = [{ title: "基本", rows: BASICS }];
    for (const g of GROUPS) {
      const rows = ALL_COMMANDS.filter((c) => c.seq?.startsWith(g.id + " ")).map((c) => ({
        keys: <LeaderKeys seq={c.seq!} />,
        title: c.title,
      }));
      if (rows.length) secs.push({ title: g.title, rows });
    }
    // Top-level leader actions (single-key seq, e.g. "," / "?").
    const top = ALL_COMMANDS.filter((c) => c.seq && !c.seq.includes(" ")).map((c) => ({
      keys: <LeaderKeys seq={c.seq!} />,
      title: c.title,
    }));
    if (top.length) secs.push({ title: "その他（リーダー）", rows: top });
    // Direct accelerators (a command may also appear above under its group).
    const direct = ALL_COMMANDS.filter((c) => c.keys?.length).map((c) => ({
      keys: <Kbd chord={c.keys![0]} />,
      title: c.title,
    }));
    if (direct.length) secs.push({ title: "アクセラレータ（直接キー）", rows: direct });
    return secs;
  }, []);

  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase();
    if (!needle) return sections;
    return sections
      .map((s) => ({ ...s, rows: s.rows.filter((r) => r.title.toLowerCase().includes(needle)) }))
      .filter((s) => s.rows.length);
  }, [sections, q]);

  if (!open) return null;

  return (
    <div className="cheat-overlay" onMouseDown={close}>
      <div
        className="cheat-panel"
        role="dialog"
        aria-modal="true"
        aria-label="キーボードショートカット一覧"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="cheat-head">
          <span className="cheat-title">キーボードショートカット</span>
          <input
            ref={inputRef}
            className="cheat-search"
            value={q}
            placeholder="絞り込み…"
            aria-label="ショートカットを絞り込み"
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setQ(e.target.value)}
          />
        </div>
        <div className="cheat-body">
          {filtered.length === 0 && <div className="cheat-empty">該当なし</div>}
          {filtered.map((s) => (
            <section className="cheat-sec" key={s.title}>
              <h4 className="cheat-sec-title">{s.title}</h4>
              <div className="cheat-rows">
                {s.rows.map((r, i) => (
                  <div className="cheat-row" key={i}>
                    <span className="cheat-row-keys">{r.keys}</span>
                    <span className="cheat-row-title">{r.title}</span>
                  </div>
                ))}
              </div>
            </section>
          ))}
        </div>
      </div>
    </div>
  );
}
