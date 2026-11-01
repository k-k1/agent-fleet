package sessionx

// GET /sessions/{name}/move/preview: what the move dialog shows before the user confirms
// (ADR 0115 P2a, decisions 5, 11.7, 12 and 13). Read without the gate, so it can be stale by the
// time the move runs; the move checks everything again under it.

import (
	"errors"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/bridge"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/httpx"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

// moveTargetOrder is every agent kind the preview reports on, eligible or not, so the dialog can
// say why one is missing.
var moveTargetOrder = []string{
	session.KindClaude, session.KindCodex, session.KindOpencode, session.KindCopilot,
	session.KindAgy, session.KindMuse, session.KindCursor, session.KindKiro, session.KindLcpp,
}

type movePreviewWire struct {
	Source string `json:"source"`
	Kind   string `json:"kind"`
	Driver string `json:"driver,omitempty"`
	State  string `json:"state"`
	// Refusal is why a move would be refused now; nil when it may start.
	Refusal *moveErrBody     `json:"refusal,omitempty"`
	Targets []moveTargetWire `json:"targets"`
	// CanFork: the source's own kind can fork, which the dialog offers instead of a same-kind move.
	CanFork   bool             `json:"canFork,omitempty"`
	Git       *moveGitWire     `json:"git,omitempty"`
	Queued    []moveQueuedWire `json:"queued,omitempty"`
	Peers     []movePeerWire   `json:"peers,omitempty"`
	Children  []moveChildWire  `json:"children,omitempty"`
	Proposals []string         `json:"proposals,omitempty"`
	// SizeBytes is the rendered file's size as it would be written now (without the per-target
	// instruction lines), and OverSizeTarget says the soft target could not be met.
	SizeBytes      int      `json:"sizeBytes,omitempty"`
	OverSizeTarget bool     `json:"overSizeTarget,omitempty"`
	Warnings       []string `json:"warnings,omitempty"`
	Moving         string   `json:"moving,omitempty"`
	MovedTo        string   `json:"movedTo,omitempty"`
	// Pending is a move waiting for retry or cancel.
	Pending *movePendingWire `json:"pending,omitempty"`
	// Delivery is, for a moved source, whether its target got the first prompt that points at
	// the file; nil once it did.
	Delivery *moveDeliveryWire `json:"delivery,omitempty"`
}

type moveTargetWire struct {
	Kind     string `json:"kind"`
	Eligible bool   `json:"eligible"`
	Reason   string `json:"reason,omitempty"`
}

type moveGitWire struct {
	Available bool   `json:"available"`
	Dirty     bool   `json:"dirty"`
	Status    string `json:"status,omitempty"`
	DiffStat  string `json:"diffStat,omitempty"`
	Ahead     string `json:"ahead,omitempty"`
}

type moveQueuedWire struct {
	Text        string   `json:"text"`
	Origin      string   `json:"origin,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
}

type movePeerWire struct {
	From    string `json:"from"`
	Intent  string `json:"intent,omitempty"`
	Message string `json:"message"`
}

type moveChildWire struct {
	Name    string `json:"name"`
	State   string `json:"state"`
	Running bool   `json:"running"`
}

type movePendingWire struct {
	Target       string `json:"target"`
	Kind         string `json:"kind,omitempty"`
	File         string `json:"file,omitempty"`
	State        string `json:"state,omitempty"`
	Error        string `json:"error,omitempty"`
	TargetExists bool   `json:"targetExists"`
	// Orphan: something runs under the target's name without a meta; cancel stops it.
	Orphan bool `json:"orphan,omitempty"`
	// Busy: a move, retry or cancel of this source is running in this Agent right now.
	Busy bool `json:"busy,omitempty"`
}

// HandleSessionMovePreview answers what a move of the session would carry and whether it may
// start. GET /sessions/{name}/move/preview.
func HandleSessionMovePreview(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if !session.ValidName(name) {
		httpx.WriteErr(w, http.StatusBadRequest, "bad_name", "invalid session name")
		return
	}
	m, ok := session.ReadMeta(name)
	if !ok {
		httpx.WriteErr(w, http.StatusNotFound, "not_found", "no such session: "+name)
		return
	}
	httpx.WriteJSON(w, http.StatusOK, buildMovePreview(m, time.Now()))
}

func buildMovePreview(m session.Meta, now time.Time) movePreviewWire {
	p := movePreviewWire{Source: m.Name, Kind: m.Kind, Driver: m.Driver, Moving: m.Moving, MovedTo: m.MovedTo,
		CanFork: moveCanFork(m.Kind), Targets: []moveTargetWire{}}
	p.State, _ = moveSourceState(m)
	// Everything below that comes out of the conversation or the working copy is scrubbed, as
	// the file is (decision 7): a queued prompt or a commit subject can carry a token, and this
	// answer goes to the browser.
	sc := bridge.ScrubSecrets
	refuse := func(err error) {
		if p.Refusal == nil {
			e := asMoveError(err)
			p.Refusal = &moveErrBody{Code: e.Code, Message: e.Message, Target: e.Target, State: e.State}
		}
	}
	if rec, ok := moveRecords.Read(m.Name); ok && (rec.Target == m.Moving || rec.Target == m.MovedTo) {
		pr := moveTargetPresence(rec)
		p.Pending = &movePendingWire{Target: rec.Target, Kind: rec.Kind, File: rec.File, State: rec.State,
			Error: sc(rec.Error), TargetExists: pr == targetRecorded || pr == targetUnreadable,
			Orphan: pr == targetOrphan, Busy: moveOpBusy(m.Name)}
	} else if m.Moving != "" {
		pr := moveTargetPresence(moveRecord{Target: m.Moving})
		p.Pending = &movePendingWire{Target: m.Moving, Busy: moveOpBusy(m.Name),
			TargetExists: pr == targetRecorded || pr == targetUnreadable, Orphan: pr == targetOrphan}
	}
	if m.MovedTo != "" {
		p.Delivery = moveDeliveryState(m.MovedTo)
	}
	if m.Kind == session.KindCursor && m.DriverKind() == session.DriverManaged {
		p.Warnings = append(p.Warnings, moveWarnCursorMirror)
	}
	if err := checkMoveRequest(m, moveRequest{Kind: anyEligibleTarget(m.Kind)}); err != nil {
		var me *moveError
		if errors.As(err, &me) && me.Code != "move_same_kind" {
			refuse(err)
		}
	}
	if err := checkMoveSource(m); err != nil {
		refuse(err)
	}
	for _, k := range moveTargetOrder {
		reason := moveTargetReason(m.Kind, k)
		p.Targets = append(p.Targets, moveTargetWire{Kind: k, Eligible: reason == "", Reason: reason})
	}
	if m.Moving != "" || m.MovedTo != "" || !AgentOf(m.Kind).Caps().CanTranscript {
		return p // nothing to snapshot: the move is over, under way, or impossible
	}
	in := readMoveSource(m)
	g := in.Git
	p.Git = &moveGitWire{Available: g.Available, Dirty: g.Status != "", Status: sc(g.Status), DiffStat: sc(g.DiffStat), Ahead: sc(g.Ahead)}
	in.Now, in.Loc = now, time.Local
	h, err := BuildHandover(in)
	if err != nil {
		refuse(err)
	} else {
		md, _ := RenderHandover(h, "")
		p.SizeBytes, p.OverSizeTarget = len(md), len(md) > handoverSizeTarget
	}
	for _, q := range handoverStopped(in, nil).Queued {
		var att []string
		for _, a := range q.Attachments {
			att = append(att, sc(a))
		}
		p.Queued = append(p.Queued, moveQueuedWire{Text: sc(q.Text), Origin: sc(q.Origin), Attachments: att})
	}
	for _, pm := range in.PeerMessages {
		p.Peers = append(p.Peers, movePeerWire{From: sc(pm.From), Intent: sc(pm.Intent), Message: sc(pm.Message)})
	}
	running := false
	for _, c := range in.Children {
		live := c.State != "stopped"
		running = running || live
		p.Children = append(p.Children, moveChildWire{Name: sc(c.Name), State: c.State, Running: live})
	}
	if running {
		p.Warnings = append(p.Warnings, moveWarnChildrenActive)
	}
	for _, pr := range in.Proposals {
		p.Proposals = append(p.Proposals, sc(pr))
	}
	return p
}

// anyEligibleTarget is some kind the source could move to, so the request check answers for the
// source alone.
func anyEligibleTarget(source string) string {
	for _, k := range moveTargetKinds() {
		if k != source {
			return k
		}
	}
	return ""
}
