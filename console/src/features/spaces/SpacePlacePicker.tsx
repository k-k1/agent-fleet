// SpacePlacePicker — the Start hub's place chip (ADR 0142 decisions 2 and 7): a new space, one of
// the recently used spaces, or any space at all. It only chooses; the launch form decides what to
// send. The chip shows the choice, and the list under it opens on a click or a key. The list is
// plain buttons in tab order (no listbox, whose arrow-key contract it would have to keep); Escape
// closes the list alone, and a pick or a close puts the focus back on the chip.
import { useEffect, useMemo, useRef, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useEscLayer } from "../../lib/escLayer.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { RECENT_SPACES, rankSpaces } from "./recent.ts";
import { spaceLabel, spacesFor, spacesOwner, useSpacesStore } from "./store.ts";
import { useSpaceGone } from "./useSpaceGone.ts";

interface Props {
  /** The folder of the chosen space; "" is a new one. */
  folder: string;
  onChange: (folder: string) => void;
  disabled?: boolean;
}

export function SpacePlacePicker({ folder, onChange, disabled = false }: Props) {
  const tr = useT();
  const [open, setOpen] = useState(false);
  const [all, setAll] = useState(false);
  const chipRef = useRef<HTMLButtonElement>(null);
  const listRef = useRef<HTMLDivElement>(null);
  const owner = spacesOwner();
  const spaces = useSpacesStore((s) => spacesFor(s, owner));
  const sessions = useSessionsStore((s) => s.sessions);
  const gone = useSpaceGone(folder);
  const ranked = useMemo(() => rankSpaces(spaces, sessions), [spaces, sessions]);
  const recent = ranked.slice(0, RECENT_SPACES);
  const rest = ranked.slice(RECENT_SPACES);

  const close = () => {
    setOpen(false);
    chipRef.current?.focus();
  };
  // The hub's Escape layer is below this one, so the list closes first and the dialog stays.
  useEscLayer(close, open);
  const pick = (f: string) => {
    onChange(f);
    close();
  };
  // The "All spaces" button is gone once pressed; the first space it revealed takes the focus.
  useEffect(() => {
    if (all) listRef.current?.querySelector<HTMLButtonElement>("[data-rest]")?.focus();
  }, [all]);

  const row = (f: string, label: string, icon: string, isRest = false) => (
    <button
      key={f || "new"}
      type="button"
      aria-pressed={f === folder}
      data-rest={isRest ? "" : undefined}
      className={"start-place-opt" + (f === folder ? " start-place-opt--on" : "")}
      onClick={() => pick(f)}
    >
      <Icon name={icon} />
      <span className="start-place-opt-label">{label}</span>
      {f === folder && <Icon name="check" className="start-place-opt-tick" />}
    </button>
  );

  return (
    <div className="start-place-pick">
      <button
        ref={chipRef}
        type="button"
        className={"start-place-chip start-place-chip--btn" + (gone ? " start-place-chip--gone" : "")}
        aria-expanded={open}
        disabled={disabled}
        title={folder ? spaceLabel(folder, spaces) : undefined}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name={gone ? "warning" : folder ? "folder" : "new-folder"} />
        <span className="start-place-chip-label">{folder ? spaceLabel(folder, spaces) : tr("start.place_new_space")}</span>
        <Icon name={open ? "chevron-up" : "chevron-down"} />
      </button>
      {open && (
        <div className="start-place-list" ref={listRef} role="group" aria-label={tr("start.place")}>
          {row("", tr("start.place_new_space"), "new-folder")}
          {recent.length > 0 && <span className="start-place-head">{tr("start.place_recent")}</span>}
          {recent.map((s) => row(s.folder, spaceLabel(s.folder, spaces), "folder"))}
          {rest.length > 0 && !all && (
            <button type="button" className="start-place-more linklike" onClick={() => setAll(true)}>
              {tr("start.place_all", { n: String(ranked.length) })}
            </button>
          )}
          {all && rest.length > 0 && <span className="start-place-head">{tr("start.place_all_head")}</span>}
          {all && rest.map((s) => row(s.folder, spaceLabel(s.folder, spaces), "folder", true))}
        </div>
      )}
    </div>
  );
}
