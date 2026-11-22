package main

// engine_wiring.go — aliases for the engine domain that lives in internal/engine (#3810).
// The declared half of the engine table moved there with exported names; main keeps its
// historical unexported ones so the engine call sites and tests do not change.

import (
	"context"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/engine"
)

type (
	engineDef           = engine.Def
	engineTable         = engine.Table
	engineIngestDef     = engine.IngestDef
	engineClass         = engine.Class
	engineOfferAttempt  = engine.OfferAttempt
	engineOfferRun      = engine.OfferRun
	engineSessionClaims = engine.SessionClaims
	engineRemotes       = engine.Remotes
	engineRemote        = engine.Remote
)

const (
	engineSessionTokenTTL = engine.SessionTokenTTL

	engineOriginEnv   = engine.OriginEnv
	engineOriginPanel = engine.OriginPanel

	engineLifecycleExternal = engine.LifecycleExternal
	engineLifecycleRemote   = engine.LifecycleRemote

	engineAPIChat   = engine.APIChat
	engineAPIImages = engine.APIImages

	engineOfferBudgetDefault = engine.OfferBudgetDefault

	engineOfferActive        = engine.OfferActive
	engineOfferUnfulfillable = engine.OfferUnfulfillable
	engineOfferInsufficient  = engine.OfferInsufficient
	engineOfferQuota         = engine.OfferQuota
	engineOfferBudget        = engine.OfferBudget
	engineOfferInterrupted   = engine.OfferInterrupted
	engineOfferUnusable      = engine.OfferUnusable

	engineModelKindLora = engine.ModelKindLora

	engineBuyOnDemand = engine.BuyOnDemand
	engineBuySpot     = engine.BuySpot

	engineVramDeclared  = engine.VramDeclared
	engineVramFloor     = engine.VramFloor
	engineVramWeightsKV = engine.VramWeightsKV
	engineVramUnknown   = engine.VramUnknown
)

var (
	errEngineBoxRegistering = engine.ErrBoxRegistering
	errEngineOffersSpent    = engine.ErrOffersSpent
	errEngineFleetRefused   = engine.ErrFleetRefused
)

func newEngineOfferRun(budget time.Duration) *engineOfferRun { return engine.NewOfferRun(budget) }

func imageProviderServable(d engineDef) bool { return engine.ImageProviderServable(d) }

func parseEngineClasses(spec string) []engineClass { return engine.ParseClasses(spec) }

func engineClassByID(list []engineClass, id string) (engineClass, bool) {
	return engine.ClassByID(list, id)
}

func engineVramStrength(source string) int { return engine.VramStrength(source) }

func engineClassFits(c engineClass, needMiB int) bool { return engine.ClassFits(c, needMiB) }

func engineClassSettingKey(key string) string { return engine.ClassSettingKey(key) }

func engineSpotSettingKey(key string) string { return engine.SpotSettingKey(key) }

func engineClassesEqual(a, b []engineClass) bool { return engine.ClassesEqual(a, b) }

func engineClassesHaveSpot(list []engineClass) bool { return engine.ClassesHaveSpot(list) }

func engineClassHasType(c engineClass, instanceType string) bool {
	return engine.ClassHasType(c, instanceType)
}

func engineSignKey(master32 []byte) []byte { return engine.SignKey(master32) }

func mintEngineIssueToken(signKey []byte, membershipID string) string {
	return engine.MintIssueToken(signKey, membershipID)
}

func verifyEngineIssueToken(signKey []byte, token string) (string, bool) {
	return engine.VerifyIssueToken(signKey, token)
}

func mintEngineSessionToken(signKey []byte, membershipID, session, key string, exp time.Time) string {
	return engine.MintSessionToken(signKey, membershipID, session, key, exp)
}

func verifyEngineSessionToken(signKey []byte, token string, now time.Time) (engineSessionClaims, bool) {
	return engine.VerifySessionToken(signKey, token, now)
}

func engineSessionMembership(token string) (string, bool) { return engine.SessionMembership(token) }

// newEngineRemotes reads the operator's borrowing declaration; nil when nothing is borrowed.
func newEngineRemotes() *engineRemotes { return engine.NewRemotes(engineClient) }

// engineRemoteHost is the registry side of borrowing (engine.RemoteHost): the four things the
// remote cluster does to main's engineRegistry and to the workspaces.
type engineRemoteHost struct {
	reg *engineRegistry
	mgr *manager
}

// newEngineRemoteHost is nil for a nil registry, as an interface: the cluster treats "no
// registry" as a no-op, and a typed nil inside a non-nil interface would defeat that check.
func newEngineRemoteHost(reg *engineRegistry, mgr *manager) engine.RemoteHost {
	if reg == nil {
		return nil
	}
	return engineRemoteHost{reg: reg, mgr: mgr}
}

func (h engineRemoteHost) Served(key string) (engine.Def, func(), bool) {
	e := h.reg.get(key)
	if e == nil {
		return engine.Def{}, nil, false
	}
	return e.def, e.catalog.invalidate, true
}

func (h engineRemoteHost) Adopt(d engine.Def) bool { return h.reg.adopt(d) }

func (h engineRemoteHost) NotifyCatalogChanged(ctx context.Context, key string) {
	// The function variable is read here, before the goroutine starts, as `go notify(...)` did at
	// the old call site: a test that swaps the stub afterwards must not race a late read.
	go notifyEngineCatalogChanged(ctx, h.mgr, key)
}
