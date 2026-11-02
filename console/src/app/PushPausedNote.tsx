// PushPausedNote — the small "live updates paused — click to resume" pill shown while the
// push stream is closed for idleness (core/push/events.ts). Without it a paused window
// would just look frozen. Any input already resumes the stream, so the click is mostly a
// reassurance; the pill vanishes on the same edge.
import { useSyncExternalStore } from "react";
import { onPushPause, pushPaused, resumePush } from "../core/push/events.ts";
import { useT } from "../lib/i18n/index.ts";

export function PushPausedNote() {
  const tr = useT();
  const paused = useSyncExternalStore(onPushPause, pushPaused);
  if (!paused) return null;
  return (
    <button type="button" className="push-paused-note" onClick={resumePush}>
      {tr("pushpause.note")}
    </button>
  );
}
