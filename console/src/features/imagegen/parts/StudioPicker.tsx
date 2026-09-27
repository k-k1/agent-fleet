// StudioPicker — which studio this pane edits, and the way to start another (ADR 0100
// decision 10). "New studio" opens beside this one rather than replacing it: the member asked
// for several studios at once, and the one on screen keeps its conversation.
import { useT } from "../../../lib/i18n/index.ts";
import type { StudioSummary } from "../wire.ts";
import { NEW_STUDIO, studioName } from "../studios.ts";

export function StudioPicker({
  studioId,
  studios,
  current,
  onOpen,
  onNew,
}: {
  studioId: string;
  studios: StudioSummary[];
  /** The pane's own studio as read, for the moment before the list has it. */
  current: StudioSummary | null;
  onOpen: (id: string) => void;
  onNew: () => void;
}) {
  const tr = useT();
  const name = (s: StudioSummary) => studioName(s, (stamp) => tr("imggen.studio_dated", { when: stamp }));
  const listed = studios.some((s) => s.id === studioId);
  return (
    <select
      className="ds-select igen-studio-pick"
      aria-label={tr("imggen.studio_pick")}
      value={studioId}
      onChange={(e) => {
        // Controlled: choosing "new" leaves the value on this studio until the new pane opens.
        if (e.target.value === NEW_STUDIO) onNew();
        else if (e.target.value) onOpen(e.target.value);
      }}
    >
      {!listed && <option value={studioId}>{current ? name(current) : "…"}</option>}
      {studios.map((s) => (
        <option key={s.id} value={s.id}>
          {name(s)}
          {s.session ? " ●" : ""}
        </option>
      ))}
      <option value={NEW_STUDIO}>{tr("imggen.studio_new")}</option>
    </select>
  );
}
