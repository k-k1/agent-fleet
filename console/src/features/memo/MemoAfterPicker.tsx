// The prerequisite field of the memo editor: the chosen memos as removable chips, and a search field
// that adds more. A busy category has dozens of candidates, and two memos of one work item share a
// key, so every name carries the work item's title, the state, a short date and a follow-up marker.
// The result list is part of the flow (not a floating popup): it fits a phone panel and needs no hover.
import { useEffect, useId, useRef, useState, type KeyboardEvent, type FocusEvent } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import type { MsgKey } from "../../lib/i18n/index.ts";
import { fmtDateTime } from "../../lib/intl.ts";
import type { Memo } from "../../types/memo.ts";
import { useWorkItemStore } from "../workitems/store.ts";
import { findWorkItem, memoReference } from "./reference.ts";
import { afterEntryOf, memoStateWords, searchAfterEntries, type AfterEntry } from "./queues.ts";

/** How many matches the list shows; the rest is reached by typing more. */
export const AFTER_RESULT_LIMIT = 8;

/** One note of the picker, in the category's order. A note that is not `offered` any more but still
 *  chosen stays as a chip until removed, and is never offered again; `why` says why when the Control
 *  Plane told. Every id of the draft is in the list, so no chosen note can drop out of a save. */
export interface AfterOption {
  id: string;
  label: string;
  offered: boolean;
  memo?: Memo;
  why?: MsgKey;
}

/** The state, date and follow-up marker of a note, as the words shown after its name. */
function useMetaOf() {
  const tr = useT();
  return (m: Memo | undefined): string => {
    if (!m) return "";
    const state = memoStateWords(m.state);
    const seq = m.chainSeq ?? 0;
    return [
      state ? tr(state.label) : "",
      m.createdAt ? fmtDateTime(m.createdAt) : "",
      m.releasedBy === "continuation" ? (seq > 0 ? tr("memo.after_part", { seq }) : tr("memo.after_part_plain")) : "",
    ]
      .filter(Boolean)
      .join(" · ");
  };
}

const flatten = (e: AfterEntry): string => e.text.replace(/\s+/g, " ").trim();
const clip = (t: string): string => (t.length > 80 ? t.slice(0, 80) + "…" : t);

/** What tells apart notes that read the same: their text from where they first differ (the start of
 *  a template is shared), and, for any that still read the same, a running number. Index-aligned with
 *  `group`, which is in the category's order, so a note keeps its number whether or not it is chosen.
 *  Notes whose excerpts collide are told apart again among themselves, so a short note that differs
 *  early does not hide where two long ones differ; the last check is over the final words of the
 *  whole group, because separate branches can come out the same. */
function excerptsOf(group: AfterEntry[]): string[] {
  const texts = group.map(flatten);
  const excerpt = new Map<string, string>();
  const refine = (ts: string[]) => {
    let lcp = 0;
    while (ts.length > 1 && ts.every((t) => t.length > lcp && t[lcp] === ts[0][lcp])) lcp++;
    const from = Math.max(0, lcp - 12);
    const by = new Map<string, string[]>();
    for (const t of ts) {
      const ex = t ? (from > 0 ? "…" : "") + clip(t.slice(from)) : "";
      by.set(ex, [...(by.get(ex) ?? []), t]);
    }
    for (const [ex, same] of by) {
      if (same.length === 1) excerpt.set(same[0], ex);
      // Still the same: a smaller group has a longer common start, so this ends.
      else if (same.length < ts.length) refine(same);
      else for (const t of same) excerpt.set(t, ex);
    }
  };
  refine([...new Set(texts)]);
  const words = texts.map((t) => excerpt.get(t) ?? "");
  const total = new Map<string, number>();
  for (const w of words) total.set(w, (total.get(w) ?? 0) + 1);
  const seen = new Map<string, number>();
  return words.map((w) => {
    if (total.get(w)! < 2) return w;
    const n = (seen.get(w) ?? 0) + 1;
    seen.set(w, n);
    return "#" + n + (w ? " " + w : "");
  });
}

interface Shown {
  entry: AfterEntry;
  meta: string;
  /** Only set when another note of the picker reads the same without it. */
  excerpt: string;
  /** The whole name for a screen reader: everything the eye gets. */
  name: string;
}

