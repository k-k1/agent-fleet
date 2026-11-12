package main

// Member UI state (ADR 0114): the Console-only state that has to be readable while the workspace
// is stopped and has to follow the member across devices. Settings the Agent acts on stay in the
// workspace's ui-prefs; never keep one setting in both (decision 1).
//
//	Console ──▶ GET   /api/me/ui-state   the member's document, plus the tenant's `defaults`
//	        ──▶ PATCH /api/me/ui-state   replaces the top-level keys it carries, keeps the rest
//	                                     (?if_absent=1: only fills keys not stored yet)
//
// Both resolve with withMembership, so they never build or wake a workspace.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// memberUIStateMaxBody caps the stored document. The keys are flags and short enums; a document
// near this size means something is being stored here that does not belong (decision 3).
const memberUIStateMaxBody = 16 << 10

// memberUIStateRetries bounds the optimistic-write loop. Losing more than a few times in a row
// means a client is writing in a tight loop, which a 409 should surface rather than absorb.
const memberUIStateRetries = 4

// memberUIStateWire is the whole document and also a PATCH body: a PATCH replaces each
// top-level key it carries and leaves the absent ones alone. Read by the Console's
// core/store/uiState.ts. Add a key here, never an open map (decision 3).
type memberUIStateWire struct {
	Onboarding *onboardingStateWire `json:"onboarding,omitempty"`
	// Launch and Ops are the member's own override of the tenant's launch defaults and ops
	// settings (decision 8, P3): the top layer of the precedence, "" / null = not overridden.
	// They prefill the launch dialog; the Agent's ui-prefs are never written from here.
	Launch *launchDefaultsWire `json:"launch,omitempty"`
	Ops    *opsSettingsWire    `json:"ops,omitempty"`
}

// onboardingStateWire is the first-run state that used to live in localStorage: whether the
// member put the setup checklist away ("later"), and which goal track they opened.
type onboardingStateWire struct {
	Dismissed bool   `json:"dismissed"`
	Track     string `json:"track"` // "" or "dev"
}

// merge copies every key the patch carries over s. With ifAbsent it only fills keys s does not
// have yet: the Console's one-time localStorage migration uses that, so an old browser value
// uploaded late can never undo what another tab already saved (decision 6).
func (s *memberUIStateWire) merge(p memberUIStateWire, ifAbsent bool) {
	if p.Onboarding != nil && (!ifAbsent || s.Onboarding == nil) {
		o := *p.Onboarding
		s.Onboarding = &o
	}
	if p.Launch != nil && (!ifAbsent || s.Launch == nil) {
		l := *p.Launch
		s.Launch = &l
	}
	if p.Ops != nil && (!ifAbsent || s.Ops == nil) {
		o := *p.Ops
		s.Ops = &o
	}
}

func (s memberUIStateWire) validate() error {
	if o := s.Onboarding; o != nil && o.Track != "" && o.Track != "dev" {
		return errors.New(`onboarding.track must be "" or "dev"`)
	}
	if s.Launch != nil {
		if err := s.Launch.validate(); err != nil {
			return err
		}
	}
	if s.Ops != nil {
		return s.Ops.validate()
	}
	return nil
}

// decodeMemberUIState decodes strictly: an unknown key is a client built against a newer CP or
// a typo, and silently dropping it would read as a setting that does not stick.
func decodeMemberUIState(b []byte) (memberUIStateWire, error) {
	var s memberUIStateWire
	dec := json.NewDecoder(bytes.NewReader(b))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&s); err != nil {
		return s, err
	}
	// A second Decode must reach the end: dec.More() only looks inside the current value and lets
	// `{}]` or a stray `}` through.
	if extra := dec.Decode(&struct{}{}); !errors.Is(extra, io.EOF) {
		return s, errors.New("trailing data after the JSON document")
	}
	return s, s.validate()
}

