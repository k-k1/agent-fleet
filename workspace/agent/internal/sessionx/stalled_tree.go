package sessionx

// The stalled-tree watch (#2397): a parent session and all its live children can sit idle,
// each waiting on another, and nothing in Fleet notices. Idle-stop eventually folds them, hours
// later. This watch injects ONE notice into the parent after the whole tree has been quiet for
// stalledTreeAfter, naming the idle children so the parent can find whoever holds the ball.
//
// A tree is a root (a live session with a live child, whose own parent is not live) and every
// live descendant through Meta.OriginSession. It is quiet when ALL of these hold: the root and
// every live descendant read idle (the state wireSession computes for the session list, the same
// one fleetgraph.ObserveState records, so there is no second classifier; a question, plan,
// permission or limit wait is not idle), none holds background work, and no held or pending peer
// message is addressed to or sent by a member.
//
// It runs on its own loop for the reason rate_limit_resume.go does: a check that fires only while
// someone polls the session list is worthless at 3 a.m. The loop re-reads each tree every tick,
// and a tree's signature (membership, state and last turn end of every member) tells it whether
// anything happened in between, which is also what absorbs the ~20 s lag of a polled state: the
// notice needs stalledTreeAfter of unchanged signature, not one lucky idle reading.
//
// Once per quiet episode. After the nudge only a change in a member OTHER than the root re-arms
// it: the root's own turn (the one the notice caused) always changes the root's state, and
// counting that would nudge a parent that answered "all done" every stalledTreeAfter, one whole
// turn each time. A parent that does act on the notice moves a child, which re-arms.
//
// Managed (codex / opencode) parents are reached: injectSessionPrompt routes them through the
// driver handle's Send, the same as the abort-resume nudge.

