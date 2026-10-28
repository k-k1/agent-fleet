package agents

import "context"

// ModelLister is an optional Agent capability: the kind's launch-time model menu, behind GET
// /agents/{kind}/models (the Console picker and MCP list_models). A kind without it has no
// model catalog and the route answers 404. It is resolved by exact registry key.
type ModelLister interface {
	ListModels(ModelQuery) ModelListing
}

// ModelQuery is what a ModelLister may shape its menu by. Each field is meaningful to the kinds
// that have the setting and ignored by the rest.
type ModelQuery struct {
	Ctx context.Context
	// CustomModels are the member's registered model ids (Settings, claudeCustomModels),
	// already normalized; a kind that offers them appends the ones its menu lacks.
	CustomModels []string
	// CatalogPref is the billing-route preference (the opencodeCatalog setting, normalized),
	// for a kind whose menu is shaped by route.
	CatalogPref string
}

// ModelListing is one kind's menu before the handler drops the models the user hides.
type ModelListing struct {
	Models []ModelChoice
	// Enumerated is how many models the CLI or daemon answered before the kind's own shaping,
	// so "the route hid everything" can be told from "there was nothing to hide". -1 for a
	// kind that does no shaping of its own.
	Enumerated int
	// Listed are every id the catalog answered, shaped away or not; nil when Models is the
	// whole catalog. A recommendation among them counts as confirmed by the account.
	Listed []string
	// Route is the billing route the menu was actually shaped by; "" for a kind without
	// routes.
	Route string
}
