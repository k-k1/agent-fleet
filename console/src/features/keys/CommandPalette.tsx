// Command palette — fuzzy-search every available command plus the session list, and
// run/open the pick. Opened by Ctrl/⌘+P (dispatcher). Joins the Esc + browser-back
// overlay stacks like every other dialog, so while it is open hasOpenOverlay() is true
// and the dispatcher stays inert (the input owns the keyboard).
//
// Scope: commands + sessions today. Files / repos are a fast follow (they need the same
// open-target wiring the rails already have).
import { useEffect, useMemo, useRef, useState } from "react";
import { Kbd } from "../../ui/Kbd.tsx";
import { paletteCommands } from "../../lib/keys/registry.ts";
import { useEscLayer } from "../../lib/escLayer.ts";
import { useBackClose } from "../../lib/backClose.ts";
import { useKeysStore } from "./store.ts";
import { useEffectiveCommands } from "./bindings.ts";
import { buildContext } from "./dispatcher.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { openSessionChat, openSessionTerminal } from "../sessions/open.ts";
import { agentOf } from "../../agents/registry.ts";

interface Item {
  id: string;
  title: string;
  sub: string;
  kbd?: string;
  run: () => void;
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
  const [q, setQ] = useState("");
  const [sel, setSel] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);

  const close = () => useKeysStore.getState().closePalette();
  useEscLayer(open ? close : undefined, open);
  useBackClose(open ? close : undefined, open);

  useEffect(() => {
    if (!open) return;
    setQ("");
    setSel(0);
    const id = requestAnimationFrame(() => inputRef.current?.focus());
    return () => cancelAnimationFrame(id);
  }, [open]);

  const items = useMemo<Item[]>(() => {
    if (!open) return [];
    const ctx = buildContext();
    const cmds: Item[] = paletteCommands(commands, ctx).map((c) => ({
      id: c.id,
      title: c.title,
      sub: "コマンド",
      kbd: c.keys?.[0],
      run: () => c.run(ctx),
    }));
    const sess: Item[] = sessions.map((s) => ({
      id: "session:" + s.name,
      title: s.title || s.name,
      sub: "セッション",
      run: () => (agentOf(s.kind).caps.chat ? openSessionChat : openSessionTerminal)(s.name),
    }));
    return [...cmds, ...sess];
  }, [open, sessions, commands]);

  const filtered = useMemo(() => items.filter((it) => fuzzy(q, it.title + " " + it.sub)), [items, q]);

  if (!open) return null;

  const run = (it?: Item) => {
    if (!it) return;
    close();
    it.run();
  };

  return (
    <div className="cp-overlay" onMouseDown={close}>
      <div
        className="cp-panel"
        role="dialog"
        aria-modal="true"
        aria-label="コマンドパレット"
        onMouseDown={(e) => e.stopPropagation()}
      >
        <input
          ref={inputRef}
          className="cp-input"
          value={q}
          placeholder="コマンド・セッションを検索…"
          aria-label="コマンド・セッションを検索"
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
          {filtered.length === 0 && <div className="cp-empty">該当なし</div>}
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
              {it.kbd && <Kbd chord={it.kbd} className="cp-kbd" />}
            </div>
          ))}
        </div>
      </div>
    </div>
  );
}