import (
	"fmt"
	"log"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/fleetgraph"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

const (
	// stalledTreeDefaultMinutes must stay well below the idle-stop timeouts, or the tree is
	// folded before it is nudged.
	stalledTreeDefaultMinutes = 15
	// stalledTreeEnv overrides it, in minutes; 0 turns the watch off. A constant plus an env
	// switch rather than a setting: the idle-stop timeouts live on the Control Plane, there is
	// no Agent-side knob next to them to join.
	stalledTreeEnv = "AF_STALLED_TREE_MINUTES"
	// stalledTreeRetry spaces delivery retries when the root cannot take the notice right now.
	stalledTreeRetry = 5 * time.Minute
)

// stalledTreeInterval is the loop's tick; a variable so a test could shorten it.
var stalledTreeInterval = 30 * time.Second

// stalledTreeAfter reads the quiet period. 0 means disabled.
func stalledTreeAfter() time.Duration {
	v := strings.TrimSpace(os.Getenv(stalledTreeEnv))
	if v == "" {
		return stalledTreeDefaultMinutes * time.Minute
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return stalledTreeDefaultMinutes * time.Minute
	}
	return time.Duration(n) * time.Minute
}

// Side effects, replaceable by tests (no tmux, no driver).
var (
	stalledTreeAlive  = SessionAlive
	stalledTreeWire   = wireSession
	stalledTreeBusy   = managedBusy
	stalledTreeReady  = func(m session.Meta) bool { ready, _ := peerDeliveryReady(m); return ready }
	stalledTreeInject = injectSessionPrompt
)

// stalledMember is one live session of a tree as observed this tick.
type stalledMember struct {
	Name string
	Idle bool
	// Busy: background work (the list's BackgroundBusy) or a Managed turn running or queued.
	Busy bool
	sig  string
}

// stalledTree is one root's observation.
type stalledTree struct {
	Root     string
	Members  []stalledMember // Members[0] is the root
	Children []string        // the live descendants, for the notice text
}

func (t stalledTree) names() map[string]bool {
	out := make(map[string]bool, len(t.Members))
	for _, m := range t.Members {
		out[m.Name] = true
	}
	return out
}

func (t stalledTree) signature(skipRoot bool) string {
	var parts []string
	for i, m := range t.Members {
		if skipRoot && i == 0 {
			continue
		}
		parts = append(parts, m.sig)
	}
	sort.Strings(parts)
	return strings.Join(parts, ";")
}

// quiet reports whether every member is idle with no background work and no peer message is
// held or queued around any of them.
func (t stalledTree) quiet() bool {
	for _, m := range t.Members {
		if !m.Idle || m.Busy {
			return false
		}
	}
	return len(t.Members) > 1 && !peersInFlight(t.names())
}

// peersInFlight reports a peer message held or queued on disk, or claimed from the pending spool
// and still being delivered, to or from a member.
func peersInFlight(members map[string]bool) bool {
	return agents.QueuedPeerTouches(members) || pendingInflightTouches(members)
}

type stalledEpisode struct {
	sig, childSig string
	since         time.Time // when the current signature was first seen
	fired         bool
	retryAt       time.Time // a failed delivery waits for this instant
}

var (
	stalledMu       sync.Mutex
	stalledEpisodes = map[string]*stalledEpisode{}
)

// stalledTreeStep advances the episode of one tree and reports whether to nudge its root now.
func stalledTreeStep(t stalledTree, now time.Time, after time.Duration) bool {
	stalledMu.Lock()
	defer stalledMu.Unlock()
	sig, childSig := t.signature(false), t.signature(true)
	ep := stalledEpisodes[t.Root]
	switch {
	case ep == nil:
		ep = &stalledEpisode{sig: sig, childSig: childSig, since: now}
		stalledEpisodes[t.Root] = ep
	case ep.fired:
		if childSig != ep.childSig {
			*ep = stalledEpisode{sig: sig, childSig: childSig, since: now}
		} else {
			ep.sig = sig
		}
	case sig != ep.sig:
		*ep = stalledEpisode{sig: sig, childSig: childSig, since: now}
	}
	if ep.fired {
		return false
	}
	if !t.quiet() {
		ep.since = now
		return false
	}
	return now.Sub(ep.since) >= after && !now.Before(ep.retryAt)
}

func stalledTreeFired(root string) {
	stalledMu.Lock()
	defer stalledMu.Unlock()
	if ep := stalledEpisodes[root]; ep != nil {
		ep.fired = true
	}
}

func stalledTreeRetryLater(root string, now time.Time) {
	stalledMu.Lock()
	defer stalledMu.Unlock()
	if ep := stalledEpisodes[root]; ep != nil {
		ep.retryAt = now.Add(stalledTreeRetry)
	}
}

// stalledTreePrune forgets episodes of roots that are no longer roots.
func stalledTreePrune(roots map[string]bool) {
	stalledMu.Lock()
	defer stalledMu.Unlock()
	for r := range stalledEpisodes {
		if !roots[r] {
			delete(stalledEpisodes, r)
		}
	}
}

// StartStalledTreeWatch runs the watch for the life of the agent.
func StartStalledTreeWatch() {
	go func() {
		time.Sleep(50 * time.Second) // let tmux and the drivers come up after a start
		for {
			stalledTreeTick(time.Now())
			time.Sleep(stalledTreeInterval)
		}
	}()
}

// stalledTreeTick is one sweep over every tree.
func stalledTreeTick(now time.Time) {
	after := stalledTreeAfter()
	if after <= 0 {
		return
	}
	trees := collectStalledTrees()
	roots := map[string]bool{}
	for _, t := range trees {
		roots[t.Root] = true
		if !stalledTreeStep(t, now, after) {
			continue
		}
		stalledTreeNudge(t, now, after)
	}
	stalledTreePrune(roots)
}

// collectStalledTrees groups the live sessions into trees. Only members of a tree are read
// through wireSession; a session with no parent and no child costs one liveness check.
func collectStalledTrees() []stalledTree {
	metas := map[string]session.Meta{}
	kids := map[string][]string{}
	for _, m := range session.ListMetas() {
		if m.Archived {
			continue
		}
		metas[m.Name] = m
		if m.OriginSession != "" && m.OriginSession != m.Name {
			kids[m.OriginSession] = append(kids[m.OriginSession], m.Name)
		}
	}
	alive := map[string]bool{}
	isLive := func(name string) bool {
		v, seen := alive[name]
		if !seen {
			m, ok := metas[name]
			v = ok && stalledTreeAlive(m)
			alive[name] = v
		}
		return v
	}
	var names []string
	for name := range kids {
		names = append(names, name)
	}
	sort.Strings(names)
	var out []stalledTree
	for _, root := range names {
		m, ok := metas[root]
		if !ok || !isLive(root) || (m.OriginSession != "" && isLive(m.OriginSession)) {
			continue // not a live root, or a part of its live parent's tree
		}
		t := stalledTree{Root: root}
		seen := map[string]bool{root: true}
		queue := []string{root}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			t.Members = append(t.Members, observeStalledMember(metas[cur], cur != root, &t))
			sort.Strings(kids[cur])
			for _, k := range kids[cur] {
				if !seen[k] && isLive(k) {
					seen[k] = true
					queue = append(queue, k)
				}
			}
		}
		if len(t.Members) > 1 {
			out = append(out, t)
		}
	}
	return out
}

