package main

// engine_wiring.go — aliases for the engine domain that lives in internal/engine (#3810).
// The declared half of the engine table moved there with exported names; main keeps its
// historical unexported ones so the engine call sites and tests do not change.

import "github.com/k-k1/agent-fleet/control-plane/internal/engine"

type (
	engineDef       = engine.Def
	engineTable     = engine.Table
	engineIngestDef = engine.IngestDef
	engineClass     = engine.Class
)

const (
	engineOriginEnv   = engine.OriginEnv
	engineOriginPanel = engine.OriginPanel

	engineLifecycleExternal = engine.LifecycleExternal
	engineLifecycleRemote   = engine.LifecycleRemote

	engineAPIChat   = engine.APIChat
	engineAPIImages = engine.APIImages

	engineOfferBudgetDefault = engine.OfferBudgetDefault

	engineBuyOnDemand = engine.BuyOnDemand
	engineBuySpot     = engine.BuySpot

	engineVramDeclared  = engine.VramDeclared
	engineVramFloor     = engine.VramFloor
	engineVramWeightsKV = engine.VramWeightsKV
	engineVramUnknown   = engine.VramUnknown
)

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
