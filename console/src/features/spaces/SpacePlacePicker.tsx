// SpacePlacePicker — the Start hub's place chip (ADR 0142 decisions 2 and 7): a new space, one of
// the recently used spaces, or any space at all. It only chooses; the launch form decides what to
// send. The chip shows the choice, and the list under it opens on a click.
import { useEffect, useMemo, useState } from "react";
import { Icon } from "../../ui/Icon.tsx";
import { useT } from "../../lib/i18n/index.ts";
import { useSessionsStore } from "../sessions/store.ts";
import { RECENT_SPACES, rankSpaces } from "./recent.ts";
import { spaceLabel, spacesFor, spacesOwner, useSpacesStore } from "./store.ts";

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
  const owner = spacesOwner();
  const spaces = useSpacesStore((s) => spacesFor(s, owner));
  const loaded = useSpacesStore((s) => s.owner === owner);
  const sessions = useSessionsStore((s) => s.sessions);
  // The list may be minutes old, and another tab can have made a space since: ask each time the
  // hub's space section opens. A failure leaves the last list, or none, and "New space" stays.
  useEffect(() => {
    void useSpacesStore.getState().refresh();
  }, []);
  const ranked = useMemo(() => rankSpaces(spaces, sessions), [spaces, sessions]);
  // A space that is gone (removed since the list was read) is no choice any more.
  useEffect(() => {
    if (folder && loaded && !spaces.some((s) => s.folder === folder)) onChange("");
  }, [folder, loaded, spaces, onChange]);

  const recent = ranked.slice(0, RECENT_SPACES);
  const rest = ranked.slice(RECENT_SPACES);
  const pick = (f: string) => {
    onChange(f);
    setOpen(false);
  };
  const row = (f: string, label: string, icon: string) => (
    <button
      key={f || "new"}
      type="button"
      role="option"
      aria-selected={f === folder}
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
        type="button"
        className="start-place-chip start-place-chip--btn"
        aria-haspopup="listbox"
        aria-expanded={open}
        disabled={disabled}
        onClick={() => setOpen((o) => !o)}
      >
        <Icon name={folder ? "folder" : "new-folder"} />
        {folder ? spaceLabel(folder, spaces) : tr("start.place_new_space")}
        <Icon name={open ? "chevron-up" : "chevron-down"} />
      </button>
      {open && (
        <div className="start-place-list" role="listbox" aria-label={tr("start.place")}>
          {row("", tr("start.place_new_space"), "new-folder")}
          {recent.length > 0 && <span className="start-place-head">{tr("start.place_recent")}</span>}
          {recent.map((s) => row(s.folder, spaceLabel(s.folder, spaces), "folder"))}
          {rest.length > 0 && !all && (
            <button type="button" className="start-place-more linklike" onClick={() => setAll(true)}>
              {tr("start.place_all", { n: String(ranked.length) })}
            </button>
          )}
          {all && rest.length > 0 && <span className="start-place-head">{tr("start.place_all_head")}</span>}
          {all && rest.map((s) => row(s.folder, spaceLabel(s.folder, spaces), "folder"))}
        </div>
      )}
    </div>
  );
}