func observeStalledMember(m session.Meta, child bool, t *stalledTree) stalledMember {
	s := stalledTreeWire(m, true)
	state, _ := fleetgraph.NormalizeState(s.State)
	if child {
		t.Children = append(t.Children, m.Name)
	}
	return stalledMember{
		Name: m.Name,
		Idle: state == fleetgraph.StateIdle,
		Busy: s.BackgroundBusy || stalledTreeBusy(m),
		// LastTurnEndAt catches a whole turn that began and ended between two ticks.
		sig: m.Name + "|" + string(state) + "|" + s.LastTurnEndAt,
	}
}

// stalledTreeNotice is the text injected into the parent.
func stalledTreeNotice(children []string, quiet time.Duration) string {
	mins := int(quiet / time.Minute)
	return fmt.Sprintf("[agent-fleet:notice kind=stalled-tree reply=none] This session and its live children (%s) "+
		"have all been idle for %d min, with no peer message queued or in flight. This is an automated check, "+
		"sent once. If you are waiting for a report, check whether someone is waiting on someone else: "+
		"read each child with peek_session_output / get_session_output, then move the one holding the ball. "+
		"If the work is finished, ignore this.", strings.Join(children, ", "), mins)
}

func stalledTreeNudge(t stalledTree, now time.Time, after time.Duration) {
	// The tick's reading can be stale by the time it reaches the deadline: a state is polled, and
	// a child may have taken an input or started a Managed turn since. Everything is read again,
	// from the metas and the live handles, immediately before sending; any change restarts the
	// quiet period instead of being retried.
	if !stalledTreeStillQuiet(t) {
		stalledTreeRestart(t.Root, now)
		return
	}
	root, _ := session.ReadMeta(t.Root)
	prompt := stalledTreeNotice(t.Children, after)
	// Recorded before the send so the mirror badges the turn from its first poll (see
	// recordInjection).
	recordInjection(t.Root, prompt, TurnSourceNotice)
	if err := stalledTreeInject(root.Name, prompt, agents.Origin{Kind: agents.OriginNotice}); err != nil {
		stalledTreeRetryLater(t.Root, now)
		log.Printf("stalled-tree: could not deliver the notice to %s: %v", t.Root, err)
		return
	}
	stalledTreeFired(t.Root)
	recordFleetGraphInstruct(t.Root, TurnSourceNotice, "", "", prompt)
	fleetgraph.RecordStall(t.Root, fmt.Sprintf("%s idle %d min", strings.Join(t.Children, ", "), int(after/time.Minute)))
	log.Printf("stalled-tree: nudged %s (children %s idle for %s)", t.Root, strings.Join(t.Children, ", "), after)
}

// stalledTreeStillQuiet is the last look before a send: every member is still alive and not
// archived, can take input right now (idle, no pending question, no initial prompt still being
// delivered), runs nothing in the background, and no peer message is in flight around the tree.
func stalledTreeStillQuiet(t stalledTree) bool {
	for _, mem := range t.Members {
		m, ok := session.ReadMeta(mem.Name)
		if !ok || m.Archived || !stalledTreeAlive(m) {
			return false
		}
		if m.InitialPromptState == session.InitialPromptPending || stalledTreeBusy(m) || !stalledTreeReady(m) {
			return false
		}
	}
	// The same reading the tick takes, again: membership, every state and last turn end must be
	// what the quiet period was counted on. A short turn that began and ended since the tick
	// leaves the member ready and idle again, and only its turn end shows it.
	for _, fresh := range collectStalledTrees() {
		if fresh.Root == t.Root {
			return fresh.quiet() && fresh.signature(false) == t.signature(false)
		}
	}
	return false
}

func stalledTreeRestart(root string, now time.Time) {
	stalledMu.Lock()
	defer stalledMu.Unlock()
	if ep := stalledEpisodes[root]; ep != nil {
		ep.since = now
	}
}
