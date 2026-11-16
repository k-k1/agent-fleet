import { Choice, OnOff, Row } from "../parts/controls.tsx";
import {
  useSettings,
  setSetting,
  SPAWN_CHILD_LIMITS,
  STOPPED_ARCHIVE_DAYS,
  STOPPED_ARCHIVE_NEVER,
  stoppedArchiveChoice,
} from "../../../lib/settings.ts";
import { SPEND_CAP_DEFAULTS } from "../../sessions/spendBudget.ts";
import { tCount, useT } from "../../../lib/i18n/index.ts";

// SessionsTab is Settings > Sessions (ADR 0138 D16): what applies to every session whatever its
// agent, so none of it belongs on the Agents page, which is about harnesses. Every row is a local
// pref synced through ui-prefs, so the page renders in every workspace state.
export function SessionsTab() {
  const tr = useT();
  const s = useSettings();
  return (
    <div className="conns">
      <section className="ds-group">
        {/* The stopped-session archive period (ADR 0097). First because it applies to every
            session, spawned or not; the note carries the child-slot consequence rather than the
            row sitting under the steering switch. "Default" is its own choice, not 7: it defers
            to the deployment's AF_SESSION_STOPPED_TTL, which the Console cannot see. */}
        <Row label={tr("agents.stopped_archive")}>
          <Choice
            value={stoppedArchiveChoice(s.sessionStoppedArchiveDays)}
            options={[
              [0, tr("agents.stopped_archive_default")],
              ...STOPPED_ARCHIVE_DAYS.map((n): [number, string] => [n, tCount("agents.stopped_archive_days", n)]),
              [STOPPED_ARCHIVE_NEVER, tr("agents.stopped_archive_never")],
            ]}
            onChange={(v) => setSetting("sessionStoppedArchiveDays", v)}
          />
        </Row>
        <p className="muted ds-note">{tr("agents.note_stopped_archive")}</p>
        {/* The default spend budget (#1054): every session, spawned or not, so it sits with the
            archive period. Fixed choices for the same reason as the child limit below. */}
        <Row label={tr("agents.spend_cap_default")}>
          <Choice
            value={SPEND_CAP_DEFAULTS.includes(s.sessionSpendCapUsd) ? s.sessionSpendCapUsd : 0}
            options={SPEND_CAP_DEFAULTS.map((n): [number, string] => [n, n === 0 ? tr("agents.spend_cap_none") : "$" + n])}
            onChange={(v) => setSetting("sessionSpendCapUsd", v)}
          />
        </Row>
        <p className="muted ds-note">{tr("agents.note_spend_cap_default")}</p>
        {/* Cross-session messaging (ADR 0041) applies to every kind af's own MCP is distributed
            to; in any one agent's card it would look like that agent's setting. */}
        <Row label={tr("agents.peer_messaging")}>
          <OnOff value={s.peerMessaging} onChange={(v) => setSetting("peerMessaging", v)} />
        </Row>
        <p className="muted ds-note">{tr("agents.note_peer_messaging")}</p>
        {/* Session steering (ADR 0073) sits directly under peer messaging because the two answer
            the same question — what may a session do about the rest of the fleet — and are most
            often turned on together. */}
        <Row label={tr("agents.fleet_spawn")}>
          <OnOff value={s.sessionFleetSpawn} onChange={(v) => setSetting("sessionFleetSpawn", v)} />
        </Row>
        <p className="muted ds-note">{tr("agents.note_fleet_spawn")}</p>
        {/* PR feedback (#3129) is the Agent typing into a session on the PR's behalf, the same
            question as the two above — what reaches a session without the member. */}
        <Row label={tr("agents.pr_feedback")}>
          <OnOff value={s.prFeedback} onChange={(v) => setSetting("prFeedback", v)} />
        </Row>
        <p className="muted ds-note">{tr("agents.note_pr_feedback")}</p>
        {/* The budget (ADR 0073 decision 6). Only with the switch on: with steering off nothing
            ever consults it. A fixed set of choices rather than a number field — the Agent answers
            anything outside its range with the default, so a range the control cannot express
            would read as a silent downgrade. */}
        {s.sessionFleetSpawn && (
          <>
            <Row label={tr("agents.spawn_child_limit")}>
              <Choice
                value={s.sessionSpawnChildLimit}
                options={SPAWN_CHILD_LIMITS.map((n) => [n, String(n)])}
                onChange={(v) => setSetting("sessionSpawnChildLimit", v)}
              />
            </Row>
            <p className="muted ds-note">{tr("agents.note_spawn_child_limit")}</p>
          </>
        )}
        {/* Past-session search (ADR 0110): the same kind of switch — one af MCP tool every kind
            gets. */}
        <Row label={tr("agents.session_search")}>
          <OnOff value={s.sessionSearch} onChange={(v) => setSetting("sessionSearch", v)} />
        </Row>
        <p className="muted ds-note">{tr("agents.note_session_search")}</p>
        {/* AF memory (ADR 0108) is another af tool set every kind gets, but its switch lives on the
            Agent memory page beside the change list and the import that needs it on; this line
            only says where, so a member looking among the tool switches still finds it. */}
        <p className="muted ds-note">{tr("agents.note_agent_memory_moved")}</p>
      </section>
    </div>
  );
}
