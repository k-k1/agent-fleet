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
//   - The popup is position:fixed and re-anchored on every scroll/resize (anchorPopup),
//     because .ui-modal-body is a scroll container: it would otherwise clip a list opened
//     near the bottom of a dialog, and float over the wrong row once the dialog scrolls.
//   - Options commit on mousedown-prevented click, as in SkillList/CommandPalette, so a pick
//     never blurs the input first and closes the list out from under the click.
//   - Rows are sized for a thumb, and the field opens the list on tap rather than a wheel.
//   - On a device with an on-screen keyboard the field is READ-ONLY until the reader taps
//     "filter". A <select> never raises a keyboard, and a text field that does the moment a
//     list appears is worse than the thing it replaced: GBoard takes half the screen, the
//     dialog scrolls to keep the field visible, and the list has to chase it. Typing is still
//     one tap away (startTyping), it is just no longer the default.
//
// The mark is decoration only (aria-hidden): the row's text is what is announced and what the
// filter matches. A model whose maker could not be resolved shows no mark rather than a
// placeholder — see modelProviderOf.
import { useCallback, useEffect, useId, useLayoutEffect, useRef, useState } from "react";
import { Icon } from "./Icon.tsx";
import { anchorPopup } from "../lib/anchorPopup.ts";
import { useEscLayer } from "../lib/escLayer.ts";
import { useT } from "../lib/i18n/index.ts";
import { primaryCoarsePointer } from "../lib/device.ts";
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
  // Whether the reader has ASKED to type. On a phone the field is read-only until then, so
  // opening the list does not summon the on-screen keyboard (see the comment on the filter
  // row below). Always true where there is no on-screen keyboard to summon.
  const [typing, setTyping] = useState(false);
  const inputRef = useRef<HTMLInputElement>(null);
  const popRef = useRef<HTMLDivElement>(null);
  const activeRef = useRef<HTMLDivElement>(null);
  // Set while the field is deliberately blurred and refocused to raise the keyboard, so the
  // blur handler does not read that as "the reader tapped elsewhere" and close the list.
  const refocusing = useRef(false);

  // Evaluated per render rather than once: a tablet can gain a keyboard mid-session, and
  // primaryCoarsePointer is a live media query (device.ts).
  const softKeyboard = primaryCoarsePointer();
  const readOnly = softKeyboard && !typing;

  const filtered = open ? filterModelOptions(options, query) : options;
  const selectedLabel = options.find(([v]) => v === value)?.[1] ?? value;
  // The field shows the query only while it can actually be typed into. Read-only it keeps
  // showing the selection, so opening the list on a phone does not blank the field.
  const showQuery = open && !readOnly;

  // A kind switch swaps the whole catalog underneath: a leftover query would hide everything
  // and a leftover open popup would list the previous CLI's models.
  useEffect(() => {
    setOpen(false);
    setQuery("");
    setTyping(false);
  }, [kind]);

  const place = useCallback(() => {
    const el = popRef.current;
    const anchor = inputRef.current;
    if (el && anchor) anchorPopup(el, anchor);
  }, []);

  // Re-anchor on every render while open, and — the part a render cannot see — whenever
  // anything MOVES the field underneath it. .ui-modal-body is a scroll container, so a
  // dialog that scrolls (which is exactly what the keyboard opening does) used to leave the
  // list floating over the wrong row. `true` for the capture phase because a scroll event on
  // an inner container does not bubble to window.
  useLayoutEffect(place);
  useEffect(() => {
    if (!open) return;
    const vv = window.visualViewport;
    window.addEventListener("scroll", place, true);
    window.addEventListener("resize", place);
    vv?.addEventListener("resize", place);
    vv?.addEventListener("scroll", place);
    return () => {
      window.removeEventListener("scroll", place, true);
      window.removeEventListener("resize", place);
      vv?.removeEventListener("resize", place);
      vv?.removeEventListener("scroll", place);
    };
  }, [open, place]);

  useEffect(() => {
    if (open) activeRef.current?.scrollIntoView({ block: "nearest" });
  }, [open, active]);

  // Escape closes the list only; the modal underneath keeps its own layer and closes on the
  // next press, which is why this joins the stack rather than handling the key inline.
  useEscLayer(() => close(), open);

  function close() {
    setOpen(false);
    setQuery("");
    setTyping(false);
  }

  // Raise the on-screen keyboard, having kept it down until now. The field is already
  // focused, and dropping `readonly` on a focused field does not open a keyboard by itself —
  // it takes a fresh focus. Both the attribute and the blur/focus pair are done on the DOM
  // node here rather than left to the re-render, because a browser only opens the keyboard
  // for a focus() that is still inside the tap's own call stack.
  function startTyping() {
    const el = inputRef.current;
    setTyping(true);
    if (!el) return;
    el.readOnly = false;
    refocusing.current = true;
    el.blur();
    el.focus();
    refocusing.current = false;
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
          // The field doubles as the filter, so it shows the query while it is being typed
          // into and the selection otherwise. A placeholder repeats the selection so an empty
          // query never reads as "nothing chosen".
          value={showQuery ? query : selectedLabel}
          placeholder={showQuery ? selectedLabel : tr("ui.filter_models")}
          // Read-only until the reader asks to type: a focused text field is what summons the
          // on-screen keyboard, and merely opening a list should not. Focus, the arrow keys
          // and a hardware keyboard all keep working.
          readOnly={readOnly}
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
          onBlur={() => {
            if (!refocusing.current) close();
          }}
          onKeyDown={onKeyDown}
        />
        <span className="model-combo-caret" aria-hidden="true">
          <Icon name="chevron-down" />
        </span>
      </div>
      {open && (
        // The popup is a plain box, and the listbox is the scrolling part inside it. Keeping
        // them separate is what lets the filter row exist: a role=listbox may only contain
        // options, and aria-activedescendant indexes into exactly those.
        <div ref={popRef} className="model-combo-pop">
          {/* The way back to filtering on a phone. It is a row rather than something in the
              field because the field is deliberately inert there, and putting the keyboard
              behind an explicit tap is the whole point: it opens when the reader asked for
              it, not every time a list does. */}
          {readOnly && (
            <button
              type="button"
              tabIndex={-1} // the field owns the focus; Tab must leave the picker, not land here
              className="model-combo-filter"
              onMouseDown={(ev) => ev.preventDefault()} // keep focus in the input
              onClick={startTyping}
            >
              <Icon name="search" />
              <span>{tr("ui.filter_models")}</span>
            </button>
          )}
          <div id={listId} className="model-combo-list" role="listbox" aria-label={tr("ui.kind_model", { kind })}>
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
        </div>
      )}
    </div>
  );
}