// memberUIStateResponse is what both routes answer: the member's own document, and beside it the
// tenant's defaults (decision 8). The defaults are read-only here: a PATCH carrying `defaults`
// is refused as an unknown key, because only a tenant admin sets them. `effective` is the CP's
// merge of built-in < tenant < the member's own keys; the Console reads it and never re-derives.
type memberUIStateResponse struct {
	memberUIStateWire
	Defaults  tenantMemberDefaultsWire `json:"defaults"`
	Effective effectiveWire            `json:"effective"`
}

type memberUIStateStore interface {
	store.MemberUIStateStore
	store.TenantMemberDefaultsStore
}

type memberUIStateAPI struct {
	memberAuth
	store memberUIStateStore
}

func newMemberUIStateAPI(m *manager) memberUIStateAPI {
	return memberUIStateAPI{memberAuth{m}, m.store}
}

// load returns the stored document and its version. A row that no longer decodes (written by a
// newer CP that knew more keys, then rolled back) reads as empty rather than failing the
// Console's boot: losing a first-run flag is cheaper than a broken page.
func (a memberUIStateAPI) load(ctx context.Context, membershipID string) (memberUIStateWire, int64, error) {
	row, ok, err := a.store.GetMemberUIState(ctx, membershipID)
	if err != nil || !ok {
		return memberUIStateWire{}, 0, err
	}
	s, err := decodeMemberUIState([]byte(row.State))
	if err != nil {
		return memberUIStateWire{}, row.Version, nil
	}
	return s, row.Version, nil
}

// respond answers s together with the member's tenant defaults.
func (a memberUIStateAPI) respond(w http.ResponseWriter, r *http.Request, mv store.MembershipView, s memberUIStateWire) {
	_, d, err := loadTenantMemberDefaults(r, a.store, mv.TenantID)
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusInternalServerError, "store", err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, memberUIStateResponse{memberUIStateWire: s, Defaults: d, Effective: effectiveDefaults(d, s)})
}

func (a memberUIStateAPI) get(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	s, _, err := a.load(r.Context(), mv.MembershipID)
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusInternalServerError, "store", err.Error()})
		return
	}
	a.respond(w, r, mv, s)
}

func (a memberUIStateAPI) patch(w http.ResponseWriter, r *http.Request, _ store.Identity, mv store.MembershipView) {
	body, err := io.ReadAll(io.LimitReader(r.Body, memberUIStateMaxBody+1))
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", err.Error()})
		return
	}
	if len(body) > memberUIStateMaxBody {
		writeAPIErr(w, &apiError{http.StatusRequestEntityTooLarge, "too_large", "ui state is limited to 16 KiB"})
		return
	}
	p, err := decodeMemberUIState(body)
	if err != nil {
		writeAPIErr(w, &apiError{http.StatusBadRequest, "bad_request", err.Error()})
		return
	}
	// The check runs inside the optimistic loop: a key that appears between our read and our
	// write fails the version and is seen on the next pass.
	ifAbsent := r.URL.Query().Get("if_absent") == "1"
	ctx := r.Context()
	for range memberUIStateRetries {
		cur, version, err := a.load(ctx, mv.MembershipID)
		if err != nil {
			writeAPIErr(w, &apiError{http.StatusInternalServerError, "store", err.Error()})
			return
		}
		cur.merge(p, ifAbsent)
		out, err := json.Marshal(cur)
		if err != nil {
			writeAPIErr(w, &apiError{http.StatusInternalServerError, "encode", err.Error()})
			return
		}
		if len(out) > memberUIStateMaxBody {
			writeAPIErr(w, &apiError{http.StatusRequestEntityTooLarge, "too_large", "ui state is limited to 16 KiB"})
			return
		}
		ok, err := a.store.PutMemberUIState(ctx, store.MemberUIState{
			MembershipID: mv.MembershipID,
			State:        string(out),
			UpdatedAt:    time.Now().UTC().Format(time.RFC3339),
		}, version)
		if err != nil {
			writeAPIErr(w, &apiError{http.StatusInternalServerError, "store", err.Error()})
			return
		}
		if ok {
			a.respond(w, r, mv, cur)
			return
		}
	}
	writeAPIErr(w, &apiError{http.StatusConflict, "conflict", "ui state changed concurrently, try again"})
}
