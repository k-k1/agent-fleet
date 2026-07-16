// Shortcut cheat-sheet — a searchable reference of every keyboard shortcut, opened
// with "?" (or leader → ?). Generated from the registry, so it can never drift from
// what actually works. Read-only; run a command from the palette (Ctrl/⌘+P) instead.
//
// Labels are i18n keys resolved for display (cmdLabel) and searched across all locales
// (cmdSearch), so the filter matches whether the user types Japanese or English. Closing
// (always a cancel here) returns focus to whoever opened the sheet.
import { useEffect, useMemo, useRef, useState } from "react";
import type { ReactNode } from "react";
import { Kbd } from "../../ui/Kbd.tsx";
import { GROUPS } from "./commands.ts";
import { useEffectiveCommands, boundChord, APP_LEADER, APP_PALETTE, APP_CHEAT } from "./bindings.ts";
import { cmdLabel, cmdSearch } from "./labels.ts";
import { useKeysStore } from "./store.ts";
import { useEscLayer } from "../../lib/escLayer.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { t, useLocale } from "../../lib/i18n/index.ts";
import { coarsePointer } from "../../lib/device.ts";

// The leader + seq keys rendered as a chip sequence, e.g. ⌘K → p → r. `leader` is the
// live-bound leader chord so a rebind shows through here too.
function LeaderKeys({ seq, leader }: { seq: string; leader: string }) {
  return (
    <span className="cheat-keys">
      <Kbd chord={leader} />
      {seq.split(" ").map((k, i) => (
        <Kbd key={i} chord={k} />
      ))}
    </span>
  );
}

interface Row {
  keys: ReactNode;
  /** i18n key (or command title key) — resolved via cmdLabel for display, cmdSearch for filtering. */
  titleKey: string;
}
interface Sec {
  titleKey: string;
  rows: Row[];
}

// "Basics" — the dispatcher-level chords that aren't registry commands. Built from the
// live-bound leader / palette / cheat chords so a rebind shows through. An unbound chord
// (user cleared it) is shown as a dash rather than an empty keycap.
function basics(leader: string, palette: string, cheat: string): Row[] {
  const chip = (c: string) => (c ? <Kbd chord={c} /> : <span className="cheat-unbound">—</span>);
  return [
    { keys: chip(leader), titleKey: "keys.cheat.whichkey" },
    { keys: chip(palette), titleKey: "keys.cheat.palette" },
    { keys: chip(cheat), titleKey: "keys.cheat.cheatsheet" },
    {
      keys: (
        <span className="cheat-keys">
          <Kbd chord="f6" />
          <Kbd chord="shift+f6" />
        </span>
      ),
      titleKey: "keys.cheat.region",
    },
    { keys: <Kbd chord="escape" />, titleKey: "keys.cheat.close" },
  ];
}

export function CheatSheet() {
  const open = useKeysStore((s) => s.cheatOpen);
  const commands = useEffectiveCommands();
  const locale = useLocale(); // recompute labels + re-render when the language changes
  const [q, setQ] = useState("");
  const inputRef = useRef<HTMLInputElement>(null);
  const openerRef = useRef<HTMLElement | null>(null);
  const leader = boundChord(APP_LEADER) || "mod+k";

  const close = () => useKeysStore.getState().closeCheat();
  const cancel = () => {
    close();
    const o = openerRef.current;
    openerRef.current = null;
    if (o && document.contains(o) && !coarsePointer()) o.focus?.();
  };
  useEscLayer(open ? cancel : undefined, open);
  useBackClose(open ? cancel : undefined, open);

  useEffect(() => {
    if (!open) return;
    openerRef.current = (document.activeElement as HTMLElement) ?? null;
    setQ("");
    const id = requestAnimationFrame(() => inputRef.current?.focus());
    return () => cancelAnimationFrame(id);
  }, [open]);

  const sections = useMemo<Sec[]>(() => {
    void locale;
    const palette = boundChord(APP_PALETTE);
    const cheat = boundChord(APP_CHEAT);
    const secs: Sec[] = [{ titleKey: "keys.cheat.secBasics", rows: basics(leader, palette, cheat) }];
    for (const g of GROUPS) {
      const rows = commands.filter((c) => c.seq?.startsWith(g.id + " ")).map((c) => ({
        keys: <LeaderKeys seq={c.seq!} leader={leader} />,
        titleKey: c.title,
      }));
      if (rows.length) secs.push({ titleKey: g.title, rows });
    }
    // Top-level leader actions (single-key seq, e.g. "," / "?").
    const top = commands.filter((c) => c.seq && !c.seq.includes(" ")).map((c) => ({
      keys: <LeaderKeys seq={c.seq!} leader={leader} />,
      titleKey: c.title,
    }));
    if (top.length) secs.push({ titleKey: "keys.cheat.secLeader", rows: top });
    // Direct accelerators (a command may also appear above under its group). Unbound
    // (user-cleared) commands drop out of this section.
    const direct = commands
      .filter((c) => c.keys?.length)
      .map((c) => ({ keys: <Kbd chord={c.keys![0]} />, titleKey: c.title }));
    if (direct.length) secs.push({ titleKey: "keys.cheat.secDirect", rows: direct });
    return secs;
  }, [commands, leader, locale]);

  const filtered = useMemo(() => {
    const needle = q.trim().toLowerCase();
    if (!needle) return sections;
    return sections
      .map((s) => ({ ...s, rows: s.rows.filter((r) => cmdSearch(r.titleKey).toLowerCase().includes(needle)) }))
      .filter((s) => s.rows.length);
  }, [sections, q]);

  if (!open) return null;

  return (
    <div className="cheat-overlay" onMouseDown={cancel}>
      <div
        className="cheat-panel"
        role="dialog"
        aria-modal="true"
        aria-label={t("keys.cheat.aria")}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <div className="cheat-head">
          <span className="cheat-title">{t("keys.cheat.title")}</span>
          <input
            ref={inputRef}
            className="cheat-search"
            value={q}
            placeholder={t("keys.cheat.filter")}
            aria-label={t("keys.cheat.filterAria")}
            autoComplete="off"
            spellCheck={false}
            onChange={(e) => setQ(e.target.value)}
          />
        </div>
        <div className="cheat-body">
          {filtered.length === 0 && <div className="cheat-empty">{t("keys.cheat.empty")}</div>}
          {filtered.map((s) => (
            <section className="cheat-sec" key={s.titleKey}>
              <h4 className="cheat-sec-title">{cmdLabel(s.titleKey)}</h4>
              <div className="cheat-rows">
                {s.rows.map((r, i) => (
                  <div className="cheat-row" key={i}>
                    <span className="cheat-row-keys">{r.keys}</span>
                    <span className="cheat-row-title">{cmdLabel(r.titleKey)}</span>
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
