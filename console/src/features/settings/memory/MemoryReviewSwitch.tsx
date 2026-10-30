// MemoryReviewSwitch is the background memory review's setting (ADR 0117, ui-prefs memoryReview,
// default off). Like the other memory settings it is a ui-pref, so it stays reachable while the
// workspace is stopped. It also needs the sessions' memory switch on; the Agent checks both at
// every wake-up, and this says so rather than letting "propose" look active while it is not.
//
// The note discloses what the switch does: conversation excerpts go to a model provider (the one
// the AI assist setting picks for this feature, not necessarily the session's own), it costs
// tokens, and nothing reaches memory without the member's approval.
//
// Automatic review (ui-prefs memoryReviewAuto, ADR 0117 decision 1) is a second, separate switch,
// off by default: the mode alone enables the review and "Suggest memories now"; only this switch
// lets the background loop spend tokens unattended.

import { Choice, OnOff, Row } from "../parts/controls.tsx";
import { setSetting, useSettings } from "../../../lib/settings.ts";
import { useMemoryReviewUnavailable } from "../../../lib/aiAssistResolution.ts";
import { useT } from "../../../lib/i18n/index.ts";

// running: the Agent can be asked; while the workspace is stopped nothing is fetched (only the pref works).
export function MemoryReviewSwitch({ running = false }: { running?: boolean }) {
  const tr = useT();
  const s = useSettings();
  const unavailable = useMemoryReviewUnavailable(running);
  return (
    <>
      <Row label={tr("mem.review_switch")}>
        <Choice
          value={s.memoryReview}
          options={[
            ["off", tr("mem.review_off")],
            ["propose", tr("mem.review_propose")],
          ]}
          onChange={(v) => setSetting("memoryReview", v)}
        />
      </Row>
      <p className="muted ds-note">{tr("mem.review_note")}</p>
      {unavailable && <p className="mem-warn">{tr("mem.review_no_backend")}</p>}
      {s.memoryReview !== "off" && !s.agentMemory && <p className="mem-warn">{tr("mem.review_needs_memory")}</p>}
      <Row label={tr("mem.review_auto_switch")}>
        <OnOff value={s.memoryReviewAuto} onChange={(v) => setSetting("memoryReviewAuto", v)} />
      </Row>
      <p className="muted ds-note">{tr("mem.review_auto_note")}</p>
      {s.memoryReviewAuto && s.memoryReview === "off" && <p className="mem-warn">{tr("mem.review_auto_needs_review")}</p>}
    </>
  );
}
