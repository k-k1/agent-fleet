// The prerequisite field of the memo editor: the chosen memos as removable chips, and a search field
// that adds more. A busy category has dozens of candidates, and two memos of one work item share a
// key, so every name carries the work item's title, the state, a short date and a follow-up marker.
// The result list is part of the flow (not a floating popup): it fits a phone panel and needs no hover.
import { useId, useState, type KeyboardEvent, type FocusEvent } from "react";
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

/** One note of the picker, in the category's order. `why` is set on a chosen note that is no longer
 *  on offer: it stays as a chip until removed, and is never offered again. */
export interface AfterOption {
  id: string;
  label: string;
  memo?: Memo;
  why?: MsgKey;
}

function EntryName({ entry }: { entry: AfterEntry }) {
  const tr = useT();
  const m = entry.memo;
  const state = m ? memoStateWords(m.state) : null;
  const seq = m?.chainSeq ?? 0;
  const meta = [
    state ? tr(state.label) : "",
    m?.createdAt ? fmtDateTime(m.createdAt) : "",
    m?.releasedBy === "continuation" ? (seq > 0 ? tr("memo.after_part", { seq }) : tr("memo.after_part_plain")) : "",
  ].filter(Boolean);
  return (
    <>
      <span className="memo-after-name">
        {entry.key && <span className="memo-after-key">{entry.key}</span>}
        {entry.title && <span className="memo-after-title">{entry.title}</span>}
      </span>
      {(meta.length > 0 || entry.why) && (
        <span className="memo-after-meta">
          {meta.join(" · ")}
          {entry.why && <span className="memo-edit-after-why">{(meta.length > 0 ? " · " : "") + tr(entry.why)}</span>}
        </span>
      )}
    </>
  );
}

/** The full text of a note, with its key: what the tooltip shows. */
const tipOf = (e: AfterEntry) => [e.key, e.text || e.title].filter(Boolean).join("\n");

export function MemoAfterPicker({ options, value, onChange }: { options: AfterOption[]; value: string[]; onChange: (ids: string[]) => void }) {
  const tr = useT();
  const items = useWorkItemStore((s) => s.payload?.items);
  const baseId = useId();
  const [query, setQuery] = useState("");
  const [open, setOpen] = useState(false);
  const [active, setActive] = useState(0);

  const entryOf = (o: AfterOption): AfterEntry => {
    const ref = o.memo ? memoReference(o.memo) : null;
    const title = ref && items ? (findWorkItem(ref, items)?.title ?? "") : "";
    return { ...afterEntryOf(o.id, o.memo, o.label, title), why: o.why };
  };
  const chosen = options.filter((o) => value.includes(o.id)).map(entryOf);
  // The nearest first: the notes closest to the end of the category's order, which is where a
  // follow-up is usually wanted.
  const candidates = options
    .filter((o) => !o.why && !value.includes(o.id))
    .map(entryOf)
    .reverse();
  const { items: shown, total } = searchAfterEntries(candidates, query, AFTER_RESULT_LIMIT);
  const cur = Math.min(active, shown.length - 1);
  const optId = (i: number) => baseId + "-o" + i;
  const listOpen = open && shown.length > 0;

  // Kept in the category's order whatever order they were added in.
  const add = (id: string) => {
    onChange(options.map((o) => o.id).filter((x) => x === id || value.includes(x)));
    setQuery("");
    setActive(0);
  };
  const remove = (id: string) => onChange(value.filter((x) => x !== id));

  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.nativeEvent.isComposing) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!open) setOpen(true);
      else if (shown.length > 0) setActive((cur + (e.key === "ArrowDown" ? 1 : shown.length - 1)) % shown.length);
    } else if (e.key === "Enter") {
      // Never reaches the editor's own Enter handling: this field only adds.
      e.preventDefault();
      e.stopPropagation();
      if (listOpen && shown[cur]) add(shown[cur].id);
    } else if (e.key === "Escape" && open) {
      // Closes the list only; the editor stays open.
      e.preventDefault();
      e.stopPropagation();
      setOpen(false);
    }
  };
  const onBlur = (e: FocusEvent<HTMLDivElement>) => {
    if (!e.currentTarget.contains(e.relatedTarget as Node | null)) setOpen(false);
  };

  return (
    <div className="memo-after-picker" onBlur={onBlur}>
      {chosen.length > 0 && (
        <ul className="memo-after-chips">
          {chosen.map((e) => (
            <li key={e.id} className="memo-after-chip" title={tipOf(e)}>
              <span className="memo-after-body">
                <EntryName entry={e} />
              </span>
              <button
                type="button"
                className="memo-after-x"
                aria-label={tr("memo.after_remove", { name: e.key || e.title })}
                onClick={() => remove(e.id)}
              >
                <Icon name="close" />
              </button>
            </li>
          ))}
        </ul>
      )}
      <input
        type="text"
        role="combobox"
        className="memo-after-search"
        aria-label={tr("memo.after_search_label")}
        aria-expanded={listOpen}
        aria-controls={baseId + "-list"}
        aria-autocomplete="list"
        aria-activedescendant={listOpen ? optId(cur) : undefined}
        placeholder={tr("memo.after_search_ph")}
        autoComplete="off"
        value={query}
        onChange={(e) => {
          setQuery(e.target.value);
          setActive(0);
          setOpen(true);
        }}
        onFocus={() => setOpen(true)}
        onClick={() => setOpen(true)}
        onKeyDown={onKeyDown}
      />
      {listOpen && (
        <ul id={baseId + "-list"} role="listbox" className="memo-after-list">
          {shown.map((e, i) => (
            <li
              key={e.id}
              id={optId(i)}
              role="option"
              aria-selected={i === cur}
              className={"memo-after-opt" + (i === cur ? " active" : "")}
              title={tipOf(e)}
              // Keeps the focus in the field, so picking by touch or mouse does not close the list first.
              onMouseDown={(ev) => ev.preventDefault()}
              onClick={() => add(e.id)}
            >
              <EntryName entry={e} />
            </li>
          ))}
          {total > shown.length && <li className="memo-edit-hint memo-after-more">{tr("memo.after_more", { n: total - shown.length })}</li>}
        </ul>
      )}
      {open && query.trim() !== "" && shown.length === 0 && <div className="memo-edit-hint">{tr("memo.after_no_match")}</div>}
    </div>
  );
}
