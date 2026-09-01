# 0066. Record occupancy per hour and draw it as a heatmap — occupancy, never money

English | [日本語](0066-uptime-heatmap.ja.md)

- Status: **accepted — P0 (hourly sampling), P1 (API) and P2 (three Console surfaces)
  implemented** (2026-09-01). The design and the measurements are in
  [docs/83](../log/83-uptime-heatmap.md).
- Related: [0048-member-cloud-cost.md](0048-member-cloud-cost.md) (the money surface; 決定 2
  there is what forbids pricing an hour) / [0029-usage-accounting.md](0029-usage-accounting.md)
  (the token surface, and why "usage" already names three things) /
  [0055-idle-stop-and-carried-interactions.md](0055-idle-stop-and-carried-interactions.md) (`sessionActivity` — the single definition of "busy")

## Background

Everything this system knows about occupancy is one number per day: `usage_daily.running_secs`
and, where there is an AWS bill, `cloud_cost_daily`. That is enough to see that Friday cost more
and not enough to see **why** — a workspace nobody stopped and a workspace somebody worked in look
identical at day resolution. The number of sessions open inside a running workspace is not
recorded over time at all: the control plane's `session` table is a snapshot that
`ReplaceSessions` wipes on every read.

An administrator looking at a member has two buttons (force-stop, disk quota) and no evidence for
choosing between them.

## Decisions

**決定 1. Hour buckets in a new table, filled by the existing sampler.** `usage_hourly` keyed by
`(membership_id, hour)` in UTC, written by the 5-minute showback sampler that already walks every
tenant's workspaces. No second resident timer: the same walk on its own ticker is how this host
has run out of memory before.

**決定 2. Occupancy only — the heatmap never carries money.** Cost Explorer reports per day, so an
hourly figure could only be seconds multiplied by a rate somebody typed once, which
[0048](0048-member-cloud-cost.md) 決定 2 refused. The feature therefore lives under **稼働時間 /
Running time**, not under Cloud cost. Where both are shown (the member detail), the heatmap sits as
a separate card **below** the cost card, so a day's bill is explained without being repriced.

**決定 3. Three states, not two — a heartbeat row records that the sampler ran.**
`membership_id = ''` is the hour's heartbeat (the same convention as `cloud_cost_daily`'s shared
bucket). Heartbeat present with no member row = observed and stopped (grey); no heartbeat = never
observed (blank). Without it, a day the control plane was down renders as a confident "this member
ran nothing". The heartbeat is written only when the sweep enumerated everything — a partial sweep
must not paint grey over workspaces it never reached.

**決定 4. `measured_secs` separates "no sessions" from "we could not ask".** A running workspace
whose Agent is unreachable is exactly the case where the count is unknown; recording zero would
draw a cold cell over a busy hour. Session counters accumulate only over the seconds the Agent
actually answered, and the cell says "running, session count unknown".

**決定 5. "Busy" is whatever `sessionActivity` says, including keep-awake pins.** The predicate is
called, never copied — a copy goes stale the day a new session state is added. Pins counting as
busy is deliberate: a pin that stayed warm all weekend is precisely the waste this screen exists
to show.

**決定 6. The API returns per-member series; the client sums them.** The aggregate and the hover
breakdown come from the same payload, so a total that disagrees with its own breakdown is not
expressible. All three entry points return one shape, for the reason
[0048](0048-member-cloud-cost.md) gave: two shapes drift the day one of them is fixed.

**決定 7. Buckets are stored in UTC and shifted to local time by the browser.** A 24-row grid drawn
in UTC tells a reader in Tokyo that they work at four in the morning. The client asks for one extra
day on each side to fill its edge columns; a half-hour offset rounds to the hour a bucket starts
in, and says so on screen.

**決定 8. The ramp is an ordinal one-hue scale, validated per mode.** Not the categorical
`--viz-1..8` slots — this encodes magnitude. Both modes were run through the dataviz validator in
`--ordinal` mode against the modal surface. Because there are only five steps, the metrics differ
in how they spend the bottom one: for session counts, "running with none open" is the lowest step
(and is *not* the grey of a stopped hour); for the running-time metric, an empty cell is not drawn
at all, so all five steps encode magnitude.

**決定 9. Retention on the hourly table, none on the daily one.** 24x the rows, answering a
question nobody asks about last spring: 92 days. `usage_daily`, which does get asked, keeps
everything.

## Consequences

- **The history cannot be backfilled.** Every day between this decision and the sampler reaching a
  deployment is permanently missing, which is why P0 shipped before the UI — the same asymmetry
  cost allocation tags taught in [0048](0048-member-cloud-cost.md).
- A per-hour table is 24x the write volume of the daily one, bounded by the sampler's interval and
  the number of *running* workspaces (a stopped workspace writes nothing).
- Reading session counts adds one Agent request per running workspace per sample, bounded at 10
  seconds each so a wedged Agent cannot outrun the sampler's own tick.
- Members now have a third occupancy-adjacent tab (tokens / cloud cost / running time). The names
  stay distinct on purpose; "usage" was not reused a fourth time.
