// Package lcpp is the vertical package for the lcpp kind (ADR 0093): a harness that talks
// directly to llama-server instead of driving a vendor CLI. This file wires the read-layer
// agents.Agent contract into the kind registry, on top of the managed driver (driver.go) and
// the transcript store (store.go) — the store is this kind's OWN transcript source, so
// Transcript() reads it directly rather than the generic managed/tui branch other kinds use.
//
// lcpp is the first kind with no Terminal(CLI) route at all (ADR 0093 decision 2): there is no
// program to put in a tmux pane, so BuildLaunch always fails and the kind is managed-only
// (agents.Caps.ManagedOnly).
package lcpp

import (
	"errors"
	"fmt"
	"strconv"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/harness"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/status"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/transcript"
)

// ErrNoTerminalRoute is BuildLaunch's unconditional refusal: lcpp has no tmux pane program, so
// this kind can never launch on the tui route (ADR 0093 decision 2).
var ErrNoTerminalRoute = errors.New("llama.cpp セッションには Terminal(CLI) 実行方式がありません（managed 専用の kind です）")

// New returns the lcpp Agent implementation for the kind registry.
func New() agents.Agent { return agentImpl{} }

type agentImpl struct{}

func (agentImpl) Kind() string { return session.KindLcpp }

// Caps: ManagedOnly (decision 2, no terminal route at all). CanTranscript/CanFork/CanForkAt/
// PermissionChoice are true because the store (store.go) and the driver (driver.go) actually
// back them now — Transcript() below reads the store unconditionally (no DriverManaged
// branch needed: the store is on disk regardless of whether a handle is live), ForkSource/
// ResolveForkAt below use the store's own ForkAt, and PermissionChoice is true because
// driver.go's approve() really does block the tool loop on a Console-answerable question-kind
// Interaction (docs/log/76's condition). UsesLabel is left false (no display-label
// convention for this kind, unlike claude).
func (agentImpl) Caps() agents.Caps {
	return agents.Caps{
		ManagedOnly:      true,
		CanTranscript:    true,
		CanFork:          true,
		CanForkAt:        true,
		PermissionChoice: true,
	}
}

// BuildLaunch always fails: lcpp has no tmux pane program (ADR 0093 decision 2).
func (agentImpl) BuildLaunch(session.Meta, agents.LaunchOpts) (agents.LaunchPlan, error) {
	return agents.LaunchPlan{}, ErrNoTerminalRoute
}

// WireLive reads the SAME generic status-store route claude's hook-driven state uses (ADR
// 0093 §4.2: "claude が使う generic の DriveState 経路に乗る") — driver.go's runTurn writes
// status.Persist itself (via agents.MarkTurnStart/MarkTurnEnd), so no per-kind polling is
// needed here, only the same EffectiveModal/LiveState read every other hook-driven kind's
// WireLive makes. LastSay carries the v1 cold-start line (decision 4) while a turn is running
// past wakingLastSayThreshold; "" once it settles or before any turn ever ran.
//
// Context is read straight off the store (Store.LastUsage — a disk read, no engine round
// trip) rather than computed here, so a session-list render never blocks on or wakes a
// sleeping engine. ADR 0093 decision 8: this kind never calls usagex.WindowGuess — when
// LastUsage's window is known (harness.EngineWindow's catalog lookup, the same one driver.go's
// runTurn already made for the turn itself, decision 7) WindowSource is "recorded"; when a
// turn ran before that lookup resolved anything, window is left at 0 and WindowSource empty
// rather than claiming "recorded" for a size nobody actually measured — the Console's own
// model-name guess is the honest fallback for that turn, same as every kind that has never
// reported a window. Before the first turn completes there is nothing recorded yet, so
// Context stays nil like every other kind's WireLive before its own first reply.
func (agentImpl) WireLive(m session.Meta, alive bool) agents.LiveInfo {
	li := agents.LiveInfo{Resumable: true}
	if u, window, ok := Open(sidFor(m)).LastUsage(); ok {
		ctx := &session.ContextUsage{Fresh: u.PromptTokens, Model: m.Model}
		if window > 0 {
			ctx.Window, ctx.WindowSource = window, "recorded"
		}
		li.Context = ctx
	}
	if !alive {
		return li
	}
	sid := sidFor(m)
	li.State = status.EffectiveModal(sid, status.LiveState(sid))
	if h := handleFor(m.Name); h != nil {
		li.LastSay = h.lastSay()
	}
	return li
}

// ClearResume is a no-op: sidFor(m) is a pure function of (dir, name), so there is no cached
// resume id to forget (driver.go's own sidFor doc comment).
func (agentImpl) ClearResume(string) {}

