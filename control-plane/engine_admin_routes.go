package main

// engine_admin_routes.go — the super-admin engine routes and who may call each. The handler set
// (engineAdminAPI) and what the mode toggle means are in engine_admin.go.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

func registerEngineAdminRoutes(mux *http.ServeMux, cfg config, reg *engineRegistry) {
	var settings store.SettingsStore
	if cfg.mgr != nil && cfg.mgr.store != nil {
		settings = cfg.mgr.store
	}
	a := engineAdminAPI{memberAuth{cfg.mgr}, reg, settings}
	// The engine list. NOT super_admin-only: a tenant_admin of a tenant the operator granted
	// `allow_engine_ingest` gets a SUBSET of the same row, which is what the ingest form and the
	// catalogue list are drawn from (ADR 0072 open question 11). Same predicate as the ingest
	// routes below, so there is one answer to "may this person take a model in" and not two.
	mux.HandleFunc("GET /api/admin/engines", a.withIngestAdmin(a.get))
	mux.HandleFunc("PUT /api/admin/engines/{key}", a.withSuperAdmin(a.put))
	mux.HandleFunc("PUT /api/admin/engines/{key}/idle", a.withSuperAdmin(a.putIdle))
	mux.HandleFunc("GET /api/admin/engines/{key}/hourly", a.withSuperAdmin(a.uptime))
	// Whose work the engine was doing (ADR 0079 open question 7). A separate route from
	// /hourly next door because it answers a separate question: that one says the GPU was up,
	// this one says who for — and on a deployment that LENDS its engines it is the only place
	// that answer exists at all.
	mux.HandleFunc("GET /api/admin/engines/{key}/attribution", a.withSuperAdmin(a.attribution))
	// The GPU this role buys (ADR 0074). A separate route from the mode toggle above because
	// it is a separate act with a separate cost: the mode buys a box now, this says what the
	// NEXT box will be.
	mux.HandleFunc("PUT /api/admin/engines/{key}/class", a.withSuperAdmin(a.putClass))
	// Whether this role may buy an INTERRUPTIBLE box. Its own route beside the class for the
	// same reason the class is its own route beside the mode: it is a separate act, and the one
	// being consented to here is not "which card" but "this engine may be taken away mid-answer".
	mux.HandleFunc("PUT /api/admin/engines/{key}/spot", a.withSuperAdmin(a.putSpot))
	// What this deployment excludes from every image this engine makes (ADR 0072 follow-up,
	// negative prompts). Super-admin like the mode and the class: it is a statement about the
	// whole deployment, not about one model or one member.
	mux.HandleFunc("PUT /api/admin/engines/{key}/negative", a.withSuperAdmin(a.putNegative))
	// Stopping the BOX without changing the mode — the second half of a class change, since a
	// new rung reaches new instances only.
	mux.HandleFunc("POST /api/admin/engines/{key}/replace-box", a.withSuperAdmin(a.replaceBox))
	// The model catalogue (ADR 0072 decision 7). A CP route, not an Agent one, so it needs no
	// entry in the agent-proxy allowlist in routes.go — that list exists for endpoints the
	// Workspace Agent implements and the CP forwards.
	mux.HandleFunc("PUT /api/admin/engines/{key}/models/{id}", a.withSuperAdmin(a.putModel))
	// Registering a file that is ALREADY in the bucket, and forgetting one. The ingest below
	// fetches; this only writes down what a staged file is, and it stays because it is the
	// route that needs no `ecs:RunTask` and works on a deployment whose egress is closed.
	mux.HandleFunc("POST /api/admin/engines/{key}/models", a.withSuperAdmin(a.postModel))
	mux.HandleFunc("DELETE /api/admin/engines/{key}/models/{id}", a.withSuperAdmin(a.deleteModel))
	// The discovery button (ADR 0082 decisions 6 and 7): what an external ComfyUI's own
	// checkpoint/LoRA/VAE folders currently hold, read off its /object_info and offered as
	// candidates. Under ingest authority, not super_admin only — the same predicate the ingest
	// form itself uses, since this is the other way a row's files get chosen rather than typed.
	mux.HandleFunc("POST /api/admin/engines/{key}/discover", a.withIngestAdmin(a.discoverModelsGrant))
	// Taking a model IN from Hugging Face / Civitai / a URL (ADR 0072 decision 6, phase P4).
	//
	// These are the ONLY engine routes that are not super_admin: a tenant_admin of a tenant
	// the operator granted `allow_engine_ingest` may drive them too (ADR 0072 open question 11 —
	// engine_ingest_perm.go says why the axis stops here).
	mux.HandleFunc("POST /api/admin/engines/{key}/ingest", a.withIngestAdmin(a.postIngest))
	// And dismissing a job. The list itself is gone (ADR 0085 decision 6 — a job is its
	// destination object's progress in the ledger, not a second list), but a `failed` one is an
	// entry on that key with exactly one act, and this is it. Under the same authority as the
	// ledger rather than super_admin, because the rule is "your own jobs": a granted tenant_admin
	// may forget theirs, and deleteIngest narrows by the same tenant the ledger does.
	mux.HandleFunc("DELETE /api/admin/engines/{key}/ingest/{id}", a.withIngestAdmin(a.deleteIngest))
	// Resolving a source WITHOUT starting anything: what the licence is, whether the repository
	// is gated, how big the file is. The panel calls it while somebody is typing, so that the
	// licence they are about to accept is on screen BEFORE the button that accepts it.
	mux.HandleFunc("POST /api/admin/engines/{key}/ingest/resolve", a.withIngestAdmin(a.resolveIngest))
	// And what the repository HAS, so the filename is picked rather than copied by hand across
	// two windows — the same read, filtered to the files this engine could actually load.
	mux.HandleFunc("POST /api/admin/engines/{key}/ingest/files", a.withIngestAdmin(a.listIngestFiles))
	// And WHICH repository, for somebody who does not already know the name (ADR 0072
	// decision 11). Reads only, filtered to what this engine could load.
	mux.HandleFunc("POST /api/admin/engines/{key}/ingest/search", a.withIngestAdmin(a.searchIngest))
	// A card identifies a repository/model; this read expands it into the versions that the
	// single-operation file picker can choose from.
	mux.HandleFunc("POST /api/admin/engines/{key}/ingest/versions", a.withIngestAdmin(a.versionsIngest))
	// The same read with no engine in the path: a deployment that has not adopted 60-engines
	// has an EMPTY panel, and "there is nothing here" is the worst answer to "what could I
	// run?". Browsing needs no engine because it needs no token, no bucket and no task.
	mux.HandleFunc("POST /api/admin/engines/search", a.withIngestAdmin(a.browseSearch))
	// The operator's Hugging Face token (ADR 0072 decision 6 as revised, phase P5). Not under
	// {key}: one token serves every role, because one ingest task does. There is no GET that
	// returns it — only whether one is registered, by whom and when.
	mux.HandleFunc("GET /api/admin/engines/hf-token", a.withSuperAdmin(a.getHfToken))
	mux.HandleFunc("PUT /api/admin/engines/hf-token", a.withSuperAdmin(a.putHfToken))
	mux.HandleFunc("DELETE /api/admin/engines/hf-token", a.withSuperAdmin(a.deleteHfToken))
	// The operator's Civitai token (engine_civitai_token.go), the same shape and the same
	// reason: one account serves every role, because one ingest task does.
	mux.HandleFunc("GET /api/admin/engines/civitai-token", a.withSuperAdmin(a.getCivitaiToken))
	mux.HandleFunc("PUT /api/admin/engines/civitai-token", a.withSuperAdmin(a.putCivitaiToken))
	mux.HandleFunc("DELETE /api/admin/engines/civitai-token", a.withSuperAdmin(a.deleteCivitaiToken))
	// The LAN ComfyUI's URL and key (engine_comfy_panel.go, #957). Super_admin like the tokens:
	// it decides where every member's images are sent, and the key is a credential. Read through
	// GET /api/admin/engines' `comfy_lan`; there is no GET of its own.
	mux.HandleFunc("PUT /api/admin/engines/comfy-lan", a.withSuperAdmin(a.putComfyLan))
	mux.HandleFunc("DELETE /api/admin/engines/comfy-lan", a.withSuperAdmin(a.deleteComfyLan))
	// Whether the catalogue's search offers Civitai Red at all (engine_civitai_red.go). Under
	// engines rather than beside the egress mode because it is a property of this panel's own
	// search, and super_admin like every other deployment-wide write — a granted tenant_admin
	// reads the resulting list off GET /api/admin/engines below and moves nothing.
	mux.HandleFunc("GET /api/admin/engines/civitai-red", a.withSuperAdmin(a.getCivitaiRed))
	mux.HandleFunc("PUT /api/admin/engines/civitai-red", a.withSuperAdmin(a.putCivitaiRed))
	// The bucket read as the ledger, and the two acts that start from it (ADR 0085 decisions 2, 3
	// and 7). One line on purpose: the route table is what three lanes writing this ADR at once
	// would otherwise each append to.
	registerEngineObjectRoutes(mux, a)
	// Re-reading a model page for the name and the picture (ADR 0088), which is how a row taken
	// in before those columns existed gets them. One line for the same reason as the one above.
	registerEngineMetaRoutes(mux, a)
	// The credential another deployment borrows these engines with (ADR 0079 decision 3, P1).
	// Super_admin only and never GET — it opens every engine here, so a tenant-scoped role is
	// not in proportion, and a credential does not belong in a URL. engine_issue_token.go.
	mux.HandleFunc("POST /api/admin/engines/issue-token", a.withSuperAdmin(a.postIssueToken))
}
