// Agent memory > Changes: the change list with its diff and the way back, and, while the background
// review is in use, the proposals waiting for the member's decision (ADR 0117).

import { useEffect, useRef, useState } from "react";
import { AgentChangesPanel } from "../memoryChanges.tsx";
import { ProposedPanel } from "../memoryProposals.tsx";
import { ViewNav } from "../viewNav.tsx";
import { useT } from "../../../../lib/i18n/index.ts";
import type { ReviewStatus } from "../memoryTypes.ts";

const LOG = "log";
const PROPOSED = "proposed";

export function ChangesView({
  reload,
  onChanged,
  review = null,
  poll = 0,
}: {
  reload: number;
  onChanged: () => void;
  /** The review's status (MemoryTab reads it for the nav's count); null while unknown. */
  review?: ReviewStatus | null;
  /** Counts the status answers; the proposals are read again when it moves. */
  poll?: number;
}) {
  const tr = useT();
  const pending = review?.pending ?? 0;
  // The sub-list is offered while the review is on or proposals are still waiting (switching the
  // review off must not hide proposals it already made).
  const offered = !!review && (review.enabled || pending > 0);
  const [sub, setSub] = useState(LOG);
  // Land on the proposals when there are some: that is what the count in the nav promised. Only on
  // the first answer, so the member's own pick is never overridden.
  const landed = useRef(false);
  useEffect(() => {
    if (landed.current || !review) return;
    landed.current = true;
    if (review.pending > 0) setSub(PROPOSED);
  }, [review]);

  if (!offered) return <AgentChangesPanel reload={reload} onChanged={onChanged} />;
  const shown = sub === PROPOSED ? PROPOSED : LOG;
  return (
    <>
      <div className="mem-changes-sub">
        <ViewNav
          label={tr("mem.changes_sub_label")}
          views={[
            { key: LOG, label: tr("mem.rv_sub_log") },
            {
              key: PROPOSED,
              label: tr("mem.rv_sub_proposed"),
              count: pending,
              ariaLabel: tr("mem.rv_sub_proposed_count", { n: pending }),
            },
          ]}
          value={shown}
          onChange={setSub}
          prefix="mem-af-changes"
        />
      </div>
      <div role="tabpanel" id="mem-af-changes-panel" aria-labelledby={`mem-af-changes-tab-${shown}`}>
        {shown === PROPOSED ? (
          <ProposedPanel reload={reload} poll={poll} onChanged={onChanged} />
        ) : (
          <AgentChangesPanel reload={reload} onChanged={onChanged} />
        )}
      </div>
    </>
  );
}
