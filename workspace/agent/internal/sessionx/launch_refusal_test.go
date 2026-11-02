package sessionx

// A launch refused by a sentinel the Console renders from its catalogue (not connected, codex
// still holding the thread) must come back as a 409 + code on every route that starts an agent,
// Terminal (CLI) included. A raw 500 start_failed / tmux_failed is retryable to the Console and
// shows only the Agent's text, which has no language setting.

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"testing"

	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/codex"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/agents/opencode"
	"github.com/k-k1/agent-fleet/workspace/agent/internal/session"
)

func setRefusalCodes(t *testing.T) {
	t.Helper()
	prevA, prevR := errCodeAgentNotConnected, errCodeAgentUnsupportedVer
	errCodeAgentNotConnected, errCodeAgentUnsupportedVer = "agent_not_connected", "agent_unsupported_version"
	t.Cleanup(func() { errCodeAgentNotConnected, errCodeAgentUnsupportedVer = prevA, prevR })
}

var launchRefusalCases = []struct {
	name   string
	err    error
	status int
	code   string
}{
	{"opencode not connected", fmt.Errorf("launch: %w", opencode.ErrNotConnected), http.StatusConflict, "agent_not_connected"},
	{"codex not logged in", fmt.Errorf("launch: %w", codex.ErrNotLoggedIn), http.StatusConflict, "agent_not_connected"},
	{"codex releasing", fmt.Errorf("launch: %w", codex.ErrThreadReleasing), http.StatusConflict, "codex_releasing"},
}

func TestStartSessionLaunchRefusals(t *testing.T) {
	setRefusalCodes(t)
	for _, tc := range launchRefusalCases {
		t.Run(tc.name, func(t *testing.T) {
			home := writebackEnv(t)
			const name = "refuse1"
			session.WriteMeta(session.Meta{Name: name, Dir: home, Kind: session.KindCodex, Driver: session.DriverManaged,
				StoppedAt: "2026-09-25T10:00:00+09:00"})
			useLockingDriver(t, session.KindCodex, &lockingDriver{resumeErr: tc.err})
			rec := callHandler(HandleStartSession, http.MethodPost, name, "")
			var body struct {
				Error struct{ Code string } `json:"error"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if rec.Code != tc.status || body.Error.Code != tc.code {
				t.Errorf("status/code = %d/%q, want %d/%q (%s)", rec.Code, body.Error.Code, tc.status, tc.code, rec.Body.String())
			}
		})
	}
}

func TestCreateTerminalLaunchRefusals(t *testing.T) {
	setRefusalCodes(t)
	for _, tc := range launchRefusalCases {
		t.Run(tc.name, func(t *testing.T) {
			p := newCreateProbe(t)
			launchTmuxFn = func(session.Meta, bool) error { return tc.err }
			rec := p.post(map[string]any{"kind": "claude", "dir": p.repo}, "")
			var body struct {
				Error struct{ Code string } `json:"error"`
			}
			_ = json.Unmarshal(rec.Body.Bytes(), &body)
			if rec.Code != tc.status || body.Error.Code != tc.code {
				t.Errorf("status/code = %d/%q, want %d/%q (%s)", rec.Code, body.Error.Code, tc.status, tc.code, rec.Body.String())
			}
		})
	}
	t.Run("other failure stays tmux_failed", func(t *testing.T) {
		p := newCreateProbe(t)
		launchTmuxFn = func(session.Meta, bool) error { return errors.New("tmux died") }
		rec := p.post(map[string]any{"kind": "claude", "dir": p.repo}, "")
		if rec.Code != http.StatusInternalServerError {
			t.Errorf("status = %d, want 500 (%s)", rec.Code, rec.Body.String())
		}
	})
}
