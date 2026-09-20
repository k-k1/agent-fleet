package fleetgraph

// The REST DTO. Field names and shapes here MUST match console/src/types/fleetgraph.ts
// exactly — that file is frozen and is the authority; this file is the Go mirror of it,
// never the other way around. Time is unix millis everywhere below (decision 2); the
// ledger's own RFC3339 lines are converted on the way out in read.go.

type BirthEvent struct {
	Ev            string `json:"ev"`
	Ts            int64  `json:"ts"`
	Name          string `json:"name"`
	Kind          string `json:"kind"`
	Repo          string `json:"repo,omitempty"`
	Origin        string `json:"origin"`
	OriginSession string `json:"originSession,omitempty"`
	Conv          string `json:"conv,omitempty"`
	ForkFrom      string `json:"forkFrom,omitempty"`
	Display       string `json:"display,omitempty"`
}

type ConvIdEvent struct {
	Ev   string `json:"ev"`
	Ts   int64  `json:"ts"`
	Name string `json:"name"`
	Conv string `json:"conv"`
}

type DeathEvent struct {
	Ev     string `json:"ev"`
	Ts     int64  `json:"ts"`
	Name   string `json:"name"`
	Reason string `json:"reason,omitempty"`
	Code   int    `json:"code,omitempty"`
	Signal int    `json:"signal,omitempty"`
}

type ReviveEvent struct {
	Ev   string `json:"ev"`
	Ts   int64  `json:"ts"`
	Name string `json:"name"`
}

type ArchivedEvent struct {
	Ev       string `json:"ev"`
	Ts       int64  `json:"ts"`
	Name     string `json:"name"`
	Archived bool   `json:"archived"`
}

type StateEvent struct {
	Ev   string `json:"ev"`
	Ts   int64  `json:"ts"`
	Name string `json:"name"`
	From string `json:"from,omitempty"`
	To   string `json:"to"`
	Raw  string `json:"raw,omitempty"`
}

type ResyncEvent struct {
	Ev   string `json:"ev"`
	Ts   int64  `json:"ts"`
	Name string `json:"name"`
	To   string `json:"to"`
	Raw  string `json:"raw,omitempty"`
}

type InstructEvent struct {
	Ev      string `json:"ev"`
	Ts      int64  `json:"ts"`
	From    string `json:"from"`
	To      string `json:"to"`
	Source  string `json:"source,omitempty"`
	Excerpt string `json:"excerpt,omitempty"`
}

type ReportEvent struct {
	Ev     string `json:"ev"`
	Ts     int64  `json:"ts"`
	From   string `json:"from"`
	To     string `json:"to"`
	Kind   string `json:"kind,omitempty"`
	Reason string `json:"reason,omitempty"`
}

type PeerEvent struct {
	Ev      string `json:"ev"`
	Ts      int64  `json:"ts"`
	From    string `json:"from"`
	To      string `json:"to"`
	Intent  string `json:"intent"`
	Excerpt string `json:"excerpt,omitempty"`
}

// GraphCoverage carries the 3-tier decay boundary (ADR 0096 decision 8) so the view can
// draw where the record thins out rather than letting the figure fade silently.
type GraphCoverage struct {
	ActivitySince    *int64 `json:"activitySince"`
	LineageSince     *int64 `json:"lineageSince"`
	BackfilledBefore int64  `json:"backfilledBefore,omitempty"`
}

// FleetGraphPage is the body of GET /api/fleet-graph?since=<ms>&until=<ms>.
type FleetGraphPage struct {
	Since    int64         `json:"since"`
	Until    int64         `json:"until"`
	Now      int64         `json:"now"`
	Lineage  []any         `json:"lineage"`
	Activity []any         `json:"activity"`
	Coverage GraphCoverage `json:"coverage"`
}

// toDTOLineage converts one on-disk lineageLine (RFC3339 ts) into its frozen wire shape
// (millis ts) — the ONE place that conversion happens (decision 2).
func toDTOLineage(l lineageLine, ms int64) any {
	switch l.Ev {
	case "birth":
		return BirthEvent{
			Ev: "birth", Ts: ms, Name: l.Name, Kind: l.Kind, Repo: l.Repo,
			Origin: l.Origin, OriginSession: l.OriginSession, Conv: l.Conv,
			ForkFrom: l.ForkFrom, Display: l.Display,
		}
	case "convid":
		return ConvIdEvent{Ev: "convid", Ts: ms, Name: l.Name, Conv: l.Conv}
	case "death":
		return DeathEvent{Ev: "death", Ts: ms, Name: l.Name, Reason: l.Reason, Code: l.Code, Signal: l.Signal}
	case "revive":
		return ReviveEvent{Ev: "revive", Ts: ms, Name: l.Name}
	case "archived":
		archived := l.Archived != nil && *l.Archived
		return ArchivedEvent{Ev: "archived", Ts: ms, Name: l.Name, Archived: archived}
	default:
		return nil
	}
}

// toDTOActivity converts one on-disk activityLine into its frozen wire shape.
func toDTOActivity(a activityLine) any {
	ms := parseMillis(a.Ts)
	switch a.Ev {
	case "state":
		return StateEvent{Ev: "state", Ts: ms, Name: a.Name, From: a.From, To: a.To, Raw: a.Raw}
	case "resync":
		return ResyncEvent{Ev: "resync", Ts: ms, Name: a.Name, To: a.To, Raw: a.Raw}
	case "instruct":
		return InstructEvent{Ev: "instruct", Ts: ms, From: a.From, To: a.To, Source: a.Source, Excerpt: a.Excerpt}
	case "report":
		return ReportEvent{Ev: "report", Ts: ms, From: a.From, To: a.To, Kind: a.Kind, Reason: a.Reason}
	case "peer":
		return PeerEvent{Ev: "peer", Ts: ms, From: a.From, To: a.To, Intent: a.Intent, Excerpt: a.Excerpt}
	default:
		return nil
	}
}
