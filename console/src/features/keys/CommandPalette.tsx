// Command palette — fuzzy-search every available command plus the session list, and
// run/open the pick. Opened by Ctrl/⌘+P (dispatcher). Joins the Esc + browser-back
// overlay stacks like every other dialog, so while it is open hasOpenOverlay() is true
// and the dispatcher stays inert (the input owns the keyboard).
//
// Search matches across ALL locales (cmdSearch) so typing English or Japanese finds a
// command regardless of the current UI language; display uses the current locale.
//
// Focus: opening from a composer/input must not strand focus. We remember the opener and,
// on a CANCEL (Esc / browser-back / backdrop), return focus to it. Running a command does
// NOT restore — the command may move focus deliberately (e.g. focus a pane).
//
// Scope: commands + sessions today. Files / repos are a fast follow (they need the same
// open-target wiring the rails already have).
import { useEffect, useMemo, useRef, useState } from "react";
import { Kbd } from "../../ui/Kbd.tsx";
import { paletteCommands } from "../../lib/keys/registry.ts";
import type { Command } from "../../lib/keys/registry.ts";
import { useEscLayer } from "../../lib/escLayer.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { t, useLocale } from "../../lib/i18n/index.ts";
import { coarsePointer } from "../../lib/device.ts";
import { useKeysStore } from "./store.ts";
import { useEffectiveCommands, boundChord, APP_LEADER } from "./bindings.ts";
import { cmdLabel, cmdSearch } from "./labels.ts";
import { buildContext } from "./dispatcher.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionChat, openSessionTerminal } from "../sessions/open.ts";
import { agentOf } from "../../agents/registry.ts";

interface Item {
  id: string;
  title: string;
  sub: string;
  /** All-locale text the fuzzy filter matches against (kept apart from the display title). */
  search: string;
  /** The effective shortcut as a chord sequence, rendered as keycaps on the right. A
   * direct accelerator is one chord (["alt+1"]); a leader command is the leader plus each
   * step (["mod+k","p","r"]). Empty when the command has no key (or a session row). */
  keys: string[];
  run: () => void;
}

// The shortcut a palette row advertises: the command's direct accelerator if it has one
// (already reflecting the user's rebind — applyOverrides rewrote `keys`), otherwise the
// leader chord followed by its sequence keys. Mirrors the cheat-sheet's rendering so the
// two never disagree. `leader` is the live-bound leader ("" only if the user cleared it).
function shortcutChords(c: Command, leader: string): string[] {
  if (c.keys && c.keys.length) return [c.keys[0]];
  if (c.seq) return leader ? [leader, ...c.seq.split(" ")] : c.seq.split(" ");
  return [];
}

// Subsequence match: every character of the query appears in order in the text.
function fuzzy(query: string, text: string): boolean {
  if (!query) return true;
  const q = query.toLowerCase();
  const t = text.toLowerCase();
  let i = 0;
  for (let j = 0; j < t.length && i < q.length; j++) if (t[j] === q[i]) i++;
  return i === q.length;
}

export function CommandPalette() {
  const open = useKeysStore((s) => s.paletteOpen);
  const sessions = useSessionsStore((s) => s.sessions);
  const commands = useEffectiveCommands();
  const locale = useLocale(); // re-render + recompute items when the UI language changes
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const openerRef = useRef<HTMLElement | null>(null);

  const close = () => useKeysStore.getState().closePalette();
  // Cancel = close without running anything → hand focus back to whoever opened us.
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
    // Capture the opener BEFORE we steal focus to the search input (the rAF below).
    openerRef.current = (document.activeElement as HTMLElement) ?? null;
    setQ("");
    setSel(0);
    const id = requestAnimationFrame(() => inputRef.current?.focus());
    return () => cancelAnimationFrame(id);
  }, [open]);

  const items = useMemo<Item[]>(() => {
    if (!open) return [];
    void locale; // dep: recompute labels/search when the language changes
    const ctx = buildContext();
    const leader = boundChord(APP_LEADER) || "mod+k";
    const cmds: Item[] = paletteCommands(commands, ctx).map((c) => ({
      id: c.id,
      title: cmdLabel(c.title),
      sub: t("keys.item.command"),
      search: cmdSearch(c.title),
      keys: shortcutChords(c, leader),
      run: () => c.run(ctx),
    }));
    const sess: Item[] = sessions.map((s) => {
      const title = s.title || s.name;
      return {
        id: "session:" + s.name,
        title,
        sub: t("keys.item.session"),
        search: title,
        keys: [],
        run: () => (agentOf(s.kind).caps.chat ? openSessionChat : openSessionTerminal)(s.name),
      };
    });
    return [...cmds, ...sess];
  }, [open, sessions, commands, locale]);

  const filtered = useMemo(() => items.filter((it) => fuzzy(q, it.search + " " + it.sub)), [items, q]);

  if (!open) return null;

  const run = (it?: Item) => {
    if (!it) return;
    openerRef.current = null; // running a command owns focus; don't restore to the opener
    close();
    it.run();
  };

  return (
    <div className="cp-overlay" onMouseDown={cancel}>
      <div
        className="cp-panel"
        role="dialog"
        aria-modal="true"
        aria-label={t("keys.app.palette")}
        onMouseDown={(e) => e.stopPropagation()}
      >
        <input
          ref={inputRef}
          className="cp-input"
          value={q}
          placeholder={t("keys.palette.placeholder")}
          aria-label={t("keys.palette.aria")}
          autoComplete="off"
          spellCheck={false}
          onChange={(e) => {
            setQ(e.target.value);
            setSel(0);
          }}
          onKeyDown={(e) => {
            if (e.nativeEvent.isComposing) return;
            if (e.key === "ArrowDown") {
              e.preventDefault();
              setSel((s) => Math.min(s + 1, filtered.length - 1));
            } else if (e.key === "ArrowUp") {
              e.preventDefault();
              setSel((s) => Math.max(s - 1, 0));
            } else if (e.key === "Enter") {
              e.preventDefault();
              run(filtered[sel]);
            }
          }}
        />
        <div className="cp-list">
          {filtered.length === 0 && <div className="cp-empty">{t("keys.palette.empty")}</div>}
          {filtered.map((it, i) => (
            <div
              key={it.id}
              className={"cp-item" + (i === sel ? " sel" : "")}
              onMouseMove={() => setSel(i)}
              onMouseDown={(e) => {
                e.preventDefault();
                run(it);
              }}
            >
              <span className="cp-title">{it.title}</span>
              <span className="cp-sub">{it.sub}</span>
              {it.keys.length > 0 && (
                <span className="cp-kbd">
                  {it.keys.map((ch, k) => (
                    <Kbd key={k} chord={ch} />
                  ))}
                </span>
              )}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