function EntryName({ shown }: { shown: Shown }) {
  const tr = useT();
  const { entry, meta, excerpt } = shown;
  return (
    <>
      <span className="memo-after-name">
        {entry.key && <span className="memo-after-key">{entry.key}</span>}
        {entry.title && <span className="memo-after-title">{entry.title}</span>}
      </span>
      {(meta || entry.why) && (
        <span className="memo-after-meta">
          {meta}
          {entry.why && <span className="memo-edit-after-why">{(meta ? " · " : "") + tr(entry.why)}</span>}
        </span>
      )}
      {excerpt && <span className="memo-after-excerpt">{excerpt}</span>}
    </>
  );
}

/** The full text of a note, with its key: what the tooltip shows. */
const tipOf = (e: AfterEntry) => [e.key, e.text || e.title].filter(Boolean).join("\n");

export function MemoAfterPicker({
  options,
  value,
  onChange,
  onEscape,
}: {
  options: AfterOption[];
  value: string[];
  onChange: (ids: string[]) => void;
  /** Esc with the list already closed: the editor's own cancel. */
  onEscape: () => void;
}) {
  const tr = useT();
  const items = useWorkItemStore((s) => s.payload?.items);
  const metaOf = useMetaOf();
  const baseId = useId();
  const rootRef = useRef<HTMLDivElement>(null);
  const searchRef = useRef<HTMLInputElement>(null);
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  // The note the keyboard points at, by its id: a poll that puts a new candidate above it must not
  // move the pointer onto another note. null = the first of the list.
  const [activeNote, setActiveNote] = useState<string | null>(null);

  const entryOf = (o: AfterOption): AfterEntry => {
    const ref = o.memo ? memoReference(o.memo) : null;
    const title = ref && items ? (findWorkItem(ref, items)?.title ?? "") : "";
    return { ...afterEntryOf(o.id, o.memo, o.label, title), why: o.why };
  };
  // What the picker shows, in the category's order; a note that was removed again shows nowhere.
  const entries = options.filter((o) => o.offered || value.includes(o.id)).map(entryOf);
  const chosenEntries = entries.filter((e) => value.includes(e.id));
  // The nearest first: the notes closest to the end of the category's order, which is where a
  // follow-up is usually wanted.
  const candidates = entries.filter((e) => !value.includes(e.id)).reverse();
  const { items: matches, total } = searchAfterEntries(candidates, query, AFTER_RESULT_LIMIT);

  // Two notes that read the same (key, title, state, minute, marker) get a start of their text, so
  // they can be told apart on a touch screen, where no tooltip shows, and by a screen reader.
  const metas = new Map(entries.map((e) => [e.id, metaOf(e.memo)]));
  const reads = new Map<string, number>();
  for (const e of entries) {
    const k = [e.key, e.title, metas.get(e.id)].join("\x00");
    reads.set(k, (reads.get(k) ?? 0) + 1);
  }
  const excerpts = new Map<string, string>();
  const groups = new Map<string, AfterEntry[]>();
  for (const e of entries) {
    const k = [e.key, e.title, metas.get(e.id)].join("\x00");
    if ((reads.get(k) ?? 0) > 1) groups.set(k, [...(groups.get(k) ?? []), e]);
  }
  for (const g of groups.values()) excerptsOf(g).forEach((x, i) => excerpts.set(g[i].id, x));
  const shownOf = (e: AfterEntry): Shown => {
    const meta = metas.get(e.id) ?? "";
    const excerpt = excerpts.get(e.id) ?? "";
    return { entry: e, meta, excerpt, name: [e.key, e.title, meta, excerpt].filter(Boolean).join(", ") };
  };
  const chips = chosenEntries.map(shownOf);
  const shown = matches.map(shownOf);

  // -1 when the pointed note has left the list: nothing is active, and Enter adds nothing until the
  // member points at one again.
  const cur = activeNote === null ? Math.min(0, shown.length - 1) : shown.findIndex((c) => c.entry.id === activeNote);
  const optId = (id: string) => baseId + "-o-" + id;
  const listOpen = open && shown.length > 0;

  // The list scrolls (40vh): the active option has to follow into view when an arrow key moves it and
  // when a new search puts another note at the same index (the list may be scrolled away from it).
  const activeId = shown[cur]?.entry.id;
  useEffect(() => {
    if (listOpen && cur >= 0) document.getElementById(optId(shown[cur].entry.id))?.scrollIntoView?.({ block: "nearest" });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only a change of the active note
  }, [activeId, listOpen, query]);

  // Always the category's order, whatever order they were added or removed in.
  const ordered = (keep: (id: string) => boolean) => options.map((o) => o.id).filter(keep);
  const add = (id: string) => {
    onChange(ordered((x) => x === id || value.includes(x)));
    setQuery("");
    setActiveNote(null);
  };
  const remove = (id: string, at: number) => {
    // The button being pressed goes with its chip, and the focus with it: it moves to the next
    // chip's × (or the previous one's, or the search field) first, so Tab goes on from there.
    const next = rootRef.current?.querySelectorAll<HTMLButtonElement>(".memo-after-x");
    const target = next?.[at + 1] ?? next?.[at - 1] ?? searchRef.current;
    target?.focus();
    onChange(ordered((x) => x !== id && value.includes(x)));
  };

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!open) setOpen(true);
      else if (shown.length > 0) {
        const n = shown.length;
        const next = e.key === "ArrowDown" ? (cur < 0 ? 0 : (cur + 1) % n) : cur < 0 ? n - 1 : (cur + n - 1) % n;
        setActiveNote(shown[next].entry.id);
      }
    } else if (e.key === "Enter") {
      // Never reaches the editor's own Enter handling: this field only adds.
      e.preventDefault();
      e.stopPropagation();
      if (listOpen && cur >= 0) add(shown[cur].entry.id);
    } else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      // The first closes the list; the next one is the editor's cancel.
      if (open) setOpen(false);
      else onEscape();
    }
  };
  const onBlur = (e: FocusEvent<HTMLDivElement>) => {
    if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOpen(false);
  };

  return (
    <div ref={rootRef} className="memo-after-picker" onBlur={onBlur}>
      {chips.length > 0 && (
        <ul className="memo-after-chips">
          {chips.map((c, i) => (
            <li key={c.entry.id} className="memo-after-chip" title={tipOf(c.entry)}>
              <span className="memo-after-body">
                <EntryName shown={c} />
              </span>
              <button type="button" className="memo-after-x" aria-label={tr("memo.after_remove", { name: c.name })} onClick={() => remove(c.entry.id, i)}>
                <Icon name="close" />
              </button>
            </li>
          ))}
        </ul>
      )}
      <input
        ref={searchRef}
        type="text"
        role="combobox"
        className="memo-after-search"
        aria-label={tr("memo.after_search_label")}
        aria-expanded={listOpen}
        aria-controls={listOpen ? baseId + "-list" : undefined}
        aria-autocomplete="list"
        aria-activedescendant={listOpen && cur >= 0 ? optId(shown[cur].entry.id) : undefined}
        placeholder={tr("memo.after_search_ph")}
        autoComplete="off"
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          setActiveNote(null);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        onClick={() => setOpen(true)}
        onKeyDown={onKeyDown}
      />
      {listOpen && (
        <ul id={baseId + "-list"} role="listbox" aria-label={tr("memo.after_search_label")} className="memo-after-list">
          {shown.map((c, i) => (
            <li
              key={c.entry.id}
              id={optId(c.entry.id)}
              role="option"
              aria-selected={i === cur}
              aria-label={c.name}
              className={"memo-after-opt" + (i === cur ? " active" : "")}
              title={tipOf(c.entry)}
              // Keeps the focus in the field, so picking by touch or mouse does not close the list first.
              onMouseDown={(ev) => ev.preventDefault()}
              onClick={() => add(c.entry.id)}
            >
              <EntryName shown={c} />
            </li>
          ))}
        </ul>
      )}
      {/* Outside the listbox: only options belong in it. */}
      {listOpen && total > shown.length && (
        <div role="status" className="memo-edit-hint memo-after-more">
          {tr("memo.after_more", { n: total - shown.length })}
        </div>
      )}
      {open && query.trim() !== "" && shown.length === 0 && (
        <div role="status" className="memo-edit-hint">
          {tr("memo.after_no_match")}
        </div>
      )}
    </div>
  );
}
