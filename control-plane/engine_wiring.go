package main

// engine_wiring.go — aliases for the engine domain that lives in internal/engine (#3810).
// The declared half of the engine table moved there with exported names; main keeps its
// historical unexported ones so the engine call sites and tests do not change.

import "github.com/k-k1/agent-fleet/control-plane/internal/engine"

type (
	engineDef       = engine.Def
	engineTable     = engine.Table
	engineIngestDef = engine.IngestDef
)

const (
	engineOriginEnv   = engine.OriginEnv
	engineOriginPanel = engine.OriginPanel

	engineLifecycleExternal = engine.LifecycleExternal
	engineLifecycleRemote   = engine.LifecycleRemote

	engineAPIChat   = engine.APIChat
	engineAPIImages = engine.APIImages

	engineOfferBudgetDefault = engine.OfferBudgetDefault
)

func imageProviderServable(d engineDef) bool { return engine.ImageProviderServable(d) }
