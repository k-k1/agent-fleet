// MoveLimitBanner — the move offered where the limit is felt (ADR 0115 P2b). A session stopped at
// its agent's usage limit (limited, or spend_limit) either waits for the reset or goes on in
// another kind; this band offers the second, opening the same dialog as the session menu with the
// kinds that still have quota left in front.
//
// Nothing here decides eligibility: the Agent's preview does, and the band draws only when that
// leaves a target. The source's own kind is dropped again here (decision 5) so a preview that
// ever listed it could not put the exhausted account first.
import { useEffect, useMemo, useState } from "react";
import { Icon } from "../../../ui/Icon.tsx";
import { useT } from "../../../lib/i18n/index.ts";
import { usageLimitState } from "../../../lib/sessionview.ts";
import { kindDisplayName } from "../../../lib/sessionkind.ts";
import { useInterval } from "../../../lib/useInterval.ts";
import { agentOf } from "../../../agents/registry.ts";
import { movePreview } from "../../sessions/moveApi.ts";
import { offeredTargets, orderByQuota, quotaState, type MovePreview } from "../../sessions/move.ts";
import { useConnectedKinds, useMoveUsage } from "../../sessions/moveHooks.ts";
import { useSessionUI } from "../../sessions/ui.ts";
import type { Session } from "../../../types/session.ts";

/** How often an open band re-reads the preview and the usage; a limit lasts hours, so this is slow. */
const REFRESH_MS = 60_000;

export function MoveLimitBanner({ session, state, running }: { session?: Session | null; state?: string; running: boolean }) {
  // A moving or moved source refuses every start; its dialog (retry, cancel, resume anyway) is
  // reached from the row, not from a limit it is no longer waiting on.
  const open = !!session && usageLimitState(session, state) && running && agentOf(session.kind).caps.transcript && !session.moving && !session.movedTo;
  // Keyed by the source: switching to another limited session starts from nothing, so the
  // previous one's preview and usage never speak for it. Split so that the reads (preview, connections, usage) exist only while a limit is showing.
  return open ? <LimitBand key={session.name} session={session} /> : null;
}

function LimitBand({ session }: { session: Session }) {
  const tr = useT();
  const openMove = useSessionUI((u) => u.openMove);
  const name = session.name;
  const [preview, setPreview] = useState<MovePreview | null>(null);
  // Bumped on a timer: the quota of a target resets (or runs out) while the source stays limited,
  // and a first read that failed would otherwise never be retried.
  const [tick, setTick] = useState(0);
  useInterval(() => setTick((n) => n + 1), REFRESH_MS);
  useEffect(() => {
    let alive = true;
    movePreview(name)
      .then((d) => alive && setPreview(d && !d.error ? d : null))
      .catch(() => alive && setPreview(null));
    return () => {
      alive = false;
    };
  }, [name, tick]);

  // Unlike the dialog, which takes the Agent's word while the check is cold, the band waits for
  // it: it claims a connected target, so it must know.
  const connected = useConnectedKinds();
  const candidates = useMemo(
    () =>
      preview && connected && !preview.refusal && !preview.movedTo && !preview.moving
        ? offeredTargets(preview, connected).filter((k) => k !== session.kind)
        : [],
    [preview, connected, session.kind],
  );
  const usage = useMoveUsage(candidates, tick);
  const ordered = orderByQuota(candidates, usage);
  const left = ordered.filter((k) => quotaState(usage[k]) === "left");
  // Every candidate at its cap: moving there only trades one wall for another.
  if (ordered.length === 0 || ordered.every((k) => quotaState(usage[k]) === "full")) return null;

  return (
    <div className="mirror-attention mirror-move-limit" role="status">
      <Icon name="arrow-swap" />
      <span className="ma-text">
        {left.length > 0
          ? tr("mirror.move_limit_left", { kinds: left.map(kindDisplayName).join(", ") })
          : tr("mirror.move_limit")}
      </span>
      <button type="button" className="btn primary ma-btn" onClick={() => openMove(session, true)}>
        {tr("mirror.move_limit_action")}
      </button>
    </div>
  );
}
