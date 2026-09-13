// ModelCombo — the live model catalogs' picker: one text field that filters, with a popup
// listbox of matches. It replaces the search-input + native <select> pair the dynamic kinds
// used to render.
//
// Why not a <select>: an <option> cannot hold markup, so the brand mark of the company that
// made each model has nowhere to go — and that mark is the whole point here. opencode's
// catalog arrives as ~59 ids in one flat list where the prefix names the BILLING ROUTE
// ("opencode/…", "opencode-go/…") and not the maker, so the only thing distinguishing
// Anthropic's row from Zhipu's is reading the middle of the string. The logo is what makes
// that list scannable at a glance.
//
// The cost of leaving <select> behind is the keyboard, the screen reader and the phone, none
// of which come for free anymore:
//   - ARIA 1.2 combobox: role=combobox on the input, aria-expanded / aria-controls /
//     aria-activedescendant pointing into a role=listbox of role=option rows. The input keeps
//     focus throughout (DOM focus never moves into the list), which is what makes typing and
//     arrowing work at the same time.
//   - The popup is position:fixed and placed with placeFixed, because .ui-modal-body scrolls
//     (overflow-y: auto) and would otherwise clip a list opened near the bottom of a dialog.
//   - Options commit on mousedown-prevented click, as in SkillList/CommandPalette, so a pick
//     never blurs the input first and closes the list out from under the click.
//   - Rows are sized for a thumb, and the field opens the list on tap rather than a wheel.
//
// The mark is decoration only (aria-hidden): the row's text is what is announced and what the
// filter matches. A model whose maker could not be resolved shows no mark rather than a
// placeholder — see modelProviderOf.
import { useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { Icon } from "./Icon.tsx";
import { placeFixed } from "../lib/placeFixed.ts";
import { useEscLayer } from "../lib/escLayer.ts";
import { useT } from "../lib/i18n/index.ts";
import { filterModelOptions } from "../lib/modelFilter.ts";
import { modelProviderOf } from "../lib/agentModels.ts";
import type { ModelOption } from "../lib/agentModels.ts";

interface ModelComboProps {
  kind: string;
  /** The full choice list, Default first. */
  options: ModelOption[];
  /** The selected model id ("" = Default). */
  value: string;
  onChange: (model: string) => void;
}

export function ModelCombo({ kind, options, value, onChange }: ModelComboProps) {
  const tr = useT();
  const listId = useId();
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const inputRef = useRef<HTMLInputElement>(null);
  const popRef = useRef<HTMLDivElement>(null);
  const activeRef = useRef<HTMLDivElement>(null);

  const filtered = open ? filterModelOptions(options, query) : options;
  const selectedLabel = options.find(([v]) => v === value)?.[1] ?? value;

  // A kind switch swaps the whole catalog underneath: a leftover query would hide everything
  // and a leftover open popup would list the previous CLI's models.
  useEffect(() => {
    setOpen(false);
    setQuery("");
  }, [kind]);

  // Anchor under the field on every render while open — the dialog around it can scroll or
  // resize, and a popup that stays where it was opened points at the wrong row.
  useLayoutEffect(() => {
    const el = popRef.current;
    const anchor = inputRef.current;
    if (!open || !el || !anchor) return;
    const a = anchor.getBoundingClientRect();
    el.style.width = a.width + "px";
    placeFixed(el, a.left, a.bottom + 2);
  });

  useEffect(() => {
    if (open) activeRef.current?.scrollIntoView({ block: "nearest" });
  }, [open, active]);

  // Escape closes the list only; the modal underneath keeps its own layer and closes on the
  // next press, which is why this joins the stack rather than handling the key inline.
  useEscLayer(() => close(), open);

  function close() {
    setOpen(false);
    setQuery("");
  }

  function openList() {
    if (open) return;
    setOpen(true);
    setQuery("");
    const i = options.findIndex(([v]) => v === value);
    setActive(i >= 0 ? i : 0);
  }

  function commit(i: number) {
    const pick = filtered[i];
    if (!pick) return;
    onChange(pick[0]);
    close();
    inputRef.current?.focus();
  }

  function onKeyDown(e: React.KeyboardEvent<HTMLInputElement>) {
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      if (!open) {
        openList();
        return;
      }
      if (!filtered.length) return;
      const d = e.key === "ArrowDown" ? 1 : -1;
      setActive((i) => (i + d + filtered.length) % filtered.length);
      return;
    }
    if (!open) return;
    if (e.key === "Home" || e.key === "End") {
      e.preventDefault();
      setActive(e.key === "Home" ? 0 : Math.max(0, filtered.length - 1));
      return;
    }
    if (e.key === "Enter") {
      e.preventDefault(); // a launch dialog would otherwise submit on the pick
      commit(active);
      return;
    }
    // Escape is handled by the Esc layer, and Tab must move on rather than being swallowed —
    // closing on blur covers it.
  }

  const provider = modelProviderOf(kind, value);
  return (
    <div className="model-combo">
      <div className="model-combo-field">
        {provider ? <Icon name={"brand:" + provider} className="model-combo-mark" /> : null}
        <input
          ref={inputRef}
          type="text"
          role="combobox"
          className={provider ? "has-mark" : undefined}
          // The field doubles as the filter, so it shows the query while the list is open and
          // the selection when it is not. A placeholder repeats the selection so an empty
          // query never reads as "nothing chosen".
          value={open ? query : selectedLabel}
          placeholder={open ? selectedLabel : tr("ui.filter_models")}
          aria-label={tr("ui.kind_model", { kind })}
          aria-expanded={open}
          aria-controls={open ? listId : undefined}
          aria-activedescendant={open && filtered[active] ? `${listId}-${active}` : undefined}
          aria-autocomplete="list"
          autoComplete="off"
          onChange={(e) => {
            setQuery(e.target.value);
            setActive(0);
            if (!open) setOpen(true);
          }}
          onMouseDown={() => openList()}
          onFocus={() => openList()}
          onBlur={() => close()}
          onKeyDown={onKeyDown}
        />
        <span className="model-combo-caret" aria-hidden="true">
          <Icon name="chevron-down" />
        </span>
      </div>
      {open && (
        <div ref={popRef} id={listId} className="model-combo-pop" role="listbox" aria-label={tr("ui.kind_model", { kind })}>
          {filtered.length === 0 ? (
            <div className="model-combo-empty">{tr("ui.no_matching_models")}</div>
          ) : (
            filtered.map(([v, label], i) => {
              const p = modelProviderOf(kind, v);
              return (
                <div
                  key={v || "default"}
                  id={`${listId}-${i}`}
                  ref={i === active ? activeRef : undefined}
                  className={"model-combo-item" + (i === active ? " sel" : "") + (v === value ? " on" : "")}
                  role="option"
                  aria-selected={v === value}
                  onMouseMove={() => setActive(i)}
                  onMouseDown={(ev) => ev.preventDefault()} // keep focus in the input
                  onClick={() => commit(i)}
                >
                  {/* A fixed-width slot whether or not there is a mark, so the labels of an
                      unplaceable model and its neighbours still line up. */}
                  <span className="model-combo-mark" aria-hidden="true">
                    {p ? <Icon name={"brand:" + p} /> : null}
                  </span>
                  <span className="model-combo-label">{label}</span>
                </div>
              );
            })
          )}
        </div>
      )}
    </div>
  );
}
