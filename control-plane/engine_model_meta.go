package main

// engine_model_meta.go — re-reading a model page for the name and the picture (ADR 0088).
//
// The ingest writes both at the moment it resolves a source, so a row taken in from now on
// arrives with them. This is the other half, and without it the feature would only ever apply to
// models nobody has taken in yet: every row a deployment already holds was written before the
// columns existed, and the id is all the panel can draw for them.
//
// It is one press per row on purpose. The alternative — filling them in behind the panel when a
// row lacking them is listed — turns opening a screen into a page of upstream requests that
// nobody asked for and that the operator cannot see the cost of.

import (
	"errors"
	"net/http"
	"strconv"
	"strings"

	"github.com/k-k1/agent-fleet/control-plane/internal/store"
)

// registerEngineMetaRoutes is called from one line of registerEngineAdminRoutes, for the reason
// registerEngineObjectRoutes is: a route table every lane appends to is one merge conflict per
// lane.
func registerEngineMetaRoutes(mux *http.ServeMux, a engineAdminAPI) {
	// Under the ingest authority rather than super_admin, like 揃える next door: it reads the same
	// upstream the ingest reads and writes nothing a member can see.
	mux.HandleFunc("POST /api/admin/engines/{key}/models/{id}/meta", a.withIngestAdmin(a.refreshModelMeta))
}

// engineSourceForRead turns a row's stored `source` back into the shape the resolve takes.
//
// 🔴 It is the inverse of what the ingest wrote and of nothing else. A `url:` source and a source
// nobody recorded both answer false: the first addresses the weights themselves — there is no
// page behind it to read a name off — and the second is a seeded or hand-registered row, which
// never had an upstream at all.
//
// The file is deliberately not carried for Hugging Face: the resolve wants one to pin a sha256,
// and this caller wants the repository's model card. The stored spelling is `hf:<owner>/<repo>/<file>`,
// so the file is what the last slash separates — and a legacy source with no file is refused
// rather than guessed at.
func engineSourceForRead(source string) (engineIngestSource, bool) {
	s := strings.TrimSpace(source)
	switch {
	case strings.HasPrefix(s, "hf:"):
		parts := strings.SplitN(strings.TrimPrefix(s, "hf:"), "/", 3)
		if len(parts) < 3 || parts[0] == "" || parts[1] == "" || parts[2] == "" {
			return engineIngestSource{}, false
		}
		return engineIngestSource{HF: &engineIngestHF{Repo: parts[0] + "/" + parts[1], File: parts[2]}}, true
	case strings.HasPrefix(s, "civitai:"):
		id, err := strconv.Atoi(strings.TrimPrefix(s, "civitai:"))
		if err != nil || id <= 0 {
			return engineIngestSource{}, false
		}
		return engineIngestSource{Civitai: &engineIngestCivitai{VersionID: id}}, true
	}
	return engineIngestSource{}, false
}

// engineNoSourceMessage says which of the two "no page to read" cases this row is, because they
// send the reader to opposite places: a row with nothing recorded came from the seed or was
// registered by hand and never had an upstream, while a `url:` row HAS a source and it addresses
// the weights themselves — clicking it would be a multi-gigabyte download, not a model card.
func engineNoSourceMessage(id, source string) string {
	if s := strings.TrimSpace(source); s != "" {
		return "the source recorded for " + id + " (" + s + ") is the file itself, not a page with a name on it"
	}
	return "nothing is recorded about where " + id + " came from, so there is no page to read a name off"
}

// refreshModelMeta (POST …/models/{id}/meta) re-reads the page a row came from and writes down
// what the publisher calls it and where its example image is.
//
// The whole resolve is run rather than a narrower read of its own. It costs two or three requests
// where one would do — Civitai's version document, its model document, and the anonymous probe of
// the download URL — and it buys the thing that matters: there is ONE way this deployment reads a
// model page, so a name here can never come from a document the ingest does not read.
func (a engineAdminAPI) refreshModelMeta(w http.ResponseWriter, r *http.Request, g engineIngestGrant) {
	e, ok := a.engineObjectsEngine(w, r, "re-reading a model page")
	if !ok {
		return
	}
	if a.mgr == nil || a.mgr.store == nil {
		writeAPIErr(w, internalErr(errors.New("no store")))
		return
	}
	ctx := r.Context()
	id := strings.TrimSpace(r.PathValue("id"))
	m, ok := engineCatalogModel(ctx, e, id)
	if !ok {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineModelUnknown,
			"no model " + id + " for engine " + e.def.Key})
		return
	}
	src, ok := engineSourceForRead(m.Source)
	if !ok {
		writeAPIErr(w, &apiError{http.StatusBadRequest, errCodeEngineNoSource,
			engineNoSourceMessage(id, m.Source)})
		return
	}
	res, aerr := engineIngestResolve(ctx, src)
	if aerr != nil {
		writeAPIErr(w, aerr)
		return
	}
	d := store.EngineModelDisplay{
		DisplayName: res.DisplayName, VersionName: res.VersionName,
		PreviewURL: res.PreviewURL, ThumbURL: res.ThumbURL,
	}
	found, err := a.mgr.store.SetEngineModelDisplay(ctx, e.def.Key, id, d)
	if err != nil {
		writeAPIErr(w, internalErr(err))
		return
	}
	if !found {
		writeAPIErr(w, &apiError{http.StatusNotFound, errCodeEngineModelUnknown,
			"no model " + id + " for engine " + e.def.Key})
		return
	}
	// The catalogue cache, and nothing else. The active set the box reads does not carry a name
	// or a picture, so there is no publish to make and no workspace to tell — which is also why
	// this route cannot break a running engine.
	e.catalog.invalidate()
	a.auditFor(r, g, "engine."+e.def.Key+".model", "meta "+id)
	writeJSON(w, http.StatusOK, map[string]any{
		"id": id, "display_name": d.DisplayName, "version_name": d.VersionName,
		"preview_url": d.PreviewURL, "thumb_url": d.ThumbURL,
		// Whether the read found anything at all, so the panel can say "the page publishes no
		// name" instead of drawing a successful press that changed nothing.
		"found": d != store.EngineModelDisplay{},
	})
}