// Transcript reads the store directly, live handle or not — decision 3's whole point is that
// the store IS the persisted conversation, so there is no separate "stopped" fallback path the
// way cursor/kiro need (kiro's own transcript.go, DriverManaged-vs-file branch, does not apply
// here). Pending/Queued are folded in from the live handle when there is one; a stopped
// session simply shows neither.
func (agentImpl) Transcript(m session.Meta) (agents.TranscriptData, bool) {
	st := Open(sidFor(m))
	turns, err := st.TranscriptFor(m.Model)
	if err != nil {
		return agents.TranscriptData{}, false
	}
	// The store records no working directory of its own, but every builtin tool resolved its
	// path argument against this one (driver.go hands the harness Runtime the same m.CWD()),
	// and the changed-files aggregation silently drops a relative edit path from a turn with
	// no Cwd (sessionx absEditPath). It is a property of the session, not of a turn, so every
	// turn carries it — the mirror then also resolves relative links in the agent's own prose
	// against it (TranscriptTurn baseDir).
	cwd := m.CWD()
	for i := range turns {
		turns[i].Cwd = cwd
	}
	td := agents.TranscriptData{Turns: turns, Path: st.Path()}
	h := handleFor(m.Name)
	if h == nil {
		return td, true
	}
	h.mu.Lock()
	inter := h.inter
	mode := h.settings.Mode
	todos := h.todos
	var queued []string
	for _, in := range h.queue {
		queued = append(queued, in.Prompt)
	}
	h.mu.Unlock()
	if inter != nil {
		td.Pending = inter.Questions
	}
	td.Queued = queued
	if mode != "" {
		td.Mode = mode
	} else {
		td.Mode = "normal"
	}
	for i, t := range todos {
		td.Tasks = append(td.Tasks, taskFromTodo(i, t))
	}
	return td, true
}

// taskFromTodo converts harness's own TodoItem (tools_todo.go) into the mirror's
// transcript.Task shape. Todos carry no stable id of their own (harness.Runtime.Todos is
// replaced wholesale on every todo_write call), so the item's position in the list is used —
// stable enough for one render, which is all Tasks is for.
func taskFromTodo(i int, t harness.TodoItem) transcript.Task {
	return transcript.Task{ID: strconv.Itoa(i), Subject: t.Content, Status: t.Status}
}

// --- Forker / ForkAtResolver (decision 3: fork/fork-at, claude-style — a single route so
// agents.ErrForkAtRoute never applies) --------------------------------------------------

// ForkSource returns the sid HandleForkSession will carry as the new session's ForkFrom.
// Since the store IS keyed by sid (unlike claude/opencode's own native conversation ids),
// this is simply sidFor(m) — driver.go's ensureForked reads it back as the SOURCE store to
// copy from.
func (agentImpl) ForkSource(m session.Meta) (string, error) {
	recs, _, err := Open(sidFor(m)).Records()
	if err != nil {
		return "", err
	}
	if len(recs) == 0 {
		return "", errors.New("まだ会話がないためフォークできません")
	}
	return sidFor(m), nil
}

// ResolveForkAt validates the clicked anchor against this session's own store and returns the
// record id driver.go's ensureForked should cut at — decision 3's "経路が1つなので「この経路
// では fork できない」場合は無い": every lcpp session is managed, so ErrForkAtRoute never
// applies here.
//
// store.ForkAt is INCLUSIVE of the id it is given (copies recs[:idx+1]), so the default
// exclusive meaning ("keep up to, not including, the clicked user turn") resolves to the
// PRECEDING record's id. Include=true keeps that turn and the reply it got: this walks
// forward to the next top-level boundary (the next real user turn, or a compaction note) and
// resolves to the record just before it; on the LAST exchange there is no next boundary, so
// it returns "" — the same "whole conversation" value driver.go's ensureForked already gives
// that meaning for a plain (non-point) fork.
func (agentImpl) ResolveForkAt(m session.Meta, at agents.ForkPoint) (string, error) {
	recs, _, err := Open(sidFor(m)).Records()
	if err != nil {
		return "", err
	}
	idx := -1
	for i, r := range recs {
		if r.ID == at.Anchor {
			idx = i
			break
		}
	}
	if idx < 0 {
		return "", fmt.Errorf("フォーク元のターンが見つかりません: %s", at.Anchor)
	}
	if !at.Include {
		if idx == 0 {
			return "", errors.New("この会話の先頭より前ではフォークできません")
		}
		return recs[idx-1].ID, nil
	}
	for j := idx + 1; j < len(recs); j++ {
		if recs[j].Kind == KindUser || (recs[j].Kind == KindSystemNote && recs[j].Note == NoteCompaction) {
			return recs[j-1].ID, nil
		}
	}
	return "", nil // the last exchange: keep the whole conversation
}
