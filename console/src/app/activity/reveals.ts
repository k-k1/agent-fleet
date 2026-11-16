// A reveal names a row; with one view on screen at a time (ADR 0140) the row's view has to be
// brought forward first, or the request lands in a pane nobody is looking at. The features keep
// firing the reveal signals they always fired, and this is the one place that knows which view
// each signal belongs to.
import { useLeftRail } from "../../core/store/leftRail.ts";
import type { RailView } from "../../core/store/leftRail.ts";
import { useFilesStore } from "../../features/files/store.ts";
import { useMemoStore } from "../../features/memo/store.ts";
import { useRepoReveal } from "../../features/repos/store.ts";
import { useSchedulesStore } from "../../features/schedules/store.ts";

export function wireRailReveals(): () => void {
  const show = (view: RailView) => useLeftRail.getState().showView(view);
  const unsubs = [
    useRepoReveal.subscribe((s, prev) => {
      if (s.n !== prev.n) show("sessions");
    }),
    useSchedulesStore.subscribe((s, prev) => {
      if (s.reveal.n !== prev.reveal.n) show("schedules");
    }),
    useMemoStore.subscribe((s, prev) => {
      if (s.reveal.n !== prev.reveal.n || s.composeReq !== prev.composeReq) show("memo");
    }),
    // Only a reveal that takes focus is the member asking to see the row. The other kind is a
    // side effect of something finishing (a clone landing) and must not pull the pane away
    // from what they are reading.
    useFilesStore.subscribe((s, prev) => {
      if (s.reveal.n !== prev.reveal.n && s.reveal.focus) show("files");
    }),
  ];
  return () => unsubs.forEach((u) => u());
}
